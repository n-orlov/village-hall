import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The one exemption from the 44 px rule (PRD R13, SPEC 15): a Timeline bar (planned, actual or lease) is a data mark sized by
// its time span, so touch.spec.ts does not hold it to 44 px. That is WCAG 2.5.8's equivalent control: the same task's row label
// is a 44 px-high target that opens the same preview. For each exempted kind this spec proves it on a phone: the bar opens a
// task's preview, and that task's own label, tapped with a finger (CDP Input.dispatchTouchEvent) and then reached by keyboard
// (focus, Enter), opens the very same preview (same dialog, same task, same URL).
test.use({ viewport: { width: 390, height: 844 }, hasTouch: true });
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

function store(work: (db: DatabaseSync) => number): number {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    return work(db);
  } finally {
    db.close();
  }
}

interface Seed {
  slug: string;
  /** The tasks, one per exempted kind: a planned bar, an actual bar and a lease bar each belong to a different task. */
  planned: string;
  actual: string;
  lease: string;
}

const KINDS = [
  { kind: 'planned bar', bar: '.vis-item.tl-planned', task: (s: Seed) => s.planned, title: 'Equivalent planned' },
  { kind: 'actual bar', bar: '.vis-item.tl-actual', task: (s: Seed) => s.actual, title: 'Equivalent actual' },
  { kind: 'lease bar', bar: '.vis-item.tl-lease', task: (s: Seed) => s.lease, title: 'Equivalent lease' },
] as const;

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const slug = `e2e-tle-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tle-${name}` },
  });
  const now = Date.now();
  await post(api, '/api/v1/initiatives', { slug, title: `Equivalent ${name}` }, 201);
  const task = async (title: string, extra: object = {}) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201)).ref;
  const planned = await task('Equivalent planned', { planned_start: new Date(now - 5 * DAY).toISOString(), planned_end: new Date(now + 3 * DAY).toISOString() });
  const actual = await task('Equivalent actual');
  const lease = await task('Equivalent lease');
  const held = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: lease, intent: 'equivalent', resources: [`repo:tle-${name}`] }, 201)).lease.ref;
  [planned, actual, lease].forEach((ref, i) => {
    const seq = Number(ref.slice(slug.length + 1));
    const changed = store((db) =>
      Number(db.prepare('UPDATE tasks SET created_at = ? WHERE seq = ? AND initiative_id = (SELECT id FROM initiatives WHERE slug = ?)').run(now - (6 - 2 * i) * DAY, seq, slug).changes),
    );
    expect(changed).toBe(1);
  });
  expect(store((db) => Number(db.prepare('UPDATE leases SET started_at = ? WHERE n = ?').run(now - 2 * DAY, Number(held.slice(1))).changes))).toBe(1);
  await api.dispose();
  return { slug, planned, actual, lease };
}

const label = (page: Page, text: string) => page.locator('.vis-labelset .vis-label', { hasText: text });
const dialogOf = (page: Page, ref: string) => page.getByRole('dialog', { name: `Task ${ref}` });

async function centreOf(locator: Locator) {
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  expect(box, 'a visible box').not.toBeNull();
  return { x: box!.x + box!.width / 2, y: box!.y + box!.height / 2 };
}

async function tap(page: Page, locator: Locator) {
  const at = await centreOf(locator);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: at.x, y: at.y, id: 0 }] });
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await cdp.detach();
}

for (const [i, kind] of KINDS.entries()) {
  test(`the ${kind.kind} and its task's 44 px row label open the same preview, by tap and by keyboard`, async ({ page, playwright, baseURL }) => {
    const s = await seed(playwright, baseURL, `k${i}`);
    const ref = kind.task(s);
    await page.goto(`/#/timeline?initiative=${s.slug}`);
    await expect(page.locator('.vis-labelset .vis-label.tl-task')).toHaveCount(3, FIVE_SECONDS);
    await expect(page.locator(`${kind.bar}[data-task="${ref}"]`)).toBeVisible(FIVE_SECONDS);
    await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
    const dialog = dialogOf(page, ref);
    const same = async () => {
      await expect(dialog.getByTestId('task-title')).toHaveText(kind.title, FIVE_SECONDS);
      await expect(dialog.getByTestId('preview-open')).toHaveAttribute('href', `#/t/${ref}`);
      expect(page.url()).toContain(`task=${ref}`);
    };

    // The exempted control: the bar, tapped.
    await tap(page, page.locator(`${kind.bar}[data-task="${ref}"]`).first());
    await same();
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);

    // The equivalent control: the same task's row label is at least 44 px high, and a tap opens the same preview.
    const row = label(page, `${ref} ${kind.title}`);
    await expect(row).toHaveCount(1);
    expect((await row.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    await tap(page, row);
    await same();
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);

    // And by keyboard: focus the label (vis-timeline settles its rows a moment after the first draw and a focused label loses the
    // focus then, so the move is made again until it holds), Enter opens the same preview.
    await expect(async () => {
      await row.focus();
      await expect(row).toBeFocused({ timeout: 500 });
      await page.keyboard.press('Enter');
      await expect(dialog).toBeVisible({ timeout: 500 });
    }).toPass(FIVE_SECONDS);
    await same();
  });
}
