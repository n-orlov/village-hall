import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { expectInitiatives, expectTags, pickInitiative, pickTag } from './chips';

// The Timeline view's saved filters (PRD R16, SPEC 11): the filters are saved under `vh.view.v1.timeline`, a bare
// `#/timeline` restores them before the first fetch (one `GET /timeline`), a fragment with its own query wins, Clear
// filters forgets them, and a restored initiative or tag the board does not have is dropped before the first fetch.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.timeline';
const FIVE_SECONDS = { timeout: 5000 };

async function post(api: APIRequestContext, path: string, data: object, status: number) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
// Timeline sits under the phone tab bar's More entry; Initiatives keeps its own tab.
const goToTimeline = async (page: Page) => {
  const more = page.getByRole('button', { name: 'More' });
  if (await more.isVisible()) {
    await more.click();
    await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: 'Timeline' }).click();
  } else {
    await nav(page).getByRole('link', { name: 'Timeline' }).click();
  }
};
const goToInitiatives = async (page: Page) => {
  await nav(page).getByRole('link', { name: 'Initiatives' }).click();
  await expect(page.getByTestId('initiative-row').first()).toBeVisible();
};
const stored = (page: Page) => page.evaluate((k) => localStorage.getItem(k), KEY);
const saved = async (page: Page) => JSON.parse((await stored(page)) as string) as { v: number; query: string };

// The `GET /timeline` requests a page sends, as their query strings.
function timelineReads(page: Page) {
  const reads = { count: 0, urls: [] as URL[] };
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname !== '/api/v1/timeline') return;
    reads.count++;
    reads.urls.push(url);
  });
  return reads;
}

// Holds every GET of the initiatives list (the restored filter's check) until `release()`; `answered` settles once the
// first held lookup has reached the page.
async function holdLookups(page: Page) {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  let held = 0;
  await page.route(
    (url) => url.pathname === '/api/v1/initiatives',
    async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      held++;
      await gate;
      await route.continue();
    },
  );
  const answered = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/initiatives');
  return { release, held: () => held, answered };
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the filters survive Initiatives and back, and a new page of the same context, with one GET /timeline each', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tlv-restore-${vp.name}`;
      const tag = `tlv-restore-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Timeline view ${vp.name}`, tags: [tag] }, 201);

      await page.goto('/#/timeline');
      await expect(page.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await pickInitiative(page, slug);
      await pickTag(page, tag);
      await page.getByTestId('closed-filter').selectOption('30');
      const query = `initiative=${slug}&tag=${tag}&closed=30`;
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?${query}$`));
      expect(await saved(page)).toEqual({ v: 1, query });

      // Away and back through the nav: the bare route restores the filters with one fetch.
      await goToInitiatives(page);
      const reads = timelineReads(page);
      await goToTimeline(page);
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?${query}$`));
      await expect(page.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await expectInitiatives(page, [slug]);
      await expectTags(page, [tag]);
      await expect(page.getByTestId('closed-filter')).toHaveValue('30');
      expect(reads.count).toBe(1);
      const url = reads.urls[0];
      expect(url.searchParams.getAll('initiative')).toEqual([slug]);
      expect(url.searchParams.getAll('tag')).toEqual([tag]);
      expect(url.searchParams.get('closed_within')).toBe('30d');

      // A new page of the same context opens `#/timeline` bare and gets the same filters, again with one fetch.
      const second = await page.context().newPage();
      const secondReads = timelineReads(second);
      await second.goto('/#/timeline');
      await expect(second.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await expectInitiatives(second, [slug]);
      await expectTags(second, [tag]);
      await expect(second).toHaveURL(new RegExp(`#/timeline\\?${query}$`));
      expect(secondReads.count).toBe(1);
      expect(secondReads.urls[0].searchParams.getAll('initiative')).toEqual([slug]);
      await second.close();
      await api.dispose();
    });

    test('a fragment with its own query overrides the saved one, and Clear filters then nav away and back shows the defaults', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const a = `e2e-tlv-own-a-${vp.name}`;
      const b = `e2e-tlv-own-b-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug: a, title: `Own a ${vp.name}` }, 201);
      await post(api, '/api/v1/initiatives', { slug: b, title: `Own b ${vp.name}` }, 201);

      await page.goto(`/#/timeline?initiative=${b}`);
      await expectInitiatives(page, [b]);
      expect((await saved(page)).query).toBe(`initiative=${b}`);

      // A new page opens a link with its own query: that one wins and is what is saved from now on.
      const link = await page.context().newPage();
      await link.goto(`/#/timeline?initiative=${a}`);
      await expectInitiatives(link, [a]);
      expect((await saved(link)).query).toBe(`initiative=${a}`);

      await goToInitiatives(link);
      await goToTimeline(link);
      await expectInitiatives(link, [a]);

      // Clear filters: the defaults at once, nothing saved, and the defaults again after nav away and back.
      await link.getByTestId('clear-filters').click();
      await expect(link).toHaveURL(/#\/timeline$/);
      await expectInitiatives(link, []);
      await expect(link.getByTestId('clear-filters')).toHaveCount(0);
      expect(await stored(link)).toBeNull();
      await goToInitiatives(link);
      const reads = timelineReads(link);
      await goToTimeline(link);
      await expect(link.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await expect(link).toHaveURL(/#\/timeline$/);
      await expectInitiatives(link, []);
      expect(reads.count).toBe(1);
      expect(reads.urls[0].searchParams.getAll('initiative')).toEqual([]);
      expect(await stored(link)).toBeNull();
      await link.close();
      await api.dispose();
    });

    test('a restored deleted initiative and removed tag are dropped before the first fetch, the valid tag, actor and closed-within stay', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tlv-drop-${vp.name}`;
      const tag = `tlv-drop-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Drop ${vp.name}`, tags: [tag] }, 201);
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const seeded = `initiative=e2e-tlv-ghost&tag=tlv-ghost-tag&tag=${tag}&actor=playwright&closed=7`;
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, seeded] as const);

      const fresh = await page.context().newPage();
      const reads = timelineReads(fresh);
      await fresh.goto('/#/timeline');
      const kept = `tag=${tag}&actor=playwright&closed=7`;
      await expect(fresh).toHaveURL(new RegExp(`#/timeline\\?${kept}$`));
      await expect(fresh.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await expectTags(fresh, [tag]);
      await expectInitiatives(fresh, []);
      await expect(fresh.getByTestId('closed-filter')).toHaveValue('7');
      // Only the valid tag (and the valid actor and window) reached the server: no initiative, no removed tag.
      expect(reads.count).toBe(1);
      const params = reads.urls[0].searchParams;
      expect(params.getAll('tag')).toEqual([tag]);
      expect(params.has('initiative')).toBe(false);
      expect(params.get('actor')).toBe('playwright');
      expect(params.get('closed_within')).toBe('7d');
      // The saved query was rewritten without the dropped values.
      expect((await saved(fresh)).query).toBe(kept);
      await fresh.close();
      await api.dispose();
    });

    test('a link opened while a restored filter is still being checked wins: the late check rewrites nothing and reads nothing', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const seeded = `initiative=e2e-tlv-race-ghost-${vp.name}&tag=tlv-race-gone-${vp.name}&closed=7`;
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, seeded] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = timelineReads(fresh);
      await fresh.goto('/#/timeline');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await fresh.evaluate((hash) => (window.location.hash = hash), '#/timeline?actor=playwright');
      await expect.poll(() => reads.count, FIVE_SECONDS).toBeGreaterThan(0);
      lookups.release();
      await lookups.answered;
      await expect(fresh.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await fresh.waitForTimeout(500); // the late check's answer has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(/#\/timeline\?actor=playwright$/);
      await expect(fresh.getByTestId('closed-filter')).not.toHaveValue('7');
      expect((await saved(fresh)).query).toBe('actor=playwright');
      // Every read was the link's: the restored initiative, tag and window never reached the server.
      for (const url of reads.urls) {
        expect(url.searchParams.get('actor')).toBe('playwright');
        expect(url.searchParams.has('initiative')).toBe(false);
        expect(url.searchParams.has('tag')).toBe(false);
        expect(url.searchParams.get('closed_within')).not.toBe('7d');
      }
      await fresh.close();
    });

    test('leaving for another view while a restored filter is still being checked: the departed Timeline rewrites nothing and reads nothing', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const seeded = `initiative=e2e-tlv-depart-ghost-${vp.name}&tag=tlv-depart-gone-${vp.name}&closed=7`;
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, seeded] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = timelineReads(fresh);
      await fresh.goto('/#/timeline');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await fresh.evaluate((hash) => (window.location.hash = hash), `#/notes?author=tlv-depart-${vp.name}`);
      await expect(fresh.getByTestId('notes-view')).toBeVisible(FIVE_SECONDS);
      await expect(fresh.getByTestId('timeline-view')).toHaveCount(0);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500); // the departed view's late check has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(new RegExp(`#/notes\\?author=tlv-depart-${vp.name}$`));
      expect(reads.urls).toEqual([]);
      await fresh.close();
    });

    test('a restored filter is emptied when the initiatives lookup answers with no initiatives at all', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const seeded = 'initiative=e2e-tlv-empty-ghost&tag=tlv-empty-gone';
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, seeded] as const);

      const fresh = await page.context().newPage();
      // The lookup succeeds with an empty list: an answer, not a failure, so nothing restored is known.
      await fresh.route(
        (url) => url.pathname === '/api/v1/initiatives',
        (route) => (route.request().method() === 'GET' ? route.fulfill({ json: { items: [] } }) : route.continue()),
      );
      const reads = timelineReads(fresh);
      await fresh.goto('/#/timeline');
      await expect(fresh.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      await expect(fresh).toHaveURL(/#\/timeline$/);
      expect(reads.count).toBe(1);
      expect(reads.urls[0].searchParams.has('initiative')).toBe(false);
      expect(reads.urls[0].searchParams.has('tag')).toBe(false);
      expect(await stored(fresh)).toBeNull();
      await fresh.close();
    });
  });
}
