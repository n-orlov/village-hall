import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { backdateInitiative, backdateTask } from './store';

// The Archive sort and saved view state (PRD R16, R7, SPEC 11): the rows sort by Item (text), Kind (kind), Resolution
// (status) and Closed (time) through the shared sort control (one select at every width, with the keyboard). The window and
// the sort ride in one fragment query and are saved under `vh.view.v1.archive`, so they survive nav away and back. The
// table has no ref column of its own (the ref sits under the title). The server is shared with every other spec, so each test
// reads only the four rows it seeded, by ref.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const KEY = 'vh.view.v1.archive';

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status = 201): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

interface Created {
  id: string;
  ref: string;
}

interface Seed {
  /** The refs of the four rows: bravo (task, completed, closed 20 days ago), charlie (task, dropped, closed now),
   * alpha (task, superseded, 10 days ago) and delta (initiative, closed 5 days ago). */
  bravo: string;
  charlie: string;
  alpha: string;
  delta: string;
  all: string[];
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
  const slug = `e2e-arcsort-${name}`;
  await post(api, '/api/v1/initiatives', { slug, title: `Archive sort ${name}` });
  const close = async (title: string, resolution: string, days: number) => {
    const task = await post<Created>(api, '/api/v1/tasks', { initiative: slug, title });
    await post(api, `/api/v1/tasks/${task.ref}/close`, { resolution }, 200);
    if (days > 0) backdateTask(task.id, days);
    return task.ref;
  };
  const bravo = await close('Bravo sort row', 'completed', 20);
  const charlie = await close('Charlie sort row', 'dropped', 0);
  const alpha = await close('Alpha sort row', 'superseded', 10);
  const delta = `e2e-arcsort-${name}-ini`;
  const initiative = await post<Created>(api, '/api/v1/initiatives', { slug: delta, title: `Delta sort row ${name}` });
  await post(api, `/api/v1/initiatives/${delta}/close`, {}, 200);
  backdateInitiative(initiative.id, 5);
  await api.dispose();
  return { bravo, charlie, alpha, delta, all: [bravo, charlie, alpha, delta] };
}

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' });
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);
const sortSelect = (page: Page): Locator => page.getByTestId('sort-select');
const saved = async (page: Page) => JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { v: number; query: string };
const stored = (page: Page) => page.evaluate((k) => localStorage.getItem(k), KEY);

// The seeded refs in the order the table shows them.
const order = (page: Page, refs: string[]) =>
  page.locator('[data-testid=archive-row]').evaluateAll((rows, own) => rows.map((row) => row.getAttribute('data-ref') ?? '').filter((ref) => own.includes(ref)), refs);

async function goTo(page: Page, label: string) {
  const link = nav(page).getByRole('link', { name: new RegExp(`^${label}`) });
  if (await link.isVisible()) {
    await link.click();
  } else {
    await page.getByRole('button', { name: 'More' }).click();
    await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: new RegExp(`^${label}`) }).click();
  }
}

async function sortBy(page: Page, column: string, dir: 'asc' | 'desc' | null) {
  await sortSelect(page).selectOption(dir === null ? '' : `${column}:${dir}`);
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('sorting by one column of each type (text, kind, status, time) through the shared control orders the rows, and the first option is the default', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `sort-${vp.name}`);
      await page.goto('/#/archive');
      // The default order: newest closed first.
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await expect(sortSelect(page)).toBeVisible();
      await expect(sortSelect(page)).toHaveValue('');
      // The headers are plain: no aria-sort and no sort button, the shared control is the one sort.
      await expect(page.locator('[data-testid=archive-view] th')).toHaveCount(5);
      await expect(page.locator('[data-testid=archive-view] th[aria-sort]')).toHaveCount(0);
      await expect(page.locator('[data-testid=archive-view] th button')).toHaveCount(0);

      // text: the title, ignoring case.
      await sortBy(page, 'item', 'asc');
      await expect.poll(() => order(page, s.all)).toEqual([s.alpha, s.bravo, s.charlie, s.delta]);
      await sortBy(page, 'item', 'desc');
      await expect.poll(() => order(page, s.all)).toEqual([s.delta, s.charlie, s.bravo, s.alpha]);
      await expect(page).toHaveURL(endsWith('#/archive?sort=item&dir=desc'));

      // kind: the initiative before the tasks ascending, the tasks (in the default order) before it descending.
      await sortBy(page, 'kind', 'asc');
      await expect.poll(() => order(page, s.all)).toEqual([s.delta, s.charlie, s.alpha, s.bravo]);
      await sortBy(page, 'kind', 'desc');
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.alpha, s.bravo, s.delta]);

      // status: the resolution.
      await sortBy(page, 'resolution', 'asc');
      await expect.poll(() => order(page, s.all)).toEqual([s.bravo, s.charlie, s.delta, s.alpha]);
      await sortBy(page, 'resolution', 'desc');
      await expect.poll(() => order(page, s.all)).toEqual([s.alpha, s.delta, s.charlie, s.bravo]);

      // time: when it was closed, oldest first ascending, newest first descending.
      await sortBy(page, 'closed', 'asc');
      await expect.poll(() => order(page, s.all)).toEqual([s.bravo, s.alpha, s.delta, s.charlie]);
      await sortBy(page, 'closed', 'desc');
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await expect(page).toHaveURL(endsWith('#/archive?sort=closed&dir=desc'));
      expect(await saved(page)).toEqual({ v: 1, query: 'sort=closed&dir=desc' });

      // The first option: the default order, gone from the fragment and from the saved state.
      await sortBy(page, 'item', 'asc');
      await sortBy(page, 'item', null);
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await expect(page).toHaveURL(endsWith('#/archive'));
      expect(await stored(page)).toBeNull();
      await expect(sortSelect(page)).toHaveValue('');
    });

    test('the window and the sort survive nav away and back, a fresh page and a reload, and a fragment with its own query wins', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `keep-${vp.name}`);
      await page.goto('/#/archive');
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await page.getByTestId('archive-window').selectOption('90');
      await expect(page).toHaveURL(endsWith('#/archive?window=90'));
      await sortBy(page, 'item', 'asc');
      await expect.poll(() => order(page, s.all)).toEqual([s.alpha, s.bravo, s.charlie, s.delta]);
      await expect(page).toHaveURL(endsWith('#/archive?window=90&sort=item&dir=asc'));
      expect(await saved(page)).toEqual({ v: 1, query: 'window=90&sort=item&dir=asc' });

      // Nav away and back: the nav link has no query, and the view opens as it was left.
      await goTo(page, 'Tasks');
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await expect(page.getByTestId('archive-view')).toHaveCount(0);
      await goTo(page, 'Archive');
      await expect(page.getByTestId('archive-view')).toBeVisible();
      await expect.poll(() => order(page, s.all)).toEqual([s.alpha, s.bravo, s.charlie, s.delta]);
      await expect(page).toHaveURL(endsWith('#/archive?window=90&sort=item&dir=asc'));
      await expect(page.getByTestId('archive-window')).toHaveValue('90');
      await expect(sortSelect(page)).toHaveValue('item:asc');

      // A sort change keeps the window, and the window change keeps the sort; a new page of the same context opens with both.
      await sortBy(page, 'closed', 'desc');
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await page.getByTestId('archive-window').selectOption('all');
      await expect(page).toHaveURL(endsWith('#/archive?window=all&sort=closed&dir=desc'));
      const second = await page.context().newPage();
      await second.goto('/#/archive');
      await expect.poll(() => order(second, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await expect(second).toHaveURL(endsWith('#/archive?window=all&sort=closed&dir=desc'));
      await expect(second.getByTestId('archive-window')).toHaveValue('all');
      await expect(sortSelect(second)).toHaveValue('closed:desc');

      // A reload of the fragment keeps both, and a link with its own query wins and is saved.
      await second.reload();
      await expect.poll(() => order(second, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await second.goto('/#/archive?window=7&sort=resolution&dir=asc');
      await expect.poll(() => order(second, s.all)).toEqual([s.charlie, s.delta]);
      await expect(second.getByTestId('archive-window')).toHaveValue('7');
      await expect(sortSelect(second)).toHaveValue('resolution:asc');
      expect(await saved(second)).toEqual({ v: 1, query: 'window=7&sort=resolution&dir=asc' });
      await second.close();
    });

    test('a saved query with an unknown column or window opens the default order and the default window', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `bad-${vp.name}`);
      await page.goto('/#/now');
      await expect(page.getByTestId('band-asks')).toBeVisible();
      await page.evaluate(([k, query]) => localStorage.setItem(k, JSON.stringify({ v: 1, query })), [KEY, 'window=14&sort=action&dir=asc'] as const);
      const fresh = await page.context().newPage();
      await fresh.goto('/#/archive');
      await expect.poll(() => order(fresh, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      await expect(fresh.getByTestId('archive-window')).toHaveValue('30');
      await expect(sortSelect(fresh)).toHaveValue('');
      await fresh.close();
    });

    test('the sort control works from the keyboard: arrow keys pick an option, the first option is the default', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `keys-${vp.name}`);
      await page.goto('/#/archive');
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
      const select = sortSelect(page);
      await select.focus();
      await expect(select).toBeFocused();
      await page.keyboard.press('ArrowDown');
      await expect(select).toHaveValue('item:asc');
      await expect.poll(() => order(page, s.all)).toEqual([s.alpha, s.bravo, s.charlie, s.delta]);
      await page.keyboard.press('ArrowDown');
      await expect(select).toHaveValue('item:desc');
      await expect.poll(() => order(page, s.all)).toEqual([s.delta, s.charlie, s.bravo, s.alpha]);
      await select.selectOption('');
      await expect.poll(() => order(page, s.all)).toEqual([s.charlie, s.delta, s.alpha, s.bravo]);
    });
  });
}
