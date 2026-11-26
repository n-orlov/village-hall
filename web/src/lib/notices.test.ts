import { afterEach, describe, expect, it, vi } from 'vitest';
import { FIRST_READ_CAP_MS, affectsNotices, noticeHref, noticeLines, readNotices, watchNotices, type Notice } from './notices';
import { DEBOUNCE_MS, Live, type LiveEnv, type StreamSource } from './live';
import type { Browser } from './session';

const NOW = new Date('2026-11-25T12:00:00Z');

function notice(ref: string, over: Partial<Notice> = {}): Notice {
  return {
    id: `ID${ref}`,
    ref,
    task: 'aa-1',
    actor: 'ops',
    intent: 'rebooting the build host',
    ttl_seconds: 3600,
    started_at: '2026-11-25T11:50:00Z',
    renewed_at: '2026-11-25T11:50:00Z',
    expires_at: '2026-11-25T12:41:00Z',
    state: 'live',
    resources: ['host:build-1', 'repo:main'],
    notice: true,
    title: 'Reboot',
    ...over,
  } as Notice;
}

function browserAnswering(body: unknown, status = 200): { browser: Browser; fetch: ReturnType<typeof vi.fn> } {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));
  return { browser: { fetch, pathname: '/', assign: () => {}, setHash: () => {} }, fetch };
}

describe('noticeLines', () => {
  it('prints the holder, the intent, the resources, the time left and a link to the task of each live notice', () => {
    expect(noticeLines([notice('L3', { task: 'aa-12' })], NOW)).toEqual([
      {
        lease: 'L3',
        holder: 'ops',
        intent: 'rebooting the build host',
        resources: ['host:build-1', 'repo:main'],
        timeLeft: 'expires in 41m',
        href: '#/t/aa-12',
        task: 'aa-12',
      },
    ]);
  });

  it('keeps the order given and leaves out a notice that has expired or ended', () => {
    const lines = noticeLines(
      [notice('L1'), notice('L2', { expires_at: '2026-11-25T11:59:59Z' }), notice('L3', { state: 'ended' }), notice('L4', { actor: 'infra' })],
      NOW,
    );
    expect(lines.map((l) => l.lease)).toEqual(['L1', 'L4']);
  });

  it('is empty when there is no notice', () => {
    expect(noticeLines([], NOW)).toEqual([]);
  });
});

describe('noticeHref', () => {
  it('is the task page of the ref', () => {
    expect(noticeHref('aa-1')).toBe('#/t/aa-1');
  });
});

describe('readNotices', () => {
  it('is the notices of GET /api/v1/now', async () => {
    const { browser, fetch } = browserAnswering({ actor: 'me', notices: [notice('L7')] });
    const got = await readNotices(browser);
    expect(got.map((n) => n.ref)).toEqual(['L7']);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0]![0]).toBe('/api/v1/now');
  });

  it('is empty from a server that has no `notices`', async () => {
    expect(await readNotices(browserAnswering({ actor: 'me' }).browser)).toEqual([]);
  });

  it('rejects on a refusal, so the banner on screen stays', async () => {
    await expect(readNotices(browserAnswering({}, 503).browser)).rejects.toThrow('503');
  });
});

describe('affectsNotices', () => {
  it('is true for lease_* events', () => {
    for (const type of ['lease_started', 'lease_renewed', 'lease_ended', 'lease_expired']) expect(affectsNotices({ type }), type).toBe(true);
  });

  it('is false for every other event', () => {
    for (const type of ['note_added', 'task_done', 'ask_raised', 'milestone', 'tasks']) expect(affectsNotices({ type }), type).toBe(false);
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

const ev = (type: string) => ({ id: 'X' + type, ref: 'E1', at: '', type, actor: 'a', principal: 'p', task: 'aa-1', summary: '', data: {} });

describe('watchNotices on a live stream', () => {
  afterEach(() => vi.useRealTimers());

  function rig(answers: Notice[][]) {
    vi.useFakeTimers();
    const source = new FakeSource();
    const env: LiveEnv = { open: () => source, setTimer: (fn, ms) => setTimeout(fn, ms), clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };
    const stream = new Live('/api/v1/stream', env);
    stream.start();
    const fetch = vi.fn();
    for (const notices of answers) fetch.mockResolvedValueOnce(new Response(JSON.stringify({ actor: 'me', notices })));
    const browser: Browser = { fetch, pathname: '/', assign: () => {}, setHash: () => {} };
    const shown: Notice[][] = [];
    const stop = watchNotices(stream, () => readNotices(browser), (n) => shown.push(n));
    return { source, shown, stop, fetch };
  }

  it('shows the notices of GET /now once when it starts', async () => {
    const { shown, fetch } = rig([[notice('L1')]]);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(shown.map((list) => list.map((n) => n.ref))).toEqual([['L1']]);
  });

  it('reads again on a lease_started and not on a note_added', async () => {
    const { source, shown, fetch } = rig([[notice('L1')], [notice('L1'), notice('L2')]]);
    await vi.advanceTimersByTimeAsync(0);
    fetch.mockClear();

    source.send('board', ev('note_added'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(fetch).toHaveBeenCalledTimes(0);

    source.send('board', ev('lease_started'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(shown.at(-1)!.map((n) => n.ref)).toEqual(['L1', 'L2']);
  });

  it('shows no notice once the last one has ended, keeps the banner when a read fails, and stops reading once stopped', async () => {
    const { source, shown, stop, fetch } = rig([[notice('L1')], []]);
    await vi.advanceTimersByTimeAsync(0);
    source.send('board', ev('lease_ended'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(shown.at(-1)).toEqual([]);

    fetch.mockRejectedValueOnce(new Error('down'));
    source.send('board', ev('lease_started'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(shown).toHaveLength(2);

    stop();
    fetch.mockClear();
    source.send('board', ev('lease_ended'));
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
    expect(fetch).toHaveBeenCalledTimes(0);
  });

  describe('settled: the view is drawn once the first read has ended', () => {
    function streamOf() {
      const source = new FakeSource();
      const env: LiveEnv = { open: () => source, setTimer: (fn, ms) => setTimeout(fn, ms), clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>) };
      const stream = new Live('/api/v1/stream', env);
      stream.start();
      return { source, stream };
    }

    it('after the first read answers, once only, and after the notices are shown', async () => {
      vi.useFakeTimers();
      const { source, stream } = streamOf();
      const order: string[] = [];
      let answer: (n: Notice[]) => void = () => {};
      const read = vi.fn(() => new Promise<Notice[]>((resolve) => (answer = resolve)));
      watchNotices(stream, read, () => order.push('shown'), () => order.push('settled'));
      await vi.advanceTimersByTimeAsync(100);
      expect(order).toEqual([]);
      answer([notice('L1')]);
      await vi.advanceTimersByTimeAsync(0);
      expect(order).toEqual(['shown', 'settled']);

      source.send('board', ev('lease_started'));
      await vi.advanceTimersByTimeAsync(DEBOUNCE_MS * 2);
      await vi.advanceTimersByTimeAsync(FIRST_READ_CAP_MS * 2);
      expect(order).toEqual(['shown', 'settled']);
    });

    it('when the first read fails or the server is old (no notices): the view is not held back', async () => {
      vi.useFakeTimers();
      const { stream } = streamOf();
      let settled = 0;
      watchNotices(stream, () => Promise.reject(new Error('down')), () => {}, () => settled++);
      await vi.advanceTimersByTimeAsync(0);
      expect(settled).toBe(1);

      let oldServer = 0;
      const { browser } = browserAnswering({ actor: 'me' });
      watchNotices(stream, () => readNotices(browser), () => {}, () => oldServer++);
      await vi.advanceTimersByTimeAsync(0);
      expect(oldServer).toBe(1);
    });

    it('when the first read never ends: after the cap, and not before', async () => {
      vi.useFakeTimers();
      const { stream } = streamOf();
      let settled = 0;
      watchNotices(stream, () => new Promise<Notice[]>(() => {}), () => {}, () => settled++);
      await vi.advanceTimersByTimeAsync(FIRST_READ_CAP_MS - 1);
      expect(settled).toBe(0);
      await vi.advanceTimersByTimeAsync(1);
      expect(settled).toBe(1);
    });

    it('stopping before the cap cancels it', async () => {
      vi.useFakeTimers();
      const { stream } = streamOf();
      let settled = 0;
      const stop = watchNotices(stream, () => new Promise<Notice[]>(() => {}), () => {}, () => settled++);
      stop();
      await vi.advanceTimersByTimeAsync(FIRST_READ_CAP_MS * 2);
      expect(settled).toBe(0);
    });
  });
});
