import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Actors sort and saved view state (PRD R16, R7, SPEC 11): the rows sort by Actor (text), Live leases (ref, the
// actor's first live lease), Last seen (time) and Way back (text) through the shared sort control (a select at every width); the sort rides in the fragment query and is saved under `vh.view.v1.actors`, so it survives nav away and back.
// The table has no status or kind column. The server is shared with every other spec, so each test reads only the rows of
// the three actors it seeded.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.actors';

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const actorApi = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, handle: string) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': handle } });

interface Seed {
  prefix: string;
  /** The three actors in the order they were last seen (oldest first): mike and zed hold a lease (mike's is the older ref), amy holds none. */
  mike: string;
  zed: string;
  amy: string;
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const prefix = `actsort-${name}-`;
  const slug = `e2e-actsort-${name}`;
  const [mike, zed, amy] = [`${prefix}mike`, `${prefix}zed`, `${prefix}amy`];
  const holder = async (handle: string, title: string) => {
    const api = await actorApi(playwright, baseURL, handle);
    if (handle === mike) await post(api, '/api/v1/initiatives', { slug, title: `Actors sort ${name}` }, 201);
    const task = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title }, 201)).ref;
    await post(api, '/api/v1/leases', { task, intent: `edit ${title}`, resources: [`repo:${prefix}${title}`] }, 201);
    await api.dispose();
  };
  await holder(mike, 'first');
  await holder(zed, 'second');
  const idle = await actorApi(playwright, baseURL, amy);
  await post(idle, '/api/v1/notes', { summary: 'The idle actor was here' }, 201);
  await idle.dispose();
  return { prefix, mike, zed, amy };
}

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);
const saved = async (page: Page) => JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { v: number; query: string };
const stored = (page: Page) => page.evaluate((k) => localStorage.getItem(k), KEY);

// The seeded actors' handles in the order the table shows them.
const order = (page: Page, prefix: string) =>
  page.locator(`[data-testid=actor-row][data-handle^="${prefix}"]`).evaluateAll((rows) => rows.map((row) => row.getAttribute('data-handle')));

async function goTo(page: Page, label: string) {
  const more = page.getByRole('button', { name: 'More' });
  const link = nav(page).getByRole('link', { name: new RegExp(`^${label}`) });
  if (await link.isVisible()) {
    await link.click();
  } else {
    await more.click();
    await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: new RegExp(`^${label}`) }).click();
  }
}

// The shared sort control is one select at every width: `column:dir`, or the empty value for the default order.
async function sortBy(page: Page, _phone: boolean, column: string, dir: 'asc' | 'desc' | null) {
  await page.getByTestId('sort-select').selectOption(dir === null ? '' : `${column}:${dir}`);
}

for (const vp of viewports) {
  const phone = vp.name === 'phone';
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('sorting by one column of each type (text, ref, time) orders the rows through the shared sort control, and the third step is the default', async ({ page, playwright, baseURL }) => {
      const { prefix, mike, zed, amy } = await seed(playwright, baseURL, `sort-${vp.name}`);
      await page.goto('/#/actors');
      // The default order: the holders first, the newest first, then the idle actor.
      await expect.poll(() => order(page, prefix)).toEqual([zed, mike, amy]);
      await expect(page.getByTestId('sort-select')).toBeVisible();
      await expect(page.locator('th [data-testid^="sort-"]')).toHaveCount(0); // the headers are plain: the control is the one sort
      for (const column of ['actor', 'leases', 'seen', 'wayBack']) await expect(page.getByTestId('sort-select')).toHaveValue('');

      // text: the handle, ascending and descending.
      await sortBy(page, phone, 'actor', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('actor:asc');
      await expect.poll(() => order(page, prefix)).toEqual([amy, mike, zed]);
      await sortBy(page, phone, 'actor', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('actor:desc');
      await expect.poll(() => order(page, prefix)).toEqual([zed, mike, amy]);
      await expect(page).toHaveURL(endsWith('#/actors?sort=actor&dir=desc'));

      // ref: the first live lease by its number, the actor without one last in both directions.
      await sortBy(page, phone, 'leases', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('leases:asc');
            await expect.poll(() => order(page, prefix)).toEqual([mike, zed, amy]);
      await sortBy(page, phone, 'leases', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('leases:desc');
      await expect.poll(() => order(page, prefix)).toEqual([zed, mike, amy]);

      // time: last seen, oldest first ascending, newest first descending.
      await sortBy(page, phone, 'seen', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('seen:asc');
      await expect.poll(() => order(page, prefix)).toEqual([mike, zed, amy]);
      await sortBy(page, phone, 'seen', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('seen:desc');
      await expect.poll(() => order(page, prefix)).toEqual([amy, zed, mike]);
      await expect(page).toHaveURL(endsWith('#/actors?sort=seen&dir=desc'));
      expect(await saved(page)).toEqual({ v: 1, query: 'sort=seen&dir=desc' });

      // Third step: the default order, gone from the fragment and from the saved state.
      await sortBy(page, phone, 'seen', null);
      await expect(page.getByTestId('sort-select')).toHaveValue('');
      await expect.poll(() => order(page, prefix)).toEqual([zed, mike, amy]);
      await expect(page).toHaveURL(endsWith('#/actors'));
      expect(await stored(page)).toBeNull();
      await expect(page.getByTestId('sort-select')).toHaveValue('');
    });

    // Way back used to be the one data column of this table with no sort.
    test('sorting by Way back orders the rows as text with the actor that has none last in both directions, and the sort survives a reload', async ({ page, playwright, baseURL }) => {
      const prefix = `actway-${vp.name}-`;
      const [bee, cat, none] = [`${prefix}bee`, `${prefix}cat`, `${prefix}none`];
      const as = async (handle: string, wayBack?: string) => {
        const api = await playwright.request.newContext({
          baseURL,
          extraHTTPHeaders: {
            Authorization: `Bearer ${process.env.E2E_API_KEY}`,
            'Board-Actor': handle,
            ...(wayBack === undefined ? {} : { 'Board-Actor-Way-Back': wayBack }),
          },
        });
        await post(api, '/api/v1/notes', { summary: `${handle} was here` }, 201);
        await api.dispose();
      };
      // Seen oldest first: bee (way back "z..."), cat (way back "a..."), none, so no other sort gives the way-back order.
      await as(bee, `zeta resume ${bee}`);
      await as(cat, `Alpha resume ${cat}`);
      await as(none);

      await page.goto('/#/actors');
      await expect.poll(() => order(page, prefix)).toHaveLength(3);
      await expect(page.getByTestId('sort-select')).toHaveValue('');
      await sortBy(page, phone, 'wayBack', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('wayBack:asc');
      await expect.poll(() => order(page, prefix)).toEqual([cat, bee, none]);
      await expect(page).toHaveURL(endsWith('#/actors?sort=wayBack&dir=asc'));
      await sortBy(page, phone, 'wayBack', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('wayBack:desc');
      await expect.poll(() => order(page, prefix)).toEqual([bee, cat, none]);
      expect(await saved(page)).toEqual({ v: 1, query: 'sort=wayBack&dir=desc' });
      await page.reload();
      await expect.poll(() => order(page, prefix)).toEqual([bee, cat, none]);
      await expect(page.getByTestId('sort-select')).toHaveValue('wayBack:desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('wayBack:desc');
      await sortBy(page, phone, 'wayBack', null);
      await expect(page.getByTestId('sort-select')).toHaveValue('');
      expect(await stored(page)).toBeNull();
    });

    test('the sort survives nav away and back, a fresh page and a reload, and a fragment with its own query wins', async ({ page, playwright, baseURL }) => {
      const { prefix, mike, zed, amy } = await seed(playwright, baseURL, `keep-${vp.name}`);
      await page.goto('/#/actors');
      await expect.poll(() => order(page, prefix)).toEqual([zed, mike, amy]);
      await sortBy(page, phone, 'actor', 'asc');
      await expect.poll(() => order(page, prefix)).toEqual([amy, mike, zed]);
      expect(await saved(page)).toEqual({ v: 1, query: 'sort=actor&dir=asc' });

      // Nav away and back: the nav link has no query, and the view opens as it was left.
      await goTo(page, 'Tasks');
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await expect(page.getByTestId('actors-view')).toHaveCount(0);
      await goTo(page, 'Actors');
      await expect(page.getByTestId('actors-view')).toBeVisible();
      await expect.poll(() => order(page, prefix)).toEqual([amy, mike, zed]);
      await expect(page).toHaveURL(endsWith('#/actors?sort=actor&dir=asc'));
      await expect(page.getByTestId('sort-select')).toHaveValue('actor:asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('actor:asc');

      // A change after coming back is what is saved, and a new page of the same context opens the bare route with it.
      await sortBy(page, phone, 'seen', 'desc');
      await expect.poll(() => order(page, prefix)).toEqual([amy, zed, mike]);
      const second = await page.context().newPage();
      await second.goto('/#/actors');
      await expect.poll(() => order(second, prefix)).toEqual([amy, zed, mike]);
      await expect(second).toHaveURL(endsWith('#/actors?sort=seen&dir=desc'));
      await expect(second.getByTestId('sort-select')).toHaveValue('seen:desc');

      // A reload of the fragment keeps the sort, and a link with its own query wins and is saved.
      await second.reload();
      await expect.poll(() => order(second, prefix)).toEqual([amy, zed, mike]);
      await second.goto('/#/actors?sort=leases&dir=asc');
      await expect.poll(() => order(second, prefix)).toEqual([mike, zed, amy]);
      await expect(second.getByTestId('sort-select')).toHaveValue('leases:asc');
      expect(await saved(second)).toEqual({ v: 1, query: 'sort=leases&dir=asc' });
      await second.close();
    });

    test('a saved query with an unknown column opens the default order', async ({ page, playwright, baseURL }) => {
      const { prefix, mike, zed, amy } = await seed(playwright, baseURL, `bad-${vp.name}`);
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, 'sort=way&dir=asc'] as const);
      const fresh = await page.context().newPage();
      await fresh.goto('/#/actors');
      await expect.poll(() => order(fresh, prefix)).toEqual([zed, mike, amy]);
      for (const column of ['actor', 'leases', 'seen', 'wayBack']) await expect(fresh.getByTestId('sort-select')).toHaveValue('');
      await fresh.close();
    });

    test('sorting by Live leases uses the first lease only and keeps the actors without a lease last in both directions in the order the view draws them', async ({ page, playwright, baseURL }) => {
      const prefix = `actfirst-${vp.name}-`;
      const slug = `e2e-actfirst-${vp.name}`;
      const [amy, bob, mike, zed] = [`${prefix}amy`, `${prefix}bob`, `${prefix}mike`, `${prefix}zed`];
      // Seen oldest first: amy, bob (no lease), then mike and zed.
      for (const handle of [amy, bob]) {
        const api = await actorApi(playwright, baseURL, handle);
        await post(api, '/api/v1/notes', { summary: `${handle} was here` }, 201);
        await api.dispose();
      }
      const mikeApi = await actorApi(playwright, baseURL, mike);
      const zedApi = await actorApi(playwright, baseURL, zed);
      await post(mikeApi, '/api/v1/initiatives', { slug, title: `Actors first lease ${vp.name}` }, 201);
      const task = async (title: string) => (await post<{ ref: string }>(mikeApi, '/api/v1/tasks', { initiative: slug, title }, 201)).ref;
      const [t1, t2, t3] = [await task('one'), await task('two'), await task('three')];
      // Lease order by ref: mike's first, zed's, mike's second. Only mike's first lease may decide his place.
      await post(mikeApi, '/api/v1/leases', { task: t1, intent: 'first', resources: [`repo:${prefix}1`] }, 201);
      await post(zedApi, '/api/v1/leases', { task: t2, intent: 'second', resources: [`repo:${prefix}2`] }, 201);
      await post(mikeApi, '/api/v1/leases', { task: t3, intent: 'third', resources: [`repo:${prefix}3`] }, 201);
      await mikeApi.dispose();
      await zedApi.dispose();

      await page.goto('/#/actors');
      // The default order: holders first by last seen newest first (mike acted last), then the idle actors newest first.
      await expect.poll(() => order(page, prefix)).toEqual([mike, zed, bob, amy]);
      await sortBy(page, phone, 'leases', 'asc');
      await expect(page.getByTestId('sort-select')).toHaveValue('leases:asc');
      await expect.poll(() => order(page, prefix)).toEqual([mike, zed, bob, amy]);
      await sortBy(page, phone, 'leases', 'desc');
      await expect(page.getByTestId('sort-select')).toHaveValue('leases:desc');
      await expect.poll(() => order(page, prefix)).toEqual([zed, mike, bob, amy]);
      await expect(page).toHaveURL(endsWith('#/actors?sort=leases&dir=desc'));
    });

    test('a saved Live leases or Way back sort opens the bare link after another view and a reload', async ({ page, playwright, baseURL }) => {
      for (const column of ['leases', 'wayBack'] as const) {
        const { prefix, mike, zed, amy } = await seed(playwright, baseURL, `col${column === 'leases' ? 'lease' : 'way'}-${vp.name}`);
        await page.goto('/#/actors');
        await expect.poll(() => order(page, prefix)).toEqual([zed, mike, amy]);
        await sortBy(page, phone, column, 'desc');
        await expect(page.getByTestId('sort-select')).toHaveValue(`${column}:desc`);
        expect(await saved(page)).toEqual({ v: 1, query: `sort=${column}&dir=desc` });
        await goTo(page, 'Tasks');
        await expect(page.getByTestId('actors-view')).toHaveCount(0);
        await goTo(page, 'Actors');
        await expect(page).toHaveURL(endsWith(`#/actors?sort=${column}&dir=desc`));
        await expect(page.getByTestId('sort-select')).toHaveValue(`${column}:desc`);
        await page.reload();
        await expect(page.getByTestId('sort-select')).toHaveValue(`${column}:desc`);
        await sortBy(page, phone, column, null);
        expect(await stored(page)).toBeNull();
      }
    });

  });
}
