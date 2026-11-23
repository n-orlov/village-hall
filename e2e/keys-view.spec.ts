import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The API keys table sort and saved view state (PRD R16, SPEC 11): the table sorts by Name (text), Status (status) and
// Expires (time) through the shared header (desktop, with the keyboard) and the Sort select (phone); the sort rides in the
// fragment query and is saved under `vh.view.v1.keys`, so it survives nav away and back. The table has no ref or kind
// column. The server is shared with every other spec (and holds the suite's own key), so each test seeds three keys of its
// own and reads only their rows.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.keys';

async function post(api: APIRequestContext, path: string, data: object | undefined, status: number) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

interface Seed {
  prefix: string;
  /** The three keys in the order the server lists them (creation order): mango expires in 10 days, apple in 5 and is revoked, zebra never expires. */
  mango: string;
  apple: string;
  zebra: string;
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const prefix = `ksort-${name}-`;
  const [mango, apple, zebra] = [`${prefix}mango`, `${prefix}apple`, `${prefix}zebra`];
  const api = await apiContext(playwright, baseURL);
  await post(api, '/api/v1/keys', { name: mango, expires_in: '10d' }, 201);
  await post(api, '/api/v1/keys', { name: apple, expires_in: '5d' }, 201);
  await post(api, '/api/v1/keys', { name: zebra }, 201);
  await post(api, `/api/v1/keys/${apple}/revoke`, undefined, 200);
  await api.dispose();
  return { prefix, mango, apple, zebra };
}

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);
const th = (page: Page, column: string): Locator => page.locator('th', { has: page.getByTestId(`sort-${column}`) });
const saved = async (page: Page) => JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { v: number; query: string };
const stored = (page: Page) => page.evaluate((k) => localStorage.getItem(k), KEY);

// The seeded keys' names in the order the table shows them.
const order = (page: Page, prefix: string) =>
  page.locator(`[data-testid=key-row][data-name^="${prefix}"]`).evaluateAll((rows) => rows.map((row) => row.getAttribute('data-name')));

async function goTo(page: Page, label: string) {
  const link = nav(page).getByRole('link', { name: new RegExp(`^${label}`) });
  if (await link.isVisible()) {
    await link.click();
  } else {
    await page.getByRole('button', { name: 'More' }).click();
    await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: new RegExp(`^${label}`) }).click();
  }
}

async function sortBy(page: Page, phone: boolean, column: string, dir: 'asc' | 'desc' | null) {
  if (phone) {
    await page.getByTestId('sort-select').selectOption(dir === null ? '' : `${column}:${dir}`);
  } else {
    // The header cycles none -> ascending -> descending -> none: click until the target is reached.
    const cycle = ['none', 'ascending', 'descending'];
    const now = cycle.indexOf((await th(page, column).getAttribute('aria-sort')) ?? 'none');
    const target = cycle.indexOf(dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : 'none');
    for (let i = 0; i < (target - now + 3) % 3; i++) await page.getByTestId(`sort-${column}`).click();
  }
}

const columns = ['name', 'prefix', 'status', 'created', 'used', 'expires'];

for (const vp of viewports) {
  const phone = vp.name === 'phone';
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('sorting by one column of each type (text, status, time) orders the rows and sets aria-sort, and the third step is the default', async ({ page, playwright, baseURL }) => {
      const { prefix, mango, apple, zebra } = await seed(playwright, baseURL, `sort-${vp.name}`);
      await page.goto('/#/settings/keys');
      // The default order is the server's: the keys in the order they were created.
      await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
      if (phone) await expect(page.getByTestId('sort-select')).toBeVisible();
      else await expect(page.getByTestId('sort-select')).toBeHidden();
      for (const column of columns) await expect(th(page, column)).toHaveAttribute('aria-sort', 'none');

      // text: the name, ascending and descending.
      await sortBy(page, phone, 'name', 'asc');
      await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'ascending');
      await expect.poll(() => order(page, prefix)).toEqual([apple, mango, zebra]);
      await sortBy(page, phone, 'name', 'desc');
      await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'descending');
      await expect.poll(() => order(page, prefix)).toEqual([zebra, mango, apple]);
      await expect(page).toHaveURL(endsWith('#/settings/keys?sort=name&dir=desc'));

      // status: active before revoked, the two active keys in the server's order.
      await sortBy(page, phone, 'status', 'asc');
      await expect(th(page, 'status')).toHaveAttribute('aria-sort', 'ascending');
      await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'none');
      await expect.poll(() => order(page, prefix)).toEqual([mango, zebra, apple]);
      await sortBy(page, phone, 'status', 'desc');
      await expect(th(page, 'status')).toHaveAttribute('aria-sort', 'descending');
      await expect.poll(() => order(page, prefix)).toEqual([apple, mango, zebra]);

      // time: Expires, soonest first ascending, the key that never expires last in both directions.
      await sortBy(page, phone, 'expires', 'asc');
      await expect(th(page, 'expires')).toHaveAttribute('aria-sort', 'ascending');
      await expect.poll(() => order(page, prefix)).toEqual([apple, mango, zebra]);
      await sortBy(page, phone, 'expires', 'desc');
      await expect(th(page, 'expires')).toHaveAttribute('aria-sort', 'descending');
      await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
      await expect(page).toHaveURL(endsWith('#/settings/keys?sort=expires&dir=desc'));
      expect(await saved(page)).toEqual({ v: 1, query: 'sort=expires&dir=desc' });

      // Third step: the default order, gone from the fragment and from the saved state.
      await sortBy(page, phone, 'expires', null);
      await expect(th(page, 'expires')).toHaveAttribute('aria-sort', 'none');
      await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
      await expect(page).toHaveURL(endsWith('#/settings/keys'));
      expect(await stored(page)).toBeNull();
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('');
    });

    test('the sort survives nav away and back, a fresh page and a reload, and a fragment with its own query wins', async ({ page, playwright, baseURL }) => {
      const { prefix, mango, apple, zebra } = await seed(playwright, baseURL, `keep-${vp.name}`);
      await page.goto('/#/settings/keys');
      await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
      await sortBy(page, phone, 'name', 'asc');
      await expect.poll(() => order(page, prefix)).toEqual([apple, mango, zebra]);
      expect(await saved(page)).toEqual({ v: 1, query: 'sort=name&dir=asc' });

      // Nav away and back: the nav link has no query, and the view opens as it was left.
      await goTo(page, 'Tasks');
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await expect(page.getByRole('heading', { level: 2, name: 'API keys' })).toHaveCount(0);
      await goTo(page, 'Settings');
      await expect(page.getByRole('heading', { level: 2, name: 'API keys' })).toBeVisible();
      await expect.poll(() => order(page, prefix)).toEqual([apple, mango, zebra]);
      await expect(page).toHaveURL(endsWith('#/settings/keys?sort=name&dir=asc'));
      await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'ascending');
      if (phone) await expect(page.getByTestId('sort-select')).toHaveValue('name:asc');

      // A change after coming back is what is saved, and a new page of the same context opens the bare route with it.
      await sortBy(page, phone, 'expires', 'desc');
      await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
      const second = await page.context().newPage();
      await second.goto('/#/settings/keys');
      await expect.poll(() => order(second, prefix)).toEqual([mango, apple, zebra]);
      await expect(second).toHaveURL(endsWith('#/settings/keys?sort=expires&dir=desc'));
      await expect(th(second, 'expires')).toHaveAttribute('aria-sort', 'descending');

      // A reload of the fragment keeps the sort, and a link with its own query wins and is saved.
      await second.reload();
      await expect.poll(() => order(second, prefix)).toEqual([mango, apple, zebra]);
      await second.goto('/#/settings/keys?sort=status&dir=desc');
      await expect.poll(() => order(second, prefix)).toEqual([apple, mango, zebra]);
      await expect(th(second, 'status')).toHaveAttribute('aria-sort', 'descending');
      expect(await saved(second)).toEqual({ v: 1, query: 'sort=status&dir=desc' });
      await second.close();
    });

    test('a saved query with an unknown column opens the default order', async ({ page, playwright, baseURL }) => {
      const { prefix, mango, apple, zebra } = await seed(playwright, baseURL, `bad-${vp.name}`);
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, 'sort=actions&dir=asc'] as const);
      const fresh = await page.context().newPage();
      await fresh.goto('/#/settings/keys');
      await expect.poll(() => order(fresh, prefix)).toEqual([mango, apple, zebra]);
      for (const column of columns) await expect(th(fresh, column)).toHaveAttribute('aria-sort', 'none');
      await fresh.close();
    });

    if (!phone) {
      test('a header sorts from the keyboard: Enter and Space, asc then desc then the default', async ({ page, playwright, baseURL }) => {
        const { prefix, mango, apple, zebra } = await seed(playwright, baseURL, `keys-${vp.name}`);
        await page.goto('/#/settings/keys');
        await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
        const name = page.getByTestId('sort-name');
        await name.focus();
        await expect(name).toBeFocused();
        await page.keyboard.press('Enter');
        await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'ascending');
        await expect.poll(() => order(page, prefix)).toEqual([apple, mango, zebra]);
        await expect(name).toBeFocused();
        await page.keyboard.press('Space');
        await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'descending');
        await expect.poll(() => order(page, prefix)).toEqual([zebra, mango, apple]);
        await page.keyboard.press('Enter');
        await expect(th(page, 'name')).toHaveAttribute('aria-sort', 'none');
        await expect.poll(() => order(page, prefix)).toEqual([mango, apple, zebra]);
      });
    }
  });
}
