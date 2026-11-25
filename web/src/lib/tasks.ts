// The Tasks view's data layer (PRD R10, SPEC 11). `TasksApi` wraps the task operations the view and the New task
// dialog use; `TasksStore` holds the list under a filter; `NewTaskStore` holds what the dialog needs (the open
// initiatives and open tasks to pick from) and the refusal of the last create.
import { mountPrefix } from './apibase';
import { canonicalTag, type TagMark } from './chipFilter';
import type { components } from './api/schema';
import { errorText, Fetches } from './fetches';
import { problemText } from './keys';
import { readMarks, tagsInUse } from './taskTags';
import { InitiativesApi, InitiativesError, type InitiativeListItem, type TaskListItem } from './initiatives';
import { toTaskCreate, type NewTaskForm, type TaskCreate } from './newTask';
import { apiQuery, DEFAULT_TASKS_FILTER, type TasksFilter } from './taskFilters';
import type { Ask } from './asks';
import { readNeeds, type NeedsAttention, type TaskList } from './needsAttention';
import type { Note, NotePost } from './notes';
import type { Browser } from './session';

export type TaskDetail = components['schemas']['TaskDetail'];
export type TaskPatch = components['schemas']['TaskPatch'];
export type TaskEvent = components['schemas']['Event'];
export type Lease = components['schemas']['Lease'];
export type Resolution = 'completed' | 'dropped' | 'superseded' | 'duplicate';

/** A page of a task's events is as large as the API allows, so a busy task takes few requests. */
const EVENT_PAGE = '1000';

export class TasksApi {
  private readonly browser: Browser;
  private readonly csrf: () => string;

  /** `csrf` is read at each write, so a page that rotates its token is followed. */
  constructor(browser: Browser, csrf: () => string) {
    this.browser = browser;
    this.csrf = csrf;
  }

  private async call(path: string, what: string, init: RequestInit): Promise<Response> {
    let response: Response;
    try {
      response = await this.browser.fetch(mountPrefix(this.browser.pathname) + path, { ...init, credentials: 'same-origin' });
    } catch {
      throw new InitiativesError(0, 'The server is not reachable.');
    }
    if (!response.ok) throw new InitiativesError(response.status, await problemText(response, what));
    return response;
  }

  /** Every task the query keeps, following `next_after` through the pages. */
  async listQuery(query: URLSearchParams): Promise<TaskListItem[]> {
    const items: TaskListItem[] = [];
    let after: string | undefined;
    do {
      const page = new URLSearchParams(query);
      if (after !== undefined) page.set('after', after);
      const response = await this.call(`/api/v1/tasks?${page.toString()}`, 'Listing the tasks', { headers: { Accept: 'application/json' } });
      const body = (await response.json()) as components['schemas']['TaskList'];
      items.push(...body.items);
      after = body.next_after;
    } while (after !== undefined);
    return items;
  }

  /** How many tasks the query keeps, from the count form of the list: one request, `{count}` and no rows. */
  async count(query: URLSearchParams): Promise<number> {
    const response = await this.call(`/api/v1/tasks/count?${query.toString()}`, 'Counting the tasks', { headers: { Accept: 'application/json' } });
    return ((await response.json()) as components['schemas']['TaskCount']).count;
  }

  /**
   * The marks of `tag` on the tasks that match it, of any status, under the server's rule (own, initiative or derived actor
   * kind): one mark per source some task shows it by. Empty when no task matches. It reads pages until all three sources
   * are found or the tasks end, so a tag's sources do not depend on which task the server lists first.
   */
  async tagMarks(tag: string): Promise<TagMark[]> {
    const found = new Map<string, TagMark>();
    const wanted = canonicalTag(tag);
    let after: string | undefined;
    do {
      const query = new URLSearchParams({ status: 'all', limit: '1000' });
      query.append('tag', wanted);
      if (after !== undefined) query.set('after', after);
      const response = await this.call(`/api/v1/tasks?${query.toString()}`, 'Looking for tasks with the tag', { headers: { Accept: 'application/json' } });
      const body = (await response.json()) as components['schemas']['TaskList'];
      for (const task of body.items) for (const mark of task.tag_marks) if (mark.tag === wanted && !found.has(mark.source)) found.set(mark.source, mark);
      after = found.size < 3 ? body.next_after : undefined;
    } while (after !== undefined);
    return [...found.values()];
  }

  /** The Needs attention section for a `GET /tasks` query: the first 200 tasks that need a person and how many more there are. */
  needs(query: URLSearchParams): Promise<NeedsAttention> {
    return readNeeds(async (page) => {
      const response = await this.call(`/api/v1/tasks?${page.toString()}`, 'Listing the tasks that need attention', { headers: { Accept: 'application/json' } });
      return (await response.json()) as TaskList;
    }, query);
  }

  /** The tasks a filter keeps, across initiatives. */
  list(filter: TasksFilter): Promise<TaskListItem[]> {
    return this.listQuery(apiQuery(filter));
  }

  /** The open tasks of every initiative: what a new task can be blocked by. */
  openTasks(): Promise<TaskListItem[]> {
    return this.listQuery(new URLSearchParams({ status: 'open' }));
  }

  /** A write: JSON in (when there is a body), the answer out. */
  private async write<T = TaskDetail>(method: string, path: string, what: string, body?: object): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json', 'X-CSRF-Token': this.csrf() };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const response = await this.call(path, what, { method, headers, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    return (await response.json()) as T;
  }

  create(body: TaskCreate): Promise<TaskDetail> {
    return this.write('POST', '/api/v1/tasks', 'Creating the task', body);
  }

  /** The task with its dependencies, blocking tasks and live leases. */
  async get(ref: string): Promise<TaskDetail> {
    const response = await this.call(`/api/v1/tasks/${encodeURIComponent(ref)}`, 'Reading the task', { headers: { Accept: 'application/json' } });
    return (await response.json()) as TaskDetail;
  }

  /** Every event of the task in the order they were written, following `next_after` through the pages. */
  async events(ref: string): Promise<TaskEvent[]> {
    const items: TaskEvent[] = [];
    let after: string | undefined;
    do {
      const query = new URLSearchParams({ limit: EVENT_PAGE });
      if (after !== undefined) query.set('after', after);
      const response = await this.call(`/api/v1/tasks/${encodeURIComponent(ref)}/events?${query.toString()}`, 'Reading the task events', {
        headers: { Accept: 'application/json' },
      });
      const page = (await response.json()) as components['schemas']['EventList'];
      items.push(...page.items);
      after = page.next_after;
    } while (after !== undefined);
    return items;
  }

  patch(ref: string, body: TaskPatch): Promise<TaskDetail> {
    return this.write('PATCH', `/api/v1/tasks/${encodeURIComponent(ref)}`, 'Saving the task', body);
  }

  /** Marks the task done; with `force` every live lease on it is ended first (what the operator confirms over another actor's lease). */
  done(ref: string, force = false): Promise<TaskDetail> {
    return this.write('POST', `/api/v1/tasks/${encodeURIComponent(ref)}/done`, 'Marking the task done', force ? { force: true } : undefined);
  }

  reopen(ref: string): Promise<TaskDetail> {
    return this.write('POST', `/api/v1/tasks/${encodeURIComponent(ref)}/reopen`, 'Reopening the task');
  }

  close(ref: string, resolution: Resolution): Promise<TaskDetail> {
    return this.write('POST', `/api/v1/tasks/${encodeURIComponent(ref)}/close`, 'Closing the task', { resolution });
  }

  private depPath(ref: string, blockedBy: string): string {
    return `/api/v1/tasks/${encodeURIComponent(ref)}/deps/${encodeURIComponent(blockedBy)}`;
  }

  addDep(ref: string, blockedBy: string): Promise<TaskDetail> {
    return this.write('POST', this.depPath(ref, blockedBy), 'Adding the dependency');
  }

  removeDep(ref: string, blockedBy: string): Promise<TaskDetail> {
    return this.write('DELETE', this.depPath(ref, blockedBy), 'Removing the dependency');
  }

  /** Gives the task one more of its own tags (`PUT /tasks/{ref}/tags/{tag}`); a tag it has is a no-op the server answers 200. */
  addTag(ref: string, tag: string): Promise<TaskDetail> {
    return this.write('PUT', `/api/v1/tasks/${encodeURIComponent(ref)}/tags/${encodeURIComponent(tag)}`, 'Adding the tag');
  }

  /** Takes one of the task's own tags off it (`DELETE /tasks/{ref}/tags/{tag}`). */
  removeTag(ref: string, tag: string): Promise<TaskDetail> {
    return this.write('DELETE', `/api/v1/tasks/${encodeURIComponent(ref)}/tags/${encodeURIComponent(tag)}`, 'Removing the tag');
  }

  /** The tags in use on the board, for the suggestions typing a tag offers: those of the first 1000 tasks of any status, sorted. */
  async tagsInUse(): Promise<string[]> {
    const response = await this.call('/api/v1/tasks?status=all&limit=1000', 'Reading the tags in use', { headers: { Accept: 'application/json' } });
    return tagsInUse(...((await response.json()) as TaskList).items.map((task) => task.tag_marks));
  }

  /** The tag marks of the tasks `refs` name, by ref, whatever their status: what a list that carries no marks shows as chips. */
  marks(refs: readonly string[]): Promise<Record<string, TagMark[]>> {
    return readMarks(async (query) => {
      const response = await this.call(`/api/v1/tasks?${query.toString()}`, 'Reading the tags', { headers: { Accept: 'application/json' } });
      return (await response.json()) as TaskList;
    }, refs);
  }

  milestone(ref: string, summary: string): Promise<TaskDetail> {
    return this.write('POST', `/api/v1/tasks/${encodeURIComponent(ref)}/milestones`, 'Recording the milestone', { summary });
  }

  recordCheck(ref: string, summary: string): Promise<TaskDetail> {
    return this.write('POST', `/api/v1/tasks/${encodeURIComponent(ref)}/checks`, 'Recording the check', { summary });
  }

  /** Every lease a query keeps, following `next_after` through the pages. */
  private async leasePages(query: URLSearchParams, what: string): Promise<Lease[]> {
    const items: Lease[] = [];
    let after: string | undefined;
    do {
      const page = new URLSearchParams(query);
      if (after !== undefined) page.set('after', after);
      const response = await this.call(`/api/v1/leases?${page.toString()}`, what, { headers: { Accept: 'application/json' } });
      const body = (await response.json()) as components['schemas']['LeaseList'];
      items.push(...body.items);
      after = body.next_after;
    } while (after !== undefined);
    return items;
  }

  /** Every lease of the task that ran out (acknowledged or not). */
  expiredLeases(ref: string): Promise<Lease[]> {
    return this.leasePages(new URLSearchParams({ task: ref, state: 'expired', limit: EVENT_PAGE }), 'Reading the expired leases');
  }

  /** Every live lease on the board, for the Now panel and the conflicts of the task page. */
  liveLeases(): Promise<Lease[]> {
    return this.leasePages(new URLSearchParams({ state: 'live', limit: EVENT_PAGE }), 'Reading the live leases');
  }

  /** Ends a live lease; the outcome is sent only when there is one. */
  endLease(lease: string, outcome: string): Promise<Lease> {
    return this.write<Lease>('POST', `/api/v1/leases/${encodeURIComponent(lease)}/end`, 'Ending the lease', outcome === '' ? undefined : { outcome });
  }

  ackLease(lease: string): Promise<Lease> {
    return this.write<Lease>('POST', `/api/v1/leases/${encodeURIComponent(lease)}/ack`, 'Acknowledging the lease');
  }

  /** Every ask of the task, whatever its state, in raising order. */
  async asksOf(task: string): Promise<Ask[]> {
    const items: Ask[] = [];
    let after: string | undefined;
    do {
      const query = new URLSearchParams({ task, state: 'all', limit: EVENT_PAGE });
      if (after !== undefined) query.set('after', after);
      const response = await this.call(`/api/v1/asks?${query.toString()}`, 'Reading the asks', { headers: { Accept: 'application/json' } });
      const page = (await response.json()) as components['schemas']['AskList'];
      items.push(...page.items);
      after = page.next_after;
    } while (after !== undefined);
    return items;
  }

  answerAsk(ask: string, answer: string): Promise<Ask> {
    return this.write<Ask>('POST', `/api/v1/asks/${encodeURIComponent(ask)}/answer`, 'Answering the ask', { answer });
  }

  withdrawAsk(ask: string, reason: string): Promise<Ask> {
    return this.write<Ask>('POST', `/api/v1/asks/${encodeURIComponent(ask)}/withdraw`, 'Withdrawing the ask', reason === '' ? {} : { reason });
  }

  /** Every note on the task, without bodies, in posting order. */
  async notesOf(task: string): Promise<Note[]> {
    const items: Note[] = [];
    let after: string | undefined;
    do {
      const query = new URLSearchParams({ task, fields: 'summary', limit: EVENT_PAGE });
      if (after !== undefined) query.set('after', after);
      const response = await this.call(`/api/v1/notes?${query.toString()}`, 'Reading the notes', { headers: { Accept: 'application/json' } });
      const page = (await response.json()) as components['schemas']['NoteList'];
      items.push(...page.items);
      after = page.next_after;
    } while (after !== undefined);
    return items;
  }

  /** One note with its body. */
  async note(ref: string): Promise<Note> {
    const response = await this.call(`/api/v1/notes/${encodeURIComponent(ref)}`, 'Reading the note', { headers: { Accept: 'application/json' } });
    return (await response.json()) as Note;
  }

  postNote(body: NotePost): Promise<Note> {
    return this.write<Note>('POST', '/api/v1/notes', 'Posting the note', body);
  }
}

export interface TasksState {
  tasks: TaskListItem[];
  filter: TasksFilter;
  loading: boolean;
  /** Why the list could not be read. */
  error: string | undefined;
}

type ListApi = Pick<TasksApi, 'list'>;

export class TasksStore {
  private readonly api: ListApi;
  private readonly onChange: (state: TasksState) => void;
  private current: TasksState;
  private readonly fetches = new Fetches();

  constructor(api: ListApi, onChange: (state: TasksState) => void, filter: TasksFilter = DEFAULT_TASKS_FILTER) {
    this.api = api;
    this.onChange = onChange;
    this.current = { tasks: [], filter, loading: true, error: undefined };
  }

  get state(): TasksState {
    return this.current;
  }

  private set(patch: Partial<TasksState>): void {
    this.current = { ...this.current, ...patch };
    this.onChange(this.current);
  }

  /** Fetches the list under the current filter; of overlapping fetches the newest outcome to land is shown. */
  async load(): Promise<void> {
    const ticket = this.fetches.begin();
    this.set({ loading: true });
    let outcome: Partial<TasksState>;
    try {
      outcome = { tasks: await this.api.list(this.current.filter), error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    const shown = this.fetches.end(ticket) ? outcome : {};
    this.set({ ...shown, loading: this.fetches.busy });
  }

  /**
   * Reads the list again for a live event: the rows stay on screen (no `loading`, so scroll and focus hold), a row that
   * left the filter (closed elsewhere) just goes, and a failed read keeps the list it had and says why in the inline error,
   * never as a toast. Overlapping fetches obey the same newest-wins rule as `load`.
   */
  async refresh(): Promise<void> {
    const ticket = this.fetches.begin(true);
    let outcome: Partial<TasksState>;
    try {
      outcome = { tasks: await this.api.list(this.current.filter), error: undefined };
    } catch (e) {
      outcome = { error: errorText(e) };
    }
    const shown = this.fetches.end(ticket) ? outcome : {};
    this.set({ ...shown, loading: this.fetches.busy });
  }

  /** Switches to a filter and fetches the list under it. */
  async setFilter(filter: TasksFilter): Promise<void> {
    this.set({ filter });
    await this.load();
  }
}

/** Whether a live event can change the Tasks list: any event that names a task (its lease, asks, notes) and an initiative's own (its close drops tasks). */
export function affectsTasks(event: { type: string; task?: string }): boolean {
  return event.task !== undefined || event.type.startsWith('initiative_');
}

export interface NewTaskState {
  /** The open initiatives the task can go in. */
  initiatives: InitiativeListItem[];
  /** The open tasks the task can be blocked by. */
  openTasks: TaskListItem[];
  loading: boolean;
  /** Why the lists could not be read, or why the last create was refused; shown in the form, which keeps what was typed. */
  error: string | undefined;
}

type NewTaskApi = Pick<TasksApi, 'openTasks' | 'create'> & { initiatives: () => Promise<InitiativeListItem[]> };

/** The New task dialog's `initiatives` source: the open initiatives. */
export function openInitiatives(api: Pick<InitiativesApi, 'list'>): () => Promise<InitiativeListItem[]> {
  return () => api.list('open');
}

export class NewTaskStore {
  private readonly api: NewTaskApi;
  private readonly onChange: (state: NewTaskState) => void;
  private current: NewTaskState = { initiatives: [], openTasks: [], loading: true, error: undefined };

  constructor(api: NewTaskApi, onChange: (state: NewTaskState) => void) {
    this.api = api;
    this.onChange = onChange;
  }

  get state(): NewTaskState {
    return this.current;
  }

  private set(patch: Partial<NewTaskState>): void {
    this.current = { ...this.current, ...patch };
    this.onChange(this.current);
  }

  /** Reads what the pickers offer. */
  async load(): Promise<void> {
    try {
      const [initiatives, openTasks] = await Promise.all([this.api.initiatives(), this.api.openTasks()]);
      this.set({ initiatives, openTasks, loading: false, error: undefined });
    } catch (e) {
      this.set({ loading: false, error: errorText(e) });
    }
  }

  /** Makes the task. Its ref when it worked; undefined on a refusal, and `error` says why. */
  async create(form: NewTaskForm): Promise<string | undefined> {
    const built = toTaskCreate(form);
    if (!built.ok) {
      this.set({ error: built.error });
      return undefined;
    }
    try {
      const task = await this.api.create(built.body);
      this.set({ error: undefined });
      return task.ref;
    } catch (e) {
      this.set({ error: errorText(e) });
      return undefined;
    }
  }

  /** Forgets a refusal. */
  clearError(): void {
    this.set({ error: undefined });
  }
}
