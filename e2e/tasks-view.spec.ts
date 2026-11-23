import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { expectInitiatives, initiativeChip, showAllInitiatives } from './chips';

// The Tasks view's sort and saved view state (PRD R16, SPEC 11): the table sorts by its columns through the shared sort control, the sort rides in the fragment query next to the filters, the whole query is
// saved under `vh.view.v1.tasks` and restored on a bare `#/tasks` with one fetch, a fragment with its own query wins,
// Clear filters forgets it all, and an initiative the board no longer has is dropped from a restored query. Each test
// seeds initiatives of its own on the shared board and filters the view down to them.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.tasks';

async function post<T = { ref: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

interface Seed {
  slug: string;
  /** Titles Mango, apple, Zebra, cherry (in that order), then two done tasks that need attention. */
  refs: string[];
  needs: string[];
}

// Four table rows: S-1 Mango (work, open), S-2 apple (watch, open), S-3 Zebra (work, closed), S-4 cherry (work, open); and two done
// tasks, which sit in Needs attention above the table, not in it.
async function seed(api: APIRequestContext, slug: string): Promise<Seed> {
  await post(api, '/api/v1/initiatives', { slug, title: `Tasks view ${slug}` }, 201);
  const task = async (title: string, extra: object = {}) => (await post(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201)).ref;
  const mango = await task('Mango');
  const apple = await task('apple', { kind: 'watch', cadence_seconds: 3600 });
  const zebra = await task('Zebra');
  const cherry = await task('cherry');
  const beta = await task('Beta');
  const gamma = await task('Gamma');
  await post(api, `/api/v1/tasks/${zebra}/done`, {}, 200);
  await post(api, `/api/v1/tasks/${zebra}/close`, { resolution: 'completed' }, 200);
  await post(api, `/api/v1/tasks/${beta}/done`, {}, 200);
  await post(api, `/api/v1/tasks/${gamma}/done`, {}, 200);
  return { slug, refs: [mango, apple, zebra, cherry], needs: [beta, gamma] };
}

const tableRefs = (page: Page) => page.getByTestId('task-ref').allTextContents();
const needsRefs = (page: Page) => page.getByTestId('needs-ref').allTextContents();
const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
const goTo = async (page: Page, label: string) => {
  await nav(page).getByRole('link', { name: label, exact: true }).click();
};
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);

// The `GET /tasks` requests a page sends: the table's own, and the Needs attention section's (attention=true).
function taskReads(page: Page) {
  const reads = { table: 0, needs: 0 };
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname !== '/api/v1/tasks') return;
    // The Now nav badge counts the live tasks with one read of its own (limit 1000, active): not the table's.
    if (url.searchParams.get('active') === 'true' && url.searchParams.get('limit') === '1000') return;
    if (url.searchParams.get('attention') === 'true') reads.needs++;
    else reads.table++;
  });
  return reads;
}

// The shared sort control is one select at every width; `phone` stays in the signature so the tests read as before.
async function sortBy(page: Page, _phone: boolean, column: string, dir: 'asc' | 'desc' | null) {
  await page.getByTestId('sort-select').selectOption(dir === null ? '' : `${column}:${dir}`);
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
  const phone = vp.name === 'phone';
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('sorting by Flags orders the rows by their badges, the unflagged row last in both directions, and the sort is kept in the fragment', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tv-flags-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Flags ${slug}` }, 201);
      const task = async (title: string, extra: object = {}) => (await post(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201)).ref;
      // Creation order: plain (no flag), leased (active), blocked by the leased one (blocked).
      const plain = await task('Plain');
      const leased = await task('Leased');
      const blocked = await task('Blocked', { blocked_by: [leased] });
      await post(api, '/api/v1/leases', { task: leased, intent: 'flags sort', resources: [`repo:${slug}`] }, 201);

      await page.goto(`/#/tasks?initiative=${slug}&status=all`);
      await expect.poll(() => tableRefs(page)).toEqual([plain, leased, blocked]);
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^flags:/);
      await sortBy(page, phone, 'flags', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('flags:asc');
      await expect.poll(() => tableRefs(page)).toEqual([leased, blocked, plain]);
      await expect(page).toHaveURL(/sort=flags&dir=asc$/);
      await sortBy(page, phone, 'flags', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('flags:desc');
      await expect.poll(() => tableRefs(page)).toEqual([blocked, leased, plain]);
      await expect(page).toHaveURL(/sort=flags&dir=desc$/);
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('flags:desc');
      await page.reload();
      await expect.poll(() => tableRefs(page)).toEqual([blocked, leased, plain]);
      await expect(page.getByTestId('sort-select')).toHaveValue('flags:desc');
      await sortBy(page, phone, 'flags', null);
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^flags:/);
      await expect.poll(() => tableRefs(page)).toEqual([plain, leased, blocked]);
      await api.dispose();
    });

    test('two filters and a sort survive Initiatives and back, and a new page of the same context, with one fetch each', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { slug, refs } = await seed(api, `e2e-tv-restore-${vp.name}`);
      const [mango, apple, , cherry] = refs;

      // Opened on its own initiative, not bare: the bare view lists the whole shared board, many pages on a long run,
      // and this test's budget is for the restore, not for store-sized reads (20 s of 30 on the agent near the end).
      await page.goto(`/#/tasks?initiative=${slug}`);
      await expectInitiatives(page, [slug]);
      await expect(page.getByTestId('status-filter')).toHaveValue('not-closed');
      await page.getByTestId('status-filter').selectOption('all');
      await expect.poll(() => tableRefs(page)).toEqual([mango, apple, refs[2], cherry]);
      await sortBy(page, phone, 'title', 'desc');
      // Title descending, ignoring case: Zebra, Mango, cherry, apple.
      const expected = [refs[2], mango, cherry, apple];
      await expect.poll(() => tableRefs(page)).toEqual(expected);
      const hash = `#/tasks?status=all&initiative=${slug}&sort=title&dir=desc`;
      await expect(page).toHaveURL(endsWith(hash));
      expect(JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string)).toEqual({ v: 1, query: hash.slice('#/tasks?'.length) });

      // Away through the nav and back through the nav: the bare route restores the state with one fetch of each list.
      await goTo(page, 'Initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const reads = taskReads(page);
      await goTo(page, 'Tasks');
      await expect(page.getByTestId('task-row').first()).toBeVisible();
      await expect.poll(() => tableRefs(page)).toEqual(expected);
      await expect(page).toHaveURL(endsWith(hash));
      await expectInitiatives(page, [slug]);
      await expect(page.getByTestId('status-filter')).toHaveValue('all');
      await expect(page.getByTestId('sort-select')).toHaveValue('title:desc');
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('title:desc');
      expect(reads).toEqual({ table: 1, needs: 1 });

      // A new page of the same context opens `#/tasks` bare and gets the same state.
      const second = await page.context().newPage();
      const secondReads = taskReads(second);
      await second.goto('/#/tasks');
      await expect(second.getByTestId('task-row').first()).toBeVisible();
      await expect.poll(() => tableRefs(second)).toEqual(expected);
      await expect(second).toHaveURL(endsWith(hash));
      await expectInitiatives(second, [slug]);
      await expect(second.getByTestId('status-filter')).toHaveValue('all');
      await expect(second.getByTestId('sort-select')).toHaveValue('title:desc');
      if (phone) await expect(second.getByTestId('sort-select')).toHaveValue('title:desc');
      expect(secondReads).toEqual({ table: 1, needs: 1 });
      await second.close();
      await api.dispose();
    });

    test('a fragment with its own query overrides the saved one, and nav away and back restores it', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const a = await seed(api, `e2e-tv-own-a-${vp.name}`);
      const b = await seed(api, `e2e-tv-own-b-${vp.name}`);

      // Something saved first: initiative b, sorted by title.
      await page.goto(`/#/tasks?initiative=${b.slug}&sort=title&dir=asc`);
      await expectInitiatives(page, [b.slug]);
      await expect.poll(() => tableRefs(page)).toEqual([b.refs[1], b.refs[3], b.refs[0]]); // apple, cherry, Mango (Zebra is closed)

      // A new page opens a link with its own query: that one wins, and it is what is saved from now on.
      const link = await page.context().newPage();
      await link.goto(`/#/tasks?initiative=${a.slug}&kind=watch&status=all&sort=ref&dir=desc`);
      await expectInitiatives(link, [a.slug]);
      await expect(link.getByTestId('kind-filter')).toHaveValue('watch');
      await expect(link.getByTestId('sort-select')).toHaveValue('ref:desc');
      await expect.poll(() => tableRefs(link)).toEqual([a.refs[1]]);
      expect(JSON.parse((await link.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe(`initiative=${a.slug}&kind=watch&status=all&sort=ref&dir=desc`) // saved as the link wrote it;

      // Away and back restores the link's query, not the earlier one.
      await goTo(link, 'Initiatives');
      await expect(link.getByTestId('initiative-row').first()).toBeVisible();
      await goTo(link, 'Tasks');
      await expectInitiatives(link, [a.slug]);
      await expect(link.getByTestId('kind-filter')).toHaveValue('watch');
      await expect.poll(() => tableRefs(link)).toEqual([a.refs[1]]);
      await link.close();
      await api.dispose();
    });

    // Its own test, not the end of the one above: away and back to the defaults lists the whole shared board and the
    // Initiatives list twice, which a long run grows to many pages each; with the link steps before it the one test's
    // budget held work that grows with the store (28 s of 30 on the agent near the end of a run).
    test('Clear filters shows the defaults at once, saves nothing, and nav away and back shows the defaults', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const a = await seed(api, `e2e-tv-clear-${vp.name}`);
      const link = page;
      await link.goto(`/#/tasks?initiative=${a.slug}&kind=watch&status=all&sort=ref&dir=desc`);
      await expectInitiatives(link, [a.slug]);
      await expect(link.getByTestId('kind-filter')).toHaveValue('watch');
      await expect.poll(() => tableRefs(link)).toEqual([a.refs[1]]);

      // Clear filters: the defaults at once, nothing saved, and the defaults again after nav away and back.
      await link.getByTestId('clear-filters').click();
      await expect(link).toHaveURL(/#\/tasks$/);
      await expectInitiatives(link, []);
      await expect(link.getByTestId('kind-filter')).toHaveValue('');
      await expect(link.getByTestId('status-filter')).toHaveValue('not-closed');
      await expect(link.getByTestId('clear-filters')).toHaveCount(0);
      await expect(link.getByTestId('sort-select')).not.toHaveValue(/^ref:/);
      expect(await link.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await goTo(link, 'Initiatives');
      await expect(link.getByTestId('initiative-row').first()).toBeVisible();
      // The defaults list the whole shared board, many pages on a long run: wait for the unfiltered read's last page
      // itself, then give the table its usual bound to draw it.
      const wholeBoard = link.waitForResponse(async (response) => {
        const url = new URL(response.url());
        if (url.pathname !== '/api/v1/tasks' || url.searchParams.has('initiative') || url.searchParams.has('active') || url.searchParams.has('attention')) return false;
        const body = (await response.json().catch(() => null)) as { next_after?: string } | null;
        return body !== null && body.next_after === undefined;
      });
      await goTo(link, 'Tasks');
      await wholeBoard;
      await expect(link.getByTestId('task-row').first()).toBeVisible();
      await expect(link).toHaveURL(/#\/tasks$/);
      await expectInitiatives(link, []);
      await expect(link.getByTestId('kind-filter')).toHaveValue('');
      await expect(link.getByTestId('status-filter')).toHaveValue('not-closed');
      await expect(link.getByTestId('sort-select')).not.toHaveValue(/^ref:/);
      if (phone) await expect(link.getByTestId('sort-select')).toHaveValue('');
      expect(await link.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await api.dispose();
    });

    test('a restored initiative the board does not have is dropped, the other restored values stay', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { slug } = await seed(api, `e2e-tv-drop-${vp.name}`);
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(
        ([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })),
        [KEY, 'initiative=e2e-tv-ghost&kind=watch&status=bogus&sort=title&dir=asc'] as const,
      );

      const fresh = await page.context().newPage();
      const reads = taskReads(fresh);
      await fresh.goto('/#/tasks');
      await expect(fresh.getByTestId('status-filter')).toHaveValue('not-closed'); // the unknown status is the default
      await expectInitiatives(fresh, []);
      await expect(fresh.getByTestId('kind-filter')).toHaveValue('watch');
      await expect(fresh.getByTestId('sort-select')).toHaveValue('title:asc');
      await expect(fresh).toHaveURL(/#\/tasks\?kind=watch&sort=title&dir=asc$/);
      await expect(fresh.getByTestId('task-row').first()).toBeVisible();
      expect(reads.table).toBe(1);
      expect(JSON.parse((await fresh.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe('kind=watch&sort=title&dir=asc');
      // The seeded initiative is still offered (the select lists real initiatives only).
      await showAllInitiatives(fresh);
      await expect(initiativeChip(fresh, slug)).toHaveCount(1);
      await expect(initiativeChip(fresh, 'e2e-tv-ghost')).toHaveCount(0);
      await fresh.close();
      await api.dispose();
    });

    test('a link opened while a restored initiative is still being checked wins: the late check rewrites nothing and reads nothing', async ({ page }) => {
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, `initiative=e2e-tv-race-ghost-${vp.name}&kind=watch`] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const sent: { initiative: string; kind: string }[] = [];
      fresh.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname === '/api/v1/tasks') sent.push({ initiative: url.searchParams.get('initiative') ?? '', kind: url.searchParams.get('kind') ?? '' });
      });
      await fresh.goto('/#/tasks');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await fresh.evaluate((hash) => (window.location.hash = hash), '#/tasks?sort=title&dir=asc');
      await expect(fresh.getByTestId('kind-filter')).toHaveValue('', { timeout: 5000 });
      await expect.poll(() => sent.length, { timeout: 5000 }).toBeGreaterThan(0);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500); // the late check's answer has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(/#\/tasks\?sort=title&dir=asc$/);
      await expect(fresh.getByTestId('kind-filter')).toHaveValue('');
      await expectInitiatives(fresh, []);
      expect(JSON.parse((await fresh.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe('sort=title&dir=asc');
      // Every read was the link's: neither the restored kind nor the unknown initiative ever reached the server.
      expect(sent.every((s) => s.initiative === '' && s.kind === '')).toBe(true);
      await fresh.close();
    });

    test('leaving for another view while a restored initiative is still being checked: the departed Tasks rewrites nothing and reads nothing', async ({ page }) => {
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, `initiative=e2e-tv-depart-ghost-${vp.name}&kind=watch`] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = taskReads(fresh);
      await fresh.goto('/#/tasks');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await fresh.evaluate((hash) => (window.location.hash = hash), `#/notes?author=tv-depart-${vp.name}`);
      await expect(fresh.getByTestId('notes-view')).toBeVisible({ timeout: 5000 });
      await expect(fresh.getByTestId('tasks-view')).toHaveCount(0);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500); // the departed view's late check has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(new RegExp(`#/notes\\?author=tv-depart-${vp.name}$`));
      expect(JSON.parse((await fresh.evaluate(() => localStorage.getItem('vh.view.v1.notes'))) as string).query).toBe(`author=tv-depart-${vp.name}`);
      // The departed Tasks never read its table or its needs-attention panel.
      expect(reads).toEqual({ table: 0, needs: 0 });
      await fresh.close();
    });

    test('a restored initiative is dropped when the initiatives lookup answers with no initiatives at all', async ({ page }) => {
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, 'initiative=e2e-tv-empty-ghost&kind=watch'] as const);

      const fresh = await page.context().newPage();
      // The lookup succeeds with an empty list: an answer, not a failure, so no restored initiative is known.
      await fresh.route(
        (url) => url.pathname === '/api/v1/initiatives',
        (route) => (route.request().method() === 'GET' ? route.fulfill({ json: { items: [] } }) : route.continue()),
      );
      const initiativesSent: string[] = [];
      fresh.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname === '/api/v1/tasks') initiativesSent.push(url.searchParams.get('initiative') ?? '');
      });
      await fresh.goto('/#/tasks');
      await expect(fresh).toHaveURL(/#\/tasks\?kind=watch$/);
      await expect(fresh.getByTestId('kind-filter')).toHaveValue('watch');
      await expect.poll(() => initiativesSent.length).toBeGreaterThan(0);
      expect(initiativesSent.every((value) => value === '')).toBe(true);
      expect(JSON.parse((await fresh.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe('kind=watch');
      await fresh.close();
    });

    test('sorting by a column of each type orders the rows and sets aria-sort, and the Needs attention section keeps its order', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { slug, refs, needs } = await seed(api, `e2e-tv-sort-${vp.name}`);
      const [mango, apple, zebra, cherry] = refs;
      const byDefault = [mango, apple, zebra, cherry];

      await page.goto(`/#/tasks?initiative=${slug}&status=all`);
      await expect.poll(() => tableRefs(page)).toEqual(byDefault);
      const needsBefore = await needsRefs(page);
      expect([...needsBefore].sort()).toEqual([...needs].sort());
      // The shared sort control shows at every width, and the table headers are plain: nothing in them sorts.
      await expect(page.getByTestId('sort-select')).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('');
      await expect(page.locator('th button')).toHaveCount(0);

      // ref (numbers compare as numbers): the first step is ascending, the second descending, the third the default.
      await sortBy(page, phone, 'ref', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('ref:desc');
      await expect.poll(() => tableRefs(page)).toEqual([cherry, zebra, apple, mango]);
      await expect(page).toHaveURL(/sort=ref&dir=desc$/);

      // text: title ascending ignores case.
      await sortBy(page, phone, 'title', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('title:asc');
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^ref:/);
      await expect.poll(() => tableRefs(page)).toEqual([apple, cherry, mango, zebra]);
      await expect(page).toHaveURL(/sort=title&dir=asc$/);

      // status: open before closed ascending, closed first descending; ties keep the default order.
      await sortBy(page, phone, 'status', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('status:asc');
      await expect.poll(() => tableRefs(page)).toEqual([mango, apple, cherry, zebra]);
      await sortBy(page, phone, 'status', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('status:desc');
      await expect.poll(() => tableRefs(page)).toEqual([zebra, mango, apple, cherry]);

      // kind: work before watch ascending, watch first descending.
      await sortBy(page, phone, 'kind', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('kind:asc');
      await expect.poll(() => tableRefs(page)).toEqual([mango, zebra, cherry, apple]);
      await sortBy(page, phone, 'kind', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('kind:desc');
      await expect.poll(() => tableRefs(page)).toEqual([apple, mango, zebra, cherry]);
      expect(await needsRefs(page)).toEqual(needsBefore);

      // Back to the default order, and the sort is gone from the fragment.
      await sortBy(page, phone, 'kind', null);
      await expect.poll(() => tableRefs(page)).toEqual(byDefault);
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^kind:/);
      await expect(page).toHaveURL(endsWith(`#/tasks?status=all&initiative=${slug}`));
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('');
      await api.dispose();
    });

    test('sorting by Initiative and Lease holder orders the rows, rows without a holder stay last both ways, equal values keep the default order, and a saved holder sort is restored on a bare link', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const prefix = `e2e-tv-cols-${vp.name}`;
      const slugA = `${prefix}-b`;
      const slugB = `${prefix}-a`;
      await post(api, '/api/v1/initiatives', { slug: slugA, title: `Cols ${slugA}` }, 201);
      await post(api, '/api/v1/initiatives', { slug: slugB, title: `Cols ${slugB}` }, 201);
      const task = async (initiative: string, title: string) => (await post(api, '/api/v1/tasks', { initiative, title }, 201)).ref;
      // Creation order: b1 (initiative ...-b), a1 (...-a), b2 (...-b), a2 (...-a), b3 (...-b).
      const b1 = await task(slugA, 'B one');
      const a1 = await task(slugB, 'A one');
      const b2 = await task(slugA, 'B two');
      const a2 = await task(slugB, 'A two');
      const b3 = await task(slugA, 'B three');
      // Holders: a1 and b2 are held by "zed", b1 by "amy"; a2 and b3 have no lease.
      const holderApi = async (actor: string) =>
        playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor } });
      const amy = await holderApi(`${prefix}-amy`);
      const zed = await holderApi(`${prefix}-zed`);
      await post(amy, '/api/v1/leases', { task: b1, intent: 'cols sort', resources: [`repo:${prefix}-1`] }, 201);
      await post(zed, '/api/v1/leases', { task: a1, intent: 'cols sort', resources: [`repo:${prefix}-2`] }, 201);
      await post(zed, '/api/v1/leases', { task: b2, intent: 'cols sort', resources: [`repo:${prefix}-3`] }, 201);
      const byDefault = [b1, a1, b2, a2, b3];

      // Scoped to the test's two initiatives (an OR of the chips), so the table it sorts is its own five rows: on a long
      // run's shared board the whole list is thousands of rows, and reading and sorting that outran the poll's bound.
      await page.goto(`/#/tasks?status=all&initiative=${slugA}&initiative=${slugB}`);
      const mine = async () => (await tableRefs(page)).filter((ref) => byDefault.includes(ref));
      await expect.poll(mine).toEqual(byDefault);

      // Initiative: text, equal initiatives keep the default order.
      await sortBy(page, phone, 'initiative', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('initiative:asc');
      await expect.poll(mine).toEqual([a1, a2, b1, b2, b3]);
      await sortBy(page, phone, 'initiative', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('initiative:desc');
      await expect.poll(mine).toEqual([b1, b2, b3, a1, a2]);

      // Lease holder: text, the two without a holder last in both directions, equal holders keep the default order.
      await sortBy(page, phone, 'holder', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('holder:asc');
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^initiative:/);
      await expect.poll(mine).toEqual([b1, a1, b2, a2, b3]);
      await sortBy(page, phone, 'holder', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('holder:desc');
      await expect.poll(mine).toEqual([a1, b2, b1, a2, b3]);

      // The saved holder sort comes back on a bare #/tasks after a visit to Initiatives.
      await expect(page).toHaveURL(/sort=holder&dir=desc$/);
      const query = new URL(page.url()).hash.split('?')[1];
      await goTo(page, 'Initiatives');
      await expect(page.getByRole('heading', { level: 2, name: 'Initiatives' })).toBeVisible();
      await goTo(page, 'Tasks');
      await expect(page).toHaveURL(endsWith(`#/tasks?${query}`));
      await expect(page.getByTestId('sort-select')).toHaveValue('holder:desc');
      await expect.poll(mine).toEqual([a1, b2, b1, a2, b3]);
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('holder:desc');
      await amy.dispose();
      await zed.dispose();
      await api.dispose();
    });

    test('the shared sort control sorts from the keyboard: the arrow keys step through the options', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { slug, refs } = await seed(api, `e2e-tv-keys-${vp.name}`);
      const [mango, apple, zebra, cherry] = refs;
      await page.goto(`/#/tasks?initiative=${slug}&status=all`);
      await expect.poll(() => tableRefs(page)).toEqual([mango, apple, zebra, cherry]);

      const select = page.getByTestId('sort-select');
      await select.focus();
      await expect(select).toBeFocused();
      await page.keyboard.press('ArrowDown'); // Default order -> Ref (ascending)
      await expect(select).toHaveValue('ref:asc');
      await page.keyboard.press('ArrowDown');
      await page.keyboard.press('ArrowDown'); // -> Title (ascending)
      await expect(select).toHaveValue('title:asc');
      await expect.poll(() => tableRefs(page)).toEqual([apple, cherry, mango, zebra]);
      await expect(page).toHaveURL(/sort=title&dir=asc$/);
      await page.keyboard.press('ArrowDown'); // -> Title (descending)
      await expect(select).toHaveValue('title:desc');
      await expect.poll(() => tableRefs(page)).toEqual([zebra, mango, cherry, apple]);
      await page.keyboard.press('Home'); // back to the default order
      await expect(select).toHaveValue('');
      await expect.poll(() => tableRefs(page)).toEqual([mango, apple, zebra, cherry]);
      await api.dispose();
    });
  });
}
