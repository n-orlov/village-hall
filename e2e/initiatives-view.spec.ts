import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Initiatives view's sort and saved view state (PRD R16, R7, SPEC 11): the table sorts by Title (text), Status (status),
// Tags (text), Tasks (number) and Created (time) through the shared sort control (a select at every width, with the keyboard); the sort rides in the
// fragment query next to the status filter; the whole query is saved under `vh.view.v1.initiatives` and restored on a
// bare `#/initiatives` with one fetch; a fragment with its own query wins; Clear filters forgets it all. The board is shared
// with every other spec, so each test seeds initiatives of its own and reads only those rows.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.initiatives';

async function post(api: APIRequestContext, path: string, data: object, status: number) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

interface Seed {
  prefix: string;
  /** The slugs in the order they were created: Mango, apple, Zebra (closed), cherry. */
  slugs: string[];
}

// Four initiatives, created one after the other (so their creation times rise in this order), titled so that no sort by title,
// status or creation time gives the creation order, and the third one closed.
async function seed(api: APIRequestContext, prefix: string): Promise<Seed> {
  const titles = ['Mango', 'apple', 'Zebra', 'cherry'];
  const slugs = titles.map((title) => `${prefix}-${title.toLowerCase()}`);
  for (const [i, title] of titles.entries()) await post(api, '/api/v1/initiatives', { slug: slugs[i], title }, 201);
  await post(api, `/api/v1/initiatives/${slugs[2]}/close`, {}, 200);
  return { prefix, slugs };
}

/** The seeded slugs the table shows, in table order: every other spec's initiatives are left out. */
const ownRows = async (page: Page, prefix: string) =>
  (await page.getByTestId('initiative-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-slug') ?? ''))).filter((slug) => slug.startsWith(`${prefix}-`));

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
const goTo = async (page: Page, label: string) => {
  await nav(page).getByRole('link', { name: new RegExp(`^${label}`) }).click();
};
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);
const saved = async (page: Page) => JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { v: number; query: string };

// The list fetches a page sends: the first page of each (a long list on the shared board is read in further pages, which are the same fetch).
function listReads(page: Page) {
  const reads = { count: 0 };
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.pathname === '/api/v1/initiatives' && !url.searchParams.has('after') && request.method() === 'GET') reads.count++;
  });
  return reads;
}

// The shared sort control is one select at every width; `phone` stays in the signature so the tests read as before.
async function sortBy(page: Page, _phone: boolean, column: string, dir: 'asc' | 'desc' | null) {
  await page.getByTestId('sort-select').selectOption(dir === null ? '' : `${column}:${dir}`);
}

for (const vp of viewports) {
  const phone = vp.name === 'phone';
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('sorting by Tags and by Tasks orders the rows (text, number), empty tags last in both directions, and the sort survives a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const prefix = `e2e-iv-cols-${vp.name}`;
      // Creation order: plain (no tags, no tasks), big (tags "zeta", 10 tasks), mid (tags "Alpha", 9 tasks), few (tags "mid", 2 tasks).
      const defs = [
        { key: 'plain', tags: [] as string[], tasks: 0 },
        { key: 'big', tags: ['zeta'], tasks: 10 },
        { key: 'mid', tags: ['Alpha'], tasks: 9 },
        { key: 'few', tags: ['mid'], tasks: 2 },
      ];
      const slug = (key: string) => `${prefix}-${key}`;
      for (const d of defs) {
        await post(api, '/api/v1/initiatives', { slug: slug(d.key), title: `Cols ${d.key}`, tags: d.tags }, 201);
        for (let i = 0; i < d.tasks; i++) await post(api, '/api/v1/tasks', { initiative: slug(d.key), title: `${d.key} ${i}` }, 201);
      }
      const [plain, big, mid, few] = defs.map((d) => slug(d.key));

      await page.goto('/#/initiatives?status=all');
      await expect.poll(() => ownRows(page, prefix)).toHaveLength(4);
      for (const column of ['tags', 'tasks']) await expect(page.getByTestId('sort-select')).not.toHaveValue(new RegExp(`^${column}:`));

      // Tags is text, case-insensitive; the untagged initiative sits last whichever the direction.
      await sortBy(page, phone, 'tags', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('tags:asc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([mid, few, big, plain]);
      await expect(page).toHaveURL(endsWith('#/initiatives?status=all&sort=tags&dir=asc'));
      await sortBy(page, phone, 'tags', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('tags:desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([big, few, mid, plain]);

      // Tasks is a number: 9 sorts before 10, an initiative with no tasks first ascending and last descending.
      await sortBy(page, phone, 'tasks', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('tasks:asc');
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^tags:/);
      await expect.poll(() => ownRows(page, prefix)).toEqual([plain, few, mid, big]);
      await sortBy(page, phone, 'tasks', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('tasks:desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([big, mid, few, plain]);
      expect((await saved(page)).query).toBe('status=all&sort=tasks&dir=desc');

      await page.reload();
      await expect.poll(() => ownRows(page, prefix)).toEqual([big, mid, few, plain]);
      await expect(page.getByTestId('sort-select')).toHaveValue('tasks:desc');
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('tasks:desc');
      await sortBy(page, phone, 'tasks', null);
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^tasks:/);
      await api.dispose();
    });

    test('Tags sorts by the whole tag list and Tasks by the total, equal values keep the default order both ways, and a saved Tags or Tasks sort is restored on a bare link', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const prefix = `e2e-iv-ties-${vp.name}`;
      // Creation order: two (tags alpha+beta, 1 task), three (tags alpha+gamma, 1 task), one (tags alpha, 1 task), same (tags alpha, 1 task).
      const defs = [
        { key: 'two', tags: ['alpha', 'beta'] },
        { key: 'three', tags: ['alpha', 'gamma'] },
        { key: 'one', tags: ['alpha'] },
        { key: 'same', tags: ['alpha'] },
      ];
      const slug = (key: string) => `${prefix}-${key}`;
      for (const d of defs) {
        await post(api, '/api/v1/initiatives', { slug: slug(d.key), title: `Ties ${d.key}`, tags: d.tags }, 201);
        await post(api, '/api/v1/tasks', { initiative: slug(d.key), title: `${d.key} task` }, 201);
      }
      const [two, three, one, same] = defs.map((d) => slug(d.key));

      await page.goto('/#/initiatives?status=all');
      await expect.poll(() => ownRows(page, prefix)).toHaveLength(4);
      const byDefault = await ownRows(page, prefix);
      const inDefaultOrder = (slugs: string[]) => byDefault.filter((s) => slugs.includes(s));

      // Tags compares the whole list as one text: "alpha" < "alpha, beta" < "alpha, gamma"; the two with equal tags keep the default order.
      await sortBy(page, phone, 'tags', 'asc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([...inDefaultOrder([one, same]), two, three]);
      await sortBy(page, phone, 'tags', 'desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([three, two, ...inDefaultOrder([one, same])]);

      // Tasks: every initiative has one task, so either direction keeps the default order.
      await sortBy(page, phone, 'tasks', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('tasks:asc');
      await expect.poll(() => ownRows(page, prefix)).toEqual(byDefault);
      await sortBy(page, phone, 'tasks', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('tasks:desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual(byDefault);

      // A saved Tags sort and a saved Tasks sort each come back on a bare #/initiatives after a visit to Tasks.
      for (const [column, dir] of [['tags', 'desc'], ['tasks', 'asc']] as const) {
        await sortBy(page, phone, column, dir);
        const query = `status=all&sort=${column}&dir=${dir}`;
        expect((await saved(page)).query).toBe(query);
        await goTo(page, 'Tasks');
        await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
        await goTo(page, 'Initiatives');
        await expect(page.getByTestId('initiative-row').first()).toBeVisible();
        await expect(page).toHaveURL(endsWith(`#/initiatives?${query}`));
        await expect(page.getByTestId('sort-select')).toHaveValue(`${column}:${dir}`);
      }
      await api.dispose();
    });

    test('sorting by one column of each type (text, status, time) orders the rows and shows it in the shared sort control, and the third step is the default', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { prefix, slugs } = await seed(api, `e2e-iv-sort-${vp.name}`);
      const [mango, apple, zebra, cherry] = slugs;

      await page.goto('/#/initiatives?status=all');
      await expect.poll(() => ownRows(page, prefix)).toHaveLength(4);
      const byDefault = await ownRows(page, prefix);
      await expect(page.getByTestId('sort-select')).toBeVisible();
      for (const column of ['title', 'status', 'tags', 'tasks', 'created']) await expect(page.getByTestId('sort-select')).not.toHaveValue(new RegExp(`^${column}:`));

      // text: title ascending ignores case, descending reverses it.
      await sortBy(page, phone, 'title', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('title:asc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([apple, cherry, mango, zebra]);
      await expect(page).toHaveURL(endsWith('#/initiatives?status=all&sort=title&dir=asc'));
      await sortBy(page, phone, 'title', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('title:desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([zebra, mango, cherry, apple]);

      // status: open before closed ascending (ties keep the default order), closed first descending.
      await sortBy(page, phone, 'status', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('status:asc');
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^title:/);
      await expect.poll(() => ownRows(page, prefix)).toEqual([...byDefault.filter((s) => s !== zebra), zebra]);
      await sortBy(page, phone, 'status', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('status:desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([zebra, ...byDefault.filter((s) => s !== zebra)]);

      // time: the creation order ascending, the reverse descending.
      await sortBy(page, phone, 'created', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('created:asc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([mango, apple, zebra, cherry]);
      await sortBy(page, phone, 'created', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('created:desc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([cherry, zebra, apple, mango]);
      await expect(page).toHaveURL(endsWith('#/initiatives?status=all&sort=created&dir=desc'));

      // Third step: back to the default order, and the sort is gone from the fragment and from the saved query.
      await sortBy(page, phone, 'created', null);
      await expect(page.getByTestId('sort-select')).not.toHaveValue(/^created:/);
      await expect.poll(() => ownRows(page, prefix)).toEqual(byDefault);
      await expect(page).toHaveURL(endsWith('#/initiatives?status=all'));
      expect((await saved(page)).query).toBe('status=all');
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('');
      await api.dispose();
    });

    test('the status filter and a sort survive Tasks and back through the nav, and a new page, with one fetch each', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { prefix, slugs } = await seed(api, `e2e-iv-restore-${vp.name}`);
      const [mango, apple, zebra, cherry] = slugs;

      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-filter')).toHaveValue('open');
      await page.getByTestId('initiative-filter').selectOption('all');
      await expect.poll(() => ownRows(page, prefix)).toHaveLength(4);
      await sortBy(page, phone, 'title', 'desc');
      const expected = [zebra, mango, cherry, apple];
      await expect.poll(() => ownRows(page, prefix)).toEqual(expected);
      const query = 'status=all&sort=title&dir=desc';
      await expect(page).toHaveURL(endsWith(`#/initiatives?${query}`));
      expect(await saved(page)).toEqual({ v: 1, query });

      await goTo(page, 'Tasks');
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      const reads = listReads(page);
      await goTo(page, 'Initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await expect.poll(() => ownRows(page, prefix)).toEqual(expected);
      await expect(page).toHaveURL(endsWith(`#/initiatives?${query}`));
      await expect(page.getByTestId('initiative-filter')).toHaveValue('all');
      await expect(page.getByTestId('sort-select')).toHaveValue('title:desc');
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('title:desc');
      expect(reads.count).toBe(1);

      // A new page of the same context opens `#/initiatives` bare and gets the same state.
      const second = await page.context().newPage();
      const secondReads = listReads(second);
      await second.goto('/#/initiatives');
      await expect(second.getByTestId('initiative-row').first()).toBeVisible();
      await expect.poll(() => ownRows(second, prefix)).toEqual(expected);
      await expect(second).toHaveURL(endsWith(`#/initiatives?${query}`));
      await expect(second.getByTestId('initiative-filter')).toHaveValue('all');
      await expect(second.getByTestId('sort-select')).toHaveValue('title:desc');
      expect(secondReads.count).toBe(1);
      await second.close();
      await api.dispose();
    });

    test('a fragment with its own query overrides the saved one, and Clear filters then nav away and back shows the defaults', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const { prefix, slugs } = await seed(api, `e2e-iv-own-${vp.name}`);
      const [mango, apple, zebra, cherry] = slugs;

      await page.goto('/#/initiatives?status=all&sort=title&dir=asc');
      await expect.poll(() => ownRows(page, prefix)).toEqual([apple, cherry, mango, zebra]);

      // A link with its own query wins and is what is saved from now on.
      const link = await page.context().newPage();
      await link.goto('/#/initiatives?status=closed&sort=created&dir=desc');
      await expect(link.getByTestId('initiative-filter')).toHaveValue('closed');
      await expect(link.getByTestId('sort-select')).toHaveValue('created:desc');
      await expect.poll(() => ownRows(link, prefix)).toEqual([zebra]);
      expect((await saved(link)).query).toBe('status=closed&sort=created&dir=desc');

      await goTo(link, 'Tasks');
      await expect(link.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await goTo(link, 'Initiatives');
      await expect(link.getByTestId('initiative-filter')).toHaveValue('closed');
      await expect(link.getByTestId('sort-select')).toHaveValue('created:desc');
      await expect.poll(() => ownRows(link, prefix)).toEqual([zebra]);

      // Clear filters: the defaults at once, nothing saved, and the defaults again after nav away and back.
      await link.getByTestId('clear-filters').click();
      await expect(link).toHaveURL(/#\/initiatives$/);
      await expect(link.getByTestId('initiative-filter')).toHaveValue('open');
      await expect(link.getByTestId('sort-select')).not.toHaveValue(/^created:/);
      await expect(link.getByTestId('clear-filters')).toHaveCount(0);
      expect(await link.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await expect.poll(() => ownRows(link, prefix)).toHaveLength(3);
      await goTo(link, 'Tasks');
      await expect(link.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await goTo(link, 'Initiatives');
      await expect(link.getByTestId('initiative-row').first()).toBeVisible();
      await expect(link).toHaveURL(/#\/initiatives$/);
      await expect(link.getByTestId('initiative-filter')).toHaveValue('open');
      await expect(link.getByTestId('sort-select')).not.toHaveValue(/^created:/);
      if (phone) await expect(link.getByTestId('sort-select')).toHaveValue('');
      expect(await link.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
      await link.close();
      await api.dispose();
    });

    test('a saved query with an unknown status or column opens at the defaults', async ({ page }) => {
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, 'status=bogus&sort=bogus&dir=asc'] as const);
      const fresh = await page.context().newPage();
      await fresh.goto('/#/initiatives');
      await expect(fresh.getByTestId('initiative-row').first()).toBeVisible();
      await expect(fresh.getByTestId('initiative-filter')).toHaveValue('open');
      for (const column of ['title', 'status', 'tags', 'tasks', 'created']) await expect(fresh.getByTestId('sort-select')).not.toHaveValue(new RegExp(`^${column}:`));
      await fresh.close();
    });

    {
      test('the shared sort control sorts from the keyboard: the arrow keys step through the options', async ({ page, playwright, baseURL }) => {
        const api = await apiContext(playwright, baseURL);
        const { prefix, slugs } = await seed(api, `e2e-iv-keys-${vp.name}`);
        const [mango, apple, zebra, cherry] = slugs;
        await page.goto('/#/initiatives?status=all');
        await expect.poll(() => ownRows(page, prefix)).toHaveLength(4);
        const byDefault = await ownRows(page, prefix);

        const select = page.getByTestId('sort-select');
        await select.focus();
        await expect(select).toBeFocused();
        for (let i = 0; i < 1; i++) await page.keyboard.press('ArrowDown'); // Default order -> Title (ascending)
        await expect(page.getByTestId('sort-select')).toHaveValue('title:asc');
        await expect.poll(() => ownRows(page, prefix)).toEqual([apple, cherry, mango, zebra]);
        await expect(select).toBeFocused();
        await page.keyboard.press('ArrowDown'); // -> Title (descending)
        await expect(page.getByTestId('sort-select')).toHaveValue('title:desc');
        await expect.poll(() => ownRows(page, prefix)).toEqual([zebra, mango, cherry, apple]);
        await page.keyboard.press('Home'); // back to the default order
        await expect(page.getByTestId('sort-select')).not.toHaveValue(/^title:/);
        await expect.poll(() => ownRows(page, prefix)).toEqual(byDefault);
        await api.dispose();
      });
    }
  });
}
