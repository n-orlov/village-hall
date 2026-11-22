// The Now view's Needs me data layer and row model (PRD R1, R9, SPEC 5.3, 6.8.1 and 11). `AttentionApi` reads
// `GET /attention/now` under the filter and the window, `AttentionStore` holds the latest answer (its attention part, In flight and Recently touched), and `rowsFor` turns one section into the rows the
// view draws: a title, a link to the task, detail lines and the actors involved, each with the way back to copy.
import { mountPrefix } from './apibase';
import type { components } from './api/schema';
import { initiativeOfHref } from './attentionActions';
import {
  ageText,
  askerWaitingLabel,
  apiQuery,
  DEFAULT_ATTENTION_FILTER,
  filterFromQuery,
  filterToQuery,
  type AttentionFilter,
  type SectionKey,
} from './attention';
import { mergeChoices, type ChipState, type TagChoice, type TagMark } from './chipFilter';
import { errorText, Fetches } from './fetches';
import { relativeTime } from './format';
import { problemText } from './keys';
import { DEFAULT_WINDOW, type InFlight, type NowWindow, type Recent } from './nowFlight';
import type { Browser } from './session';
import { clearViewQuery, openView, saveViewQuery, type ViewEnv, type ViewStorage } from './viewState';

export type Attention = components['schemas']['Attention'];
type NowBoard = components['schemas']['AttentionNow'];
type Actor = components['schemas']['AttentionActor'];
type BoardEvent = components['schemas']['Event'];

/** An actor on an item: what they did there, their handle and the way back the board has for them. */
export interface ItemActor {
  role: string;
  handle: string;
  wayBack: string | undefined;
}

/** One item of a section as the view draws it. */
export interface ItemRow {
  /** A key that is unique inside the section. */
  id: string;
  title: string;
  /** The fragment of the task the item is on, when it is on one. */
  href: string | undefined;
  /** The slug of the initiative the item belongs to, when it does (an ask's own initiative, or its task's). */
  initiative: string | undefined;
  details: string[];
  actors: ItemActor[];
  /** The instant the item is ordered by (an ask's creation, a lease's expiry, a watch's due time, when a task was done, when a conflict began). */
  at: string | undefined;
}

function actor(role: string, who: Actor): ItemActor {
  return { role, handle: who.handle, wayBack: who.way_back === undefined || who.way_back === '' ? undefined : who.way_back };
}

function taskHref(ref: string): string {
  return `#/t/${encodeURIComponent(ref)}`;
}

function askRows(items: NonNullable<Attention['open_asks']>): ItemRow[] {
  return items.map((a) => {
    const href = a.task === undefined ? undefined : taskHref(a.task);
    return {
      id: a.ref,
      title: `${a.ref}: ${a.question}`,
      href,
      initiative: a.initiative ?? initiativeOfHref(href),
      details: [`asked ${ageText(a.age_seconds)}`, a.mode === 'stop' ? 'asker stopped' : askerWaitingLabel(a.asker_waiting), ...(a.blocking ? ['blocking'] : [])],
      actors: [actor('asked by', a.asker)],
      at: a.created_at,
    };
  });
}

function doneRows(items: NonNullable<Attention['done_not_closed']>, now: Date): ItemRow[] {
  return items.map((t) => {
    const lease = t.last_lease;
    const outcome = lease?.outcome === undefined || lease.outcome === '' ? [] : [`last lease ${lease.lease}: ${lease.outcome}`];
    return {
      id: t.task,
      title: `${t.task} ${t.title}`,
      href: taskHref(t.task),
      initiative: initiativeOfHref(taskHref(t.task)),
      details: [`done ${relativeTime(t.done_at, now)}`, ...outcome],
      actors: [actor('done by', t.done_by), ...(lease === undefined ? [] : [actor('last lease', lease.holder)])],
      at: t.done_at,
    };
  });
}

function leaseRows(items: NonNullable<Attention['expired_leases']>, now: Date): ItemRow[] {
  return items.map((l) => ({
    id: l.lease,
    title: `${l.lease} on ${l.task} ${l.title}`,
    href: taskHref(l.task),
    initiative: initiativeOfHref(taskHref(l.task)),
    details: [l.intent, `expired ${relativeTime(l.expired_at, now)}`],
    actors: [actor('held by', l.holder)],
    at: l.expired_at,
  }));
}

function watchRows(items: NonNullable<Attention['overdue_watches']>, now: Date): ItemRow[] {
  return items.map((w) => ({
    id: w.task,
    title: `${w.task} ${w.title}`,
    href: taskHref(w.task),
    initiative: initiativeOfHref(taskHref(w.task)),
    details: [`check was due ${relativeTime(w.next_check_at, now)}`, ...(w.last_check === undefined ? [] : [`last check: ${w.last_check.summary}`])],
    actors: [actor('owner', w.owner), ...(w.last_check === undefined ? [] : [actor('last check by', w.last_check.by)])],
    at: w.next_check_at,
  }));
}

function conflictRows(items: NonNullable<Attention['conflicts']>): ItemRow[] {
  return items.map((c) => ({
    id: c.resource,
    title: c.resource,
    href: c.tasks.length === 1 ? taskHref(c.tasks[0]!) : undefined,
    initiative: initiativeOfHref(c.tasks.length === 1 ? taskHref(c.tasks[0]!) : undefined),
    details: c.holders.map((h) => `${h.lease} on ${h.task}: ${h.intent}`),
    actors: c.holders.map((h) => actor(h.lease, h.actor)),
    at: c.since,
  }));
}

const BUILDERS: Record<SectionKey, (attention: Attention, now: Date) => ItemRow[]> = {
  open_asks: (a) => askRows(a.open_asks ?? []),
  done_not_closed: (a, now) => doneRows(a.done_not_closed ?? [], now),
  expired_leases: (a, now) => leaseRows(a.expired_leases ?? [], now),
  overdue_watches: (a, now) => watchRows(a.overdue_watches ?? [], now),
  conflicts: (a) => conflictRows(a.conflicts ?? []),
};

/** The rows of one section; `now` phrases the ages. A section the answer lacks has no rows. */
export function rowsFor(key: SectionKey, attention: Attention, now: Date): ItemRow[] {
  return BUILDERS[key](attention, now);
}

/**
 * Whether a live event can change anything Now shows: everything but API keys and notes bound to no task. A note
 * bound to a task is that task's latest touch, so it moves the task's "last event" row and its window membership.
 */
export function affectsAttention(event: Pick<BoardEvent, 'type' | 'task'>): boolean {
  if (event.type.startsWith('key_')) return false;
  return !event.type.startsWith('note_') || !!event.task;
}

/**
 * Whether a live event can change the initiative catalog the filter controls offer: an initiative made, edited (its tags),
 * closed or reopened. Task events change the rows and their tag marks, which the board read carries.
 */
export function affectsInitiativeCatalog(event: Pick<BoardEvent, 'type'>): boolean {
  return event.type.startsWith('initiative_');
}

/**
 * Whether a live event can change the sources of a tag (PRD R1): an initiative's tags change with an initiative event, a task's
 * own tags and its initiative with a task event, and a derived actor-kind tag with a lease starting, ending or expiring (a
 * renewal, an ask, a note, a dependency or a check changes none).
 */
export function affectsTagSources(event: Pick<BoardEvent, 'type'>): boolean {
  return event.type.startsWith('initiative_') || event.type.startsWith('task_') || ['lease_started', 'lease_ended', 'lease_expired'].includes(event.type);
}

export function copyLabel(a: ItemActor): string {
  return `Copy way back of ${a.handle}`;
}

/**
 * The tag chips Now offers: the tags the board's initiatives carry (marked as an initiative's), every tag of every task the
 * board read shows (`marks`: its own, its initiative's and derived actor-kind tags, each with its source, so a tag
 * with two sources is one chip naming both), the ones already offered (`seen`: a narrower read must not take a chip away) and
 * the chosen ones (a link may name a tag nothing carries; the server still filters by it). The initiative chips are
 * the slugs of the same list, which the shared chip filter adds the chosen ones to.
 */
export function nowTagChoices(
  initiatives: readonly { tags: readonly string[] }[],
  chosen: readonly string[],
  marks: Readonly<Record<string, readonly TagMark[]>> = {},
  seen: readonly TagChoice[] = [],
): TagChoice[] {
  const own = initiatives.flatMap((i) => i.tags.map((tag) => ({ tag, source: 'initiative' as const })));
  return mergeChoices(seen, [...own, ...Object.values(marks).flat()], chosen);
}

/**
 * The query of `GET /attention/now` for the chip state and the window: a repeated `tag` for each tag chosen (all of them
 * must match), a repeated `initiative` for each initiative (any of them), and `window` only when it is not the default.
 * No chips and the default window make the empty query.
 */
export function nowQuery(chips: ChipState, window: NowWindow = DEFAULT_WINDOW): string {
  const params = apiQuery(chips);
  if (window !== DEFAULT_WINDOW) params.set('window', window);
  return params.toString();
}

export class AttentionApi {
  private readonly browser: Browser;

  constructor(browser: Browser) {
    this.browser = browser;
  }

  /** The whole Now answer under the filter and the window (the window is sent only when it is not the default). */
  async board(filter: AttentionFilter, window: NowWindow = DEFAULT_WINDOW): Promise<NowBoard> {
    const query = nowQuery(filter, window);
    const path = `/api/v1/attention/now${query === '' ? '' : `?${query}`}`;
    let response: Response;
    try {
      response = await this.browser.fetch(mountPrefix(this.browser.pathname) + path, {
        headers: { Accept: 'application/json' },
        credentials: 'same-origin',
      });
    } catch {
      throw new Error('The server is not reachable.');
    }
    if (!response.ok) throw new Error(await problemText(response, 'Reading what needs attention'));
    return (await response.json()) as NowBoard;
  }
}

export interface AttentionState {
  attention: Attention | undefined;
  /** The In flight and Recently touched parts of the same answer; undefined until it lands. */
  flight: InFlight[] | undefined;
  recent: Recent[] | undefined;
  /** The tag marks of every task the answer names, by ref (its `task_marks`): Now draws all its chips from them. */
  marks: Record<string, TagMark[]>;
  /**
   * The store's epoch when the read that gave `marks` began (the count of events that can change a tag's sources, as the
   * view keeps it): marks read before a later such event may name a source that event removed.
   */
  marksAt: number;
  filter: AttentionFilter;
  window: NowWindow;
  loading: boolean;
  error: string | undefined;
}

/**
 * The marks of the shown answer the chips may take sources from: all of them while no event that can change a tag's sources
 * arrived since its read began (`epoch` is that count now), else none. An answer read before such an event may name a
 * source the event removed; the read that event starts brings the current ones.
 */
export function currentMarks(state: Pick<AttentionState, 'marks' | 'marksAt'>, epoch: number): Record<string, TagMark[]> {
  return state.marksAt === epoch ? state.marks : {};
}

/** The marks of every task a board answer names, by ref, from its `task_marks`. */
export function marksOfBoard(board: Pick<NowBoard, 'task_marks'>): Record<string, TagMark[]> {
  return Object.fromEntries(Object.entries(board.task_marks).map(([ref, task]) => [ref, [...task.tag_marks]]));
}

export class AttentionStore {
  private readonly api: Pick<AttentionApi, 'board'>;
  private readonly onChange: (state: AttentionState) => void;
  private current: AttentionState;
  private readonly fetches = new Fetches();
  private readonly epoch: () => number;

  /** `epoch` counts the events that can change a tag's sources; each answer's marks carry its value when the read began. */
  constructor(
    api: Pick<AttentionApi, 'board'>,
    onChange: (state: AttentionState) => void,
    filter: AttentionFilter = DEFAULT_ATTENTION_FILTER,
    window: NowWindow = DEFAULT_WINDOW,
    epoch: () => number = () => 0,
  ) {
    this.api = api;
    this.onChange = onChange;
    this.epoch = epoch;
    this.current = { attention: undefined, flight: undefined, recent: undefined, marks: {}, marksAt: epoch(), filter, window, loading: true, error: undefined };
  }

  private publish(change: Partial<AttentionState>): void {
    this.current = { ...this.current, ...change };
    this.onChange(this.current);
  }

  /** Reads the sections under the current filter. A refresh keeps what is shown until the answer lands. */
  async load(): Promise<void> {
    const ticket = this.fetches.begin();
    const marksAt = this.epoch();
    const { filter, window } = this.current;
    this.publish({ loading: true });
    let outcome: Partial<AttentionState>;
    try {
      const board = await this.api.board(filter, window);
      outcome = { attention: board.attention, flight: board.in_flight, recent: board.recent, marks: marksOfBoard(board), marksAt, error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    // An answer that lands after a newer one, or for a filter no longer set, is dropped.
    const show = this.fetches.end(ticket) && filter === this.current.filter && window === this.current.window;
    this.publish({ ...(show ? outcome : {}), loading: this.fetches.busy });
  }

  async setFilter(filter: AttentionFilter): Promise<void> {
    this.current = { ...this.current, filter };
    await this.load();
  }

  /** Reads the board again for another window. */
  async setWindow(window: NowWindow): Promise<void> {
    this.current = { ...this.current, window };
    await this.load();
  }
}

/** The name of the view's saved state (PRD R16): the key is `vh.view.v1.attention`, kept from when the view was Attention. */
export const ATTENTION_VIEW = 'attention';

/** What opening the view found: the filter to start with, and whether it came from the saved query (a bare route). */
export interface OpenedAttention {
  filter: AttentionFilter;
  restored: boolean;
  /** Whether Recently touched hides closed tasks (`exclude=closed` in the query). */
  excludeClosed: boolean;
}

/** The query key and value of "Exclude closed" on Recently touched: `#/now?exclude=closed`. Never sent to the server. */
export const EXCLUDE_PARAM = 'exclude';
export const EXCLUDE_CLOSED = 'closed';

/** Whether a fragment query asks Recently touched to hide closed tasks. */
export function excludeClosedOf(query: string): boolean {
  return new URLSearchParams(query).getAll(EXCLUDE_PARAM).includes(EXCLUDE_CLOSED);
}

/** The whole query of the view, for the fragment and the saved state: the filter's, then `exclude=closed` when it is on. */
export function nowViewQuery(filter: AttentionFilter, excludeClosed: boolean): string {
  const params = new URLSearchParams(filterToQuery(filter));
  if (excludeClosed) params.append(EXCLUDE_PARAM, EXCLUDE_CLOSED);
  return params.toString();
}

/** The fragment of the view: `#/now`, or `#/now?...` when a filter or "Exclude closed" is set. */
export function nowFragment(filter: AttentionFilter, excludeClosed: boolean): string {
  const query = nowViewQuery(filter, excludeClosed);
  return query === '' ? '#/now' : `#/now?${query}`;
}

/**
 * Opens the view for the fragment it is on, before its first fetch. A fragment with a query wins and is saved; a bare
 * one (`#/now`, or any fragment the router sends here) restores the saved query by rewriting the fragment in
 * place, so the view reads `GET /attention` once, under the restored filter.
 */
export function openAttention(env: ViewEnv, hash: string): OpenedAttention {
  const at = hash.indexOf('?');
  const own = at < 0 ? '' : hash.slice(at + 1);
  const query = openView(env, ATTENTION_VIEW, '#/now', own);
  const filter = filterFromQuery(query);
  // Only a restored filter that names something is checked against the board; "Exclude closed" alone needs no check.
  return { filter, restored: own === '' && filterToQuery(filter) !== '', excludeClosed: excludeClosedOf(query) };
}

/** Saves the view's query after a change (the empty filter with "Exclude closed" off is the default and removes the key). */
export function saveAttention(storage: ViewStorage | undefined, filter: AttentionFilter, excludeClosed = false): void {
  saveViewQuery(storage, ATTENTION_VIEW, nowViewQuery(filter, excludeClosed));
}

/** Clear filters: forgets the saved query. */
export function forgetAttention(storage: ViewStorage | undefined): void {
  clearViewQuery(storage, ATTENTION_VIEW);
}

const NO_TAGS: ReadonlySet<string> = new Set();

/**
 * A restored filter without the initiatives and tags the board no longer has: `known` is the list of initiatives with
 * their tags, and `carried` the restored tags some task carries (its own or a derived actor kind; `carriedTags` in
 * lib/tagCarry.ts), which are valid too. A filter with nothing to drop is returned as it is (the same object).
 */
export function dropUnknown(
  filter: AttentionFilter,
  known: readonly { slug: string; tags: readonly string[] }[],
  carried: ReadonlySet<string> = NO_TAGS,
): AttentionFilter {
  const slugs = new Set(known.map((i) => i.slug));
  const tags = new Set([...known.flatMap((i) => i.tags), ...carried]);
  const initiatives = filter.initiatives.filter((slug) => slugs.has(slug));
  const kept = filter.tags.filter((tag) => tags.has(tag));
  if (initiatives.length === filter.initiatives.length && kept.length === filter.tags.length) return filter;
  return { initiatives, tags: kept };
}

/**
 * The filter the view starts from (or moves to on a hash change). Only a restored filter is checked, and only once the
 * lookup answered: `known` is the board's initiatives with their tags, or `undefined` when the lookup failed. An empty
 * list is an answer (the board has no initiatives), so every restored initiative and tag is dropped. A link's own query
 * is kept as it is, and the sections show the server's answer for it.
 */
export function startFilter(
  opened: Pick<OpenedAttention, 'filter' | 'restored'>,
  known: readonly { slug: string; tags: readonly string[] }[] | undefined,
  carried: ReadonlySet<string> = NO_TAGS,
): AttentionFilter {
  if (!opened.restored || known === undefined) return opened.filter;
  return dropUnknown(opened.filter, known, carried);
}
