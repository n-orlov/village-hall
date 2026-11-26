import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { catalogRead, expectInitiatives, expectTags, openChips, pickInitiative, pickTag, tagChip } from './chips';
import { backdateTask } from './store';
import { logicViewports } from './viewports';

// R7 on Archive (SPEC 11, "The chip filter on Archive"): the shared chip filter (tags AND, initiatives OR) narrows the closed
// list on the server (`GET /tasks?status=closed&tag=..&initiative=..`), the filter lives in the fragment and in the saved view
// state, and the shared sort control reorders the narrowed rows. Each test seeds two initiatives of its own with closed tasks and
// reads only those rows by ref, so the shared board's other closed items do not matter.
const KEY = 'vh.view.v1.archive';

async function post<T = { ref: string; id: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

interface Seed {
  one: string;
  two: string;
  tagA: string;
  tagB: string;
  /** Closed tasks, newest first by closing time: mango (own A and B, in `one`), apple (own B, in `two`), zebra (own A, in `two`), cherry (no tag, in `one`). */
  refs: { mango: string; apple: string; zebra: string; cherry: string };
  all: string[];
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
  const one = `e2e-archchip-${name}-one`;
  const two = `e2e-archchip-${name}-two`;
  const tagA = `archchip-${name}-a`;
  const tagB = `archchip-${name}-b`;
  await post(api, '/api/v1/initiatives', { slug: one, title: `Archive chips ${name} one` }, 201);
  await post(api, '/api/v1/initiatives', { slug: two, title: `Archive chips ${name} two` }, 201);
  // Closed 1, 2, 3 and 4 days ago: the default order is mango, apple, zebra, cherry.
  const close = async (initiative: string, title: string, tags: string[], days: number) => {
    const task = await post(api, '/api/v1/tasks', { initiative, title, tags }, 201);
    await post(api, `/api/v1/tasks/${task.ref}/close`, { resolution: 'completed' }, 200);
    backdateTask(task.id, days);
    return task.ref;
  };
  const mango = await close(one, 'Mango', [tagA, tagB], 1);
  const apple = await close(two, 'apple', [tagB], 2);
  const zebra = await close(two, 'Zebra', [tagA], 3);
  const cherry = await close(one, 'cherry', [], 4);
  await api.dispose();
  return { one, two, tagA, tagB, refs: { mango, apple, zebra, cherry }, all: [mango, apple, zebra, cherry] };
}

/** The seeded refs the table shows, in table order. */
const rows = async (page: Page, refs: string[]) =>
  (await page.getByTestId('archive-row').evaluateAll((els) => els.map((el) => el.getAttribute('data-ref') ?? ''))).filter((ref) => refs.includes(ref));

const saved = async (page: Page) => (JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { query: string }).query;
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the tag chips narrow the closed list with AND on the server, and the fragment and the saved view hold them', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `tag-${vp.name}`);
      const { mango, apple, zebra, cherry } = s.refs;
      const requests: string[] = [];
      page.on('request', (r) => {
        if (r.url().includes('/api/v1/tasks?')) requests.push(r.url());
      });
      await page.goto('/#/archive');
      await expect.poll(() => rows(page, s.all)).toEqual([mango, apple, zebra, cherry]);
      await openChips(page);
      await expect(tagChip(page, s.tagA)).toHaveAttribute('data-source', 'task');

      await pickTag(page, s.tagA);
      await expect.poll(() => rows(page, s.all)).toEqual([mango, zebra]);
      await pickTag(page, s.tagB);
      await expect.poll(() => rows(page, s.all)).toEqual([mango]);
      await expect(page).toHaveURL(endsWith(`#/archive?tag=${s.tagA}&tag=${s.tagB}`));
      expect(await saved(page)).toBe(`tag=${s.tagA}&tag=${s.tagB}`);
      // The narrowing is the server's: the request carries both tags and status=closed.
      const last = new URL(requests.at(-1) ?? '').searchParams;
      expect(last.get('status')).toBe('closed');
      expect(last.getAll('tag')).toEqual([s.tagA, s.tagB]);

      await page.reload();
      await expect.poll(() => rows(page, s.all)).toEqual([mango]);
      await openChips(page);
      await expectTags(page, [s.tagA, s.tagB]);

      // A bare #/archive after a visit elsewhere restores the saved filter; taking a tag off widens the list; Clear forgets it.
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Tasks', exact: true }).click();
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await page.goto('/#/archive');
      await expect.poll(() => rows(page, s.all)).toEqual([mango]);
      await openChips(page);
      await tagChip(page, s.tagB).click();
      await expect.poll(() => rows(page, s.all)).toEqual([mango, zebra]);
      await page.getByTestId('chip-clear').click();
      await expect(page).toHaveURL(/#\/archive$/);
      await expect.poll(() => rows(page, s.all)).toEqual([mango, apple, zebra, cherry]);
    });

    test('the initiative chips narrow with OR, combine with a tag by AND, and ride the request as repeated initiative', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `ini-${vp.name}`);
      const { mango, apple, zebra, cherry } = s.refs;
      const requests: string[] = [];
      page.on('request', (r) => {
        if (r.url().includes('/api/v1/tasks?')) requests.push(r.url());
      });
      const listed = catalogRead(page, s.two);
      await page.goto('/#/archive');
      await expect.poll(() => rows(page, s.all)).toEqual([mango, apple, zebra, cherry]);
      await listed;
      await pickInitiative(page, s.one);
      await expect.poll(() => rows(page, s.all)).toEqual([mango, cherry]);
      await pickInitiative(page, s.two);
      await expect.poll(() => rows(page, s.all)).toEqual([mango, apple, zebra, cherry]);
      await pickTag(page, s.tagA);
      await expect.poll(() => rows(page, s.all)).toEqual([mango, zebra]);
      await expect(page).toHaveURL(endsWith(`#/archive?initiative=${s.one}&initiative=${s.two}&tag=${s.tagA}`));
      const last = new URL(requests.at(-1) ?? '').searchParams;
      expect(last.getAll('initiative')).toEqual([s.one, s.two]);
      expect(last.getAll('tag')).toEqual([s.tagA]);

      await page.goto(`/#/archive?initiative=${s.two}&tag=${s.tagA}`);
      await expect.poll(() => rows(page, s.all)).toEqual([zebra]);
      await expectInitiatives(page, [s.two]);
      await expectTags(page, [s.tagA]);
    });

    test('the shared sort control reorders the narrowed rows, with the chips kept in the fragment and the saved view', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `sort-${vp.name}`);
      const { mango, apple, zebra, cherry } = s.refs;
      await page.goto('/#/archive');
      await expect.poll(() => rows(page, s.all)).toEqual([mango, apple, zebra, cherry]);
      const select = page.getByTestId('sort-select');
      await expect(select).toBeVisible();
      await expect(page.locator('th [data-testid^="sort-"]')).toHaveCount(0); // the headers are plain: the control is the one sort

      await select.selectOption('item:asc');
      await expect.poll(() => rows(page, s.all)).toEqual([apple, cherry, mango, zebra]);
      await select.selectOption('item:desc');
      await expect.poll(() => rows(page, s.all)).toEqual([zebra, mango, cherry, apple]);

      await pickTag(page, s.tagA);
      await expect.poll(() => rows(page, s.all)).toEqual([zebra, mango]);
      await expect(page).toHaveURL(endsWith(`#/archive?tag=${s.tagA}&sort=item&dir=desc`));
      expect(await saved(page)).toBe(`tag=${s.tagA}&sort=item&dir=desc`);
      await select.selectOption('closed:asc');
      await expect.poll(() => rows(page, s.all)).toEqual([zebra, mango]);
      await select.selectOption('closed:desc');
      await expect.poll(() => rows(page, s.all)).toEqual([mango, zebra]);

      await page.reload();
      await expect(select).toHaveValue('closed:desc');
      await expect.poll(() => rows(page, s.all)).toEqual([mango, zebra]);
      await select.selectOption('');
      await expect.poll(() => rows(page, s.all)).toEqual([mango, zebra]);
      await expect(page).toHaveURL(endsWith(`#/archive?tag=${s.tagA}`));
    });

    test('a chip set that matches nothing says so', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `none-${vp.name}`);
      await page.goto(`/#/archive?initiative=${s.one}&tag=${s.tagB}&tag=${s.tagA}&window=7`);
      await expect.poll(() => rows(page, s.all)).toEqual([s.refs.mango]);
      await page.goto(`/#/archive?initiative=${s.two}&tag=${s.tagB}&tag=${s.tagA}&window=7`);
      await expect(page.getByTestId('archive-empty')).toContainText('No closed item matches these chips');
    });
  });
}
