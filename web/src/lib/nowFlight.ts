// The right column of the Now view (PRD R1, SPEC 11): In flight (one row per task with a live lease, its leases nested)
// and Recently touched (tasks with no live lease touched within the window), both shaped here from the one
// `GET /attention/now` answer, and the window switch (8h, 24h or 3d) remembered under `vh.now.window.v1`.
import type { components } from './api/schema';
import { expiresIn } from './durations';
import { relativeTime } from './format';
import { viaOrigin } from './origin';

type NowBoard = components['schemas']['AttentionNow'];
export type NowWindow = components['schemas']['NowWindow'];
export type InFlight = NowBoard['in_flight'][number];
export type Recent = NowBoard['recent'][number];

export const WINDOWS: readonly { value: NowWindow; label: string }[] = [
  { value: '8h', label: '8h' },
  { value: '24h', label: '24h' },
  { value: '3d', label: '3d' },
];
export const DEFAULT_WINDOW: NowWindow = '24h';
/** The versioned storage key of the window. */
export const WINDOW_STORAGE_KEY = 'vh.now.window.v1';

/** A stored value as a window: anything that is not one of the three (or absent) is the default. */
export function parseWindow(raw: string | null | undefined): NowWindow {
  return WINDOWS.find((w) => w.value === raw)?.value ?? DEFAULT_WINDOW;
}

/** The slice of `Storage` the preference uses. */
export type WindowStorage = Pick<Storage, 'getItem' | 'setItem'>;

export function loadWindow(storage: WindowStorage | undefined): NowWindow {
  try {
    return parseWindow(storage?.getItem(WINDOW_STORAGE_KEY));
  } catch {
    return DEFAULT_WINDOW; // storage can be blocked
  }
}

export function saveWindow(storage: WindowStorage | undefined, window: NowWindow): void {
  try {
    storage?.setItem(WINDOW_STORAGE_KEY, window);
  } catch {
    // The choice still applies to this page view.
  }
}

/** The fragment of a task's page. */
function taskHref(ref: string): string {
  return `#/t/${encodeURIComponent(ref)}`;
}

/** One live lease nested under its task: who holds it, why, and how long is left. */
export interface LeaseRow {
  lease: string;
  holder: string;
  intent: string;
  /** "expires in 41m". */
  ttl: string;
}

/** One In flight row: the task and its leases in the order the server gave them. */
export interface FlightRow {
  task: string;
  title: string;
  initiative: string;
  href: string;
  leases: LeaseRow[];
  /** When the newest of its live leases started: what the sort by time reads. */
  at: string;
  /** The handle that sorts first among the lease holders: what the sort by actor reads. */
  by: string;
}

function newest(times: readonly string[]): string {
  return times.reduce((a, b) => (a === '' || Date.parse(b) > Date.parse(a) ? b : a), '');
}

function firstHandle(handles: readonly string[]): string {
  return handles.reduce((a, b) => (a === '' || b.localeCompare(a, undefined, { sensitivity: 'base' }) < 0 ? b : a), '');
}

/** The In flight rows in the server's order (the task whose newest live lease started last first); `now` phrases the TTLs. */
export function flightRows(items: readonly InFlight[], now: Date): FlightRow[] {
  return items.map((item) => ({
    at: newest(item.leases.map((l) => l.started_at)),
    by: firstHandle(item.leases.map((l) => l.holder.handle)),
    task: item.task.task,
    title: item.task.title,
    initiative: item.task.initiative,
    href: taskHref(item.task.task),
    leases: item.leases.map((l) => ({ lease: l.lease, holder: l.holder.handle, intent: l.intent, ttl: expiresIn(l.expires_at, now) })),
  }));
}

/** One Recently touched row: the task, and the last event written on it. */
export interface RecentRow {
  task: string;
  title: string;
  initiative: string;
  href: string;
  /** "lease_ended by bob 12 minutes ago", or "lease_ended by bob via alice 12 minutes ago" for a write made on an operator's behalf. */
  last: string;
  /** "via alice" when the event's own origin is set and is not its actor, else "". */
  via: string;
  /** The event's own summary. */
  summary: string;
  at: string;
  /** The handle of the actor who wrote the last event: what the sort by actor reads. */
  by: string;
}

/**
 * The Recently touched items with "Exclude closed" applied: without the closed tasks when `exclude` is set, else all of them.
 * A done task stays (it waits for the operator to verify and close it). The server's order is kept.
 */
export function withoutClosed(items: readonly Recent[], exclude: boolean): Recent[] {
  return exclude ? items.filter((item) => item.task.status !== 'closed') : [...items];
}

/** What an empty Recently touched says: `hidden` is the number of closed tasks "Exclude closed" took out of it. */
export function recentEmptyText(hidden: number): string {
  if (hidden === 0) return 'Nothing was touched in this window.';
  return `Nothing open or done was touched in this window; ${hidden} closed ${hidden === 1 ? 'task is' : 'tasks are'} hidden.`;
}

/** The Recently touched rows in the server's order (latest event first). */
export function recentRows(items: readonly Recent[], now: Date): RecentRow[] {
  return items.map((item) => {
    const e = item.last_event;
    const via = viaOrigin(e.actor.handle, e.origin);
    return {
      task: item.task.task,
      title: item.task.title,
      initiative: item.task.initiative,
      href: taskHref(item.task.task),
      last: `${e.type} by ${e.actor.handle}${via === '' ? '' : ` ${via}`} ${relativeTime(e.at, now)}`,
      via,
      summary: e.summary,
      at: e.at,
      by: e.actor.handle,
    };
  });
}
