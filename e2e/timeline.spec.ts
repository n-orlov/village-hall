import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Timeline view at #/timeline (PRD R6, SPEC 11), read through the DOM vis-timeline draws: the initiative group and
// its task rows, the planned, actual and lease bars and the markers, collapsing the initiative, the zoom buttons and
// Shift+wheel, a click on a task, a lease, a marker or a row previewing the task in a dialog (Back closes it), and one SVG arrow per dependency from the blocking
// task's bar to the dependent task's bar (end points inside the two bars, so the spec fails when none is drawn).
// The board is shared with the other specs, so each test seeds an initiative of its own and filters the view to it.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;
const HOUR = 3_600_000;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

// The e2e store is written to move history: tasks and leases are made now through the API, then dated back so the bars
// have a length on a window of three weeks (the same way needs-me.spec.ts runs a lease out).
function store<T>(work: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    return work(db);
  } finally {
    db.close();
  }
}

function dateTask(slug: string, ref: string, createdAt: number) {
  const seq = Number(ref.slice(slug.length + 1));
  const changed = store((db) =>
    db.prepare('UPDATE tasks SET created_at = ? WHERE seq = ? AND initiative_id = (SELECT id FROM initiatives WHERE slug = ?)').run(createdAt, seq, slug),
  );
  expect(changed.changes).toBe(1);
}

function dateLease(ref: string, startedAt: number, endedAt: number | null) {
  const changed = store((db) => db.prepare('UPDATE leases SET started_at = ?, ended_at = COALESCE(?, ended_at) WHERE n = ?').run(startedAt, endedAt, Number(ref.slice(1))));
  expect(changed.changes).toBe(1);
}

interface Seed {
  slug: string;
  api: APIRequestContext;
  /** In row order: A and C block B and D; E has no dependency. */
  a: string;
  b: string;
  c: string;
  d: string;
  e: string;
  ended: string;
  live: string;
}

// One initiative with five tasks (A to E), two dependency pairs (B waits on A, D waits on C), a planned bar on A, an ended
// and a live lease on A, and a milestone and a check on A.
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const slug = `e2e-tl-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tl-${name}` },
  });
  const now = Date.now();
  await post(api, '/api/v1/initiatives', { slug, title: `Timeline ${name}` }, 201);
  const task = async (title: string, extra: object = {}) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201)).ref;
  const a = await task('Task A', { kind: 'watch', cadence_seconds: 900, planned_start: new Date(now - 7 * DAY).toISOString(), planned_end: new Date(now + 2 * DAY).toISOString() });
  const b = await task('Task B');
  const c = await task('Task C');
  const d = await task('Task D');
  const e = await task('Task E');
  await post(api, `/api/v1/tasks/${b}/deps/${a}`, {}, 200);
  await post(api, `/api/v1/tasks/${d}/deps/${c}`, {}, 200);
  await post(api, `/api/v1/tasks/${a}/milestones`, { summary: 'Halfway there' }, 200);
  await post(api, `/api/v1/tasks/${a}/checks`, { summary: 'Nothing changed' }, 200);
  const ended = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: a, intent: 'first try', resources: [`repo:tl-${name}-1`] }, 201)).lease.ref;
  await post(api, `/api/v1/leases/${ended}/end`, { outcome: 'handed over' }, 200);
  const live = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: a, intent: 'second try', resources: [`repo:tl-${name}-2`] }, 201)).lease.ref;
  [a, b, c, d, e].forEach((ref, i) => dateTask(slug, ref, now - (6 - i) * DAY));
  dateLease(ended, now - 4 * DAY, now - 3 * DAY);
  dateLease(live, now - 2 * DAY, null);
  return { slug, api, a, b, c, d, e, ended, live };
}

const open = async (page: Page, s: Seed) => {
  await page.goto(`/#/timeline?initiative=${s.slug}`);
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-item.tl-actual')).toHaveCount(5, FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS); // vis-timeline's cover is off once it has drawn
};

const labels = (page: Page) => page.locator('.vis-labelset .vis-label');
const bar = (page: Page, ref: string) => page.locator(`.vis-item.tl-id-act-${ref}`);
const span = async (page: Page) => Number(await page.getByTestId('timeline-span').getAttribute('data-span-ms'));
/** The window the chart shows and the length of it, read in one go from the readout the view keeps. */
const shown = (page: Page) =>
  page.getByTestId('timeline-span').evaluate((el) => {
    const from = Number(el.getAttribute('data-from-ms'));
    const to = Number(el.getAttribute('data-to-ms'));
    return { from, to, span: to - from };
  });

/** Puts the pointer over the middle of the part of the chart that is on screen, and returns the chart's own geometry. */
async function aimAtChart(page: Page, fx = 0.5) {
  const frame = page.getByTestId('timeline-chart');
  await frame.scrollIntoViewIfNeeded();
  const box = (await page.locator('.vis-panel.vis-center').boundingBox())!;
  const top = Math.max(box.y, 0);
  const bottom = Math.min(box.y + box.height, page.viewportSize()!.height);
  const at = { x: box.x + box.width * fx, y: (top + bottom) / 2, left: box.x, width: box.width };
  await page.mouse.move(at.x, at.y);
  return at;
}

/** The instant under x pixels from the left of the chart's centre panel, in the window now shown. */
async function instantAt(page: Page, x: number, left: number, width: number) {
  const w = await shown(page);
  return { instant: w.from + ((x - left) / width) * w.span, span: w.span };
}

const arrows = (page: Page) => page.locator('svg.tl-arrows path.tl-arrow');

interface Pt {
  x: number;
  y: number;
}
interface Drawn {
  from: string;
  to: string;
  start: Pt;
  end: Pt;
  fromBox: { left: number; top: number; right: number; bottom: number } | null;
  toBox: { left: number; top: number; right: number; bottom: number } | null;
}

// Every arrow with its end points (the first and last point of its path, in page coordinates) and the page boxes of the
// two bars it joins, read from the DOM.
async function drawn(page: Page): Promise<Drawn[]> {
  return page.evaluate(() => {
    const svg = document.querySelector('svg.tl-arrows')!;
    const at = svg.getBoundingClientRect();
    const box = (ref: string) => {
      const el = document.querySelector(`.vis-item.tl-id-act-${ref}`);
      if (el === null) return null;
      const r = el.getBoundingClientRect();
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
    };
    return [...svg.querySelectorAll('path.tl-arrow')].map((path) => {
      const nums = (path.getAttribute('d') ?? '').match(/-?\d+(\.\d+)?/g)!.map(Number);
      const from = path.getAttribute('data-from')!;
      const to = path.getAttribute('data-to')!;
      return {
        from,
        to,
        start: { x: at.left + nums[0]!, y: at.top + nums[1]! },
        end: { x: at.left + nums[nums.length - 2]!, y: at.top + nums[nums.length - 1]! },
        fromBox: box(from),
        toBox: box(to),
      };
    });
  });
}

const within = (p: Pt, box: Drawn['fromBox']) =>
  box !== null && p.x >= box.left - 1 && p.x <= box.right + 1 && p.y >= box.top - 1 && p.y <= box.bottom + 1;

// Exactly the two pairs, each path ending in the bars of its two tasks.
async function expectTwoArrows(page: Page, s: Seed) {
  await expect(arrows(page)).toHaveCount(2, FIVE_SECONDS);
  const all = await drawn(page);
  expect(all.map((x) => `${x.from}>${x.to}`).sort()).toEqual([`${s.a}>${s.b}`, `${s.c}>${s.d}`].sort());
  for (const x of all) {
    expect(within(x.start, x.fromBox), `${x.from}>${x.to} starts in the bar of ${x.from}: ${JSON.stringify(x)}`).toBe(true);
    expect(within(x.end, x.toBox), `${x.from}>${x.to} ends in the bar of ${x.to}: ${JSON.stringify(x)}`).toBe(true);
  }
}

async function expectNoSidewaysScroll(page: Page) {
  const m = await page.evaluate(() => ({ s: document.documentElement.scrollWidth, c: document.documentElement.clientWidth, b: document.body.scrollWidth }));
  expect(m.s).toBeLessThanOrEqual(m.c);
  expect(m.b).toBeLessThanOrEqual(m.c);
}

async function expect44(locator: Locator) {
  const box = await locator.boundingBox();
  expect(box, 'a visible box').not.toBeNull();
  expect(box!.width).toBeGreaterThanOrEqual(44);
  expect(box!.height).toBeGreaterThanOrEqual(44);
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('draws the initiative group, its five task rows, the planned, actual and lease bars and the markers', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `rows-${vp.name}`);
      await open(page, s);
      await expect(labels(page)).toHaveCount(6);
      await expect(labels(page).first()).toContainText(`Timeline rows-${vp.name}`);
      for (const [i, ref] of [s.a, s.b, s.c, s.d, s.e].entries()) await expect(labels(page).nth(i + 1)).toContainText(`${ref} Task ${'ABCDE'[i]}`);
      await expect(page.locator('.vis-item.tl-planned')).toHaveCount(1);
      await expect(page.locator('.vis-item.tl-lease')).toHaveCount(2);
      await expect(page.locator('.vis-item.tl-lease-ended')).toHaveCount(1);
      await expect(page.locator('.vis-item.tl-lease-live')).toHaveCount(1);
      await expect(page.locator('.vis-item.vis-point.tl-marker-milestone')).toHaveText('◆');
      await expect(page.locator('.vis-item.vis-point.tl-marker-check')).toHaveText('✓');
      await expect(page.locator('.vis-item.tl-actual.tl-open')).toHaveCount(5);
      await s.api.dispose();
    });

    test('collapsing the initiative hides its task rows and bars and expanding brings them back', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `fold-${vp.name}`);
      await open(page, s);
      await labels(page).first().click();
      await expect(labels(page)).toHaveCount(1, FIVE_SECONDS);
      await expect(page.locator('.vis-item.tl-actual')).toHaveCount(0);
      await expect(arrows(page)).toHaveCount(0);
      await labels(page).first().click();
      await expect(labels(page)).toHaveCount(6, FIVE_SECONDS);
      await expect(page.locator('.vis-item.tl-actual')).toHaveCount(5);
      await expectTwoArrows(page, s);
      await s.api.dispose();
    });

    test('the zoom buttons and Shift+wheel change the range between an hour and a quarter, a plain wheel never zooms', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `zoom-${vp.name}`);
      await open(page, s);
      await expect(page.getByTestId('timeline-span')).toContainText('Showing 3 weeks');
      const start = await span(page);
      expect(Math.round(start / DAY)).toBe(21);

      await page.getByTestId('zoom-in').click();
      await expect.poll(() => span(page)).toBeLessThan(start);
      const closer = await span(page);
      await page.getByTestId('zoom-out').click();
      await expect.poll(() => span(page)).toBeGreaterThan(closer);

      // Shift+wheel over the chart zooms (one direction in, the other out), the same range the buttons move; a plain
      // wheel leaves the span alone (the navigation tests below read the window the wheel moves).
      await aimAtChart(page);
      const before = await span(page);
      await page.mouse.wheel(0, 400);
      await page.mouse.wheel(300, 0);
      expect(Math.abs((await span(page)) - before)).toBeLessThan(1000);
      await page.keyboard.down('Shift');
      await page.mouse.wheel(0, 400);
      await expect.poll(() => span(page)).not.toBe(before);
      const wheeled = await span(page);
      await page.mouse.wheel(0, -400);
      await expect.poll(() => span(page)).not.toBe(wheeled);
      await page.keyboard.up('Shift');
      expect(Math.sign(wheeled - before) * Math.sign((await span(page)) - wheeled)).toBe(-1); // back the other way

      for (let i = 0; i < 20 && (await page.getByTestId('zoom-in').isEnabled()); i++) await page.getByTestId('zoom-in').click();
      await expect(page.getByTestId('zoom-in')).toBeDisabled();
      expect(await span(page)).toBeGreaterThanOrEqual(HOUR);
      expect(await span(page)).toBeLessThanOrEqual(HOUR * 1.02);
      await expect(page.getByTestId('timeline-span')).toContainText('Showing 1 hour');

      for (let i = 0; i < 20 && (await page.getByTestId('zoom-out').isEnabled()); i++) await page.getByTestId('zoom-out').click();
      await expect(page.getByTestId('zoom-out')).toBeDisabled();
      expect(await span(page)).toBeLessThanOrEqual(92 * DAY);
      expect(await span(page)).toBeGreaterThanOrEqual(92 * DAY * 0.98);
      await expect(page.getByTestId('timeline-span')).toContainText('Showing 3 months');
      await expect(page.getByTestId('zoom-in')).toBeEnabled();
      await s.api.dispose();
    });

    // The preview of a task over the chart (PRD R5): the task page's body, read-only, in the one dialog (a bottom sheet on a phone).
    const dialogOf = (page: Page, ref: string) => page.getByRole('dialog', { name: `Task ${ref}` });
    const openers: { name: string; ref: (s: Seed) => string; title: string; target: (page: Page, s: Seed) => Locator }[] = [
      { name: 'a task bar', ref: (s) => s.e, title: 'Task E', target: (page, s) => bar(page, s.e) },
      { name: 'a lease bar', ref: (s) => s.a, title: 'Task A', target: (page) => page.locator('.vis-item.tl-lease-live') },
      { name: 'a marker', ref: (s) => s.a, title: 'Task A', target: (page) => page.locator('.vis-item.vis-point.tl-marker-check') },
      { name: 'a row label', ref: (s) => s.c, title: 'Task C', target: (page) => labels(page).nth(3) },
    ];
    for (const [i, opener] of openers.entries()) {
      test(`a click on ${opener.name} previews the task read-only in a dialog: no actions, the page and the chart do not scroll`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `prev${i}-${vp.name}`);
        await open(page, s);
        const before = await span(page);
        await opener.target(page, s).click();
        const dialog = dialogOf(page, opener.ref(s));
        await expect(dialog).toBeVisible(FIVE_SECONDS);
        await expect(dialog.getByTestId('task-title')).toHaveText(opener.title, FIVE_SECONDS);
        expect(page.url()).toContain(`task=${opener.ref(s)}`);
        // Read-only: the task page's body without its actions, and a way to the full page.
        await expect(dialog.getByTestId('task-actions')).toHaveCount(0);
        await expect(dialog.getByRole('button', { name: /^(Edit|Done|Close task|Post note|Milestone|Add dependency)$/ })).toHaveCount(0);
        await expect(dialog.getByTestId('preview-open')).toHaveAttribute('href', `#/t/${opener.ref(s)}`);
        // The page behind does not scroll while it is open, and the chart under the dialog takes no wheel.
        expect(await page.evaluate(() => [document.documentElement.style.overflow, document.body.style.overflow])).toEqual(['hidden', 'hidden']);
        await page.mouse.move(vp.size.width / 2, vp.size.height / 2);
        await page.mouse.wheel(0, 400);
        await page.mouse.wheel(300, 0);
        await page.mouse.wheel(0, -200);
        await page.waitForTimeout(300);
        expect(await span(page)).toBe(before);
        await s.api.dispose();
      });
    }

    test('Esc returns the focus to the bar that opened the preview', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `esc-${vp.name}`);
      await open(page, s);
      await bar(page, s.e).click();
      const dialog = dialogOf(page, s.e);
      await expect(dialog.getByTestId('task-title')).toHaveText('Task E', FIVE_SECONDS);
      await page.keyboard.press('Escape');
      await expect(dialog).toHaveCount(0);
      expect(page.url()).not.toContain('task=');
      expect(await page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.task)).toBe(s.e);
      await s.api.dispose();
    });

    test('the Close button returns the focus to the bar that opened the preview', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `close-${vp.name}`);
      await open(page, s);
      await bar(page, s.d).click();
      const dialog = dialogOf(page, s.d);
      await expect(dialog.getByTestId('task-title')).toHaveText('Task D', FIVE_SECONDS);
      await expect(dialog.getByTestId('preview-close')).toBeFocused();
      await dialog.getByTestId('preview-close').click();
      await expect(dialog).toHaveCount(0);
      expect(page.url()).not.toContain('task=');
      expect(await page.evaluate(() => (document.activeElement as HTMLElement | null)?.dataset.task)).toBe(s.d);
      await s.api.dispose();
    });

    test('Back closes the preview and a second Back leaves the view; Open full page lands on the task page', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `back-${vp.name}`);
      await page.goto('/#/tasks');
      await expect(page.getByTestId('tasks-view')).toBeVisible();
      await open(page, s);
      await bar(page, s.e).click();
      const dialog = dialogOf(page, s.e);
      await expect(dialog.getByTestId('task-title')).toHaveText('Task E', FIVE_SECONDS);
      await page.goBack();
      await expect(dialog).toHaveCount(0);
      expect(page.url()).not.toContain('task=');
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      expect(await page.evaluate(() => document.body.style.overflow)).toBe('');
      await page.goBack();
      await expect(page).toHaveURL(/#\/tasks$/);

      await open(page, s);
      await labels(page).nth(3).click(); // the row of task C
      const c = dialogOf(page, s.c);
      await expect(c.getByTestId('task-title')).toHaveText('Task C', FIVE_SECONDS);
      await c.getByTestId('preview-open').click();
      await expect(page).toHaveURL(new RegExp(`#/t/${s.c}$`));
      await expect(page.getByTestId('task-page').getByTestId('task-title')).toHaveText('Task C', FIVE_SECONDS);
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await s.api.dispose();
    });

    test('a link and a reload with task=<ref> reopen the preview over the given window; closing it keeps the window and Back then leaves the view', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `deep-${vp.name}`);
      const from = new Date(Date.now() - 10 * DAY).toISOString().replace(/\.\d{3}Z$/, 'Z');
      const to = new Date(Date.now() + 4 * DAY).toISOString().replace(/\.\d{3}Z$/, 'Z');
      await page.goto('/#/tasks');
      await page.goto(`/#/timeline?initiative=${s.slug}&from=${from}&to=${to}&task=${s.b}`);
      const dialog = dialogOf(page, s.b);
      await expect(dialog.getByTestId('task-title')).toHaveText('Task B', FIVE_SECONDS);
      await expect.poll(() => span(page)).toBe(14 * DAY);
      await page.reload();
      await expect(dialog.getByTestId('task-title')).toHaveText('Task B', FIVE_SECONDS);
      await expect.poll(() => span(page)).toBe(14 * DAY);
      await page.keyboard.press('Escape');
      await expect(dialog).toHaveCount(0);
      expect(page.url()).not.toContain('task=');
      expect(page.url()).toContain('from=');
      expect(await span(page)).toBe(14 * DAY);
      await page.goBack();
      await expect(page).toHaveURL(/#\/tasks$/);
      await s.api.dispose();
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`the open preview is axe clean in the ${theme} theme, has 44 px controls and does not scroll the page sideways`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `axe-${theme}-${vp.name}`);
        await page.emulateMedia({ colorScheme: theme });
        await open(page, s);
        await bar(page, s.a).click();
        const dialog = dialogOf(page, s.a);
        await expect(dialog.getByTestId('task-title')).toHaveText('Task A', FIVE_SECONDS);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
        const { violations } = await new AxeBuilder({ page }).include('[data-testid=dialog]').analyze();
        const blocking = violations.filter((v) => v.impact === 'serious' || v.impact === 'critical').map((v) => ({ rule: v.id, nodes: v.nodes.map((n) => n.target) }));
        expect(blocking, 'serious or critical axe violations').toEqual([]);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
        for (const control of [dialog.getByTestId('preview-open'), dialog.getByTestId('preview-close')]) {
          const box = (await control.boundingBox())!;
          expect(Math.min(box.width, box.height)).toBeGreaterThanOrEqual(44 - 0.5);
        }
        await s.api.dispose();
      });
    }

    test('a milestone posted through the API appears in the open preview within 5 s, and the chart redrawn under it keeps its window', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `live-${vp.name}`);
      await open(page, s);
      await page.getByTestId('zoom-in').click();
      await expect.poll(() => page.url()).toContain('from=');
      const before = await span(page);
      await bar(page, s.d).click();
      const dialog = dialogOf(page, s.d);
      await expect(dialog.getByTestId('task-title')).toHaveText('Task D', FIVE_SECONDS);
      await post(s.api, `/api/v1/tasks/${s.d}/milestones`, { summary: 'Seen live in the preview' }, 200);
      await expect(dialog.getByTestId('event-summary').filter({ hasText: 'Seen live in the preview' })).toHaveCount(1, FIVE_SECONDS);
      await expect(dialog).toBeVisible();
      expect(await span(page)).toBe(before);
      await s.api.dispose();
    });

    test('draws one arrow per dependency, from the blocking bar to the dependent bar, and redraws it on zoom, pan and collapse', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `arrows-${vp.name}`);
      await open(page, s);
      await expectTwoArrows(page, s);
      const first = JSON.stringify(await drawn(page));

      await page.getByTestId('zoom-in').click();
      await expect.poll(async () => JSON.stringify(await drawn(page))).not.toBe(first); // moved with the bars
      await expectTwoArrows(page, s);

      // The filters above the chart wrap onto several rows at phone width, so the chart's lower edge can be below the fold:
      // drag on the part of it that is on screen and clear of the phone tab bar (the bottom 60 px).
      await page.getByTestId('timeline-chart').scrollIntoViewIfNeeded();
      const chart = (await page.getByTestId('timeline-chart').boundingBox())!;
      const y = Math.min(chart.y + chart.height - 20, vp.size.height - 100);
      const before = JSON.stringify(await drawn(page));
      await page.mouse.move(chart.x + chart.width / 2, y);
      await page.mouse.down();
      await page.mouse.move(chart.x + chart.width / 2 + 40, y, { steps: 4 });
      await page.mouse.up();
      await expect.poll(async () => JSON.stringify(await drawn(page))).not.toBe(before);
      await expectTwoArrows(page, s);

      await labels(page).first().click();
      await expect(arrows(page)).toHaveCount(0, FIVE_SECONDS);
      await labels(page).first().click();
      await expectTwoArrows(page, s);
      await s.api.dispose();
    });

    test('keeps the page from scrolling sideways, and its controls and rows are 44 px targets', async ({ page, playwright, baseURL }) => {
      // The 44 px targets are the phone's and Sparse's; Compact (the default) from 768 px lowers them to 28 px (e2e/density-lists.spec.ts).
      await page.addInitScript(() => localStorage.setItem('vh.density.v1', 'sparse'));
      const s = await seed(playwright, baseURL, `fit-${vp.name}`);
      await open(page, s);
      await expectNoSidewaysScroll(page);
      await expect44(page.getByTestId('zoom-in'));
      await expect44(page.getByTestId('zoom-out'));
      if (vp.name === 'phone') {
        for (const label of await labels(page).all()) {
          const box = (await label.boundingBox())!;
          expect(box.height).toBeGreaterThanOrEqual(44);
        }
        const chart = (await page.getByTestId('timeline-chart').boundingBox())!;
        expect(chart.x + chart.width).toBeLessThanOrEqual(vp.size.width + 0.5);
      }
      await page.getByTestId('zoom-out').click();
      await expectNoSidewaysScroll(page);
      await s.api.dispose();
    });
  });
}

// The chart is as tall as the screen below its top (SPEC 11), whatever lands above it later: the board's initiative chips
// come after the rows, so a slow initiatives answer draws the chips after the chart, and the chart follows its top down.
test.describe('the chart under the filters that arrive late', () => {
  test.use({ viewport: viewports[1].size });

  test('a slow initiatives answer leaves the chart as tall as the screen below its top, not as it was drawn', async ({ page, playwright, baseURL }) => {
    const s = await seed(playwright, baseURL, 'late-chips');
    // Enough initiatives of its own that their chips wrap onto several lines, which pushes the chart down when they land.
    for (let i = 0; i < 40; i++) await post(s.api, '/api/v1/initiatives', { slug: `${s.slug}-chip-${String(i).padStart(2, '0')}`, title: `Late chip ${i}` }, 201);
    // More rows than the screen below the chart holds, so the chart takes all of it (it is no taller than its rows).
    for (let i = 0; i < 30; i++) await post(s.api, '/api/v1/tasks', { initiative: s.slug, title: `Late chip row ${i}` }, 201);
    let held = 0;
    await page.route(/\/api\/v1\/initiatives\?/, async (route) => {
      held++;
      await new Promise((resolve) => setTimeout(resolve, 1500));
      await route.continue();
    });
    await page.goto(`/#/timeline?initiative=${s.slug}`);
    await expect(page.locator('.vis-labelset .vis-label').first()).toBeVisible(FIVE_SECONDS);
    const drawnAt = (await page.getByTestId('timeline-chart').boundingBox())!.y;
    await expect(page.getByTestId('chip-initiative')).not.toHaveCount(1, { timeout: 10_000 }); // the chosen one's chip is there from the start
    expect((await page.getByTestId('timeline-chart').boundingBox())!.y, 'the chips pushed the chart down').toBeGreaterThan(drawnAt + 20);
    expect(held).toBeGreaterThan(0);

    const viewport = page.viewportSize()!.height;
    const tabBar = 16; // below 768 px the phone's tab bar takes 80; this is a desktop
    const fits = async () => {
      const box = (await page.getByTestId('timeline-chart').boundingBox())!;
      const top = box.y + (await page.evaluate(() => window.scrollY));
      return { height: Math.round(box.height), want: Math.floor(Math.max(300, viewport - top - tabBar, (viewport - tabBar) * 0.7)) };
    };
    await expect.poll(async () => Math.abs((await fits()).height - (await fits()).want), FIVE_SECONDS).toBeLessThanOrEqual(1);
    await s.api.dispose();
  });
});

// PRD R1: the wheel, a pinch and a drag navigate the chart. vis-timeline pans and pinches by itself; the view takes the
// wheel (a sideways one pans, Shift or Ctrl zoom around the pointer, a vertical one scrolls the rows).
test.describe('desktop wheel navigation', () => {
  test.use({ viewport: viewports[1].size });

  test('a vertical wheel leaves the span alone, a sideways wheel moves the window, Shift+wheel and Ctrl+wheel zoom around the pointer', async ({ page, playwright, baseURL }) => {
    const s = await seed(playwright, baseURL, 'wheel-desktop');
    await open(page, s);
    const pointer = await aimAtChart(page, 0.3);

    // A vertical wheel scrolls the rows (or the page): the window does not move and the span does not change.
    const start = await shown(page);
    await page.mouse.wheel(0, 400);
    await page.mouse.wheel(0, -400);
    await aimAtChart(page, 0.3); // the page may have scrolled under the pointer
    const vertical = await shown(page);
    expect(vertical.span).toBe(start.span);
    expect(vertical.from).toBe(start.from);

    // A sideways wheel moves the window, later for a positive delta and earlier for a negative one, without zooming.
    await page.mouse.wheel(300, 0);
    await expect.poll(async () => (await shown(page)).from).toBeGreaterThan(start.from);
    const later = await shown(page);
    expect(Math.abs(later.span - start.span)).toBeLessThan(1000);
    await page.mouse.wheel(-600, 0);
    await expect.poll(async () => (await shown(page)).from).toBeLessThan(later.from);
    expect(Math.abs((await shown(page)).span - start.span)).toBeLessThan(1000);

    // Shift+wheel and Ctrl+wheel each zoom in one direction and out the other, the instant under the pointer staying put.
    for (const key of ['Shift', 'Control']) {
      await aimAtChart(page, 0.3);
      await page.keyboard.down(key);
      for (const delta of [-300, 300, 300, -300]) {
        const before = await shown(page);
        const anchor = await instantAt(page, pointer.x, pointer.left, pointer.width);
        await page.mouse.wheel(0, delta);
        await expect.poll(async () => (await shown(page)).span).not.toBe(before.span);
        const after = await shown(page);
        expect(Math.sign(after.span - before.span)).toBe(Math.sign(delta)); // a positive delta zooms out, a negative one in
        const kept = await instantAt(page, pointer.x, pointer.left, pointer.width);
        expect(Math.abs(kept.instant - anchor.instant)).toBeLessThanOrEqual(0.02 * kept.span);
      }
      await page.keyboard.up(key);
    }
    await s.api.dispose();
  });

  test('zooming by the wheel stops at an hour in and 92 days out', async ({ page, playwright, baseURL }) => {
    const s = await seed(playwright, baseURL, 'wheelbounds-desktop');
    await open(page, s);
    await aimAtChart(page);
    await page.keyboard.down('Shift');
    for (let i = 0; i < 30; i++) await page.mouse.wheel(0, -400);
    await expect.poll(() => span(page)).toBeLessThanOrEqual(HOUR * 1.02);
    expect(await span(page)).toBeGreaterThanOrEqual(HOUR);
    await expect(page.getByTestId('zoom-in')).toBeDisabled();
    for (let i = 0; i < 40; i++) await page.mouse.wheel(0, 400);
    await expect.poll(() => span(page)).toBeGreaterThanOrEqual(92 * DAY * 0.98);
    expect(await span(page)).toBeLessThanOrEqual(92 * DAY);
    await expect(page.getByTestId('zoom-out')).toBeDisabled();
    await page.keyboard.up('Shift');
    await s.api.dispose();
  });
});

// A touch is a CDP Input.dispatchTouchEvent: chromium turns it into pointer and touch events like a finger would.
test.describe('phone touch navigation', () => {
  test.use({ viewport: viewports[0].size, hasTouch: true });

  type Finger = { x: number; y: number };
  async function gesture(page: Page, steps: Finger[][]) {
    const cdp = await page.context().newCDPSession(page);
    const points = (fingers: Finger[]) => fingers.map((f, id) => ({ x: f.x, y: f.y, id }));
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: points(steps[0]!) });
    for (const step of steps.slice(1)) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: points(step) });
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await cdp.detach();
  }
  /** Fingers `from` px apart moving to `to` px apart, level, about the point (x, y), in ten steps (both ends inside the chart, which a phone draws about 235 px wide). */
  const pinch = (x: number, y: number, from: number, to: number): Finger[][] =>
    Array.from({ length: 11 }, (_, i) => {
      const half = (from + ((to - from) * i) / 10) / 2;
      return [{ x: x - half, y }, { x: x + half, y }];
    });

  test('a pinch out zooms in, a pinch in zooms out, and a one-finger horizontal drag moves the window without zooming', async ({ page, playwright, baseURL }) => {
    const s = await seed(playwright, baseURL, 'touch-phone');
    await open(page, s);
    const at = await aimAtChart(page);
    const start = await shown(page);

    await gesture(page, pinch(at.x, at.y, 40, 160));
    await expect.poll(() => span(page)).toBeLessThan(start.span);
    const closer = await shown(page);

    await gesture(page, pinch(at.x, at.y, 160, 40));
    await expect.poll(() => span(page)).toBeGreaterThan(closer.span);

    const settled = await shown(page);
    await gesture(page, Array.from({ length: 11 }, (_, i) => [{ x: at.x - 100 + i * 20, y: at.y }]));
    await expect.poll(async () => (await shown(page)).from).toBeLessThan(settled.from); // dragged right: earlier times come into view
    const dragged = await shown(page);
    expect(Math.abs(dragged.span - settled.span)).toBeLessThanOrEqual(0.005 * settled.span);
    await s.api.dispose();
  });
});
