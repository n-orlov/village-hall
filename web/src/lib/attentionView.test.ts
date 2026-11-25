import { describe, expect, it, vi } from 'vitest';
import { DEFAULT_ATTENTION_FILTER, filterFromQuery } from './attention';
import {
  AttentionApi,
  AttentionStore,
  ATTENTION_VIEW,
  affectsAttention,
  affectsInitiativeCatalog,
  affectsTagSources,
  copyLabel,
  dropUnknown,
  excludeClosedOf,
  nowFragment,
  nowQuery,
  nowViewQuery,
  nowTagChoices,
  forgetAttention,
  currentMarks,
  marksOfBoard,
  openAttention,
  rowsFor,
  saveAttention,
  startFilter,
  type Attention,
  type AttentionState,
} from './attentionView';
import type { Browser } from './session';
import { viewKey, type ViewEnv, type ViewStorage } from './viewState';

const NOW = new Date('2026-01-01T12:00:00Z');
const counts = { open_asks: 0, done_not_closed: 0, expired_leases: 0, overdue_watches: 0, conflicts: 0, total: 0 };
const bob = { handle: 'bob', way_back: 'ralphctl status bob' };
const sue = { handle: 'sue' };

const full: Attention = {
  counts,
  open_asks: [
    { ref: 'A1', question: 'Which one?', task: 'aa-1', asker: bob, mode: 'poll', asker_waiting: true, blocking: true, created_at: '2026-01-01T11:00:00Z', age_seconds: 3600 },
    { ref: 'A2', question: 'Free?', asker: sue, mode: 'stop', asker_waiting: false, blocking: false, created_at: '2026-01-01T11:59:50Z', age_seconds: 10 },
  ],
  done_not_closed: [
    {
      task: 'aa-2',
      title: 'Ship it',
      done_by: bob,
      done_at: '2026-01-01T10:00:00Z',
      last_lease: { lease: 'L3', holder: sue, outcome: 'shipped', ended_at: '2026-01-01T09:59:00Z' },
      tags: [],
      tag_marks: [],
    },
    { task: 'aa-4', title: 'No lease', done_by: sue, done_at: '2026-01-01T11:59:30Z', last_lease: { lease: 'L9', holder: bob, ended_at: '2026-01-01T09:59:00Z' }, tags: [], tag_marks: [] },
    { task: 'aa-5', title: 'Plain', done_by: sue, done_at: '2026-01-01T11:59:30Z', tags: [], tag_marks: [] },
  ],
  expired_leases: [{ lease: 'L4', holder: bob, task: 'aa-3', title: 'Slow', intent: 'ride', renewed_at: '2026-01-01T08:00:00Z', expired_at: '2026-01-01T09:00:00Z', notice: false, tags: [], tag_marks: [] }],
  overdue_watches: [
    { task: 'aa-6', title: 'Watch', owner: bob, next_check_at: '2026-01-01T10:00:00Z', last_check: { summary: 'all well', by: sue, at: '2026-01-01T09:00:00Z' }, tags: [], tag_marks: [] },
    { task: 'aa-7', title: 'Fresh', owner: sue, next_check_at: '2026-01-01T10:00:00Z', tags: [], tag_marks: [] },
  ],
  conflicts: [
    {
      resource: 'db/main',
      holders: [
        { lease: 'L1', actor: bob, task: 'aa-1', intent: 'migrate', expires_at: '2026-01-01T13:00:00Z' },
        { lease: 'L2', actor: sue, task: 'aa-2', intent: 'vacuum', expires_at: '2026-01-01T13:00:00Z' },
      ],
      tasks: ['aa-1', 'aa-2'],
      since: '2026-01-01T09:30:00Z',
    },
    {
      resource: 'solo',
      holders: [{ lease: 'L5', actor: bob, task: 'aa-1', intent: 'x', expires_at: '2026-01-01T13:00:00Z' }],
      tasks: ['aa-1'],
      since: '2026-01-01T08:00:00Z',
    },
  ],
};

describe('rowsFor', () => {
  it('shows an ask with its question, task link, age, mode and the asker with the way back', () => {
    const [a1, a2] = rowsFor('open_asks', full, NOW);
    expect(a1).toEqual({
      id: 'A1',
      title: 'A1: Which one?',
      href: '#/t/aa-1',
      initiative: 'aa',
      details: ['asked 1 hour ago', 'asker waiting', 'blocking'],
      actors: [{ role: 'asked by', handle: 'bob', wayBack: 'ralphctl status bob' }],
      at: '2026-01-01T11:00:00Z',
    });
    expect(a2).toEqual({
      id: 'A2',
      title: 'A2: Free?',
      href: undefined,
      initiative: undefined,
      details: ['asked just now', 'asker stopped'],
      actors: [{ role: 'asked by', handle: 'sue', wayBack: undefined }],
      at: '2026-01-01T11:59:50Z',
    });
  });

  it('keeps the initiative of an ask raised on an initiative with no task, and links no task for it', () => {
    const ask = { question: 'Which?', asker: bob, mode: 'stop' as const, asker_waiting: false, blocking: false, created_at: '2026-01-01T11:59:50Z', age_seconds: 10 };
    const rows = rowsFor(
      'open_asks',
      {
        counts,
        open_asks: [
          { ...ask, ref: 'A3', initiative: 'bb' },
          { ...ask, ref: 'A4', task: 'my-slug-3', initiative: 'my-slug' },
          { ...ask, ref: 'A5', task: 'cc-2' },
          { ...ask, ref: 'A6' },
        ],
      },
      NOW,
    );
    expect(rows.map((r) => [r.id, r.href, r.initiative])).toEqual([
      ['A3', undefined, 'bb'],
      ['A4', '#/t/my-slug-3', 'my-slug'],
      ['A5', '#/t/cc-2', 'cc'],
      ['A6', undefined, undefined],
    ]);
  });

  it('shows a done task with who marked it done, when, and its last lease outcome', () => {
    const [done, noOutcome, plain] = rowsFor('done_not_closed', full, NOW);
    expect(done).toEqual({
      id: 'aa-2',
      title: 'aa-2 Ship it',
      href: '#/t/aa-2',
      initiative: 'aa',
      details: ['done 2 hours ago', 'last lease L3: shipped'],
      actors: [
        { role: 'done by', handle: 'bob', wayBack: 'ralphctl status bob' },
        { role: 'last lease', handle: 'sue', wayBack: undefined },
      ],
      at: '2026-01-01T10:00:00Z',
    });
    expect(noOutcome!.details).toEqual(['done just now']);
    expect(noOutcome!.actors.map((a) => a.role)).toEqual(['done by', 'last lease']);
    expect(plain!.actors.map((a) => a.role)).toEqual(['done by']);
    expect(plain!.details).toEqual(['done just now']);
  });

  it('shows an expired lease with its holder, intent and expiry', () => {
    expect(rowsFor('expired_leases', full, NOW)).toEqual([
      {
        id: 'L4',
        title: 'L4 on aa-3 Slow',
        href: '#/t/aa-3',
        initiative: 'aa',
        details: ['ride', 'expired 3 hours ago'],
        actors: [{ role: 'held by', handle: 'bob', wayBack: 'ralphctl status bob' }],
        at: '2026-01-01T09:00:00Z',
      },
    ]);
  });

  it('marks an expired notice with notice: true and leaves the mark off an ordinary expired lease', () => {
    const lease = full.expired_leases![0]!;
    const rows = rowsFor('expired_leases', { ...full, expired_leases: [{ ...lease, lease: 'L8', notice: true }, lease] }, NOW);
    expect(rows.map((r) => [r.id, r.notice])).toEqual([
      ['L8', true],
      ['L4', undefined],
    ]);
  });

  it('shows an overdue watch with its owner and the last check when there is one', () => {
    const [checked, fresh] = rowsFor('overdue_watches', full, NOW);
    expect(checked!.details).toEqual(['check was due 2 hours ago', 'last check: all well']);
    expect(checked!.actors.map((a) => [a.role, a.handle])).toEqual([
      ['owner', 'bob'],
      ['last check by', 'sue'],
    ]);
    expect(fresh!.details).toEqual(['check was due 2 hours ago']);
    expect(fresh!.actors.map((a) => a.role)).toEqual(['owner']);
  });

  it('shows a conflict with every holder; it links the task only when there is one', () => {
    const [many, one] = rowsFor('conflicts', full, NOW);
    expect(many).toEqual({
      id: 'db/main',
      title: 'db/main',
      href: undefined,
      initiative: undefined,
      details: ['L1 on aa-1: migrate', 'L2 on aa-2: vacuum'],
      actors: [
        { role: 'L1', handle: 'bob', wayBack: 'ralphctl status bob' },
        { role: 'L2', handle: 'sue', wayBack: undefined },
      ],
      at: '2026-01-01T09:30:00Z',
    });
    expect(one!.at).toBe('2026-01-01T08:00:00Z');
    expect(one!.href).toBe('#/t/aa-1');
  });

  it('has no rows for a section the answer lacks', () => {
    for (const key of ['open_asks', 'done_not_closed', 'expired_leases', 'overdue_watches', 'conflicts'] as const) {
      expect(rowsFor(key, { counts }, NOW)).toEqual([]);
    }
  });

  it('treats a blank way back as none', () => {
    const odd: Attention = { counts, expired_leases: [{ ...full.expired_leases![0]!, holder: { handle: 'x', way_back: '' } }] };
    expect(rowsFor('expired_leases', odd, NOW)[0]!.actors[0]!.wayBack).toBeUndefined();
  });

  it('encodes a task ref in its link', () => {
    const odd: Attention = { counts, overdue_watches: [{ ...full.overdue_watches![1]!, task: 'a b' }] };
    expect(rowsFor('overdue_watches', odd, NOW)[0]!.href).toBe('#/t/a%20b');
  });
});

describe('affectsAttention', () => {
  it.each([['ask_raised'], ['ask_polling'], ['task_done'], ['lease_expired'], ['check_recorded'], ['initiative_updated']])('keeps %s', (type) => {
    expect(affectsAttention({ type })).toBe(true);
  });
  it.each([['note_posted'], ['note_edited'], ['note_anything']])('keeps %s when it names a task (its last touch)', (type) => {
    expect(affectsAttention({ type, task: 'aa-1' })).toBe(true);
  });
  it('ignores a note that names no task', () => {
    expect(affectsAttention({ type: 'note_posted' })).toBe(false);
    expect(affectsAttention({ type: 'note_posted', task: '' })).toBe(false);
  });
  it.each([['key_created'], ['key_revoked']])('ignores %s, even when a task is named', (type) => {
    expect(affectsAttention({ type })).toBe(false);
    expect(affectsAttention({ type, task: 'aa-1' })).toBe(false);
  });
});

describe('affectsInitiativeCatalog', () => {
  it.each([['initiative_created'], ['initiative_updated'], ['initiative_closed'], ['initiative_reopened']])('keeps %s', (type) => {
    expect(affectsInitiativeCatalog({ type })).toBe(true);
  });
  it.each([['task_created'], ['task_updated'], ['note_posted'], ['key_created'], ['lease_expired'], ['ask_raised']])('ignores %s: the board read carries its tag marks', (type) => {
    expect(affectsInitiativeCatalog({ type })).toBe(false);
  });
});

describe('copyLabel', () => {
  it('names the actor', () => {
    expect(copyLabel({ role: 'r', handle: 'bob', wayBack: 'x' })).toBe('Copy way back of bob');
  });
});

describe('nowTagChoices', () => {
  it('offers each initiative tag once as an initiative tag, plus the chosen ones, sorted', () => {
    const list = [{ tags: ['work', 'a'] }, { tags: ['work'] }];
    expect(nowTagChoices(list, ['q', 'a'])).toEqual([
      { tag: 'a', sources: ['initiative'] },
      { tag: 'q', sources: [] },
      { tag: 'work', sources: ['initiative'] },
    ]);
    expect(nowTagChoices([], [])).toEqual([]);
  });

  it('offers the own, initiative and actor-kind tags of the tasks shown, each with its sources, and one chip for a tag with two', () => {
    const marks = {
      'aa-1': [
        { tag: 'urgent', source: 'task' as const },
        { tag: 'work', source: 'initiative' as const },
        { tag: 'ralph', source: 'actor' as const },
      ],
      'aa-2': [{ tag: 'work', source: 'task' as const }],
    };
    expect(nowTagChoices([{ tags: ['work'] }], [], marks)).toEqual([
      { tag: 'ralph', sources: ['actor'] },
      { tag: 'urgent', sources: ['task'] },
      { tag: 'work', sources: ['initiative', 'task'] },
    ]);
  });

  it('keeps a chip it offered before when a narrower read shows fewer tasks', () => {
    const wide = nowTagChoices([], [], { 'aa-1': [{ tag: 'urgent', source: 'task' as const }] });
    const narrow = nowTagChoices([{ tags: ['work'] }], ['work'], {}, wide);
    expect(narrow.map((c) => c.tag)).toEqual(['urgent', 'work']);
    expect(narrow[0]).toEqual({ tag: 'urgent', sources: ['task'] });
  });
});

describe('nowQuery', () => {
  it('is empty with no chips and the default window', () => {
    expect(nowQuery({ tags: [], initiatives: [] })).toBe('');
  });

  it('repeats tag for every tag chosen', () => {
    expect(nowQuery({ tags: ['work', 'home'], initiatives: [] })).toBe('tag=work&tag=home');
  });

  it('repeats initiative for every initiative chosen', () => {
    expect(nowQuery({ tags: [], initiatives: ['aa', 'bb'] })).toBe('initiative=aa&initiative=bb');
  });

  it('carries both kinds of chip and a window that is not the default', () => {
    expect(nowQuery({ tags: ['work', 'home'], initiatives: ['aa', 'bb'] }, '8h')).toBe('tag=work&tag=home&initiative=aa&initiative=bb&window=8h');
    expect(nowQuery({ tags: [], initiatives: [] }, '3d')).toBe('window=3d');
    expect(nowQuery({ tags: [], initiatives: [] }, '24h')).toBe('');
  });
});

function browser(respond: (url: string) => Promise<Response>, pathname = '/'): { browser: Browser; fetchFn: ReturnType<typeof vi.fn> } {
  const fetchFn = vi.fn((url: string) => respond(url));
  return { browser: { fetch: fetchFn, assign: vi.fn(), pathname, setHash: vi.fn() }, fetchFn };
}

describe('AttentionApi', () => {
  it('reads /attention/now with the filter as repeated keys and the session cookie, and gives the whole answer', async () => {
    const { browser: b, fetchFn } = browser(async () => new Response(JSON.stringify({ window: '24h', attention: full, in_flight: [], recent: [], task_marks: {} })));
    const got = await new AttentionApi(b).board({ tags: ['a', 'b'], initiatives: ['x'] });
    expect(got.attention).toEqual(full);
    expect(fetchFn.mock.calls[0]![0]).toBe('/api/v1/attention/now?tag=a&tag=b&initiative=x');
    expect(fetchFn.mock.calls[0]![1]).toMatchObject({ credentials: 'same-origin', headers: { Accept: 'application/json' } });
  });

  it('asks for no query without a filter and follows the mount prefix', async () => {
    const { browser: b, fetchFn } = browser(async () => new Response(JSON.stringify({ window: '24h', attention: { counts }, in_flight: [], recent: [], task_marks: {} })), '/board/');
    await new AttentionApi(b).board(DEFAULT_ATTENTION_FILTER);
    expect(fetchFn.mock.calls[0]![0]).toBe('/board/api/v1/attention/now');
  });

  it('sends the window only when it is not the default, after the filter', async () => {
    const { browser: b, fetchFn } = browser(async () => new Response(JSON.stringify({ window: '3d', attention: { counts }, in_flight: [], recent: [], task_marks: {} })));
    await new AttentionApi(b).board({ tags: ['a'], initiatives: [] }, '3d');
    await new AttentionApi(b).board(DEFAULT_ATTENTION_FILTER, '8h');
    await new AttentionApi(b).board(DEFAULT_ATTENTION_FILTER, '24h');
    expect(fetchFn.mock.calls.map((c) => c[0])).toEqual(['/api/v1/attention/now?tag=a&window=3d', '/api/v1/attention/now?window=8h', '/api/v1/attention/now']);
  });

  it('says the server is not reachable when the fetch throws', async () => {
    const { browser: b } = browser(async () => Promise.reject(new TypeError('down')));
    await expect(new AttentionApi(b).board(DEFAULT_ATTENTION_FILTER)).rejects.toThrow('The server is not reachable.');
  });

  it('reports a refusal by its problem detail, else by its status', async () => {
    const problem = browser(async () => new Response(JSON.stringify({ detail: 'no such initiative' }), { status: 404, headers: { 'Content-Type': 'application/problem+json' } }));
    await expect(new AttentionApi(problem.browser).board(DEFAULT_ATTENTION_FILTER)).rejects.toThrow('no such initiative');
    const bare = browser(async () => new Response('x', { status: 500 }));
    await expect(new AttentionApi(bare.browser).board(DEFAULT_ATTENTION_FILTER)).rejects.toThrow('Reading what needs attention failed (HTTP 500).');
  });
});

describe('AttentionStore', () => {
  function rig(get: (f: unknown) => Promise<Attention>) {
    const states: AttentionState[] = [];
    const board = async (f: unknown) => ({ window: '24h', attention: await get(f), in_flight: [], recent: [], task_marks: {} });
    const store = new AttentionStore({ board } as never, (s) => states.push(s));
    return { store, states, last: () => states[states.length - 1]! };
  }

  it('loads the sections under the filter and stops loading', async () => {
    const { store, last } = rig(async () => full);
    await store.load();
    expect(last()).toMatchObject({ attention: full, loading: false, error: undefined });
  });

  it('keeps what is shown while a refresh is out, and shows the error when it fails', async () => {
    let fail = false;
    const { store, states, last } = rig(async () => {
      if (fail) throw new Error('boom');
      return full;
    });
    await store.load();
    fail = true;
    await store.load();
    expect(states.some((s) => s.loading && s.attention === full)).toBe(true);
    expect(last()).toMatchObject({ attention: full, error: 'boom', loading: false });
  });

  it('clears the error once a load works again', async () => {
    let fail = true;
    const { store, last } = rig(async () => {
      if (fail) throw new Error('boom');
      return full;
    });
    await store.load();
    expect(last().error).toBe('boom');
    fail = false;
    await store.load();
    expect(last().error).toBeUndefined();
  });

  it('fetches under the new filter, and drops an answer for an older one that lands late', async () => {
    const waits: ((a: Attention) => void)[] = [];
    const seen: unknown[] = [];
    const { store, last } = rig((f) => {
      seen.push(f);
      return new Promise<Attention>((resolve) => waits.push(resolve));
    });
    const first = store.load();
    const second = store.setFilter({ tags: ['a'], initiatives: [] });
    waits[1]!({ counts, conflicts: [] });
    await second;
    waits[0]!(full);
    await first;
    expect(last().filter).toEqual({ tags: ['a'], initiatives: [] });
    expect(last().attention).toEqual({ counts, conflicts: [] });
    expect(seen[1]).toEqual({ tags: ['a'], initiatives: [] });
    expect(last().loading).toBe(false);
  });

  it('stays loading while a fetch is out', async () => {
    const waits: ((a: Attention) => void)[] = [];
    const { store, last } = rig(() => new Promise<Attention>((resolve) => waits.push(resolve)));
    const p = store.load();
    expect(last().loading).toBe(true);
    waits[0]!(full);
    await p;
    expect(last().loading).toBe(false);
  });
});

describe('AttentionStore flight and window', () => {
  const flight = [{ task: { task: 'aa-1', title: 't', initiative: 'aa', status: 'open', kind: 'work' }, leases: [] }];
  const recent = [{ task: { task: 'aa-2', title: 'u', initiative: 'aa', status: 'open', kind: 'work' }, last_event: { type: 'x', summary: 's', actor: { handle: 'bob' }, at: '2026-01-01T11:00:00Z' } }];

  it('keeps In flight and Recently touched from the same answer, and reads again for another window', async () => {
    const seen: unknown[] = [];
    const board = vi.fn(async (f: unknown, w: unknown) => {
      seen.push([f, w]);
      return { window: w, attention: full, in_flight: flight, recent, task_marks: {} };
    });
    const states: AttentionState[] = [];
    const store = new AttentionStore({ board } as never, (s) => states.push(s), DEFAULT_ATTENTION_FILTER, '8h');
    expect(states).toEqual([]);
    await store.load();
    expect(states[states.length - 1]).toMatchObject({ flight, recent, window: '8h', loading: false });
    await store.setWindow('3d');
    expect(seen[1]).toEqual([DEFAULT_ATTENTION_FILTER, '3d']);
    expect(states[states.length - 1]).toMatchObject({ window: '3d' });
  });

  it('drops an answer for an older window that lands late', async () => {
    const waits: ((b: unknown) => void)[] = [];
    const board = () => new Promise((resolve) => waits.push(resolve));
    const states: AttentionState[] = [];
    const store = new AttentionStore({ board } as never, (s) => states.push(s));
    const first = store.load();
    const second = store.setWindow('8h');
    waits[1]!({ window: '8h', attention: { counts }, in_flight: [], recent, task_marks: {} });
    await second;
    waits[0]!({ window: '24h', attention: full, in_flight: flight, recent: [], task_marks: {} });
    await first;
    const last = states[states.length - 1]!;
    expect(last.window).toBe('8h');
    expect(last.flight).toEqual([]);
    expect(last.recent).toEqual(recent);
  });
});

describe('the marks of a board answer', () => {
  const task_marks = {
    'aa-1': { tags: ['mine'], tag_marks: [{ tag: 'mine', source: 'task' }, { tag: 'web', source: 'initiative' }, { tag: 'claude', source: 'actor' }] },
    'aa-2': { tags: [], tag_marks: [] },
  };

  it('are the task_marks by ref, each source kept, and an empty list for a task with no tags', () => {
    expect(marksOfBoard({ task_marks } as never)).toEqual({
      'aa-1': [{ tag: 'mine', source: 'task' }, { tag: 'web', source: 'initiative' }, { tag: 'claude', source: 'actor' }],
      'aa-2': [],
    });
  });

  it('are empty for an answer that names no task', () => {
    expect(marksOfBoard({ task_marks: {} })).toEqual({});
  });

  it('reach the state with the answer, replace the last answer’s, and need no read of the tasks', async () => {
    const answers = [{ window: '24h', attention: full, in_flight: [], recent: [], task_marks }, { window: '24h', attention: full, in_flight: [], recent: [], task_marks: { 'aa-2': task_marks['aa-2'] } }];
    const board = vi.fn(async () => answers.shift()!);
    const states: AttentionState[] = [];
    const store = new AttentionStore({ board } as never, (s) => states.push(s));
    expect(states).toEqual([]);
    await store.load();
    expect(Object.keys(states[states.length - 1]!.marks)).toEqual(['aa-1', 'aa-2']);
    await store.load();
    expect(states[states.length - 1]!.marks).toEqual({ 'aa-2': [] });
    expect(board).toHaveBeenCalledTimes(2);
  });

  it('carry the epoch their read began at, not the one they landed at', async () => {
    const waits: ((b: unknown) => void)[] = [];
    const board = () => new Promise((resolve) => waits.push(resolve));
    const states: AttentionState[] = [];
    let epoch = 3;
    const store = new AttentionStore({ board } as never, (s) => states.push(s), DEFAULT_ATTENTION_FILTER, '24h', () => epoch);
    const first = store.load();
    epoch = 4;
    const second = store.load();
    waits[0]!({ window: '24h', attention: full, in_flight: [], recent: [], task_marks });
    await first;
    expect(states[states.length - 1]).toMatchObject({ marksAt: 3 });
    expect(currentMarks(states[states.length - 1]!, epoch)).toEqual({});
    waits[1]!({ window: '24h', attention: full, in_flight: [], recent: [], task_marks: { 'aa-2': task_marks['aa-2'] } });
    await second;
    expect(states[states.length - 1]).toMatchObject({ marksAt: 4, error: undefined });
    expect(states[states.length - 1]!.marks).toEqual({ 'aa-2': [] });
    expect(currentMarks(states[states.length - 1]!, epoch)).toEqual({ 'aa-2': [] });
  });

  it('give the chips sources only while no source event arrived since their read began', () => {
    const marks = { 'aa-1': [{ tag: 'mine', source: 'task' as const }] };
    expect(currentMarks({ marks, marksAt: 2 }, 2)).toBe(marks);
    expect(currentMarks({ marks, marksAt: 2 }, 3)).toEqual({});
    expect(currentMarks({ marks: {}, marksAt: 0 }, 0)).toEqual({});
  });

  it('are kept while a refresh is out (the rows keep their chips) and start empty', async () => {
    const waits: ((b: unknown) => void)[] = [];
    const board = () => new Promise((resolve) => waits.push(resolve));
    const states: AttentionState[] = [];
    const store = new AttentionStore({ board } as never, (s) => states.push(s));
    const first = store.load();
    waits[0]!({ window: '24h', attention: full, in_flight: [], recent: [], task_marks });
    await first;
    const second = store.load();
    expect(states[states.length - 1]).toMatchObject({ loading: true });
    expect(Object.keys(states[states.length - 1]!.marks)).toEqual(['aa-1', 'aa-2']);
    waits[1]!({ window: '24h', attention: full, in_flight: [], recent: [], task_marks: {} });
    await second;
    expect(states[states.length - 1]!.marks).toEqual({});
  });
});

function memory(initial: Record<string, string> = {}): ViewStorage & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
    key: (i) => [...data.keys()][i] ?? null,
    get length() {
      return data.size;
    },
  };
}

function env(storage: ViewStorage | undefined): ViewEnv & { hashes: string[] } {
  const hashes: string[] = [];
  return { storage, replaceHash: (h) => void hashes.push(h), hashes };
}

describe('saved Attention filters', () => {
  const KEY = 'vh.view.v1.attention';

  it('is saved under vh.view.v1.attention', () => {
    expect(viewKey(ATTENTION_VIEW)).toBe(KEY);
  });

  it('saves the filter as the fragment query and removes the key for the empty filter', () => {
    const storage = memory();
    saveAttention(storage, { tags: ['work', 'home'], initiatives: ['aa'] });
    expect(JSON.parse(storage.data.get(KEY)!)).toEqual({ v: 1, query: 'tag=work&tag=home&initiative=aa' });
    saveAttention(storage, DEFAULT_ATTENTION_FILTER);
    expect(storage.data.has(KEY)).toBe(false);
  });

  it('restores a bare route in place, before any fetch, and reports it as restored', () => {
    const e = env(memory({ [KEY]: JSON.stringify({ v: 1, query: 'tag=work&initiative=aa' }) }));
    expect(openAttention(e, '#/now')).toEqual({ filter: { tags: ['work'], initiatives: ['aa'] }, restored: true, excludeClosed: false });
    expect(e.hashes).toEqual(['#/now?tag=work&initiative=aa']);
    // Any fragment the router sends to Attention is bare too.
    expect(openAttention(env(memory({ [KEY]: JSON.stringify({ v: 1, query: 'tag=work' }) })), '#/').restored).toBe(true);
  });

  it('lets a fragment with its own query win and saves it, without restoring', () => {
    const storage = memory({ [KEY]: JSON.stringify({ v: 1, query: 'tag=old' }) });
    const e = env(storage);
    expect(openAttention(e, '#/now?tag=new')).toEqual({ filter: { tags: ['new'], initiatives: [] }, restored: false, excludeClosed: false });
    expect(e.hashes).toEqual([]);
    expect(JSON.parse(storage.data.get(KEY)!).query).toBe('tag=new');
  });

  it('starts at the defaults with nothing saved, a malformed value, another version or no storage', () => {
    for (const raw of [undefined, 'nope', '{"v":2,"query":"tag=x"}', '{"v":1,"query":5}']) {
      const e = env(memory(raw === undefined ? {} : { [KEY]: raw }));
      expect(openAttention(e, '#/now')).toEqual({ filter: DEFAULT_ATTENTION_FILTER, restored: false, excludeClosed: false });
      expect(e.hashes).toEqual([]);
    }
    expect(openAttention(env(undefined), '#/now').restored).toBe(false);
  });

  it('saves "Exclude closed" after the filter, alone too, and the default removes the key', () => {
    const storage = memory();
    saveAttention(storage, { tags: ['work'], initiatives: [] }, true);
    expect(JSON.parse(storage.data.get(KEY)!)).toEqual({ v: 1, query: 'tag=work&exclude=closed' });
    saveAttention(storage, DEFAULT_ATTENTION_FILTER, true);
    expect(JSON.parse(storage.data.get(KEY)!)).toEqual({ v: 1, query: 'exclude=closed' });
    saveAttention(storage, DEFAULT_ATTENTION_FILTER, false);
    expect(storage.data.has(KEY)).toBe(false);
  });

  it('restores "Exclude closed" from a bare route in place; alone it needs no check against the board', () => {
    const e = env(memory({ [KEY]: JSON.stringify({ v: 1, query: 'exclude=closed' }) }));
    expect(openAttention(e, '#/now')).toEqual({ filter: DEFAULT_ATTENTION_FILTER, restored: false, excludeClosed: true });
    expect(e.hashes).toEqual(['#/now?exclude=closed']);
    const both = env(memory({ [KEY]: JSON.stringify({ v: 1, query: 'tag=work&exclude=closed' }) }));
    expect(openAttention(both, '#/now')).toEqual({ filter: { tags: ['work'], initiatives: [] }, restored: true, excludeClosed: true });
  });

  it('reads "Exclude closed" from a link and saves it', () => {
    const storage = memory();
    expect(openAttention(env(storage), '#/now?initiative=aa&exclude=closed')).toEqual({ filter: { tags: [], initiatives: ['aa'] }, restored: false, excludeClosed: true });
    expect(JSON.parse(storage.data.get(KEY)!).query).toBe('initiative=aa&exclude=closed');
  });

  it('forgets the saved filter', () => {
    const storage = memory({ [KEY]: JSON.stringify({ v: 1, query: 'tag=x' }) });
    forgetAttention(storage);
    expect(storage.data.has(KEY)).toBe(false);
    expect(openAttention(env(storage), '#/now').restored).toBe(false);
  });
});

describe('dropUnknown', () => {
  const known = [
    { slug: 'aa', tags: ['work'] },
    { slug: 'bb', tags: ['home', 'work'] },
  ];

  it('keeps a filter whose values are all known, as the same object', () => {
    const filter = { initiatives: ['bb'], tags: ['home', 'work'] };
    expect(dropUnknown(filter, known)).toBe(filter);
    expect(dropUnknown(DEFAULT_ATTENTION_FILTER, known)).toBe(DEFAULT_ATTENTION_FILTER);
  });

  it('drops an unknown initiative and keeps the rest', () => {
    expect(dropUnknown({ initiatives: ['ghost', 'aa'], tags: ['work'] }, known)).toEqual({ initiatives: ['aa'], tags: ['work'] });
  });

  it('drops an unknown tag and keeps the rest', () => {
    expect(dropUnknown({ initiatives: ['aa'], tags: ['gone', 'home'] }, known)).toEqual({ initiatives: ['aa'], tags: ['home'] });
  });

  it('drops everything when the board knows nothing the filter names', () => {
    expect(dropUnknown({ initiatives: ['ghost'], tags: ['gone'] }, known)).toEqual({ initiatives: [], tags: [] });
  });

  it('keeps a tag no initiative lists when a task carries it (its own, or a derived actor kind)', () => {
    const filter = { initiatives: ['aa'], tags: ['only-own', 'ralph', 'gone'] };
    expect(dropUnknown(filter, known, new Set(['only-own', 'ralph']))).toEqual({ initiatives: ['aa'], tags: ['only-own', 'ralph'] });
    expect(dropUnknown({ initiatives: [], tags: ['only-own'] }, known, new Set(['only-own']))).toEqual({ initiatives: [], tags: ['only-own'] });
  });
});

describe('startFilter', () => {
  const known = [{ slug: 'aa', tags: ['work'] }];
  const named = { initiatives: ['ghost', 'aa'], tags: ['gone', 'work'] };

  it('drops the unknown values of a restored filter', () => {
    expect(startFilter({ filter: named, restored: true }, known)).toEqual({ initiatives: ['aa'], tags: ['work'] });
  });

  it('drops every restored value when the lookup answered with no initiatives at all', () => {
    expect(startFilter({ filter: named, restored: true }, [])).toEqual({ initiatives: [], tags: [] });
  });

  it('keeps a restored filter as it is when the lookup failed', () => {
    expect(startFilter({ filter: named, restored: true }, undefined)).toBe(named);
  });

  it('keeps a restored filter whose values are all known, as the same object', () => {
    const filter = { initiatives: ['aa'], tags: ['work'] };
    expect(startFilter({ filter, restored: true }, known)).toBe(filter);
  });

  it('keeps the values a link names, even unknown ones and an empty answer', () => {
    expect(startFilter({ filter: named, restored: false }, known)).toBe(named);
    expect(startFilter({ filter: named, restored: false }, [])).toBe(named);
  });

  it('restores from storage and drops through the same module, the empty answer included', () => {
    const e = env(memory({ ['vh.view.v1.attention']: JSON.stringify({ v: 1, query: 'tag=gone&initiative=ghost' }) }));
    const opened = openAttention(e, '#/now');
    expect(opened.restored).toBe(true);
    expect(startFilter(opened, [])).toEqual(DEFAULT_ATTENTION_FILTER);
  });
});

describe('affectsTagSources', () => {
  it.each(['initiative_created', 'initiative_updated', 'initiative_closed', 'initiative_reopened', 'task_created', 'task_updated', 'task_closed', 'lease_started', 'lease_ended', 'lease_expired'])('%s can change a tag source', (type) => {
    expect(affectsTagSources({ type })).toBe(true);
  });
  it.each(['lease_renewed', 'lease_acked', 'ask_raised', 'ask_answered', 'note_posted', 'dep_added', 'check_recorded', 'milestone', 'key_created', 'catchup'])('%s changes none', (type) => {
    expect(affectsTagSources({ type })).toBe(false);
  });
});

describe('"Exclude closed" in the query', () => {
  it('is on only for exclude=closed, among other keys too', () => {
    expect(excludeClosedOf('exclude=closed')).toBe(true);
    expect(excludeClosedOf('tag=work&exclude=closed&initiative=aa')).toBe(true);
    expect(excludeClosedOf('exclude=done&exclude=closed')).toBe(true);
    expect(excludeClosedOf('')).toBe(false);
    expect(excludeClosedOf('exclude=done')).toBe(false);
    expect(excludeClosedOf('closed=exclude')).toBe(false);
    expect(excludeClosedOf('exclude=Closed')).toBe(false);
  });

  it('joins the filter in the fragment, last, and is left out when off', () => {
    expect(nowFragment(DEFAULT_ATTENTION_FILTER, false)).toBe('#/now');
    expect(nowFragment(DEFAULT_ATTENTION_FILTER, true)).toBe('#/now?exclude=closed');
    expect(nowFragment({ tags: ['work'], initiatives: ['aa'] }, false)).toBe('#/now?tag=work&initiative=aa');
    expect(nowFragment({ tags: ['work'], initiatives: ['aa'] }, true)).toBe('#/now?tag=work&initiative=aa&exclude=closed');
    expect(nowViewQuery({ tags: [], initiatives: ['aa'] }, true)).toBe('initiative=aa&exclude=closed');
    expect(excludeClosedOf(nowViewQuery({ tags: ['x'], initiatives: [] }, true))).toBe(true);
  });

  it('is never sent to the server: the read keeps only the filter and the window', () => {
    expect(nowQuery(filterFromQuery('tag=work&exclude=closed'))).toBe('tag=work');
  });
});
