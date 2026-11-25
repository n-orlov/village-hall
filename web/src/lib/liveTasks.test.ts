import { afterEach, describe, expect, it, vi, type Mock } from 'vitest';
import { affectsLiveTasks, countLiveTasks, liveTasksQuery, watchLiveTasks } from './liveTasks';
import { DEBOUNCE_MS, Live, type LiveEnv, type StreamSource } from './live';

describe('liveTasksQuery', () => {
  it('asks for the open tasks that have a live lease, with no page size: the count form has no rows', () => {
    expect(liveTasksQuery().toString()).toBe('status=open&active=true');
  });
});

describe('countLiveTasks', () => {
  it('is the count the count form answers for the open, active tasks', async () => {
    const count = vi.fn().mockResolvedValue(3);
    expect(await countLiveTasks({ count })).toBe(3);
    expect(count).toHaveBeenCalledTimes(1);
    expect(count.mock.calls[0][0].get('active')).toBe('true');
    expect(count.mock.calls[0][0].get('status')).toBe('open');
  });

  it('is zero when no task holds a live lease', async () => {
    expect(await countLiveTasks({ count: vi.fn().mockResolvedValue(0) })).toBe(0);
  });

  it('rejects when the read fails, so the caller keeps the count it showed', async () => {
    await expect(countLiveTasks({ count: vi.fn().mockRejectedValue(new Error('down')) })).rejects.toThrow('down');
  });
});

describe('affectsLiveTasks', () => {
  it('is true for task_* and lease_* events', () => {
    for (const type of ['task_created', 'task_done', 'task_closed', 'task_reopened', 'task_updated', 'lease_started', 'lease_ended', 'lease_expired', 'lease_renewed']) {
      expect(affectsLiveTasks({ type }), type).toBe(true);
    }
  });

  it('is false for every other event, even one that names a task', () => {
    for (const type of ['note_added', 'milestone', 'check_recorded', 'ask_raised', 'ask_answered', 'dep_added', 'tag_added', 'initiative_created', 'tasks', 'lease']) {
      expect(affectsLiveTasks({ type, task: 'a-1' } as { type: string }), type).toBe(false);
    }
  });
});

class FakeSource implements StreamSource {
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

const ev = (type: string, task?: string) => ({ id: 'X' + type, ref: 'E1', at: '', type, actor: 'a', principal: 'p', task, summary: '', data: {} });

describe('watchLiveTasks on a live stream', () => {
  afterEach(() => vi.useRealTimers());

  function rig(count: Mock<(query: URLSearchParams) => Promise<number>>) {
    vi.useFakeTimers();
    const source = new FakeSource();
    const env: LiveEnv = { open: () => source, setTimer: (fn, ms) => setTimeout(fn, ms), clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };
    const stream = new Live('/api/v1/stream', env);
    stream.start();
    const shown: number[] = [];
    const stop = watchLiveTasks(stream, { count }, (n) => shown.push(n));
    return { source, shown, stop };
  }

  it('reads the count once when it starts', async () => {
    const count = vi.fn<(query: URLSearchParams) => Promise<number>>().mockResolvedValue(2);
    const { shown } = rig(count);
    await vi.advanceTimersByTimeAsync(0);
    expect(count).toHaveBeenCalledTimes(1);
    expect(shown).toEqual([2]);
  });

  it('makes no badge call for a note_added on a task, and one call of the count form for a lease_started', async () => {
    const count = vi.fn<(query: URLSearchParams) => Promise<number>>().mockResolvedValue(1);
    const { source, shown } = rig(count);
    await vi.advanceTimersByTimeAsync(0);
    count.mockClear();

    source.send('board', ev('note_added', 'aa-1'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(count).toHaveBeenCalledTimes(0);

    count.mockResolvedValue(2);
    source.send('board', ev('lease_started', 'aa-1'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(count).toHaveBeenCalledTimes(1);
    expect(count.mock.calls[0][0].toString()).toBe('status=open&active=true');
    expect(shown.at(-1)).toBe(2);
  });

  it('keeps the count on screen when a read fails, and stops reading once stopped', async () => {
    const count = vi.fn<(query: URLSearchParams) => Promise<number>>().mockResolvedValueOnce(4).mockRejectedValueOnce(new Error('down')).mockResolvedValue(5);
    const { source, shown, stop } = rig(count);
    await vi.advanceTimersByTimeAsync(0);
    source.send('board', ev('lease_ended', 'aa-1'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(shown).toEqual([4]);
    stop();
    source.send('board', ev('task_done', 'aa-1'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(count).toHaveBeenCalledTimes(2);
  });
});
