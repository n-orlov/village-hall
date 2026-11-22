import { describe, expect, it } from 'vitest';
import {
  DEFAULT_WINDOW,
  WINDOW_STORAGE_KEY,
  WINDOWS,
  flightRows,
  loadWindow,
  parseWindow,
  recentEmptyText,
  recentRows,
  saveWindow,
  withoutClosed,
  type InFlight,
  type Recent,
} from './nowFlight';

const NOW = new Date('2026-01-01T12:00:00Z');

describe('the window preference', () => {
  it('uses a versioned vh. key, never the prototype one', () => {
    expect(WINDOW_STORAGE_KEY).toMatch(/^vh\.[a-z.]+\.v\d+$/);
    expect(WINDOW_STORAGE_KEY.startsWith('vh.proto.')).toBe(false);
  });

  it('offers 8h, 24h and 3d, defaulting to 24h', () => {
    expect(WINDOWS.map((w) => w.value)).toEqual(['8h', '24h', '3d']);
    expect(DEFAULT_WINDOW).toBe('24h');
    expect(loadWindow({ getItem: () => null, setItem: () => undefined })).toBe('24h');
    expect(loadWindow(undefined)).toBe('24h');
  });

  it('persists a choice and reads it back', () => {
    const data = new Map<string, string>();
    const storage = { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
    saveWindow(storage, '3d');
    expect(data.get(WINDOW_STORAGE_KEY)).toBe('3d');
    expect(loadWindow(storage)).toBe('3d');
  });

  it('ignores a stored value that is not one of the three', () => {
    for (const bad of ['', '1h', '24H', '7d', '"24h"', 'null', '{}']) expect(parseWindow(bad)).toBe('24h');
    expect(parseWindow(null)).toBe('24h');
    expect(parseWindow(undefined)).toBe('24h');
    for (const good of ['8h', '24h', '3d']) expect(parseWindow(good)).toBe(good);
  });

  it('survives blocked storage', () => {
    const blocked = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    };
    expect(loadWindow(blocked)).toBe('24h');
    expect(() => saveWindow(blocked, '8h')).not.toThrow();
  });
});

const task = (ref: string, title = `Title ${ref}`) => ({ task: ref, title, initiative: 'aa', status: 'open' as const, kind: 'work' as const, tags: [], tag_marks: [] });

describe('flightRows', () => {
  const items: InFlight[] = [
    {
      task: task('aa-2'),
      leases: [
        { lease: 'L7', holder: { handle: 'claude:x', way_back: 'w' }, intent: 'edit', started_at: '2026-01-01T11:00:00Z', renewed_at: '2026-01-01T11:30:00Z', expires_at: '2026-01-01T12:41:00Z' },
        { lease: 'L9', holder: { handle: 'sue' }, intent: 'review', started_at: '2026-01-01T11:10:00Z', renewed_at: '2026-01-01T11:10:00Z', expires_at: '2026-01-01T12:00:30Z' },
      ],
    },
    { task: task('aa/1'), leases: [] },
  ];

  it('makes one row per task with its leases nested, holder, intent and time left', () => {
    const rows = flightRows(items, NOW);
    expect(rows.map((r) => r.task)).toEqual(['aa-2', 'aa/1']);
    expect(rows[0]).toMatchObject({ title: 'Title aa-2', initiative: 'aa', href: '#/t/aa-2' });
    expect(rows[0]!.leases).toEqual([
      { lease: 'L7', holder: 'claude:x', intent: 'edit', ttl: 'expires in 41m' },
      { lease: 'L9', holder: 'sue', intent: 'review', ttl: 'expires in under a minute' },
    ]);
  });

  it('escapes the ref in the link and phrases a lease past its time', () => {
    expect(flightRows(items, NOW)[1]!.href).toBe('#/t/aa%2F1');
    const late = flightRows([{ task: task('aa-3'), leases: [{ ...items[0]!.leases[0]!, expires_at: '2026-01-01T11:58:00Z' }] }], NOW);
    expect(late[0]!.leases[0]!.ttl).toBe('expired 2m ago');
  });

  it('is empty for no tasks', () => {
    expect(flightRows([], NOW)).toEqual([]);
  });
});

describe('recentRows', () => {
  const items: Recent[] = [
    { task: task('aa-4'), last_event: { type: 'lease_ended', summary: 'L3 ended: done', actor: { handle: 'bob' }, at: '2026-01-01T11:48:00Z' } },
  ];

  it('shows the last event with its type, actor and age, in the server order', () => {
    const rows = recentRows(items, NOW);
    expect(rows).toEqual([
      { task: 'aa-4', title: 'Title aa-4', initiative: 'aa', href: '#/t/aa-4', last: 'lease_ended by bob 12 minutes ago', via: '', summary: 'L3 ended: done', at: '2026-01-01T11:48:00Z', by: 'bob' },
    ]);
  });

  describe('via <origin>, read from the event itself', () => {
    const ev = (actor: string, origin?: string | null): Recent => ({
      task: task('aa-5'),
      last_event: { type: 'task_created', summary: 's', actor: { handle: actor }, at: '2026-01-01T11:48:00Z', ...(origin === undefined ? {} : { origin }) } as Recent['last_event'],
    });

    it('says "via X" when the event\'s origin is set and is not its actor', () => {
      const [row] = recentRows([ev('pi:review', 'operator')], NOW);
      expect(row.via).toBe('via operator');
      expect(row.last).toBe('task_created by pi:review via operator 12 minutes ago');
    });
    it('says nothing when the origin is the actor', () => {
      const [row] = recentRows([ev('alice', 'alice')], NOW);
      expect(row.via).toBe('');
      expect(row.last).toBe('task_created by alice 12 minutes ago');
    });
    it('says nothing when the origin is absent, null or empty', () => {
      for (const origin of [undefined, null, '']) {
        const [row] = recentRows([ev('pi:review', origin)], NOW);
        expect(row.via).toBe('');
        expect(row.last).toBe('task_created by pi:review 12 minutes ago');
      }
    });
    it('reads each row\'s own origin, so two events by one actor differ', () => {
      const rows = recentRows([ev('bot', 'alice'), ev('bot', 'bob'), ev('bot', 'bot')], NOW);
      expect(rows.map((r) => r.via)).toEqual(['via alice', 'via bob', '']);
    });
  });
});

describe('withoutClosed ("Exclude closed")', () => {
  const touched = (ref: string, status: 'open' | 'done' | 'closed'): Recent => ({
    task: { ...task(ref), status },
    last_event: { type: 'task_updated', summary: ref, actor: { handle: 'bob' }, at: '2026-01-01T11:48:00Z' },
  });
  const items = [touched('aa-1', 'closed'), touched('aa-2', 'done'), touched('aa-3', 'open'), touched('aa-4', 'closed')];

  it('keeps every task, in the server order, when it is off', () => {
    const kept = withoutClosed(items, false);
    expect(kept.map((i) => i.task.task)).toEqual(['aa-1', 'aa-2', 'aa-3', 'aa-4']);
    expect(kept).not.toBe(items);
  });

  it('drops only the closed tasks when it is on: a done task stays', () => {
    expect(withoutClosed(items, true).map((i) => i.task.task)).toEqual(['aa-2', 'aa-3']);
  });

  it('leaves nothing when every task is closed, and an empty list empty', () => {
    expect(withoutClosed([touched('aa-1', 'closed')], true)).toEqual([]);
    expect(withoutClosed([], true)).toEqual([]);
  });
});

describe('recentEmptyText', () => {
  it('says nothing was touched when nothing is hidden', () => {
    expect(recentEmptyText(0)).toBe('Nothing was touched in this window.');
  });

  it('counts the closed tasks "Exclude closed" hides', () => {
    expect(recentEmptyText(1)).toBe('Nothing open or done was touched in this window; 1 closed task is hidden.');
    expect(recentEmptyText(3)).toBe('Nothing open or done was touched in this window; 3 closed tasks are hidden.');
  });
});
