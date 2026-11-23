import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// R13 Tasks layout (SPEC 11, Tasks view, wide): from 1536 px the Now panel (400 px, never folded) sits in a side column to
// the right of the task table, sticky at the top of the viewport and scrolling on its own when its content is taller than
// the viewport; the pair is at most 2560 px wide and centred. Below 1536 px the panel is above the table. Everything is
// measured by bounding box, scroll offsets and computed style, never by pixels.
const FIVE_SECONDS = { timeout: 5000 };
const PAIR_CAP = 2560;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status = 201): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

function apiContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
}

// An initiative with `tasks` tasks, so the table is taller than the viewport. The Tasks view is opened on that initiative.
async function seedTasks(api: APIRequestContext, slug: string, tasks: number): Promise<string[]> {
  await post(api, '/api/v1/initiatives', { slug, title: `Tasks wide ${slug}` });
  const refs: string[] = [];
  for (let n = 0; n < tasks; n++) {
    refs.push((await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: `Wide task ${n}` })).ref);
  }
  return refs;
}

async function openTasks(page: Page, slug: string) {
  await page.goto(`/#/tasks?initiative=${slug}`);
  await expect(page.getByTestId('task-row').first()).toBeVisible(FIVE_SECONDS);
  await expect(page.getByTestId('now-panel')).toBeVisible(FIVE_SECONDS);
}

async function box(page: Page, testid: string) {
  return (await page.getByTestId(testid).boundingBox())!;
}

test.describe('wide 2560x1080', () => {
  test.use({ viewport: { width: 2560, height: 1080 } });

  test('the Now panel is a side column right of the task table and stays in place when the page scrolls', async ({ page, playwright, baseURL }) => {
    const slug = 'e2e-tw-side';
    await seedTasks(await apiContext(playwright, baseURL), slug, 60); // Compact rows are short: 60 keep the page taller than the 1000 px scrolled below
    await openTasks(page, slug);

    const table = (await page.locator('[data-testid=tasks-view] table').boundingBox())!;
    const now = await box(page, 'now-panel');
    expect(now.x).toBeGreaterThanOrEqual(table.x + table.width);
    expect(now.width).toBeGreaterThanOrEqual(398);
    expect(now.width).toBeLessThanOrEqual(400);
    // The tops overlap: the panel starts before the table ends, and the table starts before the panel ends.
    expect(now.y).toBeLessThan(table.y + table.height);
    expect(table.y).toBeLessThan(now.y + now.height);
    // The body is open: the side column never folds.
    await expect(page.getByTestId('now-body')).toBeVisible();
    await expect(page.getByTestId('now-toggle')).toBeHidden();

    const before = await box(page, 'now-panel');
    await page.evaluate(() => window.scrollTo(0, 1000));
    expect(await page.evaluate(() => window.scrollY)).toBe(1000);
    const after = await box(page, 'now-panel');
    expect(after.y).toBeGreaterThanOrEqual(0);
    expect(Math.abs(after.y - before.y)).toBeLessThanOrEqual(1);
    expect(Math.abs(after.x - before.x)).toBeLessThanOrEqual(1);
    // The table moved up by the scroll while the panel did not.
    const moved = (await page.locator('[data-testid=tasks-view] table').boundingBox())!;
    expect(moved.y).toBeLessThan(table.y - 900);
  });

  test('a Now panel taller than the viewport scrolls on its own and the page does not move', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const slug = 'e2e-tw-tall';
    const refs = await seedTasks(api, slug, 30);
    const leases: string[] = [];
    for (const [n, ref] of refs.slice(0, 18).entries()) {
      const started = await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: ref, intent: `Seeded lease ${n} for the tall Now panel` });
      leases.push(started.lease.ref);
    }
    try {
      await openTasks(page, slug);
      await expect(page.getByTestId('now-lease').filter({ hasText: 'Seeded lease 17' })).toHaveCount(1, FIVE_SECONDS);

      const side = page.getByTestId('now-side');
      const viewport = page.viewportSize()!;
      const measured = await side.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, scrollTop: el.scrollTop }));
      // The content is taller than the viewport, and the column is held to the viewport.
      expect(measured.scrollHeight).toBeGreaterThan(viewport.height);
      expect(measured.clientHeight).toBeLessThanOrEqual(viewport.height);
      expect((await side.boundingBox())!.height).toBeLessThanOrEqual(viewport.height);
      expect(measured.scrollTop).toBe(0);

      const scrollYBefore = await page.evaluate(() => window.scrollY);
      await side.hover();
      await page.mouse.wheel(0, 400);
      await expect.poll(() => side.evaluate((el) => el.scrollTop), FIVE_SECONDS).toBeGreaterThan(0);
      expect(await page.evaluate(() => window.scrollY)).toBe(scrollYBefore);
    } finally {
      for (const lease of leases) await post(api, `/api/v1/leases/${lease}/end`, {}, 200);
    }
  });
});

test.describe('ultrawide 3440x1440', () => {
  test.use({ viewport: { width: 3440, height: 1440 } });

  test('the table and Now panel pair is at most 2560 px wide and centred in the main column', async ({ page, playwright, baseURL }) => {
    const slug = 'e2e-tw-ultra';
    await seedTasks(await apiContext(playwright, baseURL), slug, 3);
    await openTasks(page, slug);

    const column = await page.locator('#main').evaluate((el) => {
      const rect = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      return { left: rect.left + parseFloat(style.paddingLeft), right: rect.right - parseFloat(style.paddingRight) };
    });
    expect(column.right - column.left).toBeGreaterThan(PAIR_CAP);
    const view = await box(page, 'tasks-view');
    const now = await box(page, 'now-panel');
    const table = (await page.locator('[data-testid=tasks-view] table').boundingBox())!;
    // The pair spans from the table's left edge to the panel's right edge.
    const pairLeft = Math.min(table.x, now.x);
    const pairRight = Math.max(table.x + table.width, now.x + now.width);
    expect(pairRight - pairLeft).toBeLessThanOrEqual(PAIR_CAP);
    expect(view.width).toBeLessThanOrEqual(PAIR_CAP);
    expect(now.x).toBeGreaterThanOrEqual(table.x + table.width);
    const left = view.x - column.left;
    const right = column.right - (view.x + view.width);
    expect(left).toBeGreaterThan(0);
    expect(Math.abs(left - right)).toBeLessThanOrEqual(2);
  });
});

test.describe('desktop 1440x900', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('below 1536 px the Now panel is still above the table', async ({ page, playwright, baseURL }) => {
    const slug = 'e2e-tw-desktop';
    await seedTasks(await apiContext(playwright, baseURL), slug, 3);
    await openTasks(page, slug);

    const table = (await page.locator('[data-testid=tasks-view] table').boundingBox())!;
    const now = await box(page, 'now-panel');
    expect(now.y + now.height).toBeLessThanOrEqual(table.y);
    expect(await page.getByTestId('now-side').evaluate((el) => getComputedStyle(el).position)).toBe('static');
  });
});
