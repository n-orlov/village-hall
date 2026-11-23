import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Timeline's colours and legend (SPEC 11, R2): a board with every kind and state the chart draws, read through the
// DOM. The legend is visible, and each swatch's computed --tl equals that of a drawn item of the same class and is the
// value of the design token the class is meant to map to, in both themes at the phone and the desktop viewport.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const themes = ['light', 'dark'] as const;
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;

/** Every class the legend lists and the design token it takes its --tl from (SPEC 11, lib/timeline.ts TIMELINE_LEGEND). */
const TOKENS: Readonly<Record<string, string>> = {
  'tl-planned': '--text-muted',
  'tl-actual tl-open': '--accent',
  'tl-actual tl-done': '--success',
  'tl-actual tl-closed': '--text-muted',
  'tl-lease tl-lease-live': '--success',
  'tl-lease tl-lease-expired': '--warning',
  'tl-lease tl-lease-ended': '--text-muted',
  'tl-marker tl-marker-milestone': '--accent',
  'tl-marker tl-marker-ask': '--warning',
  'tl-marker tl-marker-check': '--success',
  'tl-marker tl-marker-done': '--success',
  'tl-marker tl-marker-closed': '--text-muted',
};

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

// Runs a lease out the way the Go scenarios do: its expiry moves into the past in the e2e store, then an API read expires it.
async function expireLease(api: APIRequestContext, ref: string) {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db.prepare('UPDATE leases SET expires_at = ? WHERE n = ?').run(Date.now() - 30_000, Number(ref.slice(1)));
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
  expect((await api.get('/api/v1/leases?state=expired')).status()).toBe(200);
}

// One initiative: task A is open, planned, has a milestone, a check, an open ask and a live, an ended and an expired lease;
// task B is done; task C is closed (so its bar is closed and it has a close marker).
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<string> {
  const slug = `e2e-tlc-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: {
      Authorization: `Bearer ${process.env.E2E_API_KEY}`,
      'Board-Actor': `tlc-${name}`,
      'Board-Actor-Way-Back': `ralphctl status tlc-${name}`,
    },
  });
  const now = Date.now();
  await post(api, '/api/v1/initiatives', { slug, title: `Timeline colours ${name}` }, 201);
  const task = async (title: string, extra: object = {}) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201)).ref;
  const a = await task('Task A', { kind: 'watch', cadence_seconds: 900, planned_start: new Date(now - 3 * DAY).toISOString(), planned_end: new Date(now + 2 * DAY).toISOString() });
  const b = await task('Task B');
  const c = await task('Task C');
  await post(api, `/api/v1/tasks/${a}/milestones`, { summary: 'Halfway there' }, 200);
  await post(api, `/api/v1/tasks/${a}/checks`, { summary: 'Nothing changed' }, 200);
  await post(api, '/api/v1/asks', { question: `Which way for ${name}?`, task: a, mode: 'poll', blocking: true }, 201);
  const lease = async (intent: string, n: number) => (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: a, intent, resources: [`repo:tlc-${name}-${n}`] }, 201)).lease.ref;
  const ended = await lease('first try', 1);
  await post(api, `/api/v1/leases/${ended}/end`, { outcome: 'handed over' }, 200);
  const expired = await lease('ran out', 2);
  await expireLease(api, expired);
  await lease('second try', 3);
  await post(api, `/api/v1/tasks/${b}/done`, {}, 200);
  await post(api, `/api/v1/tasks/${c}/close`, { resolution: 'superseded' }, 200);
  await api.dispose();
  return slug;
}

const selector = (className: string) => `.${className.split(' ').join('.')}`;

/** The computed --tl of an element, and of the design token the class is meant to map to, read where the chart is drawn. */
const colour = (page: Page, locator: string, token?: string) =>
  page.locator(locator).first().evaluate((el, tok) => (tok === undefined ? getComputedStyle(el).getPropertyValue('--tl') : getComputedStyle(document.documentElement).getPropertyValue(tok)).trim(), token);

for (const vp of viewports) {
  for (const theme of themes) {
    test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}, ${theme} theme`, () => {
      test.use({ viewport: vp.size, colorScheme: theme });

      test('the legend is visible and each swatch has the --tl of a drawn item of its class and of its design token', async ({ page, playwright, baseURL }) => {
        const slug = await seed(playwright, baseURL, `${vp.name}-${theme}`);
        await page.goto(`/#/timeline?initiative=${slug}&closed=7`);
        await expect(page.getByTestId('timeline-view')).toBeVisible();
        await expect(page.locator('.vis-item.tl-actual')).toHaveCount(3, FIVE_SECONDS);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);

        const legend = page.getByTestId('timeline-legend');
        await expect(legend).toBeVisible();
        await expect(legend.locator('li')).toHaveCount(Object.keys(TOKENS).length);

        const seen = new Set<string>();
        for (const [className, token] of Object.entries(TOKENS)) {
          const drawn = `.timeline-frame .vis-item${selector(className)}`;
          await expect(page.locator(drawn).first(), `${className} drawn`).toBeAttached(FIVE_SECONDS);
          const swatch = `[data-testid=timeline-legend] ${selector(className)}`;
          await expect(page.locator(swatch), `${className} in the legend`).toHaveCount(1);
          await expect(page.locator(swatch)).toBeVisible();
          const want = await colour(page, drawn);
          expect(want, `${className} sets --tl`).not.toBe('');
          expect(await colour(page, swatch), `${className}: the swatch and the drawn item`).toBe(want);
          expect(want, `${className} maps to ${token}`).toBe(await colour(page, ':root', token));
          seen.add(want);
        }
        // Four different colours tell the states apart: accent, success, warning and muted.
        expect(seen.size).toBe(4);
      });
    });
  }
}
