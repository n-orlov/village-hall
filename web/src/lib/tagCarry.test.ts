import { describe, expect, it, vi } from 'vitest';
import type { TagMark } from './chipFilter';
import { carriedTags, SourceReads, SourceRefresh, type TagProbe } from './tagCarry';

const known = [{ tags: ['work'] }, { tags: ['home'] }];
const mark = (tag: string, source: TagMark['source']): TagMark => ({ tag, source });

// A fixture that answers one tag at a time, as the batch probe the views use: it asks the fixture for each tag of the batch and
// rejects when any rejects (a failed batch fails every tag in it).
const perTag =
  (one: (tag: string) => Promise<readonly TagMark[]>): TagProbe =>
  async (tags) => {
    const answers = await Promise.all(tags.map(async (tag) => [tag, (await one(tag)).map((m) => m.source)] as const));
    return Object.fromEntries(answers.filter(([, sources]) => sources.length > 0));
  };

describe('carriedTags', () => {
  it('probes every tag once and keeps the unlisted ones a task carries, with every mark found', async () => {
    const probe = vi.fn(async (tag: string) => (tag === 'gone' ? [] : tag === 'ralph' ? [mark('ralph', 'actor')] : [mark(tag, 'task')]));
    const { tags, marks, answered } = await carriedTags(['work', 'only-own', 'ralph', 'gone', 'only-own'], known, perTag(probe));
    expect([...tags].sort()).toEqual(['only-own', 'ralph']);
    expect([...answered].sort()).toEqual(['gone', 'only-own', 'ralph', 'work']);
    expect(probe.mock.calls.map((c) => c[0]).sort()).toEqual(['gone', 'only-own', 'ralph', 'work']);
    expect(marks).toEqual([mark('work', 'task'), mark('only-own', 'task'), mark('ralph', 'actor')]);
  });

  it('keeps the sources of a tag an initiative lists that a task also carries as its own', async () => {
    const { tags, marks } = await carriedTags(['work'], known, perTag(async () => [mark('work', 'task')]));
    expect(tags.size).toBe(0);
    expect(marks).toEqual([mark('work', 'task')]);
  });

  it('keeps a tag whose check failed, with no marks: what cannot be checked is not dropped', async () => {
    const { tags, marks, answered } = await carriedTags(['flaky'], known, perTag(() => Promise.reject(new Error('down'))));
    expect([...tags]).toEqual(['flaky']);
    expect(marks).toEqual([]);
    expect(answered.size).toBe(0);
  });

  it('drops a tag nothing carries and no initiative lists', async () => {
    const { tags, marks, answered } = await carriedTags(['ghost'], known, perTag(async () => []));
    expect(tags.size).toBe(0);
    expect(marks).toEqual([]);
    expect([...answered]).toEqual(['ghost']);
  });

  it('asks nothing for no tags', async () => {
    const probe = vi.fn(async () => []);
    expect((await carriedTags([], known, perTag(probe))).tags.size).toBe(0);
    expect(probe).not.toHaveBeenCalled();
  });

  it('reads each tag in the server\'s spelling: a padded or upper-case saved tag is probed and listed as the server holds it', async () => {
    const probe = vi.fn(async (tag: string) => [mark(tag, 'task')]);
    const { tags, marks } = await carriedTags(['\u0085only-own\u0085', 'WORK', ' Only-Own'], known, perTag(probe));
    expect(probe.mock.calls.map((c) => c[0]).sort()).toEqual(['only-own', 'work']);
    expect([...tags]).toEqual(['only-own']);
    expect(marks).toEqual([mark('only-own', 'task'), mark('work', 'task')]);
  });

  it('drops a tag the server\'s grammar refuses without asking: it can be on no task, and the server would answer 400', async () => {
    const probe = vi.fn(async (tag: string) => [mark(tag, 'task')]);
    const { tags, marks } = await carriedTags(['\ufeffonly-own', 'has space', 'x'.repeat(41), 'fine'], known, perTag(probe));
    expect(probe.mock.calls.map((c) => c[0])).toEqual(['fine']);
    expect([...tags]).toEqual(['fine']);
    expect(marks).toEqual([mark('fine', 'task')]);
  });
});

describe('SourceReads', () => {
  it('reads the sources of a round of 10 tags in one probe call, whatever the tags are', async () => {
    const tags = Array.from({ length: 10 }, (_, i) => `tag-${i}`);
    const probe = vi.fn<TagProbe>(async (asked) => Object.fromEntries(asked.map((tag) => [tag, ['task', 'actor'] as const])));
    const answer = await new SourceReads(probe).read([...tags, 'TAG-0'], []);
    expect(probe).toHaveBeenCalledTimes(1);
    expect([...probe.mock.calls[0]![0]].sort()).toEqual([...tags].sort());
    expect([...answer.fresh().answered].sort()).toEqual([...tags].sort());
    expect(answer.fresh().marks).toHaveLength(20);
    expect(answer.fresh().marks).toContainEqual(mark('tag-7', 'actor'));
  });

  it('fails every tag of a batch the server refused, and answers a tag with no entry as carrying nothing', async () => {
    const down = await new SourceReads(() => Promise.reject(new Error('down'))).read(['a', 'b']);
    expect([...down.tags].sort()).toEqual(['a', 'b']);
    expect(down.fresh().answered.size).toBe(0);
    const some = await new SourceReads(async () => ({ a: ['task'] as const })).read(['a', 'constructor']);
    expect([...some.fresh().answered].sort()).toEqual(['a', 'constructor']);
    expect(some.fresh().marks).toEqual([mark('a', 'task')]);
  });

  // A probe whose answers are released by hand, so the test chooses the order they land in.
  function gated() {
    const pending: { tag: string; release: (marks: TagMark[]) => void; fail: () => void }[] = [];
    const probe = (tag: string) =>
      new Promise<readonly TagMark[]>((resolve, reject) => pending.push({ tag, release: resolve, fail: () => reject(new Error('down')) }));
    return { pending, reads: new SourceReads(perTag(probe)) };
  }

  it('drops an older answer for a tag that lands after a newer read of it', async () => {
    const { pending, reads } = gated();
    const older = reads.read(['own']);
    const newer = reads.read(['own']);
    pending[1]!.release([mark('own', 'initiative')]);
    expect((await newer).fresh()).toEqual({ answered: new Set(['own']), marks: [mark('own', 'initiative')] });
    pending[0]!.release([mark('own', 'task'), mark('own', 'initiative')]);
    expect((await older).fresh()).toEqual({ answered: new Set(), marks: [] });
  });

  it('drops an older answer that landed first but is applied after a newer read began', async () => {
    const { pending, reads } = gated();
    const older = reads.read(['own']);
    pending[0]!.release([mark('own', 'task')]);
    const answer = await older;
    expect(answer.fresh().marks).toEqual([mark('own', 'task')]);
    void reads.read(['own']);
    expect(answer.fresh()).toEqual({ answered: new Set(), marks: [] });
  });

  it('keeps an older answer for the tags no newer read names, in any spelling', async () => {
    const { pending, reads } = gated();
    const older = reads.read(['own', 'ralph']);
    void reads.read(['  OWN ']);
    pending[0]!.release([mark('own', 'task')]);
    pending[1]!.release([mark('ralph', 'actor')]);
    const answer = await older;
    expect(answer.fresh()).toEqual({ answered: new Set(['ralph']), marks: [mark('ralph', 'actor')] });
    // validity is the read's own: the view's filter turns decide whether a restore still applies
    expect([...answer.tags].sort()).toEqual(['own', 'ralph']);
  });

  it('keeps nothing from a newer read that failed, and still drops the older answer', async () => {
    const { pending, reads } = gated();
    const older = reads.read(['own']);
    const newer = reads.read(['own']);
    pending[1]!.fail();
    expect((await newer).fresh()).toEqual({ answered: new Set(), marks: [] });
    pending[0]!.release([mark('own', 'task')]);
    expect((await older).fresh()).toEqual({ answered: new Set(), marks: [] });
  });

  it('names nothing from a read whose navigation was superseded, even when no newer read names its tag', async () => {
    const { pending, reads } = gated();
    let mine = true;
    const abandoned = reads.read(['own'], [], () => mine);
    mine = false; // a chip change made a newer navigation current, and read nothing for this tag
    pending[0]!.release([mark('own', 'task')]);
    expect((await abandoned).fresh()).toEqual({ answered: new Set(), marks: [] });
  });

  it('owes a chosen tag whose newest read is out or was abandoned, and no longer once a read of it was applied', async () => {
    const { pending, reads } = gated();
    expect(reads.owed(['own'])).toEqual([]);
    let mine = true;
    const abandoned = reads.read(['own', 'ralph'], [], () => mine);
    expect(reads.owed(['OWN', 'ralph', 'never'])).toEqual(['OWN', 'ralph']);
    mine = false;
    pending[0]!.release([mark('own', 'task')]);
    pending[1]!.release([mark('ralph', 'actor')]);
    (await abandoned).fresh();
    expect(reads.owed(['own', 'ralph'])).toEqual(['own', 'ralph']);
    const again = reads.read(['own']);
    pending[2]!.release([mark('own', 'task'), mark('own', 'initiative')]);
    expect((await again).fresh().marks).toEqual([mark('own', 'task'), mark('own', 'initiative')]);
    expect(reads.owed(['own', 'ralph'])).toEqual(['ralph']);
  });

  it('owes the tags a restoration claimed before it read them, until a read of them is applied', async () => {
    const { pending, reads } = gated();
    reads.claim(['own', 'kept']);
    expect(reads.owed(['own', 'kept', 'other'])).toEqual(['own', 'kept']);
    const own = reads.read(['own']);
    pending[0]!.release([mark('own', 'task')]);
    (await own).fresh();
    expect(reads.owed(['own', 'kept'])).toEqual(['kept']);
  });

  it('owes a tag again once a restoration claims it after it was read, and drops the older read\'s answer', async () => {
    const { pending, reads } = gated();
    const older = reads.read(['own']);
    reads.claim(['own']);
    pending[0]!.release([mark('own', 'task')]);
    expect((await older).fresh()).toEqual({ answered: new Set(), marks: [] });
    expect(reads.owed(['own'])).toEqual(['own']);
  });

  it('owes no tag the server\'s grammar refuses: no read can answer for it', async () => {
    const reads = new SourceReads(perTag(async () => []));
    void reads.read(['bad tag!']);
    expect(reads.owed(['bad tag!'])).toEqual([]);
  });

  it('checks a restored tag against the initiatives it is given', async () => {
    const reads = new SourceReads(perTag(async () => []));
    const answer = await reads.read(['work', 'ghost'], known);
    expect([...answer.tags]).toEqual([]);
    expect([...answer.fresh().answered].sort()).toEqual(['ghost', 'work']);
  });

  // The restore's lookup is out while an initiative event takes the tag off its last initiative: the answer is checked against
  // the catalog as it is now, not the one the lookup began under.
  describe('carriedAgainst the newest catalog', () => {
    const before = [{ tags: ['shared'] }];
    const after = [{ tags: [] as string[] }];

    it('keeps a tag a task confirms after the catalog stopped listing it, which the catalog it began under still did', async () => {
      const answer = await new SourceReads(perTag(async (tag) => [mark(tag, 'task'), mark(tag, 'initiative')])).read(['shared'], before);
      expect(answer.tags.size).toBe(0);
      expect([...answer.carriedAgainst(after, true)]).toEqual(['shared']);
      expect([...answer.carriedAgainst(after)]).toEqual(['shared']);
    });

    it('does not carry a tag the newest catalog still lists: the initiatives offer it', async () => {
      const answer = await new SourceReads(perTag(async (tag) => [mark(tag, 'task')])).read(['shared'], after);
      expect([...answer.tags]).toEqual(['shared']);
      expect(answer.carriedAgainst(before, true).size).toBe(0);
    });

    it('drops the initiative marks of an answer that predates a catalog read, and keeps the task and actor ones', async () => {
      const answer = await new SourceReads(perTag(async (tag) => [mark(tag, 'initiative'), mark(tag, 'task'), mark(tag, 'actor')])).read(['shared'], before);
      expect(answer.fresh().marks).toEqual([mark('shared', 'initiative'), mark('shared', 'task'), mark('shared', 'actor')]);
      expect(answer.fresh(true).marks).toEqual([mark('shared', 'task'), mark('shared', 'actor')]);
      expect([...answer.fresh(true).answered]).toEqual(['shared']);
    });

    it('drops a tag only an initiative source confirmed once the catalog moved and no initiative lists it', async () => {
      const answer = await new SourceReads(perTag(async (tag) => [mark(tag, 'initiative')])).read(['shared'], before);
      expect(answer.carriedAgainst(after, false).has('shared')).toBe(true);
      expect(answer.carriedAgainst(after, true).size).toBe(0);
    });

    it('keeps a tag whose check failed whatever the catalog says', async () => {
      const answer = await new SourceReads(perTag(() => Promise.reject(new Error('down')))).read(['shared'], before);
      expect([...answer.carriedAgainst(after, true)]).toEqual(['shared']);
    });
  });
});

describe('SourceRefresh', () => {
  const gate = () => {
    let resolve!: () => void;
    const promise = new Promise<void>((done) => (resolve = done));
    return { promise, resolve };
  };

  it('runs a round for a poke, handing it the extra tags', async () => {
    const seen: string[][] = [];
    const refresh = new SourceRefresh(async (extra) => void seen.push([...extra]));
    refresh.poke(['a']);
    await vi.waitFor(() => expect(seen).toEqual([['a']]));
  });

  it('calls an answer stale once an event came after its round began, and runs the next round with the same extra tags', async () => {
    const first = gate();
    const rounds: { extra: string[]; stale: () => boolean }[] = [];
    const refresh = new SourceRefresh(async (extra, stale) => {
      rounds.push({ extra: [...extra], stale });
      if (rounds.length === 1) await first.promise;
    });
    refresh.poke(['linked']);
    await vi.waitFor(() => expect(rounds).toHaveLength(1));
    expect(rounds[0].stale()).toBe(false);
    refresh.poke();
    expect(rounds[0].stale()).toBe(true);
    expect(rounds).toHaveLength(1); // one round at a time
    first.resolve();
    await vi.waitFor(() => expect(rounds).toHaveLength(2));
    expect(rounds[1].extra).toEqual(['linked']);
    expect(rounds[1].stale()).toBe(false);
  });

  it('merges events that land while a round is out into one more round, and forgets the extra tags once a round was applied', async () => {
    const first = gate();
    const rounds: string[][] = [];
    const refresh = new SourceRefresh(async (extra) => {
      rounds.push([...extra]);
      if (rounds.length === 1) await first.promise;
    });
    refresh.poke();
    await vi.waitFor(() => expect(rounds).toHaveLength(1));
    refresh.poke();
    refresh.poke(['x']);
    refresh.poke();
    first.resolve();
    await vi.waitFor(() => expect(rounds).toHaveLength(2));
    refresh.poke();
    await vi.waitFor(() => expect(rounds).toHaveLength(3));
    expect(rounds).toEqual([[], ['x'], []]);
  });

  it('survives a round that fails and runs again on the next event', async () => {
    let calls = 0;
    const refresh = new SourceRefresh(async () => {
      calls += 1;
      if (calls === 1) throw new Error('down');
    });
    refresh.poke();
    await vi.waitFor(() => expect(calls).toBe(1));
    refresh.poke();
    await vi.waitFor(() => expect(calls).toBe(2));
  });
});
