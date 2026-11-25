// Live refresh (SPEC 11, 7.1): one EventSource per tab on GET /api/v1/stream. A view subscribes
// with a predicate over the events that concern it and a refetch; a matching event refetches that
// view, at most once per DEBOUNCE_MS burst. `attention` counts go to a listener without a fetch.
// A `reset` and a reconnect after an error refetch every subscribed (visible) view. An arrival listener hears each
// event the moment it arrives (before any debounce), and `undefined` when events may have been missed (a reset or a
// reconnect), so a view can tell a read begun before an event from one begun after it. Nothing here
// polls: the only timers are the debounce and the reopen of a stream the browser gave up on. While the document is hidden
// no subscriber refetches: each one an event (or a reset, or a reconnect) reached is marked dirty, and becoming visible
// runs one refetch per dirty subscriber.
import type { components } from './api/schema';
import { apiUrl, currentApiBase } from './apibase';

export type BoardEvent = components['schemas']['Event'];
export type AttentionCounts = components['schemas']['AttentionCounts'];

/** The indicator in the shell: the stream is open, is being (re)opened, or the browser is offline. */
export type LiveState = 'live' | 'reconnecting' | 'offline';

export const DEBOUNCE_MS = 500;
export const REOPEN_MS = 3000;
const CLOSED = 2; // EventSource.CLOSED

/** The part of EventSource this module uses; tests supply a fake. */
export interface StreamSource {
  readyState: number;
  onopen: ((e: unknown) => void) | null;
  onerror: ((e: unknown) => void) | null;
  addEventListener(type: string, listener: (e: { data: string }) => void): void;
  close(): void;
}

export interface LiveEnv {
  open(url: string): StreamSource;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(handle: unknown): void;
}

interface Subscription {
  matches: (event: BoardEvent) => boolean;
  refetch: () => unknown;
  timer?: unknown;
  /** An event reached it while the document was hidden: one refetch is owed when it is visible again. */
  dirty?: boolean;
}

function parse<T>(data: string): T | undefined {
  try {
    return JSON.parse(data) as T;
  } catch {
    return undefined;
  }
}

export class Live {
  private source?: StreamSource;
  private subs = new Set<Subscription>();
  private attention = new Set<(counts: AttentionCounts) => void>();
  private arrivals = new Set<(event: BoardEvent | undefined) => void>();
  private states = new Set<(state: LiveState) => void>();
  private online = true;
  private visible = true;
  private open = false;
  private errored = false;
  private reopenTimer?: unknown;
  private current: LiveState = 'reconnecting';

  constructor(
    private readonly url: string,
    private readonly env: LiveEnv,
  ) {}

  get state(): LiveState {
    return this.current;
  }

  /** Opens the stream (once); a second call while it is open does nothing. */
  start(): void {
    if (this.source === undefined) this.connect();
  }

  /** Closes the stream and drops every pending refetch (and every refetch owed to a hidden document). Subscribers stay registered. */
  stop(): void {
    this.source?.close();
    this.source = undefined;
    this.open = false;
    this.cancelReopen();
    for (const sub of this.subs) {
      this.cancel(sub);
      sub.dirty = false;
    }
    this.publish();
  }

  /** Refetches whenever an event matching `matches` arrives (debounced), on reset and after a reconnect. */
  subscribe(matches: (event: BoardEvent) => boolean, refetch: () => unknown): () => void {
    const sub: Subscription = { matches, refetch };
    this.subs.add(sub);
    return () => {
      this.cancel(sub);
      this.subs.delete(sub);
    };
  }

  onAttention(listener: (counts: AttentionCounts) => void): () => void {
    this.attention.add(listener);
    return () => this.attention.delete(listener);
  }

  /**
   * Hears every board event as it arrives, before the debounced refetches it schedules, and `undefined` on a reset or a
   * reconnect (events may have been missed then). The refetches of that arrival always run after it.
   */
  onArrival(listener: (event: BoardEvent | undefined) => void): () => void {
    this.arrivals.add(listener);
    return () => this.arrivals.delete(listener);
  }

  onState(listener: (state: LiveState) => void): () => void {
    this.states.add(listener);
    return () => this.states.delete(listener);
  }

  /** The browser's online/offline signal. Coming back online reopens a stream the browser gave up on. */
  setOnline(online: boolean): void {
    this.online = online;
    this.publish();
    if (online && this.source !== undefined && !this.open) this.reopenNow();
  }

  /**
   * The document's visibility. Hidden: pending refetches are cancelled and their subscribers marked dirty, and no
   * refetch runs until visible. Visible again: each dirty subscriber refetches once.
   */
  setVisible(visible: boolean): void {
    if (visible === this.visible) return;
    this.visible = visible;
    for (const sub of [...this.subs]) {
      if (!visible) {
        if (sub.timer !== undefined) {
          this.cancel(sub);
          sub.dirty = true;
        }
      } else if (sub.dirty === true) {
        sub.dirty = false;
        this.run(sub);
      }
    }
  }

  private connect(): void {
    const source = this.env.open(this.url);
    this.source = source;
    source.onopen = () => this.opened();
    source.onerror = () => this.failed(source);
    source.addEventListener('board', (e) => this.board(e.data));
    source.addEventListener('attention', (e) => this.counts(e.data));
    source.addEventListener('reset', () => this.refetchAll());
  }

  private opened(): void {
    this.open = true;
    this.publish();
    if (this.errored) {
      this.errored = false;
      this.refetchAll();
    }
  }

  private failed(source: StreamSource): void {
    this.open = false;
    this.errored = true;
    this.publish();
    // The browser retries by itself unless it closed the stream (a refused or non-event-stream answer).
    if (source.readyState === CLOSED && this.reopenTimer === undefined) {
      this.reopenTimer = this.env.setTimer(() => this.reopenNow(), REOPEN_MS);
    }
  }

  private reopenNow(): void {
    this.cancelReopen();
    this.source?.close();
    this.connect();
  }

  private cancelReopen(): void {
    if (this.reopenTimer !== undefined) this.env.clearTimer(this.reopenTimer);
    this.reopenTimer = undefined;
  }

  private board(data: string): void {
    const event = parse<BoardEvent>(data);
    if (event === undefined) return;
    for (const listener of this.arrivals) listener(event);
    for (const sub of this.subs) if (sub.matches(event)) this.schedule(sub);
  }

  private counts(data: string): void {
    const counts = parse<AttentionCounts>(data);
    if (counts === undefined) return;
    for (const listener of this.attention) listener(counts);
  }

  private refetchAll(): void {
    for (const listener of this.arrivals) listener(undefined);
    for (const sub of this.subs) this.schedule(sub);
  }

  /** Starts the burst's timer unless one is running: events inside the burst share one refetch. */
  private schedule(sub: Subscription): void {
    if (!this.visible) {
      sub.dirty = true;
      return;
    }
    if (sub.timer !== undefined) return;
    sub.timer = this.env.setTimer(() => {
      sub.timer = undefined;
      this.run(sub);
    }, DEBOUNCE_MS);
  }

  private run(sub: Subscription): void {
    // A refetch is the view's own business; one that fails shows its own error.
    Promise.resolve()
      .then(sub.refetch)
      .catch(() => {});
  }

  private cancel(sub: Subscription): void {
    if (sub.timer !== undefined) this.env.clearTimer(sub.timer);
    sub.timer = undefined;
  }

  private publish(): void {
    const next: LiveState = !this.online ? 'offline' : this.open ? 'live' : 'reconnecting';
    if (next === this.current) return;
    this.current = next;
    for (const listener of this.states) listener(next);
  }
}

let shared: Live | undefined;

/** The tab's one Live, wired to the browser's EventSource, timers and online/offline events. */
export function live(): Live {
  if (shared === undefined) {
    const url = apiUrl(currentApiBase(), 'v1/stream');
    shared = new Live(url, {
      open: (u) => new EventSource(u) as unknown as StreamSource,
      setTimer: (fn, ms) => setTimeout(fn, ms),
      clearTimer: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
    });
    shared.setOnline(navigator.onLine);
    if (typeof document !== 'undefined') {
      shared.setVisible(document.visibilityState !== 'hidden');
      document.addEventListener('visibilitychange', () => shared?.setVisible(document.visibilityState !== 'hidden'));
    }
    window.addEventListener('online', () => shared?.setOnline(true));
    window.addEventListener('offline', () => shared?.setOnline(false));
  }
  return shared;
}
