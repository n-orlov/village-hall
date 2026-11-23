import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// R13 initiative page layout (SPEC 11, initiative page, wide): from 1536 px the header, Needs attention and Tasks are the main
// column, and Open asks and Notes are a 420 px side column on the right, sticky at the top of the viewport and scrolling on
// its own; the pair is at most 2560 px wide and centred. Everything is measured by bounding box, scroll offsets and computed
// style, never by pixels.
const FIVE_SECONDS = { timeout: 5000 };
const PAIR_CAP = 2560;
const STICKY_TOP = 24;

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

// An initiative with `tasks` tasks (a main column taller than the viewport), `asks` open asks with long questions and `notes`
// notes (a side column taller than the viewport when there are enough). Returns the refs of the asks, withdrawn by release.
async function seed(api: APIRequestContext, slug: string, tasks: number, asks: number, notes: number): Promise<string[]> {
  await post(api, '/api/v1/initiatives', { slug, title: `Initiative wide ${slug}` });
  for (let n = 0; n < tasks; n++) await post(api, '/api/v1/tasks', { initiative: slug, title: `Wide task ${n}` });
  const refs: string[] = [];
  for (let n = 0; n < asks; n++) {
    const question = `Seeded question ${n} for the wide initiative page. ${'Which of the two ways should this take? '.repeat(3)}`;
    refs.push((await post<{ ref: string }>(api, '/api/v1/asks', { initiative: slug, question })).ref);
  }
  for (let n = 0; n < notes; n++) await post(api, '/api/v1/notes', { initiative: slug, summary: `Seeded note ${n}` });
  return refs;
}

async function release(api: APIRequestContext, asks: string[]) {
  for (const ask of asks) await post(api, `/api/v1/asks/${ask}/withdraw`, { reason: 'wide layout spec done' }, 200);
}

async function openInitiative(page: Page, slug: string) {
  await page.goto(`/#/i/${slug}`);
  await expect(page.getByTestId('task-row').first()).toBeVisible(FIVE_SECONDS);
  await expect(page.getByTestId('open-asks-section')).toBeVisible(FIVE_SECONDS);
  await expect(page.getByTestId('notes-section')).toBeVisible(FIVE_SECONDS);
}

async function box(page: Page, testid: string) {
  return (await page.getByTestId(testid).boundingBox())!;
}

test.describe('wide 2560x1080', () => {
  test.use({ viewport: { width: 2560, height: 1080 } });

  test('Open asks and Notes are a 420 px side column right of Tasks, and the main column holds the rest', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const asks = await seed(api, 'e2e-iw-side', 3, 2, 2);
    try {
      await openInitiative(page, 'e2e-iw-side');
      const tasks = await box(page, 'tasks-heading');
      const table = (await page.locator('[data-testid=initiative-main] table').boundingBox())!;
      const open = await box(page, 'open-asks-section');
      expect(open.x).toBeGreaterThanOrEqual(tasks.x + tasks.width);
      expect(open.x).toBeGreaterThanOrEqual(table.x + table.width);
      const side = await box(page, 'initiative-side');
      expect(side.width).toBeGreaterThanOrEqual(418);
      expect(side.width).toBeLessThanOrEqual(420);
      expect((await box(page, 'notes-section')).x).toBeGreaterThanOrEqual(side.x);
      // The header and Tasks lie in the main column, left of the side column.
      for (const testid of ['initiative-title', 'initiative-counts', 'tasks-heading']) {
        const at = await box(page, testid);
        expect(at.x + at.width, testid).toBeLessThanOrEqual(side.x);
      }
      expect(await page.locator('[data-testid=initiative-side] [data-testid=open-asks-section]').count()).toBe(1);
      expect(await page.locator('[data-testid=initiative-side] [data-testid=notes-section]').count()).toBe(1);
      expect(await page.locator('[data-testid=initiative-side] [data-testid=tasks-heading]').count()).toBe(0);
    } finally {
      await release(api, asks);
    }
  });

  test('a side column taller than the viewport scrolls on its own and holds its place when the page scrolls', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const asks = await seed(api, 'e2e-iw-tall', 60, 24, 3);
    try {
      await openInitiative(page, 'e2e-iw-tall');
      await expect(page.getByTestId('initiative-asks').getByTestId('ask')).toHaveCount(24, FIVE_SECONDS);
      const side = page.getByTestId('initiative-side');
      const viewport = page.viewportSize()!;
      const measured = await side.evaluate((el) => ({ scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, scrollTop: el.scrollTop }));
      expect(measured.scrollHeight).toBeGreaterThan(viewport.height);
      expect(measured.clientHeight).toBeLessThanOrEqual(viewport.height);
      expect(measured.scrollTop).toBe(0);
      expect(await page.evaluate(() => document.documentElement.scrollHeight)).toBeGreaterThan(viewport.height + 1000);

      // Scrolling the side column moves it, and the page keeps its place.
      const scrollYBefore = await page.evaluate(() => window.scrollY);
      await side.hover();
      await page.mouse.wheel(0, 400);
      await expect.poll(() => side.evaluate((el) => el.scrollTop), FIVE_SECONDS).toBeGreaterThan(0);
      expect(await page.evaluate(() => window.scrollY)).toBe(scrollYBefore);

      // Scrolling the page by 1000 px leaves the side column's top at the top of the viewport.
      await page.evaluate(() => window.scrollTo(0, 1000));
      expect(await page.evaluate(() => window.scrollY)).toBe(1000);
      const at = (await side.boundingBox())!;
      expect(at.y).toBeGreaterThanOrEqual(0);
      expect(at.y).toBeLessThanOrEqual(STICKY_TOP + 1);
      expect(at.height).toBeLessThanOrEqual(viewport.height);
      expect(await side.evaluate((el) => getComputedStyle(el).position)).toBe('sticky');
    } finally {
      await release(api, asks);
    }
  });
});

test.describe('ultrawide 3440x1440', () => {
  test.use({ viewport: { width: 3440, height: 1440 } });

  test('the pair is at most 2560 px wide with equal margins in the main column', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const asks = await seed(api, 'e2e-iw-ultra', 2, 1, 1);
    try {
      await openInitiative(page, 'e2e-iw-ultra');
      const column = await page.locator('#main').evaluate((el) => {
        const rect = el.getBoundingClientRect();
        const style = getComputedStyle(el);
        return { left: rect.left + parseFloat(style.paddingLeft), right: rect.right - parseFloat(style.paddingRight) };
      });
      expect(column.right - column.left).toBeGreaterThan(PAIR_CAP);
      const section = await box(page, 'initiative-page');
      const main = await box(page, 'initiative-main');
      const side = await box(page, 'initiative-side');
      expect(Math.max(main.x + main.width, side.x + side.width) - Math.min(main.x, side.x)).toBeLessThanOrEqual(PAIR_CAP);
      expect(section.width).toBeLessThanOrEqual(PAIR_CAP);
      expect(side.x).toBeGreaterThanOrEqual(main.x + main.width);
      const left = section.x - column.left;
      const right = column.right - (section.x + section.width);
      expect(left).toBeGreaterThan(0);
      expect(Math.abs(left - right)).toBeLessThanOrEqual(2);
    } finally {
      await release(api, asks);
    }
  });
});

test.describe('desktop 1440x900', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('below 1536 px Open asks is not right of Tasks but under it', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const asks = await seed(api, 'e2e-iw-desktop', 3, 1, 1);
    try {
      await openInitiative(page, 'e2e-iw-desktop');
      const tasks = await box(page, 'tasks-heading');
      const table = (await page.locator('[data-testid=initiative-main] table').boundingBox())!;
      const open = await box(page, 'open-asks-section');
      expect(open.x).toBeLessThan(tasks.x + tasks.width);
      expect(open.y).toBeGreaterThanOrEqual(table.y + table.height);
      expect(await page.getByTestId('initiative-side').evaluate((el) => getComputedStyle(el).position)).toBe('static');
    } finally {
      await release(api, asks);
    }
  });
});
