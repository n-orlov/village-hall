import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { chosenInitiatives, expectInitiatives, initiativeChip, openChips, pickInitiative, pickTag, tagChip } from './chips';

// R7 on Tasks (SPEC 11, "The chip filter, the shared sort control and the tag colour"): tag chips narrow with AND, initiative
// chips widen with OR, the filter goes to `GET /tasks` as repeated `tag` and `initiative` and lives in the fragment and the saved
// view state; the shared sort control reorders the rows and survives a reload; each chip names its source; axe passes in both
// themes. Each test seeds initiatives and tags of its own on the shared board and filters the view down to them.
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

const apiAs = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, actor = 'playwright') =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor } });

interface Seed {
  slug: string;
  tagA: string;
  tagB: string;
  /** Titles Mango (A, B), Zebra (A), apple (B), cherry (no tag), in creation order. */
  refs: { both: string; onlyA: string; onlyB: string; none: string };
}

async function seed(api: APIRequestContext, name: string): Promise<Seed> {
  const slug = `e2e-tchip-${name}`;
  const tagA = `tchip-${name}-a`;
  const tagB = `tchip-${name}-b`;
  await post(api, '/api/v1/initiatives', { slug, title: `Chips ${name}` }, 201);
  const task = async (title: string, tags: string[]) => (await post(api, '/api/v1/tasks', { initiative: slug, title, tags }, 201)).ref;
  const both = await task('Mango', [tagA, tagB]);
  const onlyA = await task('Zebra', [tagA]);
  const onlyB = await task('apple', [tagB]);
  const none = await task('cherry', []);
  return { slug, tagA, tagB, refs: { both, onlyA, onlyB, none } };
}

const tableRefs = (page: Page) => page.getByTestId('task-ref').allTextContents();
const select = (page: Page) => page.getByTestId('sort-select');

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('picking two tags narrows to their intersection, the URL and the saved state hold the filter and a reload keeps it', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `and-${vp.name}`);
      const { both, onlyA, onlyB, none } = s.refs;
      const sent: string[][] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname === '/api/v1/tasks' && url.searchParams.get('attention') !== 'true') sent.push(url.searchParams.getAll('tag'));
      });

      await page.goto('/#/tasks');
      await pickInitiative(page, s.slug);
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([both, onlyA, onlyB, none].sort());

      await pickTag(page, s.tagA);
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([both, onlyA].sort());
      await pickTag(page, s.tagB);
      await expect.poll(() => tableRefs(page)).toEqual([both]);
      await expect(page).toHaveURL(new RegExp(`#/tasks\\?initiative=${s.slug}&tag=${s.tagA}&tag=${s.tagB}$`));
      // The server filters: the request carries both tags, repeated.
      expect(sent.at(-1)).toEqual([s.tagA, s.tagB]);
      expect(JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe(`initiative=${s.slug}&tag=${s.tagA}&tag=${s.tagB}`);

      await page.reload();
      await expect.poll(() => tableRefs(page)).toEqual([both]);
      await openChips(page);
      await expect(tagChip(page, s.tagA)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, s.tagB)).toHaveAttribute('aria-pressed', 'true');
      await expectInitiatives(page, [s.slug]);

      // Taking one tag off widens the list again; Clear chips empties the filter.
      await tagChip(page, s.tagB).click();
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([both, onlyA].sort());
      await expect(page).toHaveURL(new RegExp(`#/tasks\\?initiative=${s.slug}&tag=${s.tagA}$`));
      await page.getByTestId('chip-clear').click();
      await expect(page).toHaveURL(/#\/tasks$/);
      await expect(page.locator('[data-testid="chip-tag"][aria-pressed="true"]')).toHaveCount(0);
      expect(await chosenInitiatives(page)).toEqual([]);
      await api.dispose();
    });

    test('two initiatives are an OR, and the two kinds of chip are an AND', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const one = await seed(api, `or1-${vp.name}`);
      const two = await seed(api, `or2-${vp.name}`);
      const sent: string[][] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname === '/api/v1/tasks' && url.searchParams.get('attention') !== 'true') sent.push(url.searchParams.getAll('initiative'));
      });
      await page.goto(`/#/tasks?initiative=${one.slug}`);
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual(Object.values(one.refs).sort());
      await pickInitiative(page, two.slug);
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([...Object.values(one.refs), ...Object.values(two.refs)].sort());
      expect(sent.at(-1)).toEqual([one.slug, two.slug]);
      await expect(page).toHaveURL(new RegExp(`#/tasks\\?initiative=${one.slug}&initiative=${two.slug}$`));

      // Tag A of the first initiative AND either initiative: only the first initiative's tasks carry that tag.
      await pickTag(page, one.tagA);
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([one.refs.both, one.refs.onlyA].sort());
      await page.reload();
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([one.refs.both, one.refs.onlyA].sort());
      await expectInitiatives(page, [one.slug, two.slug]);
      await api.dispose();
    });

    test('each chip names its source to screen readers and on hover', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `src-${vp.name}`);
      // A live lease held by a ralph agent gives its task the derived actor-kind tag `ralph`.
      const ralph = await apiAs(playwright, baseURL, `ralph:e2e-tchip-${vp.name}`);
      await post(ralph, '/api/v1/leases', { task: s.refs.none, intent: 'chip source' }, 201);
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      await expect.poll(async () => (await tableRefs(page)).length).toBe(4);
      await openChips(page);

      const own = tagChip(page, s.tagA);
      await expect(own).toHaveAttribute('title', `${s.tagA}: task tag`);
      await expect(own).toHaveAccessibleName(`${s.tagA}, task tag`);
      const kind = tagChip(page, 'ralph');
      await expect(kind).toHaveAttribute('data-source', 'actor');
      await expect(kind).toHaveAttribute('title', /actor kind/);
      await expect(kind).toHaveAccessibleName(/^ralph, actor kind/);
      const initiative = initiativeChip(page, s.slug);
      await expect(initiative).toBeAttached(); // named by the URL, so on show even in a folded list
      await expect(initiative).toHaveAttribute('title', `${s.slug}: initiative`);
      await expect(initiative).toHaveAccessibleName(`${s.slug}, initiative`);
      await api.dispose();
      await ralph.dispose();
    });

    test('the shared sort control reorders the seeded rows and survives a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `sort-${vp.name}`);
      const { both, onlyA, onlyB, none } = s.refs; // Mango, Zebra, apple, cherry
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyA, onlyB, none]);
      await expect(select(page)).toBeVisible();

      await select(page).selectOption('title:asc');
      await expect.poll(() => tableRefs(page)).toEqual([onlyB, none, both, onlyA]); // apple, cherry, Mango, Zebra
      await expect(page).toHaveURL(new RegExp(`#/tasks\\?initiative=${s.slug}&sort=title&dir=asc$`));
      await select(page).selectOption('title:desc');
      await expect.poll(() => tableRefs(page)).toEqual([onlyA, both, none, onlyB]);

      await page.reload();
      await expect(select(page)).toHaveValue('title:desc');
      await expect.poll(() => tableRefs(page)).toEqual([onlyA, both, none, onlyB]);

      // A chip change keeps the sort, and a bare #/tasks restores both from the saved state.
      await pickTag(page, s.tagA);
      await expect.poll(() => tableRefs(page)).toEqual([onlyA, both]);
      await expect(page).toHaveURL(new RegExp(`sort=title&dir=desc$`));
      expect(JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe(`initiative=${s.slug}&tag=${s.tagA}&sort=title&dir=desc`);
      await select(page).selectOption('');
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyA]);
      await api.dispose();
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`the chip filter and the sort control pass axe in the ${theme} theme, chips chosen and not`, async ({ page, playwright, baseURL }) => {
        const api = await apiAs(playwright, baseURL);
        const s = await seed(api, `axe-${theme}-${vp.name}`);
        await page.addInitScript(([key, t]) => localStorage.setItem(key!, t!), ['vh.theme', theme]);
        await page.goto(`/#/tasks?initiative=${s.slug}&tag=${s.tagA}&sort=title&dir=asc`);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await expect.poll(async () => (await tableRefs(page)).length).toBe(2);
        await openChips(page);
        await expect(tagChip(page, s.tagA)).toHaveAttribute('aria-pressed', 'true');
        await expect(tagChip(page, s.tagB)).toHaveAttribute('aria-pressed', 'false');
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
        const { violations } = await new AxeBuilder({ page }).include('[data-testid=chip-filter]').include('[data-testid=sort-select]').analyze();
        const blocking = violations
          .filter((v) => v.impact === 'serious' || v.impact === 'critical')
          .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
        expect(blocking, 'serious or critical axe violations').toEqual([]);
        await api.dispose();
      });
    }
  });
}
