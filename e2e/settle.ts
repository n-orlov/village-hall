import { expect } from '@playwright/test';

/**
 * Waits until `read()` returns the same number on `reads` consecutive polls (two by default): the page has issued
 * every request it is going to issue after it was ready. This is the explicit readiness a spec uses where it would
 * otherwise wait for the browser's own idle heuristic, which holds every test for a fixed quiet period whatever
 * the page does. The poll uses the ordinary expect timeout, which stays unchanged.
 */
export async function readsTheSame(read: () => number, what: string, reads = 2): Promise<void> {
  let last = -1;
  let run = 0;
  await expect
    .poll(
      () => {
        const now = read();
        run = now === last ? run + 1 : 1;
        last = now;
        return run >= reads;
      },
      { message: `${what} kept changing`, intervals: [100] },
    )
    .toBe(true);
}
