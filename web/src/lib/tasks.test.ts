import { afterEach, describe, expect, it, vi } from 'vitest';
import { InitiativesError, type InitiativeListItem, type TaskListItem } from './initiatives';
import { emptyNewTask } from './newTask';
import { DEFAULT_TASKS_FILTER } from './taskFilters';
import { affectsTasks, NewTaskStore, openInitiatives, TasksApi, TasksStore, watchTasks, type NewTaskState, type TasksState } from './tasks';
import { DEBOUNCE_MS, Live, type LiveEnv, type StreamSource } from './live';
import type { Browser } from './session';

const task = (ref: string) => ({ id: 'id-' + ref, ref, title: 'Title ' + ref }) as TaskListItem;
const json = (body: object, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const problem = (status: number, body: object) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/problem+json' } });

function api(respond: (url: string, init?: RequestInit) => Promise<Response>, pathname = '/') {
  const fetchFn = vi.fn((input: string, init?: RequestInit) => respond(input, init));
  const browser: Browser = { fetch: fetchFn, assign: vi.fn(), pathname, setHash: vi.fn() };
  return { api: new TasksApi(browser, () => 'csrf-1'), fetchFn };
}

describe('TasksApi', () => {
  it('lists the tasks of a filter with the API query, following next_after through the pages', async () => {
    const { api: a, fetchFn } = api(async (url) =>
      url.includes('after=') ? json({ items: [task('aa-2')] }) : json({ items: [task('aa-1')], next_after: 'aa-1' }),
    );
    const items = await a.list({ ...DEFAULT_TASKS_FILTER, kind: 'watch', blocked: 'yes', text: 'a b' });
    expect(items.map((t) => t.ref)).toEqual(['aa-1', 'aa-2']);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual([
      '/api/v1/tasks?status=not_closed&kind=watch&blocked=true&q=a+b&limit=1000',
      '/api/v1/tasks?status=not_closed&kind=watch&blocked=true&q=a+b&limit=1000&after=aa-1',
    ]);
    expect(fetchFn.mock.calls[0]![1]).toMatchObject({ credentials: 'same-origin', headers: { Accept: 'application/json' } });
  });

  it('reads one refresh of 600 not-closed tasks in one list call at limit=1000', async () => {
    const six = Array.from({ length: 600 }, (_, i) => task(`aa-${i + 1}`));
    const { api: a, fetchFn } = api(async () => json({ items: six }));
    const items = await a.list(DEFAULT_TASKS_FILTER);
    expect(items).toHaveLength(600);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/tasks?status=not_closed&limit=1000']);
  });

  it('counts the tasks of a query with the count form: one request, the count and no rows', async () => {
    const { api: a, fetchFn } = api(async () => json({ count: 7 }));
    expect(await a.count(new URLSearchParams({ status: 'open', active: 'true' }))).toBe(7);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/tasks/count?status=open&active=true']);
    expect(fetchFn.mock.calls[0]![1]).toMatchObject({ credentials: 'same-origin', headers: { Accept: 'application/json' } });
  });

  it('reads the open tasks of every initiative', async () => {
    const { api: a, fetchFn } = api(async () => json({ items: [task('aa-1')] }));
    expect((await a.openTasks()).map((t) => t.ref)).toEqual(['aa-1']);
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks?status=open');
  });

  it('reads the marks a tag has on the tasks that match it, one per source, across pages until all three are found', async () => {
    const m = (tag: string, source: string) => ({ tag, source });
    const { api: a, fetchFn } = api(async (url) =>
      url.includes('after=aa-2')
        ? json({ items: [{ ...task('aa-3'), tag_marks: [m('has', 'actor'), m('has', 'initiative')] }] })
        : json({ items: [{ ...task('aa-1'), tag_marks: [m('has', 'task'), m('other', 'task')] }, { ...task('aa-2'), tag_marks: [m('has', 'task')] }], next_after: 'aa-2' }),
    );
    expect(await a.tagMarks('has')).toEqual([m('has', 'task'), m('has', 'actor'), m('has', 'initiative')]);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/tasks?status=all&limit=1000&tag=has', '/api/v1/tasks?status=all&limit=1000&tag=has&after=aa-2']);
  });

  it('reads the marks of a tag spelled otherwise (padded, upper case) under the server\'s spelling', async () => {
    const m = (source: string) => ({ tag: 'ralph', source });
    const { api: a, fetchFn } = api(async () => json({ items: [{ ...task('aa-1'), tag_marks: [m('actor'), { tag: 'other', source: 'task' }] }] }));
    expect(await a.tagMarks(' RALPH ')).toEqual([m('actor')]);
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks?status=all&limit=1000&tag=ralph');
  });

  it('stops reading once every source is found, and has no marks for a tag nothing carries', async () => {
    const m = (source: string) => ({ tag: 'has', source });
    const { api: a, fetchFn } = api(async (url) =>
      url.includes('tag=has') ? json({ items: [{ ...task('aa-1'), tag_marks: [m('task'), m('actor'), m('initiative')] }], next_after: 'aa-1' }) : json({ items: [] }),
    );
    expect(await a.tagMarks('has')).toHaveLength(3);
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(await a.tagMarks('none')).toEqual([]);
  });

  it('refuses the tag read when the server does', async () => {
    const { api: a } = api(async () => problem(500, { title: 'boom' }));
    await expect(a.tagMarks('x')).rejects.toBeInstanceOf(InitiativesError);
  });

  it('honours the mount prefix', async () => {
    const { api: a, fetchFn } = api(async () => json({ items: [] }), '/board/');
    await a.openTasks();
    expect(fetchFn.mock.calls[0]![0]).toBe('/board/api/v1/tasks?status=open');
  });

  it('posts a create with the JSON body and the CSRF token and returns the task', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }, 201));
    const made = await a.create({ initiative: 'aa', title: 'T' });
    expect(made.ref).toBe('aa-1');
    const [url, init] = fetchFn.mock.calls[0]!;
    expect(url).toBe('/api/v1/tasks');
    expect(init).toMatchObject({ method: 'POST', credentials: 'same-origin', body: '{"initiative":"aa","title":"T"}' });
    expect(init!.headers).toMatchObject({ 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-1' });
  });

  it('turns a refusal into an error carrying the status and the problem detail', async () => {
    const { api: a } = api(async () => problem(409, { title: 'Conflict', detail: 'initiative is closed' }));
    await expect(a.create({ initiative: 'aa', title: 'T' })).rejects.toMatchObject({ status: 409, message: expect.stringContaining('initiative is closed') });
  });

  it('says the server is not reachable when the request fails', async () => {
    const { api: a } = api(async () => {
      throw new Error('offline');
    });
    await expect(a.openTasks()).rejects.toEqual(new InitiativesError(0, 'The server is not reachable.'));
  });
});

describe('TasksStore', () => {
  function store(list: () => Promise<TaskListItem[]>) {
    const seen: TasksState[] = [];
    return { s: new TasksStore({ list }, (st) => seen.push(st)), seen };
  }

  it('starts loading under the default filter, or the one it is given', () => {
    expect(store(async () => []).s.state).toEqual({ tasks: [], filter: DEFAULT_TASKS_FILTER, loading: true, error: undefined });
    const filter = { ...DEFAULT_TASKS_FILTER, kind: 'watch' as const };
    expect(new TasksStore({ list: async () => [] }, () => undefined, filter).state.filter).toBe(filter);
  });

  it('loads the tasks and stops loading', async () => {
    const { s } = store(async () => [task('aa-1')]);
    await s.load();
    expect(s.state).toMatchObject({ loading: false, error: undefined });
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['aa-1']);
  });

  it('shows the error of a failed load and stops loading', async () => {
    const { s } = store(async () => {
      throw new InitiativesError(500, 'boom');
    });
    await s.load();
    expect(s.state).toMatchObject({ loading: false, error: 'boom', tasks: [] });
  });

  it('fetches under the new filter when it is set', async () => {
    const list = vi.fn(async () => [task('aa-1')]);
    const { s } = store(list);
    const filter = { ...DEFAULT_TASKS_FILTER, status: 'done' as const };
    await s.setFilter(filter);
    expect(list).toHaveBeenCalledWith(filter);
    expect(s.state.filter).toBe(filter);
  });

  it('shows the newest fetch when an older one lands after it', async () => {
    const resolvers: ((t: TaskListItem[]) => void)[] = [];
    const { s } = store(() => new Promise((resolve) => resolvers.push(resolve)));
    const first = s.load();
    const second = s.load();
    resolvers[1]!([task('new')]);
    await second;
    resolvers[0]!([task('old')]);
    await first;
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['new']);
    expect(s.state.loading).toBe(false);
  });
});

describe('TasksStore refresh (a live event)', () => {
  const tick = () => new Promise((r) => setTimeout(r, 0));
  function gated() {
    const gates: { resolve: (t: TaskListItem[]) => void; reject: (e: unknown) => void }[] = [];
    const list = () => new Promise<TaskListItem[]>((resolve, reject) => gates.push({ resolve, reject }));
    return { gates, list };
  }

  it('swaps the rows without making the list loading, and a row that left the filter goes', async () => {
    const { gates, list } = gated();
    const seen: boolean[] = [];
    const s = new TasksStore({ list }, (st) => seen.push(st.loading));
    const first = s.load();
    gates[0]!.resolve([task('aa-1'), task('aa-2')]);
    await first;
    seen.length = 0;
    const again = s.refresh();
    await tick();
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['aa-1', 'aa-2']);
    gates[1]!.resolve([task('aa-1')]);
    await again;
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['aa-1']);
    expect(s.state).toMatchObject({ loading: false, error: undefined });
    expect(seen).not.toContain(true);
  });

  it('keeps the rows it had when the read fails and says why', async () => {
    const { gates, list } = gated();
    const s = new TasksStore({ list }, () => undefined);
    const first = s.load();
    gates[0]!.resolve([task('aa-1')]);
    await first;
    const again = s.refresh();
    gates[1]!.reject(new InitiativesError(503, 'down'));
    await again;
    expect(s.state).toMatchObject({ loading: false, error: 'down' });
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['aa-1']);
  });

  it('drops an older read that lands after a newer one', async () => {
    const { gates, list } = gated();
    const s = new TasksStore({ list }, () => undefined);
    const first = s.refresh();
    const second = s.refresh();
    gates[1]!.resolve([task('new')]);
    await second;
    gates[0]!.resolve([task('old')]);
    await first;
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['new']);
  });

  it('a first load still pending keeps the list loading after a refresh lands', async () => {
    const { gates, list } = gated();
    const s = new TasksStore({ list }, () => undefined);
    const first = s.load();
    await tick();
    const again = s.refresh();
    gates[1]!.resolve([task('x')]);
    await again;
    expect(s.state.loading).toBe(true);
    gates[0]!.resolve([task('old')]);
    await first;
    expect(s.state.loading).toBe(false);
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['x']);
  });

  it('refreshes that keep overlapping do not hold the skeleton once the first load has landed', async () => {
    const { gates, list } = gated();
    const s = new TasksStore({ list }, () => undefined);
    const first = s.load();
    await tick();
    const r1 = s.refresh();
    gates[0]!.resolve([task('a')]);
    await first;
    expect(s.state.loading).toBe(false);
    const r2 = s.refresh();
    gates[1]!.resolve([task('b')]);
    await r1;
    expect(s.state).toMatchObject({ loading: false });
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['b']);
    gates[2]!.resolve([task('c')]);
    await r2;
    expect(s.state.tasks.map((t) => t.ref)).toEqual(['c']);
  });
});

describe('affectsTasks', () => {
  it('is true for an event that names a task and for an initiative event, false for the rest', () => {
    expect(affectsTasks({ type: 'task_done', task: 'aa-1' })).toBe(true);
    expect(affectsTasks({ type: 'ask_raised', task: 'aa-1' })).toBe(true);
    expect(affectsTasks({ type: 'initiative_closed' })).toBe(true);
    expect(affectsTasks({ type: 'note_posted' })).toBe(false);
    expect(affectsTasks({ type: 'key_created' })).toBe(false);
  });

  it('is false for a note, a milestone and a recorded check, which change no row; true for a lease start', () => {
    expect(affectsTasks({ type: 'note_added', task: 'aa-1' })).toBe(false);
    expect(affectsTasks({ type: 'milestone', task: 'aa-1' })).toBe(false);
    expect(affectsTasks({ type: 'check_recorded', task: 'aa-1' })).toBe(false);
    expect(affectsTasks({ type: 'lease_started', task: 'aa-1' })).toBe(true);
  });
});

class FakeStream implements StreamSource {
  readyState = 1;
  onopen: ((e: unknown) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  private listeners = new Map<string, (e: { data: string }) => void>();
  addEventListener(type: string, listener: (e: { data: string }) => void): void {
    this.listeners.set(type, listener);
  }
  close(): void {}
  send(type: string, data: unknown): void {
    this.listeners.get(type)?.({ data: JSON.stringify(data) });
  }
}

describe('watchTasks on a live stream', () => {
  afterEach(() => vi.useRealTimers());

  const event = (type: string) => ({ id: 'X' + type, ref: 'E1', at: '', type, actor: 'a', principal: 'p', task: 'aa-1', summary: '', data: {} });

  // The Tasks view's own wiring: a real Live on a fake stream, a real TasksStore reading through TasksApi; every list call counted.
  function rig() {
    vi.useFakeTimers();
    const stream = new FakeStream();
    const env: LiveEnv = { open: () => stream, setTimer: (fn, ms) => setTimeout(fn, ms), clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };
    const live = new Live('/api/v1/stream', env);
    live.start();
    const six = Array.from({ length: 600 }, (_, i) => task(`aa-${i + 1}`));
    const { api: a, fetchFn } = api(async () => json({ items: six }));
    const store = new TasksStore(a, () => undefined);
    const stop = watchTasks(live, () => store.refresh());
    return { stream, fetchFn, store, stop };
  }

  for (const type of ['note_added', 'milestone', 'check_recorded']) {
    it(`makes no Tasks refresh for a ${type} on a task`, async () => {
      const { stream, fetchFn } = rig();
      stream.send('board', event(type));
      await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
      expect(fetchFn).toHaveBeenCalledTimes(0);
    });
  }

  it('makes one Tasks refresh, one list call at limit=1000 for 600 not-closed tasks, for a lease_started', async () => {
    const { stream, fetchFn, store } = rig();
    stream.send('board', event('lease_started'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/tasks?status=not_closed&limit=1000']);
    expect(store.state.tasks).toHaveLength(600);
  });

  it('counts 0, 0, 0 then 1 refresh for the four events in a row, and none once stopped', async () => {
    const { stream, fetchFn, stop } = rig();
    const calls: number[] = [];
    for (const type of ['note_added', 'milestone', 'check_recorded', 'lease_started']) {
      const before = fetchFn.mock.calls.length;
      stream.send('board', event(type));
      await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
      calls.push(fetchFn.mock.calls.length - before);
    }
    expect(calls).toEqual([0, 0, 0, 1]);
    stop();
    stream.send('board', event('lease_started'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });
});

describe('NewTaskStore', () => {
  const initiatives = [{ slug: 'aa' }] as InitiativeListItem[];
  function store(over: Partial<ConstructorParameters<typeof NewTaskStore>[0]> = {}) {
    const seen: NewTaskState[] = [];
    const create = vi.fn(async () => ({ ref: 'aa-7' }) as never);
    const s = new NewTaskStore({ initiatives: async () => initiatives, openTasks: async () => [task('aa-1')], create, ...over }, (st) => seen.push(st));
    return { s, seen, create };
  }

  it('starts loading with nothing to pick from', () => {
    expect(store().s.state).toEqual({ initiatives: [], openTasks: [], loading: true, error: undefined });
  });

  it('loads the open initiatives and the open tasks', async () => {
    const { s } = store();
    await s.load();
    expect(s.state).toMatchObject({ loading: false, error: undefined, initiatives });
    expect(s.state.openTasks.map((t) => t.ref)).toEqual(['aa-1']);
  });

  it('shows why the pickers could not be read', async () => {
    const { s } = store({
      openTasks: async () => {
        throw new InitiativesError(500, 'no tasks');
      },
    });
    await s.load();
    expect(s.state).toMatchObject({ loading: false, error: 'no tasks', initiatives: [], openTasks: [] });
  });

  it('creates the task from the form and gives back its ref', async () => {
    const { s, create } = store();
    const ref = await s.create({ ...emptyNewTask('aa'), title: ' T ' });
    expect(ref).toBe('aa-7');
    expect(create).toHaveBeenCalledWith({ initiative: 'aa', title: 'T', kind: 'work' });
    expect(s.state.error).toBeUndefined();
  });

  it('refuses a form the body cannot be built from without calling the API', async () => {
    const { s, create } = store();
    expect(await s.create({ ...emptyNewTask('aa'), kind: 'watch' })).toBeUndefined();
    expect(create).not.toHaveBeenCalled();
    expect(s.state.error).toContain('Cadence');
  });

  it('keeps the refusal of the server for the form and forgets it when asked', async () => {
    const { s } = store({
      create: async () => {
        throw new InitiativesError(409, 'initiative is closed');
      },
    });
    expect(await s.create(emptyNewTask('aa'))).toBeUndefined();
    expect(s.state.error).toBe('initiative is closed');
    s.clearError();
    expect(s.state.error).toBeUndefined();
  });

  it('clears an earlier refusal when a create works', async () => {
    const { s } = store();
    await s.create({ ...emptyNewTask('aa'), kind: 'watch' });
    expect(s.state.error).toBeDefined();
    await s.create(emptyNewTask('aa'));
    expect(s.state.error).toBeUndefined();
  });
});

describe('openInitiatives', () => {
  it('lists the open initiatives', async () => {
    const list = vi.fn(async () => initiativesOf(['aa']));
    expect(await openInitiatives({ list })()).toEqual(initiativesOf(['aa']));
    expect(list).toHaveBeenCalledWith('open');
  });
});

function initiativesOf(slugs: string[]) {
  return slugs.map((slug) => ({ slug }) as InitiativeListItem);
}

describe('TasksApi task page calls', () => {
  it('reads a task by its encoded ref', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    expect((await a.get('aa-1')).ref).toBe('aa-1');
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks/aa-1');
    expect(fetchFn.mock.calls[0]![1]).toMatchObject({ credentials: 'same-origin', headers: { Accept: 'application/json' } });
    await a.get('a/b');
    expect(fetchFn.mock.calls[1]![0]).toBe('/api/v1/tasks/a%2Fb');
  });

  it('reads every event, following next_after through the pages', async () => {
    const { api: a, fetchFn } = api(async (url) =>
      url.includes('after=') ? json({ items: [{ ref: 'E2' }] }) : json({ items: [{ ref: 'E1' }], next_after: 'E1' }),
    );
    expect((await a.events('aa-1')).map((e) => e.ref)).toEqual(['E1', 'E2']);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/tasks/aa-1/events?limit=1000', '/api/v1/tasks/aa-1/events?limit=1000&after=E1']);
  });

  it('patches with the JSON body and the CSRF token', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.patch('aa-1', { title: 'N', planned_end: null });
    const [url, init] = fetchFn.mock.calls[0]!;
    expect(url).toBe('/api/v1/tasks/aa-1');
    expect(init).toMatchObject({ method: 'PATCH', credentials: 'same-origin', body: '{"title":"N","planned_end":null}' });
    expect(init!.headers).toMatchObject({ 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-1' });
  });

  it('posts done and reopen without a body, and close with its resolution', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.done('aa-1');
    await a.reopen('aa-1');
    await a.close('aa-1', 'superseded');
    const calls = fetchFn.mock.calls.map((c) => [c[0], (c[1] as RequestInit).method, (c[1] as RequestInit).body]);
    expect(calls).toEqual([
      ['/api/v1/tasks/aa-1/done', 'POST', undefined],
      ['/api/v1/tasks/aa-1/reopen', 'POST', undefined],
      ['/api/v1/tasks/aa-1/close', 'POST', '{"resolution":"superseded"}'],
    ]);
    expect(fetchFn.mock.calls[0]![1]!.headers).toEqual({ Accept: 'application/json', 'X-CSRF-Token': 'csrf-1' });
    expect(fetchFn.mock.calls[2]![1]!.headers).toMatchObject({ 'Content-Type': 'application/json' });
  });

  it('posts done with force true when asked to force it', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.done('aa-1', true);
    const init = fetchFn.mock.calls[0]![1] as RequestInit;
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks/aa-1/done');
    expect(init).toMatchObject({ method: 'POST', body: '{"force":true}' });
    expect(init.headers).toMatchObject({ 'Content-Type': 'application/json' });
  });

  it('names the failed action when a write is refused without a detail', async () => {
    const { api: a } = api(async () => new Response('', { status: 500 }));
    await expect(a.done('aa-1')).rejects.toMatchObject({ status: 500, message: 'Marking the task done failed (HTTP 500).' });
  });
});

describe('TasksApi dependency, note and lease calls', () => {
  const calls = (fetchFn: { mock: { calls: unknown[][] } }) => fetchFn.mock.calls.map((c) => [c[0], (c[1] as RequestInit).method, (c[1] as RequestInit).body]);

  it('adds and removes a dependency on the encoded refs, without a body', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.addDep('aa-1', 'bb-2');
    await a.removeDep('aa-1', 'b/b');
    expect(calls(fetchFn)).toEqual([
      ['/api/v1/tasks/aa-1/deps/bb-2', 'POST', undefined],
      ['/api/v1/tasks/aa-1/deps/b%2Fb', 'DELETE', undefined],
    ]);
    expect(fetchFn.mock.calls[1]![1]!.headers).toEqual({ Accept: 'application/json', 'X-CSRF-Token': 'csrf-1' });
  });

  it('posts a milestone and a check with their summary', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.milestone('aa-1', 'shipped');
    await a.recordCheck('aa-1', 'green');
    expect(calls(fetchFn)).toEqual([
      ['/api/v1/tasks/aa-1/milestones', 'POST', '{"summary":"shipped"}'],
      ['/api/v1/tasks/aa-1/checks', 'POST', '{"summary":"green"}'],
    ]);
  });

  it('reads the expired leases of a task, following next_after through the pages', async () => {
    const { api: a, fetchFn } = api(async (url) => (url.includes('after=') ? json({ items: [{ ref: 'L2' }] }) : json({ items: [{ ref: 'L1' }], next_after: 'L1' })));
    expect((await a.expiredLeases('aa-1')).map((l) => l.ref)).toEqual(['L1', 'L2']);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual([
      '/api/v1/leases?task=aa-1&state=expired&limit=1000',
      '/api/v1/leases?task=aa-1&state=expired&limit=1000&after=L1',
    ]);
  });

  it('reads every live lease of the board, following next_after through the pages', async () => {
    const { api: a, fetchFn } = api(async (url) => (url.includes('after=') ? json({ items: [{ ref: 'L2' }] }) : json({ items: [{ ref: 'L1' }], next_after: 'L1' })));
    expect((await a.liveLeases()).map((l) => l.ref)).toEqual(['L1', 'L2']);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/leases?state=live&limit=1000', '/api/v1/leases?state=live&limit=1000&after=L1']);
  });

  it('ends a lease with its outcome only when there is one, and acknowledges an expired lease', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'L1' }));
    expect((await a.endLease('L1', 'done')).ref).toBe('L1');
    await a.endLease('L1', '');
    await a.ackLease('L1');
    expect(calls(fetchFn)).toEqual([
      ['/api/v1/leases/L1/end', 'POST', '{"outcome":"done"}'],
      ['/api/v1/leases/L1/end', 'POST', undefined],
      ['/api/v1/leases/L1/ack', 'POST', undefined],
    ]);
  });

  it('reads every ask and every note of a task, following next_after through the pages', async () => {
    const pages = async (url: string) => (url.includes('after=') ? json({ items: [{ ref: 'X2' }] }) : json({ items: [{ ref: 'X1' }], next_after: 'X1' }));
    const { api: a, fetchFn } = api(pages);
    expect((await a.asksOf('aa-1')).map((x) => x.ref)).toEqual(['X1', 'X2']);
    expect((await a.notesOf('aa-1')).map((x) => x.ref)).toEqual(['X1', 'X2']);
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual([
      '/api/v1/asks?task=aa-1&state=all&limit=1000',
      '/api/v1/asks?task=aa-1&state=all&limit=1000&after=X1',
      '/api/v1/notes?task=aa-1&fields=summary&limit=1000',
      '/api/v1/notes?task=aa-1&fields=summary&limit=1000&after=X1',
    ]);
  });

  it('reads one note, answers and withdraws an ask and posts a note', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'N1', body: 'b' }));
    expect((await a.note('N1')).body).toBe('b');
    await a.answerAsk('A1', 'yes');
    await a.withdrawAsk('A1', 'why');
    await a.withdrawAsk('A1', '');
    await a.postNote({ summary: 's', task: 'aa-1' });
    expect(calls(fetchFn)).toEqual([
      ['/api/v1/notes/N1', undefined, undefined],
      ['/api/v1/asks/A1/answer', 'POST', '{"answer":"yes"}'],
      ['/api/v1/asks/A1/withdraw', 'POST', '{"reason":"why"}'],
      ['/api/v1/asks/A1/withdraw', 'POST', '{}'],
      ['/api/v1/notes', 'POST', '{"summary":"s","task":"aa-1"}'],
    ]);
  });

  it('names the failed action when an ask or note call is refused without a detail', async () => {
    const { api: a } = api(async () => new Response('', { status: 500 }));
    await expect(a.asksOf('aa-1')).rejects.toMatchObject({ message: 'Reading the asks failed (HTTP 500).' });
    await expect(a.notesOf('aa-1')).rejects.toMatchObject({ message: 'Reading the notes failed (HTTP 500).' });
    await expect(a.note('N1')).rejects.toMatchObject({ message: 'Reading the note failed (HTTP 500).' });
    await expect(a.answerAsk('A1', 'x')).rejects.toMatchObject({ message: 'Answering the ask failed (HTTP 500).' });
    await expect(a.withdrawAsk('A1', '')).rejects.toMatchObject({ message: 'Withdrawing the ask failed (HTTP 500).' });
    await expect(a.postNote({ summary: 's' })).rejects.toMatchObject({ message: 'Posting the note failed (HTTP 500).' });
  });

  it('names the failed action when a lease write is refused without a detail', async () => {
    const { api: a } = api(async () => new Response('', { status: 500 }));
    await expect(a.endLease('L1', '')).rejects.toMatchObject({ message: 'Ending the lease failed (HTTP 500).' });
    await expect(a.ackLease('L1')).rejects.toMatchObject({ message: 'Acknowledging the lease failed (HTTP 500).' });
    await expect(a.addDep('aa-1', 'bb-1')).rejects.toMatchObject({ message: 'Adding the dependency failed (HTTP 500).' });
    await expect(a.removeDep('aa-1', 'bb-1')).rejects.toMatchObject({ message: 'Removing the dependency failed (HTTP 500).' });
    await expect(a.milestone('aa-1', 's')).rejects.toMatchObject({ message: 'Recording the milestone failed (HTTP 500).' });
    await expect(a.recordCheck('aa-1', 's')).rejects.toMatchObject({ message: 'Recording the check failed (HTTP 500).' });
    await expect(a.expiredLeases('aa-1')).rejects.toMatchObject({ message: 'Reading the expired leases failed (HTTP 500).' });
    await expect(a.liveLeases()).rejects.toMatchObject({ message: 'Reading the live leases failed (HTTP 500).' });
  });
});

describe('TasksApi tag calls', () => {
  it('puts one tag on a task with the CSRF token and no body, the tag and the ref encoded', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.addTag('aa-1', 'a:b');
    const [url, init] = fetchFn.mock.calls[0]!;
    expect(url).toBe('/api/v1/tasks/aa-1/tags/a%3Ab');
    expect(init).toMatchObject({ method: 'PUT', credentials: 'same-origin' });
    expect(init!.headers).toMatchObject({ 'X-CSRF-Token': 'csrf-1' });
    expect(init!.body).toBeUndefined();
  });

  it('takes one tag off a task with DELETE', async () => {
    const { api: a, fetchFn } = api(async () => json({ ref: 'aa-1' }));
    await a.removeTag('aa-1', 'urgent');
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks/aa-1/tags/urgent');
    expect(fetchFn.mock.calls[0]![1]).toMatchObject({ method: 'DELETE' });
  });

  it('turns a refused tag change into its problem text', async () => {
    const { api: a } = api(async () => problem(409, { detail: 'task aa-1 is closed' }));
    await expect(a.addTag('aa-1', 'x')).rejects.toThrow(/task aa-1 is closed/);
  });

  it('reads the marks of some refs, whatever their status', async () => {
    const { api: a, fetchFn } = api(async () => json({ items: [{ ref: 'aa-1', tag_marks: [{ tag: 'x', source: 'task' }] }] }));
    expect(await a.marks(['aa-1'])).toEqual({ 'aa-1': [{ tag: 'x', source: 'task' }] });
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks?status=all&limit=100&ref=aa-1');
  });

  it('lists the tags in use from one page of up to 1000 tasks', async () => {
    const { api: a, fetchFn } = api(async () =>
      json({ items: [{ ref: 'aa-1', tag_marks: [{ tag: 'b', source: 'task' }, { tag: 'a', source: 'initiative' }] }, { ref: 'aa-2', tag_marks: [{ tag: 'b', source: 'actor' }] }] }),
    );
    expect(await a.tagsInUse()).toEqual(['a', 'b']);
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/tasks?status=all&limit=1000');
  });
});
