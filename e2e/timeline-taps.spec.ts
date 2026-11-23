import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// A tap on the Timeline acts only on what it went down on (PRD R6, SPEC 11): the target is decided at pointerdown and a
// task opens only when both the pointer's release and its click land on that same element. Cases 1 to 5, with a finger
// (CDP Input.dispatchTouchEvent) on the phone and a mouse on the desktop: a bar, a label or the empty part of a row
// opens its task; a press on one task released over another's row opens nothing; a drag past the slop that ends on a bar
// pans; a tap on an initiative's label only collapses or expands it, even where the collapse moves a task's row under
// the pointer; a tap beside a bar opens the nearest bar of its row within 16 px. (Case 6, a pointer lost without its end, is in timelineView.test.ts: a browser
// cannot be made to lose a pointer.) Each test seeds initiatives of its own and filters the view to them.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 }, touch: true },
  { name: 'desktop', size: { width: 1440, height: 900 }, touch: false },
] as const;
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

function dateTask(slug: string, ref: string, createdAt: number) {
  const seq = Number(ref.slice(slug.length + 1));
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db.prepare('UPDATE tasks SET created_at = ? WHERE seq = ? AND initiative_id = (SELECT id FROM initiatives WHERE slug = ?)').run(createdAt, seq, slug);
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
}

interface Seed {
  api: APIRequestContext;
  /** The first initiative: three open tasks, the first holding a live lease. */
  slug: string;
  one: string;
  two: string;
  three: string;
  /** A second initiative with `many` tasks, for the collapse that moves rows. */
  other: string;
  otherTitle: string;
  many: string[];
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string, withOther = false): Promise<Seed> {
  const slug = `e2e-tlt-${name}`;
  const other = `e2e-tlt-${name}-z`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tlt-${name}` },
  });
  const now = Date.now();
  await post(api, '/api/v1/initiatives', { slug, title: `Taps ${name}` }, 201);
  const task = async (initiative: string, title: string) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative, title }, 201)).ref;
  const one = await task(slug, 'Tap one');
  const two = await task(slug, 'Tap two');
  const three = await task(slug, 'Tap three');
  await post(api, '/api/v1/leases', { task: one, intent: 'holding one', resources: [`repo:tlt-${name}`] }, 201);
  [one, two, three].forEach((ref, i) => dateTask(slug, ref, now - (6 - 2 * i) * DAY));
  const many: string[] = [];
  const otherTitle = `Taps ${name} other`;
  if (withOther) {
    await post(api, '/api/v1/initiatives', { slug: other, title: otherTitle }, 201);
    for (let i = 0; i < 9; i++) many.push(await task(other, `Other ${i}`));
    many.forEach((ref, i) => dateTask(other, ref, now - (3 + (i % 3)) * DAY));
  }
  return { api, slug, one, two, three, other, otherTitle, many };
}

const open = async (page: Page, query: string, taskRows: number) => {
  await page.goto(`/#/timeline?${query}`);
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-labelset .vis-label.tl-task')).toHaveCount(taskRows, FIVE_SECONDS);
  await expect(page.locator('.vis-item.tl-actual').first()).toBeVisible(FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
};

const label = (page: Page, text: string) => page.locator('.vis-labelset .vis-label', { hasText: text });
const bar = (page: Page, ref: string) => page.locator(`.vis-item.tl-id-act-${ref}`);
const dialogOf = (page: Page, ref: string) => page.getByRole('dialog', { name: `Task ${ref}` });

async function boxOf(locator: Locator) {
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  expect(box, 'a visible box').not.toBeNull();
  return box!;
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size, hasTouch: vp.touch });

    type Point = { x: number; y: number };
    /** One finger (phone) or the mouse (desktop) goes down at the first point, moves through the rest and comes up at the last. */
    async function stroke(page: Page, points: Point[]) {
      if (!vp.touch) {
        await page.mouse.move(points[0]!.x, points[0]!.y);
        await page.mouse.down();
        for (const p of points.slice(1)) await page.mouse.move(p.x, p.y);
        await page.mouse.up();
        return;
      }
      const cdp = await page.context().newCDPSession(page);
      const at = (p: Point) => [{ x: p.x, y: p.y, id: 0 }];
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: at(points[0]!) });
      for (const p of points.slice(1)) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: at(p) });
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
      await cdp.detach();
    }
    const tapAt = (page: Page, p: Point) => stroke(page, [p]);
    const centre = (b: { x: number; y: number; width: number; height: number }): Point => ({ x: b.x + b.width / 2, y: b.y + b.height / 2 });
    const noDialog = async (page: Page) => {
      await page.waitForTimeout(400); // a click that was going to open one has landed by now
      await expect(page.getByRole('dialog')).toHaveCount(0);
      expect(page.url()).not.toContain('task=');
    };
    const closePreview = async (page: Page, ref: string) => {
      await page.keyboard.press('Escape');
      await expect(dialogOf(page, ref)).toHaveCount(0);
    };

    test('case 1: a tap on a task\'s bar, its label or its row opens that task', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `c1-${vp.name}`);
      await open(page, `initiative=${s.slug}`, 3);

      await tapAt(page, centre(await boxOf(bar(page, s.two))));
      await expect(dialogOf(page, s.two)).toBeVisible(FIVE_SECONDS);
      await closePreview(page, s.two);

      await tapAt(page, centre(await boxOf(label(page, `${s.three} Tap three`))));
      await expect(dialogOf(page, s.three)).toBeVisible(FIVE_SECONDS);
      await closePreview(page, s.three);

      // The row: just under the bar, where the row is empty, opens the task of the bar above it.
      const under = await boxOf(bar(page, s.one));
      await tapAt(page, { x: under.x + under.width / 2, y: under.y + under.height + 4 });
      await expect(dialogOf(page, s.one)).toBeVisible(FIVE_SECONDS);
      await s.api.dispose();
    });

    test('case 2: a press on one task\'s bar released over another task\'s row opens nothing', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `c2-${vp.name}`);
      await open(page, `initiative=${s.slug}`, 3);
      // The lease bar sits at the bottom of the first task's row; the second task's row starts 4 px below the bar's
      // bottom edge, so a release 8 px down is inside the 10 px slop and over the second row.
      // A mouse's click lands on the common ancestor of the two rows; a finger's is aimed by the browser at where it went
      // down (the lease bar). Either way the pointer came up over the second row, so nothing opens.
      const lease = await boxOf(page.locator('.vis-item.tl-lease-live'));
      const next = await boxOf(label(page, `${s.two} Tap two`));
      const x = lease.x + lease.width / 2;
      const from = { x, y: lease.y + lease.height - 2 };
      const to = { x, y: next.y + 4 };
      expect(Math.hypot(to.x - from.x, to.y - from.y)).toBeLessThanOrEqual(10);
      await stroke(page, [from, to]);
      await noDialog(page);

      // The same press lifted where it went down is a tap on the first task, so the stroke above was a tap in reach.
      await stroke(page, [from, { x, y: from.y - 1 }]);
      await expect(dialogOf(page, s.one)).toBeVisible(FIVE_SECONDS);
      await s.api.dispose();
    });

    test('case 3: a drag past the slop that ends on a bar pans the chart and opens nothing', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `c3-${vp.name}`);
      await open(page, `initiative=${s.slug}`, 3);
      const before = Number(await page.getByTestId('timeline-span').getAttribute('data-from-ms'));
      const target = await boxOf(bar(page, s.two));
      const y = target.y + target.height / 2;
      // From the middle of a bar, 40 px sideways and back onto it: the pointer ends on the bar it started on, past the slop.
      const x = target.x + target.width / 2;
      await stroke(page, [{ x, y }, { x: x - 20, y }, { x: x - 40, y }, { x: x - 20, y }, { x: x + 20, y }, { x: x + 40, y }]);
      await expect.poll(async () => Number(await page.getByTestId('timeline-span').getAttribute('data-from-ms')), FIVE_SECONDS).not.toBe(before);
      await noDialog(page);
      await s.api.dispose();
    });

    test('case 4: a tap on an initiative label only collapses or expands it, never opens a task, even where the collapse moves a task row under the pointer', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `c4-${vp.name}`, true);
      await open(page, `initiative=${s.slug}&initiative=${s.other}`, 12);
      const taskLabels = page.locator('.vis-labelset .vis-label.tl-task');
      await expect(taskLabels).toHaveCount(12);

      const first = label(page, `Taps c4-${vp.name}`).first();
      await tapAt(page, centre(await boxOf(first)));
      await expect(taskLabels).toHaveCount(9, FIVE_SECONDS); // the first initiative's three rows are gone
      await noDialog(page);
      await tapAt(page, centre(await boxOf(label(page, `Taps c4-${vp.name}`).first())));
      await expect(taskLabels).toHaveCount(12, FIVE_SECONDS);
      await noDialog(page);

      // Scrolled to the end of the rows, collapsing the last initiative shortens them and the rows slide down under the pointer.
      const second = label(page, s.otherTitle);
      await page.locator('.vis-panel.vis-left').evaluate((el) => {
        el.scrollTop = el.scrollHeight;
      });
      const at = centre(await boxOf(second));
      await tapAt(page, at);
      await expect(taskLabels).toHaveCount(3, FIVE_SECONDS);
      await noDialog(page);
      await s.api.dispose();
    });

    test('case 5: a tap on the empty part of a task row opens the nearest bar of that row within 16 px, and nothing farther', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `c5-${vp.name}`);
      await open(page, `initiative=${s.slug}`, 3);
      // An open task's span ends at now: right of it the row is empty.
      const target = await boxOf(bar(page, s.three));
      const y = target.y + target.height / 2;
      const edge = target.x + target.width;
      await tapAt(page, { x: edge + 12, y });
      await expect(dialogOf(page, s.three)).toBeVisible(FIVE_SECONDS);
      await closePreview(page, s.three);

      await tapAt(page, { x: edge + 40, y });
      await noDialog(page);
      await s.api.dispose();
    });
  });
}
