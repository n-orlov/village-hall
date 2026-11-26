// The notices banner above every view (PRD R9, SPEC 11): the live notices of every actor (a notice is a lease that
// says "this affects everyone": an outage, a freeze, a reboot). They are the `notices` of `GET /api/v1/now`, read once
// the shell is up and again, at most once per burst, on a `lease_*` event only: a note, an ask or a task edit cannot
// start, renew or end a notice. A notice whose time has run out is left out of the banner without a read.
import { mountPrefix } from './apibase';
import { expiresIn } from './durations';
import { isLive } from './conflicts';
import type { components } from './api/schema';
import type { Live } from './live';
import type { Browser } from './session';

export type Notice = components['schemas']['NowLease'];

/** One notice as the banner prints it. */
export interface NoticeLine {
  lease: string;
  holder: string;
  intent: string;
  resources: string[];
  timeLeft: string;
  /** The task page of the notice's task. */
  href: string;
  task: string;
}

/** The task page's fragment. */
export function noticeHref(task: string): string {
  return `#/t/${encodeURIComponent(task)}`;
}

/** The notices still live at `now`, as the banner prints them, in the order given (oldest first). */
export function noticeLines(notices: readonly Notice[], now: Date): NoticeLine[] {
  return notices
    .filter((n) => isLive(n, now))
    .map((n) => ({
      lease: n.ref,
      holder: n.actor,
      intent: n.intent,
      resources: n.resources,
      timeLeft: expiresIn(n.expires_at, now),
      href: noticeHref(n.task),
      task: n.task,
    }));
}

/** The caller's check-in, from which the banner takes the notices. A failed read rejects: the caller keeps the banner it showed. */
export async function readNotices(browser: Browser): Promise<Notice[]> {
  const response = await browser.fetch(`${mountPrefix(browser.pathname)}/api/v1/now`, { headers: { Accept: 'application/json' }, credentials: 'same-origin' });
  if (!response.ok) throw new Error(`Reading the notices failed (${response.status}).`);
  // An older server has no `notices`: no banner.
  return ((await response.json()) as Partial<components['schemas']['Now']>).notices ?? [];
}

/** Whether a live event can change the notices: a lease event (started, renewed, ended, expired ...). */
export function affectsNotices(event: { type: string }): boolean {
  return event.type.startsWith('lease_');
}

/** The longest the first read of the notices holds the view back (ms): a slow or hung read must not hide the board. */
export const FIRST_READ_CAP_MS = 3000;

/**
 * Shows the notices now and again on each event that can change them (and on a reset or reconnect, which `subscribe` adds).
 * A failed read keeps the banner on screen; the next event reads again. `settled` is called once, when the first read has
 * ended (answered or failed) or `FIRST_READ_CAP_MS` has passed, whichever is first: the view is drawn from then on, under
 * a banner that is already there. Returns the stop function.
 */
export function watchNotices(
  stream: Pick<Live, 'subscribe'>,
  read: () => Promise<Notice[]>,
  show: (notices: Notice[]) => void,
  settled: () => void = () => {},
): () => void {
  let first = true;
  const settle = () => {
    if (!first) return;
    first = false;
    settled();
  };
  const cap = setTimeout(settle, FIRST_READ_CAP_MS);
  const refresh = async () => {
    try {
      show(await read());
    } catch {
      // keep the banner on screen
    } finally {
      settle();
    }
  };
  void refresh();
  const stop = stream.subscribe(affectsNotices, refresh);
  return () => {
    clearTimeout(cap);
    stop();
  };
}
