import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { expectInitiatives, expectTags, pickInitiative, pickTag } from './chips';

// The Timeline view's filters, panning and live refresh (PRD R6, SPEC 11): the four filters (initiative, tag, actor,
// closed within) change which task rows are drawn, live in the fragment and survive a reload; panning refetches
// GET /timeline for the new range, once the pan has settled; and a lease, a milestone or a task made through the API
// shows without a reload. Each test seeds initiatives, tags and actors of its own on the shared board.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const actorApi = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, handle: string) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': handle } });

const labels = (page: Page) => page.locator('.vis-labelset .vis-label');
const row = (page: Page, ref: string) => labels(page).filter({ hasText: `${ref} ` });
const timelineReads = (page: Page) => {
  const urls: URL[] = [];
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname === '/api/v1/timeline') urls.push(url);
  });
  return urls;
};
const select = (page: Page, testid: string) => page.getByTestId(testid);

interface Seed {
  api: APIRequestContext;
  x: string;
  y: string;
  tagX: string;
  tagY: string;
  ada: string;
  bob: string;
  /** X's tasks: held by ada, held by bob, closed. Y's task is held by ada. */
  x1: string;
  x2: string;
  x3: string;
  y1: string;
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const ada = `tlf-ada-${name}`;
  const bob = `tlf-bob-${name}`;
  const api = await actorApi(playwright, baseURL, ada);
  const bobApi = await actorApi(playwright, baseURL, bob);
  const x = `e2e-tlf-x-${name}`;
  const y = `e2e-tlf-y-${name}`;
  const tagX = `tlf-x-${name}`;
  const tagY = `tlf-y-${name}`;
  await post(api, '/api/v1/initiatives', { slug: x, title: `Filters X ${name}`, tags: [tagX] }, 201);
  await post(api, '/api/v1/initiatives', { slug: y, title: `Filters Y ${name}`, tags: [tagY] }, 201);
  const task = async (initiative: string, title: string) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative, title }, 201)).ref;
  const x1 = await task(x, 'Held by ada');
  const x2 = await task(x, 'Held by bob');
  const x3 = await task(x, 'Closed');
  const y1 = await task(y, 'Other initiative');
  await post(api, '/api/v1/leases', { task: x1, intent: 'ada works', resources: [`repo:${ada}-1`] }, 201);
  await post(bobApi, '/api/v1/leases', { task: x2, intent: 'bob works', resources: [`repo:${bob}`] }, 201);
  await post(api, '/api/v1/leases', { task: y1, intent: 'ada elsewhere', resources: [`repo:${ada}-2`] }, 201);
  await post(api, `/api/v1/tasks/${x3}/close`, { resolution: 'dropped' }, 200);
  await bobApi.dispose();
  return { api, x, y, tagX, tagY, ada, bob, x1, x2, x3, y1 };
}

const openUnfiltered = async (page: Page) => {
  await page.goto('/#/timeline');
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
};

const hash = (page: Page) => new URL(page.url()).hash;

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the initiative, tag, actor and closed-within filters change the tasks drawn, live in the fragment and survive a reload', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `f-${vp.name}`);
      await openUnfiltered(page);
      await expect(row(page, s.x1)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.y1)).toHaveCount(1);
      await expect(row(page, s.x3)).toHaveCount(0); // closed tasks are left out until a closed-within is chosen

      // Initiative.
      await pickInitiative(page, s.x);
      await expect(row(page, s.y1)).toHaveCount(0, FIVE_SECONDS);
      await expect(row(page, s.x1)).toHaveCount(1);
      await expect(row(page, s.x2)).toHaveCount(1);
      expect(hash(page)).toBe(`#/timeline?initiative=${s.x}`);
      await page.reload();
      await expectInitiatives(page, [s.x]);
      await expect(row(page, s.x2)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.y1)).toHaveCount(0);

      // Actor, within that initiative: only the task ada held a lease on.
      await select(page, 'actor-filter').selectOption(s.ada);
      await expect(row(page, s.x2)).toHaveCount(0, FIVE_SECONDS);
      await expect(row(page, s.x1)).toHaveCount(1);
      expect(hash(page)).toBe(`#/timeline?initiative=${s.x}&actor=${s.ada}`);
      await page.reload();
      await expect(select(page, 'actor-filter')).toHaveValue(s.ada);
      await expect(row(page, s.x1)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.x2)).toHaveCount(0);
      await select(page, 'actor-filter').selectOption(s.bob);
      await expect(row(page, s.x2)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.x1)).toHaveCount(0);

      // Closed within: the closed task joins the rows.
      await select(page, 'actor-filter').selectOption('');
      await expect(row(page, s.x1)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.x3)).toHaveCount(0);
      await select(page, 'closed-filter').selectOption('7');
      await expect(row(page, s.x3)).toHaveCount(1, FIVE_SECONDS);
      expect(hash(page)).toBe(`#/timeline?initiative=${s.x}&closed=7`);
      await page.reload();
      await expect(select(page, 'closed-filter')).toHaveValue('7');
      await expect(row(page, s.x3)).toHaveCount(1, FIVE_SECONDS);

      // Clear filters empties the fragment and brings the other initiative back.
      await page.getByTestId('clear-filters').click();
      await expect(row(page, s.y1)).toHaveCount(1, FIVE_SECONDS);
      expect(hash(page)).toBe('#/timeline');
      await expect(page.getByTestId('clear-filters')).toHaveCount(0);

      // Tag: the initiatives that carry it.
      await pickTag(page, s.tagY);
      await expect(row(page, s.x1)).toHaveCount(0, FIVE_SECONDS);
      await expect(row(page, s.y1)).toHaveCount(1);
      expect(hash(page)).toBe(`#/timeline?tag=${s.tagY}`);
      await page.reload();
      await expectTags(page, [s.tagY]);
      await expect(row(page, s.y1)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.x1)).toHaveCount(0);
      await s.api.dispose();
    });

    test('a link with a filter opens already filtered, in one read', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `l-${vp.name}`);
      const reads = timelineReads(page);
      await page.goto(`/#/timeline?initiative=${s.y}&actor=${s.ada}&closed=30`);
      await expect(row(page, s.y1)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.x1)).toHaveCount(0);
      await expectInitiatives(page, [s.y]);
      await expect(select(page, 'actor-filter')).toHaveValue(s.ada);
      await expect(select(page, 'closed-filter')).toHaveValue('30');
      expect(reads.length).toBeGreaterThanOrEqual(1);
      for (const url of reads) {
        expect(url.searchParams.getAll('initiative')).toEqual([s.y]);
        expect(url.searchParams.get('actor')).toBe(s.ada);
        expect(url.searchParams.get('closed_within')).toBe('30d');
      }
      await s.api.dispose();
    });

    test('panning the chart refetches the timeline for the new range, once it has settled', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `p-${vp.name}`);
      const reads = timelineReads(page);
      await page.goto(`/#/timeline?initiative=${s.x}`);
      await expect(row(page, s.x1)).toHaveCount(1, FIVE_SECONDS);
      await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
      // Let the opening settle (vis-timeline reports its first range a moment after it draws).
      await expect.poll(() => reads.length, FIVE_SECONDS).toBeGreaterThanOrEqual(1);
      await page.waitForTimeout(700);
      const opened = reads.length;
      const first = reads[0]!;
      expect(opened).toBe(1); // opening the view and settling its range is one read, not two

      await page.getByTestId('timeline-chart').scrollIntoViewIfNeeded(); // the filters push a phone's chart toward the fold
      const chart = (await page.getByTestId('timeline-chart').boundingBox())!;
      const y = Math.min(chart.y + chart.height - 20, vp.size.height - 100);
      const x0 = chart.x + chart.width * 0.7;
      await page.mouse.move(x0, y);
      await page.mouse.down();
      await page.mouse.move(x0 - chart.width * 0.5, y, { steps: 12 }); // a long drag: many range events, one read after it
      await page.mouse.up();
      await expect.poll(() => reads.length, FIVE_SECONDS).toBe(opened + 1);
      await page.waitForTimeout(700);
      expect(reads.length).toBe(opened + 1);

      const panned = reads[reads.length - 1]!;
      expect(new Date(panned.searchParams.get('from')!).getTime()).toBeGreaterThan(new Date(first.searchParams.get('from')!).getTime());
      expect(new Date(panned.searchParams.get('to')!).getTime()).toBeGreaterThan(new Date(first.searchParams.get('to')!).getTime());
      expect(panned.searchParams.getAll('initiative')).toEqual([s.x]); // the filter rides along
      await expect(row(page, s.x1)).toHaveCount(1); // the rows are still there, refreshed in place

      // A zoom button changes the range too, and reads for it.
      await page.getByTestId('zoom-in').click();
      await expect.poll(() => reads.length, FIVE_SECONDS).toBe(opened + 2);
      const zoomed = reads[reads.length - 1]!;
      const length = (u: URL) => new Date(u.searchParams.get('to')!).getTime() - new Date(u.searchParams.get('from')!).getTime();
      expect(length(zoomed)).toBeLessThan(length(panned));
      await s.api.dispose();
    });

    test('a refresh keeps an initiative collapsed', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `c-${vp.name}`);
      await page.goto(`/#/timeline?initiative=${s.x}`);
      await expect(labels(page)).toHaveCount(3, FIVE_SECONDS); // the initiative and its tasks X1 and X2 (X3 is closed)
      await labels(page).first().click();
      await expect(labels(page)).toHaveCount(1, FIVE_SECONDS);
      await post(s.api, `/api/v1/tasks/${s.x1}/milestones`, { summary: 'while folded' }, 200);
      await expect(page.locator('.vis-item.vis-point.tl-marker-milestone')).toHaveCount(0);
      const reads = timelineReads(page);
      await post(s.api, `/api/v1/tasks/${s.x2}/milestones`, { summary: 'again' }, 200);
      await expect.poll(() => reads.length, FIVE_SECONDS).toBeGreaterThanOrEqual(1);
      await expect(labels(page)).toHaveCount(1);
      await labels(page).first().click();
      await expect(labels(page)).toHaveCount(3, FIVE_SECONDS);
      await expect(page.locator('.vis-item.vis-point.tl-marker-milestone')).toHaveCount(2, FIVE_SECONDS);
      await s.api.dispose();
    });

    test('a lease started through the API, a milestone and a new task appear within 5 s, without a reload', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `v-${vp.name}`);
      await page.goto(`/#/timeline?initiative=${s.x}`);
      await expect(row(page, s.x1)).toHaveCount(1, FIVE_SECONDS);
      await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
      const leases = page.locator('.vis-item.tl-lease');
      await expect(leases).toHaveCount(2); // ada's on X1 and bob's on X2

      const other = await actorApi(playwright, baseURL, `tlf-cy-${vp.name}`);
      const task = (await post<{ ref: string }>(other, '/api/v1/tasks', { initiative: s.x, title: 'Made while watching' }, 201)).ref;
      await expect(row(page, task)).toHaveCount(1, FIVE_SECONDS);
      await post(other, '/api/v1/leases', { task, intent: 'started while watching', resources: [`repo:tlf-cy-${vp.name}`] }, 201);
      await expect(leases).toHaveCount(3, FIVE_SECONDS);
      await expect(page.locator('.vis-item.tl-lease-live')).toHaveCount(3);

      await post(other, `/api/v1/tasks/${task}/milestones`, { summary: 'Halfway there' }, 200);
      await expect(page.locator('.vis-item.vis-point.tl-marker-milestone')).toHaveCount(1, FIVE_SECONDS);

      await post(other, `/api/v1/tasks/${task}/close`, { resolution: 'completed' }, 200);
      await expect(row(page, task)).toHaveCount(0, FIVE_SECONDS); // closed: out of the unfiltered-by-closed view
      await other.dispose();
      await s.api.dispose();
    });
  });
}
