import { expect, test, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { initiativeChip, openChips, pickInitiative, pickTag, revealInitiative, tagChip } from './chips';

// The Now view's saved filters (PRD R16, SPEC 11): the tag and initiative filters are saved under
// `vh.view.v1.attention`, a bare `#/now` restores them before the first fetch (one `GET /attention/now`), a fragment
// with its own query wins, Clear filters forgets them, and a restored initiative or tag the board does not have is dropped.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.attention';
const FIVE_SECONDS = { timeout: 5000 };

async function post(api: APIRequestContext, path: string, data: object, status: number) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
const goTo = async (page: Page, label: string) => {
  await nav(page).getByRole('link', { name: new RegExp(`^${label}`) }).click();
};
const saved = async (page: Page) => JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { v: number; query: string };

// The `GET /attention/now` requests a page sends (the read behind the Needs me bands).
function attentionReads(page: Page) {
  const reads = { count: 0, queries: [] as string[] };
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname !== '/api/v1/attention/now') return;
    reads.count++;
    reads.queries.push(url.search);
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

// Resolves once a read of the initiatives catalog has ended whose last page (`next_after` absent) lists `slug`. The list is
// in creation order, so a new initiative is on the last page. On a long run's shared board the catalog takes many pages,
// and the tag chips and the restored filter both wait for it, so the test waits for that read itself before it gives the
// page its usual bound to draw the result.
function catalogWith(page: Page, slug: string) {
  return page.waitForResponse(async (response) => {
    if (new URL(response.url()).pathname !== '/api/v1/initiatives' || response.request().method() !== 'GET') return false;
    const body = (await response.json().catch(() => null)) as { items: { slug: string }[]; next_after?: string } | null;
    return body !== null && body.next_after === undefined && body.items.some((i) => i.slug === slug);
  });
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a tag and an initiative filter survive Initiatives and back, and a new page of the same context, with one GET /attention/now each', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-av-restore-${vp.name}`;
      const tag = `av-restore-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Attention view ${vp.name}`, tags: [tag] }, 201);

      const firstCatalog = catalogWith(page, slug);
      await page.goto('/#/now');
      await firstCatalog;
      await openChips(page);
      await expect(tagChip(page, tag)).toBeVisible(FIVE_SECONDS);
      await pickTag(page, tag);
      await pickInitiative(page, slug);
      const query = `tag=${tag}&initiative=${slug}`;
      await expect(page).toHaveURL(new RegExp(`#/now\\?${query}$`));
      expect(await saved(page)).toEqual({ v: 1, query });

      // Away through the nav and back through the nav: the bare route restores the filters with one fetch.
      await goTo(page, 'Initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const reads = attentionReads(page);
      const backCatalog = catalogWith(page, slug);
      await goTo(page, 'Now');
      await backCatalog;
      await expect(page).toHaveURL(new RegExp(`#/now\\?${query}$`));
      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(initiativeChip(page, slug)).toHaveAttribute('aria-pressed', 'true');
      await expect(page.locator('[data-testid=now-bands], [data-testid=now-dock]')).toBeVisible(FIVE_SECONDS);
      expect(reads.count).toBe(1);
      expect(reads.queries).toEqual([`?${query}`]);

      // A new page of the same context opens `#/now` bare and gets the same filters, again with one fetch.
      const second = await page.context().newPage();
      const secondReads = attentionReads(second);
      const secondCatalog = catalogWith(second, slug);
      await second.goto('/#/now');
      await secondCatalog;
      await expect(tagChip(second, tag)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(initiativeChip(second, slug)).toHaveAttribute('aria-pressed', 'true');
      await expect(second.locator('[data-testid=now-bands], [data-testid=now-dock]')).toBeVisible(FIVE_SECONDS);
      await expect(second).toHaveURL(new RegExp(`#/now\\?${query}$`));
      expect(secondReads.count).toBe(1);
      expect(secondReads.queries).toEqual([`?${query}`]);
      await second.close();
      await api.dispose();
    });

    test('a fragment with its own query overrides the saved one, and Clear filters then nav away and back shows the defaults', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const a = `e2e-av-own-a-${vp.name}`;
      const b = `e2e-av-own-b-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug: a, title: `Own a ${vp.name}` }, 201);
      await post(api, '/api/v1/initiatives', { slug: b, title: `Own b ${vp.name}` }, 201);

      await page.goto(`/#/now?initiative=${b}`);
      await expect(initiativeChip(page, b)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      expect((await saved(page)).query).toBe(`initiative=${b}`);

      // A new page opens a link with its own query: that one wins and is what is saved from now on.
      const link = await page.context().newPage();
      await link.goto(`/#/now?initiative=${a}`);
      await expect(initiativeChip(link, a)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(await revealInitiative(link, b)).toHaveAttribute('aria-pressed', 'false');
      expect((await saved(link)).query).toBe(`initiative=${a}`);

      await goTo(link, 'Initiatives');
      await expect(link.getByTestId('initiative-row').first()).toBeVisible();
      await goTo(link, 'Now');
      await expect(initiativeChip(link, a)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(await revealInitiative(link, b)).toHaveAttribute('aria-pressed', 'false');

      // Clear filters: the defaults at once, nothing saved, and the defaults again after nav away and back.
      await openChips(link);
      await link.getByTestId('chip-clear').click();
      await expect(link).toHaveURL(/#\/now$/);
      await expect(await revealInitiative(link, a)).toHaveAttribute('aria-pressed', 'false');
      await expect(link.getByTestId('chip-clear')).toHaveCount(0);
      expect(await link.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await goTo(link, 'Initiatives');
      await expect(link.getByTestId('initiative-row').first()).toBeVisible();
      const reads = attentionReads(link);
      await goTo(link, 'Now');
      await expect(link.locator('[data-testid=now-bands], [data-testid=now-dock]')).toBeVisible(FIVE_SECONDS);
      await expect(link).toHaveURL(/#\/now$/);
      await expect(await revealInitiative(link, a)).toHaveAttribute('aria-pressed', 'false');
      await expect(link.getByTestId('chip-clear')).toHaveCount(0);
      expect(reads.queries).toEqual(['']);
      expect(await link.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await link.close();
      await api.dispose();
    });

    test('a restored initiative and tag the board does not have are dropped, the known ones stay', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-av-drop-${vp.name}`;
      const tag = `av-drop-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Drop ${vp.name}`, tags: [tag] }, 201);
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const query = `tag=${tag}&tag=av-ghost-tag&initiative=e2e-av-ghost&initiative=${slug}`;
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, query] as const);

      const fresh = await page.context().newPage();
      const reads = attentionReads(fresh);
      await fresh.goto('/#/now');
      const kept = `tag=${tag}&initiative=${slug}`;
      await expect(fresh).toHaveURL(new RegExp(`#/now\\?${kept}$`));
      await expect(tagChip(fresh, tag)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(initiativeChip(fresh, slug)).toHaveAttribute('aria-pressed', 'true');
      await expect(fresh.locator('[data-testid=now-bands], [data-testid=now-dock]')).toBeVisible(FIVE_SECONDS);
      await expect(tagChip(fresh, 'av-ghost-tag')).toHaveCount(0);
      await expect(initiativeChip(fresh, 'e2e-av-ghost')).toHaveCount(0);
      // The unknown values never reached the server, and the saved query was rewritten without them.
      expect(reads.queries).toEqual([`?${kept}`]);
      expect((await saved(fresh)).query).toBe(kept);
      await fresh.close();
      await api.dispose();
    });

    test('a link opened while a restored filter is still being checked wins: the late check rewrites nothing and reads nothing', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-av-race-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Race ${vp.name}` }, 201);
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const restored = `tag=av-race-gone-${vp.name}&initiative=e2e-av-race-ghost-${vp.name}`;
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, restored] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = attentionReads(fresh);
      await fresh.goto('/#/now');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await fresh.evaluate((hash) => (window.location.hash = hash), `#/now?initiative=${slug}`);
      await expect.poll(() => reads.count, FIVE_SECONDS).toBe(1);
      lookups.release();
      await lookups.answered;
      await expect(initiativeChip(fresh, slug)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await fresh.waitForTimeout(500); // the late check's answer has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(new RegExp(`#/now\\?initiative=${slug}$`));
      expect((await saved(fresh)).query).toBe(`initiative=${slug}`);
      // Only the link's query was read: the restored values never reached the server, and nothing read again after.
      expect(reads.queries).toEqual([`?initiative=${slug}`]);
      await fresh.close();
      await api.dispose();
    });

    test('leaving for another view while a restored filter is still being checked: the departed Now rewrites nothing and reads nothing', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const restored = `tag=av-depart-gone-${vp.name}&initiative=e2e-av-depart-ghost-${vp.name}`;
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, restored] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = attentionReads(fresh);
      await fresh.goto('/#/now');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await fresh.evaluate((hash) => (window.location.hash = hash), '#/tasks?kind=watch');
      await expect(fresh.getByTestId('tasks-view')).toBeVisible(FIVE_SECONDS);
      await expect(fresh.getByTestId('now-view')).toHaveCount(0);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500); // the departed view's late check has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(/#\/tasks\?kind=watch$/);
      await expect(fresh.getByTestId('kind-filter')).toHaveValue('watch');
      expect(reads.queries).toEqual([]);
      await fresh.close();
    });

    test('a restored filter is emptied when the initiatives lookup answers with no initiatives at all', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const query = 'tag=av-empty-gone&initiative=e2e-av-empty-ghost';
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, query] as const);

      const fresh = await page.context().newPage();
      // The lookup succeeds with an empty list: an answer, not a failure, so nothing restored is known.
      let lookups = 0;
      await fresh.route(
        (url) => url.pathname === '/api/v1/initiatives',
        (route) => {
          if (route.request().method() !== 'GET') return route.continue();
          lookups++;
          return route.fulfill({ json: { items: [] } });
        },
      );
      const reads = attentionReads(fresh);
      await fresh.goto('/#/now');
      await expect(fresh.locator('[data-testid=now-bands], [data-testid=now-dock]')).toBeVisible(FIVE_SECONDS);
      await expect(fresh).toHaveURL(/#\/now$/);
      expect(lookups).toBeGreaterThan(0);
      // The unknown values never reached the server, and the saved query is gone (the empty filter is the default).
      expect(reads.queries).toEqual(['']);
      expect(await fresh.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await fresh.close();
    });
  });
}
