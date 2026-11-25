import { describe, expect, it } from 'vitest';
import type { Attention } from './attentionView';
import { BANDS, bandRows } from './nowBands';
import { flightRows, recentRows, type InFlight, type Recent } from './nowFlight';
import {
  DEFAULT_ORDER,
  NOW_CHOICES,
  NOW_COLUMNS,
  ORDER_STORAGE_KEY,
  loadOrder,
  orderToSort,
  parseOrder,
  saveOrder,
  sortByOrder,
  sortToOrder,
  type NowOrder,
} from './nowSort';
import { optionValue, pickOption, sortOptions } from './sortControl';

const NOW = new Date('2026-01-01T12:00:00Z');
const counts = { open_asks: 0, done_not_closed: 0, expired_leases: 0, overdue_watches: 0, conflicts: 0, total: 0 };
const who = (handle: string) => ({ handle });
// Three items per band, as the server might give them: neither newest nor oldest first, handles out of order.
// Times (hours before 12:00): 1 (zed), 3 (amy), 2 (bob). Newest = zed, bob, amy. Oldest = amy, bob, zed. By actor = amy, bob, zed.
const T = { zed: '2026-01-01T11:00:00Z', amy: '2026-01-01T09:00:00Z', bob: '2026-01-01T10:00:00Z' };
const EXPECT: Record<NowOrder, string[]> = { newest: ['zed', 'bob', 'amy'], oldest: ['amy', 'bob', 'zed'], actor: ['amy', 'bob', 'zed'] };

const attention: Attention = {
  counts,
  open_asks: (['zed', 'amy', 'bob'] as const).map((h) => ({
    ref: `A-${h}`,
    question: 'q',
    asker: who(h),
    mode: 'poll' as const,
    asker_waiting: true,
    blocking: false,
    created_at: T[h],
    age_seconds: 1,
  })),
  expired_leases: (['zed', 'amy', 'bob'] as const).map((h) => ({ lease: `L-${h}`, holder: who(h), task: 't-1', title: 'x', intent: 'i', renewed_at: T[h], expired_at: T[h], notice: false, tags: [], tag_marks: [] })),
  overdue_watches: (['zed', 'amy', 'bob'] as const).map((h) => ({ task: `w-${h}`, title: 'x', owner: who(h), next_check_at: T[h], tags: [], tag_marks: [] })),
  done_not_closed: (['zed', 'amy', 'bob'] as const).map((h) => ({ task: `d-${h}`, title: 'x', done_by: who(h), done_at: T[h], tags: [], tag_marks: [] })),
};
const task = (ref: string) => ({ task: ref, title: 'x', initiative: 'aa', status: 'open' as const, kind: 'work' as const, tags: [], tag_marks: [] });
const inFlight: InFlight[] = (['zed', 'amy', 'bob'] as const).map((h) => ({
  task: task(`f-${h}`),
  leases: [{ lease: `F-${h}`, holder: who(h), intent: 'i', started_at: T[h], renewed_at: T[h], expires_at: '2026-01-01T13:00:00Z' }],
}));
const recent: Recent[] = (['zed', 'amy', 'bob'] as const).map((h) => ({
  task: task(`r-${h}`),
  last_event: { type: 'note_added', summary: 's', actor: who(h), at: T[h] },
}));

const handleOf = (id: string) => id.slice(id.indexOf('-') + 1);
const orders: NowOrder[] = ['newest', 'oldest', 'actor'];

describe('the order inside each of the six band kinds', () => {
  const band = (key: string) => BANDS.find((b) => b.key === key)!;
  const kinds: [string, (order: NowOrder) => string[]][] = [
    ['asks', (o) => sortByOrder(bandRows(band('asks'), attention, NOW), o, (r) => ({ at: r.row.at, by: r.row.actors[0]?.handle })).map((r) => handleOf(r.row.id))],
    ['expired leases', (o) => sortByOrder(bandRows(band('expired'), attention, NOW), o, (r) => ({ at: r.row.at, by: r.row.actors[0]?.handle })).map((r) => handleOf(r.row.id))],
    ['overdue watches', (o) => sortByOrder(bandRows(band('watches'), attention, NOW), o, (r) => ({ at: r.row.at, by: r.row.actors[0]?.handle })).map((r) => handleOf(r.row.id))],
    ['done, not closed', (o) => sortByOrder(bandRows(band('done'), attention, NOW), o, (r) => ({ at: r.row.at, by: r.row.actors[0]?.handle })).map((r) => handleOf(r.row.id))],
    ['in flight', (o) => sortByOrder(flightRows(inFlight, NOW), o, (r) => r).map((r) => handleOf(r.task))],
    ['recently touched', (o) => sortByOrder(recentRows(recent, NOW), o, (r) => r).map((r) => handleOf(r.task))],
  ];
  for (const [name, read] of kinds) {
    for (const order of orders) {
      it(`${name}: ${order}`, () => expect(read(order)).toEqual(EXPECT[order]));
    }
  }

  // Conflicts began at: db 10:30 (holder zoe), cache 08:30 (holder amy), queue 11:30 (holder bob). Leases: zed 11:00, bob 10:00, amy 09:00.
  const conflict = (resource: string, handle: string, since: string | undefined) => ({
    resource,
    holders: [{ lease: `H-${resource}`, actor: who(handle), task: 't-1', intent: 'i', expires_at: T.zed }],
    tasks: ['t-1'],
    since: since as string,
  });
  const key = (r: ReturnType<typeof bandRows>[number]) => ({ at: r.row.at, by: r.row.actors[0]?.handle });
  const ids = (a: Attention, order: NowOrder) => sortByOrder(bandRows(band('expired'), a, NOW), order, key).map((r) => r.row.id);
  const mixed: Attention = {
    ...attention,
    conflicts: [conflict('db', 'zoe', '2026-01-01T10:30:00Z'), conflict('cache', 'amy', '2026-01-01T08:30:00Z'), conflict('queue', 'bob', '2026-01-01T11:30:00Z')],
  };

  it('expired leases and conflicts sort together by time: a conflict is as old as its contest', () => {
    expect(ids(mixed, 'newest')).toEqual(['queue', 'L-zed', 'db', 'L-bob', 'L-amy', 'cache']);
    expect(ids(mixed, 'oldest')).toEqual(['cache', 'L-amy', 'L-bob', 'db', 'L-zed', 'queue']);
  });

  it('breaks an actor tie between a lease and a conflict with the newer time', () => {
    expect(ids(mixed, 'actor')).toEqual(['L-amy', 'cache', 'queue', 'L-bob', 'L-zed', 'db']);
    const tied: Attention = { ...mixed, conflicts: [conflict('older', 'amy', '2026-01-01T07:00:00Z'), conflict('newer', 'amy', '2026-01-01T11:45:00Z')] };
    expect(ids(tied, 'actor').slice(0, 3)).toEqual(['newer', 'L-amy', 'older']);
  });

  it('orders conflicts of different ages among themselves in both time orders, whatever the order given', () => {
    const only: Attention = { counts, conflicts: mixed.conflicts };
    expect(ids(only, 'newest')).toEqual(['queue', 'db', 'cache']);
    expect(ids(only, 'oldest')).toEqual(['cache', 'db', 'queue']);
    expect(ids({ counts, conflicts: [...mixed.conflicts!].reverse() }, 'newest')).toEqual(['queue', 'db', 'cache']);
  });

  it('a conflict row carries the instant its contest began as its time', () => {
    expect(bandRows(band('expired'), mixed, NOW).filter((r) => r.section === 'conflicts').map((r) => r.row.at)).toEqual([
      '2026-01-01T10:30:00Z',
      '2026-01-01T08:30:00Z',
      '2026-01-01T11:30:00Z',
    ]);
  });
});

describe('sortByOrder', () => {
  const key = (r: { at?: string; by?: string }) => r;
  it('breaks an actor tie with the newest first, and keeps equal items in their input order', () => {
    const rows = [
      { id: 1, by: 'bob', at: T.amy },
      { id: 2, by: 'Bob', at: T.zed },
      { id: 3, by: 'amy', at: T.amy },
      { id: 4, by: 'amy', at: T.amy },
    ];
    expect(sortByOrder(rows, 'actor', key).map((r) => r.id)).toEqual([3, 4, 2, 1]);
    expect(sortByOrder(rows, 'newest', key).map((r) => r.id)).toEqual([2, 1, 3, 4]);
    expect(sortByOrder(rows, 'oldest', key).map((r) => r.id)).toEqual([1, 3, 4, 2]);
  });

  it('puts an item with no time or no actor last, and does not touch its input', () => {
    const rows = [{ id: 1 }, { id: 2, at: T.amy, by: 'bob' }, { id: 3, at: 'not a time', by: 'amy' }];
    const copy = JSON.stringify(rows);
    expect(sortByOrder(rows, 'newest', key).map((r) => r.id)).toEqual([2, 1, 3]);
    expect(sortByOrder(rows, 'oldest', key).map((r) => r.id)).toEqual([2, 1, 3]);
    expect(sortByOrder(rows, 'actor', key).map((r) => r.id)).toEqual([3, 2, 1]);
    expect(JSON.stringify(rows)).toBe(copy);
  });

  it('takes the newest lease of a task for its time and the first handle among its leases for its actor', () => {
    const two: InFlight[] = [
      {
        task: task('f-1'),
        leases: [
          { lease: 'a', holder: who('zed'), intent: 'i', started_at: T.amy, renewed_at: T.amy, expires_at: T.zed },
          { lease: 'b', holder: who('Bob'), intent: 'i', started_at: T.zed, renewed_at: T.zed, expires_at: T.zed },
        ],
      },
    ];
    const [row] = flightRows(two, NOW);
    expect([row!.at, row!.by]).toEqual([T.zed, 'Bob']);
  });
});

describe('the shared control on Now', () => {
  it('offers newest first (the default), oldest first and by actor, and reads each back', () => {
    expect(NOW_CHOICES.map((c) => c.label)).toEqual(['Newest first', 'Oldest first', 'By actor']);
    expect(DEFAULT_ORDER).toBe('newest');
    for (const order of orders) {
      const sort = orderToSort(order);
      expect(sortToOrder(sort)).toBe(order);
      expect(NOW_CHOICES.map((c) => c.value)).toContain(optionValue(sort));
      expect(sortToOrder(pickOption(optionValue(sort), NOW_COLUMNS))).toBe(order);
    }
    // The values are the shared control's own option values, so a shared-control test can drive the select by value.
    const shared = new Set(sortOptions(NOW_COLUMNS).map((o) => o.value));
    for (const c of NOW_CHOICES) expect(shared.has(c.value)).toBe(true);
  });

  it('reads time descending as newest first', () => {
    expect(sortToOrder({ column: 'time', dir: 'desc' })).toBe('newest');
  });
});

describe('the remembered order', () => {
  const memory = () => {
    const data = new Map<string, string>();
    return { data, storage: { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) } };
  };

  it('lives under a versioned vh. key, never the prototype one', () => {
    expect(ORDER_STORAGE_KEY).toMatch(/^vh\.[a-z.]+\.v\d+$/);
    expect(ORDER_STORAGE_KEY.startsWith('vh.proto.')).toBe(false);
  });

  it('persists each order and reads it back', () => {
    for (const order of orders) {
      const { data, storage } = memory();
      saveOrder(storage, order);
      expect(data.get(ORDER_STORAGE_KEY)).toBe(order);
      expect(loadOrder(storage)).toBe(order);
    }
  });

  it('falls back to newest first for nothing, a bad value or blocked storage', () => {
    for (const bad of ['', 'Newest', 'time:asc', '{}', 'null']) expect(parseOrder(bad)).toBe('newest');
    expect(parseOrder(null)).toBe('newest');
    expect(parseOrder(undefined)).toBe('newest');
    expect(loadOrder(undefined)).toBe('newest');
    const blocked = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    };
    expect(loadOrder(blocked)).toBe('newest');
    expect(() => saveOrder(blocked, 'actor')).not.toThrow();
  });
});
