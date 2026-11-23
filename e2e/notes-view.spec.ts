import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { logicViewports } from './viewports';

// The Notes view's saved filters (PRD R16, SPEC 11): the six filters are saved under `vh.view.v1.notes` when they
// are applied, a bare `#/notes` restores them before the first fetch (one notes read), a fragment with its own query
// wins, and Clear filters forgets them.
const KEY = 'vh.view.v1.notes';
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
const stored = (page: Page) => page.evaluate((k) => localStorage.getItem(k), KEY);
const filters = (page: Page) => page.getByTestId('notes-filters');

// The `GET /notes` requests a page sends.
function notesReads(page: Page) {
  const reads = { count: 0, queries: [] as string[] };
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname !== '/api/v1/notes') return;
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

async function apply(page: Page, fields: { author?: string; resource?: string; broadcast?: boolean }) {
  const form = filters(page);
  if (fields.broadcast === true) await form.getByTestId('broadcast-filter').click();
  if (fields.author !== undefined) await form.getByLabel('Author', { exact: true }).fill(fields.author);
  if (fields.resource !== undefined) await form.getByLabel('Resource', { exact: true }).fill(fields.resource);
  await page.getByTestId('apply-filters').click();
}

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('applied filters survive Initiatives and back, and a new page of the same context, with one notes read each', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const author = `nv-restore-${vp.name}`;
      const resource = `host:nv-restore-${vp.name}`;
      await post(api, '/api/v1/notes', { summary: `Restore note ${vp.name}`, resource }, 201);

      await page.goto('/#/notes');
      await expect(page.getByTestId('notes-view')).toBeVisible(FIVE_SECONDS);
      await apply(page, { broadcast: true, resource, author });
      const query = `broadcast=only&resource=${encodeURIComponent(resource)}&author=${author}`;
      await expect(page).toHaveURL(new RegExp(`#/notes\\?${query.replace(/[?.]/g, '\\$&')}$`));
      expect(await saved(page)).toEqual({ v: 1, query });

      // Away through the nav and back through the nav: the bare route restores the filters with one read.
      await goTo(page, 'Initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const reads = notesReads(page);
      await goTo(page, 'Notes');
      await expect(filters(page).getByLabel('Author', { exact: true })).toHaveValue(author, FIVE_SECONDS);
      await expect(filters(page).getByLabel('Resource', { exact: true })).toHaveValue(resource);
      await expect(filters(page).getByTestId('broadcast-filter')).toHaveAttribute('aria-pressed', 'true');
      await expect(page).toHaveURL(new RegExp(`#/notes\\?${query.replace(/[?.]/g, '\\$&')}$`));
      await expect(page.getByTestId('no-notes').or(page.getByTestId('notes-list'))).toBeVisible(FIVE_SECONDS);
      expect(reads.count).toBe(1);
      expect(new URLSearchParams(reads.queries[0]).get('author')).toBe(author);
      expect(new URLSearchParams(reads.queries[0]).get('resource')).toBe(resource);
      expect(new URLSearchParams(reads.queries[0]).get('broadcast')).toBe('only');

      // A new page of the same context opens `#/notes` bare and gets the same filters, again with one read.
      const second = await page.context().newPage();
      const secondReads = notesReads(second);
      await second.goto('/#/notes');
      await expect(filters(second).getByLabel('Author', { exact: true })).toHaveValue(author, FIVE_SECONDS);
      await expect(second.getByTestId('no-notes').or(second.getByTestId('notes-list'))).toBeVisible(FIVE_SECONDS);
      await expect(second).toHaveURL(new RegExp(`#/notes\\?${query.replace(/[?.]/g, '\\$&')}$`));
      expect(secondReads.count).toBe(1);
      expect(new URLSearchParams(secondReads.queries[0]).get('author')).toBe(author);
      await second.close();
      await api.dispose();
    });

    test('restored initiative and task the board no longer has are dropped before the first read, valid ones and other filters stay', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-nv-drop-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Notes drop ${vp.name}` }, 201);
      const created = await api.post('/api/v1/tasks', { data: { initiative: slug, title: 'Known task' } });
      expect(created.status()).toBe(201);
      const ref = ((await created.json()) as { ref: string }).ref;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const ghost = `e2e-nv-ghost-${vp.name}`;
      const author = `nv-drop-${vp.name}`;
      const put = (query: string) => page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, query] as const);

      // A removed initiative alone, then a removed task beside a valid initiative, then a removed task and initiative together.
      for (const [seeded, kept] of [
        [`initiative=${ghost}&author=${author}`, `author=${author}`],
        [`task=${ghost}-1&initiative=${slug}&author=${author}`, `initiative=${slug}&author=${author}`],
        [`task=${ghost}-1&initiative=${ghost}&resource=host%3Ax&author=${author}`, `resource=host%3Ax&author=${author}`],
        [`task=${ref}&initiative=${slug}&author=${author}`, `task=${encodeURIComponent(ref)}&initiative=${slug}&author=${author}`],
      ]) {
        await put(seeded);
        const fresh = await page.context().newPage();
        const reads = notesReads(fresh);
        const failed: number[] = [];
        fresh.on('response', (response) => {
          if (new URL(response.url()).pathname === '/api/v1/notes' && response.status() >= 400) failed.push(response.status());
        });
        await fresh.goto('/#/notes');
        await expect(fresh).toHaveURL(new RegExp(`#/notes\\?${kept.replace(/[?.]/g, '\\$&')}$`));
        await expect(fresh.getByTestId('no-notes').or(fresh.getByTestId('notes-list'))).toBeVisible(FIVE_SECONDS);
        await expect(fresh.getByTestId('notes-error')).toHaveCount(0);
        expect(failed).toEqual([]);
        expect(reads.count).toBe(1);
        const sent = new URLSearchParams(reads.queries[0]);
        const wanted = new URLSearchParams(kept);
        for (const name of ['task', 'initiative', 'resource', 'author']) expect(sent.get(name), name).toBe(wanted.get(name));
        await expect(filters(fresh).getByLabel('Author', { exact: true })).toHaveValue(author);
        await expect(filters(fresh).getByLabel('Initiative', { exact: true })).toHaveValue(wanted.get('initiative') ?? '');
        await expect(filters(fresh).getByLabel('Task', { exact: true })).toHaveValue(wanted.get('task') ?? '');
        expect((await saved(fresh)).query).toBe(kept);
        await fresh.close();
      }
      await api.dispose();
    });

    test('a link opened while a restored filter is still being checked wins: the late check rewrites nothing and reads nothing', async ({ page }) => {
      const ghost = `e2e-nv-race-ghost-${vp.name}`;
      const restored = `nv-race-restored-${vp.name}`;
      const linked = `nv-race-link-${vp.name}`;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, `initiative=${ghost}&author=${restored}`] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = notesReads(fresh);
      const failed: number[] = [];
      fresh.on('response', (response) => {
        if (new URL(response.url()).pathname === '/api/v1/notes' && response.status() >= 400) failed.push(response.status());
      });
      await fresh.goto('/#/notes');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      // The link arrives while the check of the restored filter is held: it is read at once.
      await fresh.evaluate((hash) => (window.location.hash = hash), `#/notes?author=${linked}`);
      await expect(filters(fresh).getByLabel('Author', { exact: true })).toHaveValue(linked, FIVE_SECONDS);
      await expect.poll(() => reads.count, FIVE_SECONDS).toBe(1);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500); // the late check's answer has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(new RegExp(`#/notes\\?author=${linked}$`));
      await expect(filters(fresh).getByLabel('Author', { exact: true })).toHaveValue(linked);
      await expect(filters(fresh).getByLabel('Initiative', { exact: true })).toHaveValue('');
      expect((await saved(fresh)).query).toBe(`author=${linked}`);
      // Only the link's query was ever read: never the restored author, never the unknown initiative.
      expect(reads.queries.map((q) => new URLSearchParams(q).get('author'))).toEqual([linked]);
      expect(reads.queries.every((q) => !new URLSearchParams(q).has('initiative'))).toBe(true);
      expect(failed).toEqual([]);
      await expect(fresh.getByTestId('notes-error')).toHaveCount(0);
      await fresh.close();
    });

    test('leaving for another view while a restored filter is still being checked: the departed Notes rewrites nothing and reads nothing', async ({ page }) => {
      const ghost = `e2e-nv-depart-ghost-${vp.name}`;
      const restored = `nv-depart-restored-${vp.name}`;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, `initiative=${ghost}&author=${restored}`] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = notesReads(fresh);
      await fresh.goto('/#/notes');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      // A link to the Timeline with its own query arrives while the Notes check is held.
      await fresh.evaluate((hash) => (window.location.hash = hash), '#/timeline?actor=playwright');
      await expect(fresh.getByTestId('timeline-view')).toBeVisible(FIVE_SECONDS);
      await expect(fresh.getByTestId('notes-view')).toHaveCount(0);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500); // the departed view's late check has had time to (wrongly) apply itself
      await expect(fresh).toHaveURL(/#\/timeline\?actor=playwright$/);
      await expect(fresh.getByTestId('timeline-view')).toBeVisible();
      expect(JSON.parse((await fresh.evaluate(() => localStorage.getItem('vh.view.v1.timeline'))) as string).query).toBe('actor=playwright');
      // The departed Notes never read: not under the restored filter, not under what its check kept.
      expect(reads.queries).toEqual([]);
      await fresh.close();
    });

    test('leaving for a bare view while a restored filter is still being checked: the late check neither drags the URL back nor reads', async ({ page }) => {
      const ghost = `e2e-nv-depart-bare-${vp.name}`;
      const restored = `nv-depart-bare-${vp.name}`;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, `initiative=${ghost}&task=${ghost}-7&author=${restored}`] as const);

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = notesReads(fresh);
      await fresh.goto('/#/notes');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      await goTo(fresh, 'Initiatives');
      await expect(fresh).toHaveURL(/#\/initiatives$/);
      await expect(fresh.getByTestId('notes-view')).toHaveCount(0, FIVE_SECONDS);
      // The Initiatives list (status=open) is read page by page, many pages on a long run's board: wait for its last page
      // itself, then give the rows their usual bound to draw.
      const list = fresh.waitForResponse(async (response) => {
        const url = new URL(response.url());
        if (url.pathname !== '/api/v1/initiatives' || url.searchParams.get('status') !== 'open') return false;
        const body = (await response.json().catch(() => null)) as { next_after?: string } | null;
        return body !== null && body.next_after === undefined;
      });
      lookups.release(); // the Initiatives list waits on the same held lookup, so it shows only after the release
      await list;
      await expect(fresh.getByTestId('initiative-row').first()).toBeVisible(FIVE_SECONDS);
      await fresh.waitForTimeout(500);
      await expect(fresh).toHaveURL(/#\/initiatives$/);
      expect(reads.queries).toEqual([]);
      await fresh.close();
    });

    test('filters applied while a restored filter is still being checked win: the late check rewrites nothing and reads nothing', async ({ page }) => {
      const ghost = `e2e-nv-race-apply-ghost-${vp.name}`;
      const restored = `nv-race-apply-restored-${vp.name}`;
      const chosen = `nv-race-apply-chosen-${vp.name}`;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await page.evaluate(
        ([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })),
        [KEY, `task=${ghost}-1&initiative=${ghost}&author=${restored}`] as const,
      );

      const fresh = await page.context().newPage();
      const lookups = await holdLookups(fresh);
      const reads = notesReads(fresh);
      await fresh.goto('/#/notes');
      await expect.poll(() => lookups.held()).toBeGreaterThan(0);
      // The user clears the restored values by hand and applies another author while the check is held.
      const form = filters(fresh);
      await form.getByLabel('Task', { exact: true }).fill('');
      await form.getByLabel('Initiative', { exact: true }).fill('');
      await form.getByLabel('Author', { exact: true }).fill(chosen);
      await fresh.getByTestId('apply-filters').click();
      await expect(fresh).toHaveURL(new RegExp(`#/notes\\?author=${chosen}$`));
      await expect.poll(() => reads.count, FIVE_SECONDS).toBe(1);
      lookups.release();
      await lookups.answered;
      await fresh.waitForTimeout(500);
      await expect(fresh).toHaveURL(new RegExp(`#/notes\\?author=${chosen}$`));
      await expect(form.getByLabel('Author', { exact: true })).toHaveValue(chosen);
      expect((await saved(fresh)).query).toBe(`author=${chosen}`);
      expect(reads.queries.map((q) => new URLSearchParams(q).get('author'))).toEqual([chosen]);
      await expect(fresh.getByTestId('notes-error')).toHaveCount(0);
      await fresh.close();
    });

    test('a saved filter that names only references the board no longer has opens at the defaults with one unfiltered read and saves nothing', async ({ page }) => {
      const ghost = `e2e-nv-all-gone-${vp.name}`;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, `task=${ghost}-1&initiative=${ghost}`] as const);

      const fresh = await page.context().newPage();
      const reads = notesReads(fresh);
      const failed: number[] = [];
      fresh.on('response', (response) => {
        if (new URL(response.url()).pathname === '/api/v1/notes' && response.status() >= 400) failed.push(response.status());
      });
      await fresh.goto('/#/notes');
      await expect(fresh.getByTestId('no-notes').or(fresh.getByTestId('notes-list'))).toBeVisible(FIVE_SECONDS);
      await expect(fresh).toHaveURL(/#\/notes$/);
      await expect(filters(fresh).getByLabel('Initiative', { exact: true })).toHaveValue('');
      await expect(filters(fresh).getByLabel('Task', { exact: true })).toHaveValue('');
      await expect(filters(fresh).getByTestId('clear-filters')).toHaveCount(0);
      await expect(fresh.getByTestId('notes-error')).toHaveCount(0);
      expect(reads.count).toBe(1);
      expect(reads.queries[0]).not.toMatch(/initiative=|task=/);
      expect(failed).toEqual([]);
      // Nothing is left to restore: the dropped references are not saved back.
      expect(await stored(fresh)).toBeNull();
      await fresh.close();
    });

    test('a restored task whose lookup fails is kept while a removed initiative beside it is still dropped', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-nv-lookup-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Notes lookup ${vp.name}` }, 201);
      const created = await api.post('/api/v1/tasks', { data: { initiative: slug, title: 'Lookup task' } });
      expect(created.status()).toBe(201);
      const ref = ((await created.json()) as { ref: string }).ref;
      const ghost = `e2e-nv-lookup-ghost-${vp.name}`;
      const author = `nv-lookup-${vp.name}`;
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await page.evaluate(([k, q]) => localStorage.setItem(k, JSON.stringify({ v: 1, query: q })), [KEY, `task=${ref}&initiative=${ghost}&author=${author}`] as const);

      const fresh = await page.context().newPage();
      // The board cannot tell whether the task exists: its lookup answers 500.
      await fresh.route(
        (url) => url.pathname.startsWith('/api/v1/tasks/'),
        (route) => (route.request().method() === 'GET' ? route.fulfill({ status: 500, contentType: 'application/problem+json', body: '{"title":"down","status":500}' }) : route.continue()),
      );
      const reads = notesReads(fresh);
      await fresh.goto('/#/notes');
      const kept = `task=${encodeURIComponent(ref)}&author=${author}`;
      await expect(fresh).toHaveURL(new RegExp(`#/notes\\?${kept.replace(/[?.]/g, '\\$&')}$`), FIVE_SECONDS);
      await expect(filters(fresh).getByLabel('Task', { exact: true })).toHaveValue(ref);
      await expect(filters(fresh).getByLabel('Initiative', { exact: true })).toHaveValue('');
      await expect.poll(() => reads.count, FIVE_SECONDS).toBe(1);
      const sent = new URLSearchParams(reads.queries[0]);
      expect(sent.get('task')).toBe(ref);
      expect(sent.has('initiative')).toBe(false);
      expect(sent.get('author')).toBe(author);
      expect((await saved(fresh)).query).toBe(kept);
      await fresh.close();
      await api.dispose();
    });

    test('a link that names an unknown initiative is not rewritten as restored state', async ({ page }) => {
      const ghost = `e2e-nv-link-ghost-${vp.name}`;
      await page.goto(`/#/notes?initiative=${ghost}`);
      await expect(page).toHaveURL(new RegExp(`#/notes\\?initiative=${ghost}$`));
      await expect(filters(page).getByLabel('Initiative', { exact: true })).toHaveValue(ghost);
      await expect(page.getByTestId('notes-error')).toBeVisible(FIVE_SECONDS);
      expect(await saved(page)).toEqual({ v: 1, query: `initiative=${ghost}` });
    });

    test('a fragment with its own query overrides the saved one, and Clear filters then nav away and back shows the defaults', async ({ page }) => {
      await page.goto('/#/notes');
      await expect(page.getByTestId('notes-view')).toBeVisible(FIVE_SECONDS);
      await apply(page, { author: `nv-own-a-${vp.name}` });
      expect(await saved(page)).toEqual({ v: 1, query: `author=nv-own-a-${vp.name}` });

      // A link with its own query wins and replaces the saved one.
      await page.goto(`/#/notes?author=nv-own-b-${vp.name}`);
      await expect(filters(page).getByLabel('Author', { exact: true })).toHaveValue(`nv-own-b-${vp.name}`, FIVE_SECONDS);
      expect(await saved(page)).toEqual({ v: 1, query: `author=nv-own-b-${vp.name}` });

      // Clear filters: the defaults at once, nothing saved, and the defaults again after nav away and back.
      await filters(page).getByTestId('clear-filters').click();
      await expect(page).toHaveURL(/#\/notes$/);
      await expect(filters(page).getByLabel('Author', { exact: true })).toHaveValue('');
      await expect(filters(page).getByTestId('clear-filters')).toHaveCount(0);
      expect(await stored(page)).toBeNull();
      await goTo(page, 'Initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await goTo(page, 'Notes');
      await expect(page.getByTestId('notes-view')).toBeVisible();
      await expect(page).toHaveURL(/#\/notes$/);
      await expect(filters(page).getByLabel('Author', { exact: true })).toHaveValue('');
      await expect(filters(page).getByTestId('clear-filters')).toHaveCount(0);
    });
  });
}
