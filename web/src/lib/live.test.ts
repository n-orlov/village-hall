import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEBOUNCE_MS, REOPEN_MS, Live, live, type AttentionCounts, type LiveEnv, type LiveState, type StreamSource } from './live';

class FakeSource implements StreamSource {
  readyState = 0;
  onopen: ((e: unknown) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  closed = false;
  private listeners = new Map<string, (e: { data: string }) => void>();
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (e: { data: string }) => void): void {
    this.listeners.set(type, listener);
  }
  close(): void {
    this.closed = true;
    this.readyState = 2;
  }
  // test drivers
  opens(): void {
    this.readyState = 1;
    this.onopen?.({});
  }
  errors(closedByBrowser = false): void {
    this.readyState = closedByBrowser ? 2 : 0;
    this.onerror?.({});
  }
  send(type: string, data: unknown): void {
    this.listeners.get(type)?.({ data: typeof data === 'string' ? data : JSON.stringify(data) });
  }
}

function rig() {
  vi.useFakeTimers();
  const sources: FakeSource[] = [];
  const env: LiveEnv = {
    open: (url) => {
      const s = new FakeSource(url);
      sources.push(s);
      return s;
    },
    setTimer: (fn, ms) => setTimeout(fn, ms),
    clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
  };
  const l = new Live('/api/v1/stream', env);
  const states: LiveState[] = [];
  l.onState((s) => states.push(s));
  return { l, sources, states };
}

const ev = (type: string, initiative?: string, task?: string) => ({ id: 'X' + type, ref: 'E1', at: '', type, actor: 'a', principal: 'p', initiative, task, summary: '', data: {} });

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('connect', () => {
  it('opens one stream on the given url, once, however often it is started', () => {
    const { l, sources } = rig();
    l.start();
    l.start();
    expect(sources).toHaveLength(1);
    expect(sources[0].url).toBe('/api/v1/stream');
  });

  it('does not refetch when the stream first opens', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => true, refetch);
    l.start();
    sources[0].opens();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refetch).not.toHaveBeenCalled();
  });
});

describe('routing by predicate', () => {
  it('refetches only the subscribers whose predicate matches the event', async () => {
    const { l, sources } = rig();
    const mine = vi.fn();
    const other = vi.fn();
    l.subscribe((e) => e.initiative === 'aa', mine);
    l.subscribe((e) => e.task === 'bb-1' || e.type === 'note_posted', other);
    l.start();
    sources[0].send('board', ev('task_done', 'aa', 'aa-1'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(mine).toHaveBeenCalledTimes(1);
    expect(other).not.toHaveBeenCalled();
    sources[0].send('board', ev('note_posted'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(mine).toHaveBeenCalledTimes(1);
    expect(other).toHaveBeenCalledTimes(1);
  });

  it('stops refetching a subscriber that unsubscribed, even with a refetch pending', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    const off = l.subscribe(() => true, refetch);
    l.start();
    sources[0].send('board', ev('task_done'));
    off();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refetch).not.toHaveBeenCalled();
  });

  it('ignores a board message that is not JSON', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => true, refetch);
    l.start();
    sources[0].send('board', 'not json');
    await vi.advanceTimersByTimeAsync(2000);
    expect(refetch).not.toHaveBeenCalled();
  });

  it('survives a refetch that fails', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn(async () => {
      throw new Error('boom');
    });
    l.subscribe(() => true, refetch);
    l.start();
    sources[0].send('board', ev('task_done'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
  });
});

describe('debounce', () => {
  it('turns a burst of matching events into one refetch, and a later burst into another', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => true, refetch);
    l.start();
    for (let i = 0; i < 5; i++) {
      sources[0].send('board', ev('task_done'));
      await vi.advanceTimersByTimeAsync(DEBOUNCE_MS / 5 - 1);
    }
    expect(refetch).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
    sources[0].send('board', ev('task_done'));
    sources[0].send('board', ev('task_done'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(2);
  });
});

describe('attention', () => {
  it('passes the counts to listeners without refetching anything', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    const got: AttentionCounts[] = [];
    l.subscribe(() => true, refetch);
    const off = l.onAttention((c) => got.push(c));
    l.start();
    const counts = { open_asks: 1, done_not_closed: 0, expired_leases: 2, overdue_watches: 0, conflicts: 0, total: 3 };
    sources[0].send('attention', counts);
    sources[0].send('attention', 'garbage');
    await vi.advanceTimersByTimeAsync(2000);
    expect(got).toEqual([counts]);
    expect(refetch).not.toHaveBeenCalled();
    off();
    sources[0].send('attention', counts);
    expect(got).toHaveLength(1);
  });
});

describe('arrival', () => {
  it('hears each board event as it arrives, before the debounced refetch, and undefined on a reset or a reconnect', async () => {
    const { l, sources } = rig();
    const order: string[] = [];
    l.subscribe(() => true, () => order.push('refetch'));
    const off = l.onArrival((e) => order.push(e === undefined ? 'missed' : e.type));
    l.start();
    sources[0].opens();
    sources[0].send('board', ev('task_updated'));
    sources[0].send('board', 'garbage');
    expect(order).toEqual(['task_updated']);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(order).toEqual(['task_updated', 'refetch']);
    sources[0].send('reset', { reason: 'too_many' });
    expect(order).toEqual(['task_updated', 'refetch', 'missed']);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    sources[0].errors();
    sources[0].opens();
    expect(order).toEqual(['task_updated', 'refetch', 'missed', 'refetch', 'missed']);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    off();
    sources[0].send('board', ev('lease_started'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(order).toEqual(['task_updated', 'refetch', 'missed', 'refetch', 'missed', 'refetch', 'refetch']);
  });
});

describe('reset and reconnect', () => {
  it('refetches every subscriber on reset', async () => {
    const { l, sources } = rig();
    const a = vi.fn();
    const b = vi.fn();
    l.subscribe(() => false, a);
    l.subscribe(() => false, b);
    l.start();
    sources[0].send('reset', { reason: 'too_many' });
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
  });

  it('refetches once the stream is open again after an error, not on the error itself', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => false, refetch);
    l.start();
    sources[0].opens();
    sources[0].errors();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refetch).not.toHaveBeenCalled();
    sources[0].opens();
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
    sources[0].opens();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it('reopens a stream the browser closed, after a pause, and refetches when that one opens', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => false, refetch);
    l.start();
    sources[0].opens();
    sources[0].errors(true);
    sources[0].errors(true);
    expect(sources).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(REOPEN_MS);
    expect(sources).toHaveLength(2);
    expect(sources[0].closed).toBe(true);
    sources[1].opens();
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it('stop closes the stream, cancels pending work and the reopen', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => true, refetch);
    l.start();
    sources[0].send('board', ev('task_done'));
    sources[0].errors(true);
    l.stop();
    await vi.advanceTimersByTimeAsync(REOPEN_MS * 2);
    expect(sources).toHaveLength(1);
    expect(sources[0].closed).toBe(true);
    expect(refetch).not.toHaveBeenCalled();
  });
});

describe('a hidden document', () => {
  it('sends no refetch for 5 events while hidden, then one per subscriber that had an event once visible', async () => {
    const { l, sources } = rig();
    const tasks = vi.fn();
    const notes = vi.fn();
    const quiet = vi.fn();
    l.subscribe((e) => e.type.startsWith('task'), tasks);
    l.subscribe((e) => e.type.startsWith('note'), notes);
    l.subscribe((e) => e.type === 'never', quiet);
    l.start();
    sources[0].opens();
    l.setVisible(false);
    for (const type of ['task_done', 'task_closed', 'note_added', 'task_done', 'task_done']) sources[0].send('board', ev(type));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 10);
    expect([tasks, notes, quiet].map((f) => f.mock.calls.length)).toEqual([0, 0, 0]);
    l.setVisible(true);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 10);
    expect([tasks, notes, quiet].map((f) => f.mock.calls.length)).toEqual([1, 1, 0]);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 10);
    expect([tasks, notes, quiet].map((f) => f.mock.calls.length)).toEqual([1, 1, 0]);
  });

  it('holds a refetch whose debounce was running when the document was hidden, and runs it once on visible', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => true, refetch);
    l.start();
    sources[0].send('board', ev('task_done'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS / 2);
    l.setVisible(false);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 4);
    expect(refetch).not.toHaveBeenCalled();
    l.setVisible(true);
    l.setVisible(true);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 4);
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it('does not refetch on becoming visible when no event came while hidden', async () => {
    const { l, sources } = rig();
    const refetch = vi.fn();
    l.subscribe(() => true, refetch);
    l.start();
    sources[0].opens();
    l.setVisible(false);
    l.setVisible(true);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 4);
    expect(refetch).not.toHaveBeenCalled();
  });

  it('refetches nothing for a reconnect or a reset while hidden until visible, then once per subscriber', async () => {
    const { l, sources } = rig();
    const one = vi.fn();
    const two = vi.fn();
    l.subscribe(() => false, one);
    l.subscribe(() => false, two);
    l.start();
    sources[0].opens();
    l.setVisible(false);
    sources[0].errors();
    sources[0].opens();
    sources[0].send('reset', {});
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 10);
    expect([one, two].map((f) => f.mock.calls.length)).toEqual([0, 0]);
    l.setVisible(true);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 10);
    expect([one, two].map((f) => f.mock.calls.length)).toEqual([1, 1]);
  });

  it('still tells arrival listeners about events while hidden, and drops what is owed on stop or unsubscribe', async () => {
    const { l, sources } = rig();
    const arrived: unknown[] = [];
    l.onArrival((e) => arrived.push(e?.type));
    const stopped = vi.fn();
    const gone = vi.fn();
    l.subscribe(() => true, stopped);
    const off = l.subscribe(() => true, gone);
    l.start();
    l.setVisible(false);
    sources[0].send('board', ev('task_done'));
    expect(arrived).toEqual(['task_done']);
    off();
    l.stop();
    l.setVisible(true);
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 4);
    expect(stopped).not.toHaveBeenCalled();
    expect(gone).not.toHaveBeenCalled();
  });
});

describe('indicator states', () => {
  it('goes reconnecting, live, reconnecting after an error, live again', () => {
    const { l, sources, states } = rig();
    expect(l.state).toBe('reconnecting');
    l.start();
    sources[0].opens();
    expect(l.state).toBe('live');
    sources[0].errors();
    expect(l.state).toBe('reconnecting');
    sources[0].opens();
    expect(l.state).toBe('live');
    expect(states).toEqual(['live', 'reconnecting', 'live']);
  });

  it('is offline while the browser is offline, whatever the stream does, then recovers', async () => {
    const { l, sources, states } = rig();
    const refetch = vi.fn();
    l.subscribe(() => false, refetch);
    l.start();
    sources[0].opens();
    l.setOnline(false);
    expect(l.state).toBe('offline');
    sources[0].errors();
    expect(l.state).toBe('offline');
    l.setOnline(true);
    expect(l.state).toBe('reconnecting');
    sources[0].opens();
    expect(l.state).toBe('live');
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
    expect(states).toEqual(['live', 'offline', 'reconnecting', 'live']);
  });

  it('reopens at once on coming back online when the browser had closed the stream', () => {
    const { l, sources } = rig();
    l.start();
    sources[0].opens();
    l.setOnline(false);
    sources[0].errors(true);
    l.setOnline(true);
    expect(sources).toHaveLength(2);
  });

  it('stops telling a listener that unsubscribed', () => {
    const { l, sources } = rig();
    const seen: LiveState[] = [];
    const off = l.onState((s) => seen.push(s));
    l.start();
    off();
    sources[0].opens();
    expect(seen).toEqual([]);
  });
});

describe('the tab-wide instance', () => {
  it('is one Live on the stream under the page path, tracking the browser online state', async () => {
    vi.useFakeTimers();
    const handlers = new Map<string, () => void>();
    const made: FakeSource[] = [];
    vi.stubGlobal('window', { location: { pathname: '/board/' }, addEventListener: (t: string, f: () => void) => handlers.set(t, f) });
    vi.stubGlobal('navigator', { onLine: false });
    const doc = { visibilityState: 'visible', addEventListener: (t: string, f: () => void) => handlers.set(t, f) };
    vi.stubGlobal('document', doc);
    vi.stubGlobal(
      'EventSource',
      class extends FakeSource {
        constructor(url: string) {
          super(url);
          made.push(this);
        }
      },
    );
    const first = live();
    expect(live()).toBe(first);
    expect(first.state).toBe('offline');
    handlers.get('online')?.();
    expect(first.state).toBe('reconnecting');
    handlers.get('offline')?.();
    expect(first.state).toBe('offline');
    const refetch = vi.fn();
    first.subscribe(() => true, refetch);
    first.start();
    expect(made.map((s) => s.url)).toEqual(['/board/api/v1/stream']);
    made[0].send('board', ev('task_done'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    expect(refetch).toHaveBeenCalledTimes(1);
    doc.visibilityState = 'hidden';
    handlers.get('visibilitychange')?.();
    made[0].send('board', ev('task_done'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(refetch).toHaveBeenCalledTimes(1);
    doc.visibilityState = 'visible';
    handlers.get('visibilitychange')?.();
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(refetch).toHaveBeenCalledTimes(2);
    first.stop();
  });
});
