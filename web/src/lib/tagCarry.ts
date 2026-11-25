// Which restored tag chips are still valid, and where each comes from (PRD R7/R10, SPEC 11). A saved filter may name a tag an
// initiative lists, a task's own tag or a derived actor-kind tag. The board's list of initiatives knows only the first; the rest
// are valid when a task matches them under the server's own rule (`GET /tasks?tag=`: own, initiative or actor kind), so this asks
// the server instead of re-deriving the rule. The same answer names the sources the tag has on those tasks, so a restored chip
// keeps its accessible name and hover text whether or not the combined filter returns a row.

import { canonicalTag, type TagMark } from './chipFilter';
import { TAG_PATTERN } from './taskTags';

/**
 * The sources each of `tags` has on the tasks, in ONE request (`GET /tasks/tag-sources`): a tag no task carries has no entry.
 * Rejects when the answer cannot be had, which fails every tag of the batch.
 */
export type TagProbe = (tags: readonly string[]) => Promise<Readonly<Record<string, readonly TagMark['source'][]>>>;

/** The restored tags some task carries, and the marks (each with its source) the probe found for every restored tag. */
export interface Carried {
  /** Valid without an initiative listing them: a task matches them, or the check could not be made. */
  tags: ReadonlySet<string>;
  marks: TagMark[];
  /** The tags the probe answered for: `marks` holds every source each of them has now (a failed probe's tag is not here). */
  answered: ReadonlySet<string>;
}

/**
 * What the restored `tags` carry (in the server's spelling). Every tag is probed once, the initiatives' own too, because a tag an initiative lists may also be
 * a task's own and the chip names both. A tag an initiative of `known` lists needs no probe to be valid. A probe that fails keeps
 * its tag, with no marks: what cannot be checked is not dropped, the view shows the server's answer for it.
 */
export async function carriedTags(
  tags: readonly string[],
  known: readonly { tags: readonly string[] }[],
  probe: TagProbe,
): Promise<Carried> {
  return carriedFrom(await probeAll(tags, probe), known);
}

/** One tag's answer: the marks the probe found, or that it failed. */
interface Answer {
  tag: string;
  marks: readonly TagMark[];
  failed: boolean;
}

async function probeAll(tags: readonly string[], probe: TagProbe): Promise<Answer[]> {
  // Each tag in the server's spelling, once; one the server's grammar refuses cannot be on any task or initiative, so it is unknown
  // without asking (the server would answer 400, which is a failed check and would keep it).
  const wanted = [...new Set(tags.map(canonicalTag))].filter((tag) => TAG_PATTERN.test(tag));
  if (wanted.length === 0) return [];
  let sources: Awaited<ReturnType<TagProbe>>;
  try {
    sources = await probe(wanted);
  } catch {
    return wanted.map((tag): Answer => ({ tag, marks: [], failed: true }));
  }
  return wanted.map((tag): Answer => ({ tag, marks: (Object.hasOwn(sources, tag) ? sources[tag]! : []).map((source) => ({ tag, source })), failed: false }));
}

/**
 * What the answers carry against the initiatives of `known`, which may be newer than the catalog the probe began under. With
 * `initiativeMoved` the catalog has been read again since the probe began, so its `initiative` marks may name an initiative that
 * no longer lists the tag: they are dropped (`known` offers what the initiatives list now), and a tag is then confirmed only by
 * the sources the answer still holds, not by one that may have gone while the lookup was out.
 */
function carriedFrom(answers: readonly Answer[], known: readonly { tags: readonly string[] }[], initiativeMoved = false): Carried {
  const listed = new Set(known.flatMap((i) => i.tags.map(canonicalTag)));
  const carried = new Set<string>();
  const answered = new Set<string>();
  const marks: TagMark[] = [];
  for (const answer of answers) {
    const own = initiativeMoved ? answer.marks.filter((m) => m.source !== 'initiative') : answer.marks;
    marks.push(...own);
    if (!answer.failed) answered.add(answer.tag);
    if (!listed.has(answer.tag) && (answer.failed || own.length > 0)) carried.add(answer.tag);
  }
  return { tags: carried, marks, answered };
}

/** One source read's answer: the `Carried` of its tags, and the part of it that still holds when it is applied. */
export interface SourceRead extends Carried {
  /**
   * The answered tags and their marks, less every tag a newer read has begun for since this one began: an older answer landing
   * after a newer one (or while it is out) must not put back a source the newer one no longer reports. Asked when the answer is
   * applied, not when it lands, so a wait in between (the catalog's read) cannot let a stale answer through.
   */
  fresh(initiativeMoved?: boolean): Pick<Carried, 'answered' | 'marks'>;
  /**
   * The restored tags a task carries when checked against the newest catalog (`known`), not the one the read began under: a tag
   * an initiative listed then and a task confirms now stays valid after an event takes it off its last initiative.
   * `initiativeMoved` is whether the catalog was read again since the read began.
   */
  carriedAgainst(known: readonly { tags: readonly string[] }[], initiativeMoved?: boolean): ReadonlySet<string>;
}

/**
 * The per-tag source reads of one view, in the order they began (PRD R7/R10, SPEC 11). A link, a restoration, a hash change
 * and a chip change each read the sources of their tags; their answers may land in any order, and only the newest read of a tag
 * names its sources. A read belongs to the navigation that began it (`owner`): once a newer navigation is current, its answer
 * names nothing, so an abandoned link or restoration can neither set nor take away a source the current one shows. The
 * validity of a restored tag (`tags`) is the read's own: the view's filter turns already drop a restore that is no longer current.
 */
export class SourceReads {
  private began = 0;
  private readonly newest = new Map<string, number>();
  private readonly applied = new Map<string, number>();

  constructor(private readonly probe: TagProbe) {}

  /**
   * Begins a read of `tags` (now, before it awaits anything) against the initiatives of `known`, for the navigation `owner`
   * answers for (true while it is current).
   */
  async read(tags: readonly string[], known: readonly { tags: readonly string[] }[] = [], owner: () => boolean = () => true): Promise<SourceRead> {
    const turn = ++this.began;
    for (const tag of tags) this.newest.set(canonicalTag(tag), turn);
    const answers = await probeAll(tags, this.probe);
    const current = (tag: string) => this.newest.get(tag) === turn;
    const fresh = (initiativeMoved = false) => {
      if (!owner()) return { answered: new Set<string>(), marks: [] };
      for (const answer of answers) if (current(answer.tag)) this.applied.set(answer.tag, turn);
      const c = carriedFrom(answers, [], initiativeMoved);
      return { answered: new Set([...c.answered].filter(current)), marks: c.marks.filter((m) => current(m.tag)) };
    };
    return {
      ...carriedFrom(answers, known),
      fresh,
      carriedAgainst: (newest, initiativeMoved = false) => carriedFrom(answers, newest, initiativeMoved).tags,
    };
  }

  /**
   * A restoration names `tags` before it can read them (it waits for the board's list first): they are owed from now on, so a
   * change that supersedes it before it reads (or before its read is applied) reads them as its own.
   */
  claim(tags: readonly string[]): void {
    const turn = ++this.began;
    for (const tag of tags) this.newest.set(canonicalTag(tag), turn);
  }

  /**
   * The tags of `tags` (as given) whose newest read (or claim) has not been applied: it is still out, or the navigation that
   * began it was superseded before it named anything. A new navigation selecting one of them reads it again as its own, so its chip still
   * names the tag's sources when no row of the combined filter does.
   */
  owed(tags: readonly string[]): string[] {
    return tags.filter((tag) => {
      const key = canonicalTag(tag);
      const turn = this.newest.get(key);
      return TAG_PATTERN.test(key) && turn !== undefined && this.applied.get(key) !== turn;
    });
  }
}

/**
 * The reads that keep a view's chip sources current after a live event (PRD R1, SPEC 11): an event may change any tag's sources,
 * so the sources of the tags on offer are read again. One round runs at a time; an event while a round is out makes that round's
 * answer stale (it began before the event, so it could put back a source the event removed): it is dropped and a new round
 * runs. Events landing together are one round. `round` is handed the extra tags still to read and `stale()`, and must not apply
 * its answer once that says so.
 */
export class SourceRefresh {
  private running = false;
  private dirty = false;
  private readonly extra = new Set<string>();

  constructor(private readonly round: (extra: readonly string[], stale: () => boolean) => Promise<void>) {}

  /** Something that can change sources happened (or an older read was dropped as stale: its tags are `extra`, read again). */
  poke(extra: readonly string[] = []): void {
    for (const tag of extra) this.extra.add(tag);
    this.dirty = true;
    if (!this.running) void this.loop();
  }

  private async loop(): Promise<void> {
    this.running = true;
    try {
      while (this.dirty) {
        this.dirty = false;
        const extra = [...this.extra];
        try {
          await this.round(extra, () => this.dirty);
          // An answer that was applied has read its extra tags; one dropped as stale leaves them for the round that follows.
          if (!this.dirty) this.extra.clear();
        } catch {
          // A failed read keeps the chips as they were; the next event reads again.
          if (!this.dirty) this.extra.clear();
        }
      }
    } finally {
      this.running = false;
    }
  }
}
