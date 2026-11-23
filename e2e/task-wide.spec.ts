import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// R13 task page layout (SPEC 11, task page, wide): from 1536 px the header, description, Links, Blocked by, Blocking, Notes
// and Timeline are the main column, and the Stranded panel, Live leases, Conflicts, Expired leases and Asks are a 420 px side
// column on the right, sticky at the top of the viewport and scrolling on its own; the pair is at most 2560 px wide and
// centred. Everything is measured by bounding box, scroll offsets and computed style, never by pixels.
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

interface Seeded {
  ref: string;
  lease: string;
  asks: string[];
}

// A task with a live lease, `asks` open asks with long questions (a side column taller than the viewport) and `events`
// milestones (a Timeline taller than the viewport, so the page scrolls).
async function seed(api: APIRequestContext, slug: string, asks: number, events: number): Promise<Seeded> {
  await post(api, '/api/v1/initiatives', { slug, title: `Task wide ${slug}` });
  const ref = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: `Wide task ${slug}`, description: 'Seeded description for the wide task page.' })).ref;
  const lease = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: ref, intent: 'Seeded lease for the wide task page' })).lease.ref;
  const refs: string[] = [];
  for (let n = 0; n < asks; n++) {
    const question = `Seeded question ${n} for the wide task page. ${'Which of the two ways should this task take? '.repeat(3)}`;
    refs.push((await post<{ ref: string }>(api, '/api/v1/asks', { task: ref, question, mode: 'poll' })).ref);
  }
  for (let n = 0; n < events; n++) await post(api, `/api/v1/tasks/${ref}/milestones`, { summary: `Seeded milestone ${n}` }, 200);
  return { ref, lease, asks: refs };
}

async function release(api: APIRequestContext, seeded: Seeded) {
  await post(api, `/api/v1/leases/${seeded.lease}/end`, {}, 200);
  for (const ask of seeded.asks) await post(api, `/api/v1/asks/${ask}/withdraw`, { reason: 'wide layout spec done' }, 200);
}

async function openTask(page: Page, ref: string) {
  await page.goto(`/#/t/${ref}`);
  await expect(page.getByTestId('task-title')).toBeVisible(FIVE_SECONDS);
  await expect(page.getByTestId('live-leases-section')).toBeVisible(FIVE_SECONDS);
  await expect(page.getByTestId('task-leases')).toBeVisible(FIVE_SECONDS);
  await expect(page.getByTestId('timeline-section')).toBeVisible(FIVE_SECONDS);
}

async function box(page: Page, testid: string) {
  return (await page.getByTestId(testid).boundingBox())!;
}

test.describe('wide 2560x1080', () => {
  test.use({ viewport: { width: 2560, height: 1080 } });

  test('the side column holds leases and asks right of the main column, 420 px wide', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const seeded = await seed(api, 'e2e-pw-side', 2, 3);
    try {
      await openTask(page, seeded.ref);
      const timeline = await box(page, 'timeline-section');
      const leases = await box(page, 'live-leases-section');
      expect(leases.x).toBeGreaterThanOrEqual(timeline.x + timeline.width);
      const side = await box(page, 'task-side');
      expect(side.width).toBeGreaterThanOrEqual(418);
      expect(side.width).toBeLessThanOrEqual(420);
      // Everything the criterion lists lies in one column or the other, by bounding box.
      for (const testid of ['task-title', 'task-status', 'task-description', 'no-links', 'no-deps', 'no-blocking', 'no-notes', 'timeline-section']) {
        const at = await box(page, testid);
        expect(at.x + at.width, testid).toBeLessThanOrEqual(side.x);
      }
      for (const testid of ['live-leases-section', 'asks-section']) {
        expect(await page.locator(`[data-testid=task-side] [data-testid=${testid}]`).count(), testid).toBe(1);
        expect((await box(page, testid)).x, testid).toBeGreaterThanOrEqual(side.x);
      }
      expect(await page.locator('[data-testid=task-side] [data-testid=timeline-section]').count()).toBe(0);
    } finally {
      await release(api, seeded);
    }
  });

  test('a side column taller than the viewport scrolls on its own and holds its place when the page scrolls', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const seeded = await seed(api, 'e2e-pw-tall', 24, 40);
    try {
      await openTask(page, seeded.ref);
      await expect(page.getByTestId('task-asks')).toBeVisible(FIVE_SECONDS);
      const side = page.getByTestId('task-side');
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
      const top = (await side.boundingBox())!.y;
      expect(top).toBeGreaterThanOrEqual(0);
      expect(top).toBeLessThanOrEqual(STICKY_TOP + 1);
      expect((await side.boundingBox())!.height).toBeLessThanOrEqual(viewport.height);
      expect(await side.evaluate((el) => getComputedStyle(el).position)).toBe('sticky');
    } finally {
      await release(api, seeded);
    }
  });
});

test.describe('ultrawide 3440x1440', () => {
  test.use({ viewport: { width: 3440, height: 1440 } });

  test('the pair is at most 2560 px wide and centred in the main column', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const seeded = await seed(api, 'e2e-pw-ultra', 1, 2);
    try {
      await openTask(page, seeded.ref);
      const column = await page.locator('#main').evaluate((el) => {
        const rect = el.getBoundingClientRect();
        const style = getComputedStyle(el);
        return { left: rect.left + parseFloat(style.paddingLeft), right: rect.right - parseFloat(style.paddingRight) };
      });
      expect(column.right - column.left).toBeGreaterThan(PAIR_CAP);
      const page_ = await box(page, 'task-page');
      const main = await box(page, 'task-main');
      const side = await box(page, 'task-side');
      expect(Math.max(main.x + main.width, side.x + side.width) - Math.min(main.x, side.x)).toBeLessThanOrEqual(PAIR_CAP);
      expect(page_.width).toBeLessThanOrEqual(PAIR_CAP);
      expect(side.x).toBeGreaterThanOrEqual(main.x + main.width);
      const left = page_.x - column.left;
      const right = column.right - (page_.x + page_.width);
      expect(left).toBeGreaterThan(0);
      expect(Math.abs(left - right)).toBeLessThanOrEqual(2);
    } finally {
      await release(api, seeded);
    }
  });
});

test.describe('desktop 1440x900', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('below 1536 px the Live leases section is not right of the Timeline section', async ({ page, playwright, baseURL }) => {
    const api = await apiContext(playwright, baseURL);
    const seeded = await seed(api, 'e2e-pw-desktop', 1, 2);
    try {
      await openTask(page, seeded.ref);
      const timeline = await box(page, 'timeline-section');
      const leases = await box(page, 'live-leases-section');
      expect(leases.x).toBeLessThan(timeline.x + timeline.width);
      expect(leases.y + leases.height).toBeLessThanOrEqual(timeline.y);
      expect(await page.getByTestId('task-side').evaluate((el) => getComputedStyle(el).position)).toBe('static');
    } finally {
      await release(api, seeded);
    }
  });
});
