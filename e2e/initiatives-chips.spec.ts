import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { openChips, pickTag, tagChip } from './chips';

// R7 on Initiatives and on an initiative's page (SPEC 11, "The chip filter on Initiatives and on an initiative's page"): the
// shared chip filter offers tags only (no initiative chips: an initiative is not in an initiative), tags narrow with AND, the
// filter lives in the fragment and in the saved view state, the shared sort control replaces the page's own sort, and a tag has
// the one colour wherever it is drawn. Each test seeds initiatives and tags of its own on the shared board and reads only its rows.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const INITIATIVES_KEY = 'vh.view.v1.initiatives';
const PAGE_KEY = 'vh.view.v1.initiative';

async function post<T = { ref: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const apiAs = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

const select = (page: Page) => page.getByTestId('sort-select');
const savedQuery = async (page: Page, key: string) => (JSON.parse((await page.evaluate((k) => localStorage.getItem(k), key)) as string) as { query: string }).query;
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);
const hueOf = async (el: ReturnType<Page['locator']>) => ((await el.first().getAttribute('style')) ?? '').match(/--h:\s*(\d+)/)?.[1];

// ---- Initiatives --------------------------------------------------------------------------------------------------------

interface InitiativesSeed {
  prefix: string;
  tagA: string;
  tagB: string;
  /** Titles Mango (A, B), apple (B), Zebra (A) and cherry (no tag), created in this order. */
  slugs: { both: string; onlyB: string; onlyA: string; none: string };
}

async function seedInitiatives(api: APIRequestContext, name: string): Promise<InitiativesSeed> {
  const prefix = `e2e-ichip-${name}`;
  const tagA = `ichip-${name}-a`;
  const tagB = `ichip-${name}-b`;
  const make = async (key: string, title: string, tags: string[]) => {
    const slug = `${prefix}-${key}`;
    await post(api, '/api/v1/initiatives', { slug, title, tags }, 201);
    return slug;
  };
  const both = await make('mango', 'Mango', [tagA, tagB]);
  const onlyB = await make('apple', 'apple', [tagB]);
  const onlyA = await make('zebra', 'Zebra', [tagA]);
  const none = await make('cherry', 'cherry', []);
  return { prefix, tagA, tagB, slugs: { both, onlyB, onlyA, none } };
}

/** The seeded slugs the table shows, in table order: every other spec's initiatives are left out. */
const ownRows = async (page: Page, prefix: string) =>
  (await page.getByTestId('initiative-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-slug') ?? ''))).filter((slug) => slug.startsWith(`${prefix}-`));

// ---- an initiative's page -----------------------------------------------------------------------------------------------

interface PageSeed {
  slug: string;
  tagA: string;
  tagB: string;
  /** Titles Mango (A, B), apple (B), Zebra (A) and cherry (no tag), created in this order. */
  refs: { both: string; onlyB: string; onlyA: string; none: string };
}

async function seedPage(api: APIRequestContext, name: string): Promise<PageSeed> {
  const slug = `e2e-pchip-${name}`;
  const tagA = `pchip-${name}-a`;
  const tagB = `pchip-${name}-b`;
  await post(api, '/api/v1/initiatives', { slug, title: `Page chips ${name}` }, 201);
  const task = async (title: string, tags: string[]) => (await post(api, '/api/v1/tasks', { initiative: slug, title, tags }, 201)).ref;
  const both = await task('Mango', [tagA, tagB]);
  const onlyB = await task('apple', [tagB]);
  const onlyA = await task('Zebra', [tagA]);
  const none = await task('cherry', []);
  return { slug, tagA, tagB, refs: { both, onlyB, onlyA, none } };
}

const tableRefs = (page: Page) => page.getByTestId('task-ref').allTextContents();

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('Initiatives: the tag chips narrow the list with AND, offer tags only, and the fragment, the saved view and a reload hold them', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seedInitiatives(api, `and-${vp.name}`);
      const { both, onlyB, onlyA, none } = s.slugs;
      await page.goto('/#/initiatives');
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both, onlyB, onlyA, none]);

      // Tags only: the filter has a tag group and no initiative group.
      await openChips(page);
      await expect(page.getByTestId('chip-tags')).toBeVisible();
      await expect(page.getByTestId('chip-initiatives')).toHaveCount(0);
      await expect(page.getByTestId('chip-initiative')).toHaveCount(0);
      await expect(tagChip(page, s.tagA)).toHaveAttribute('data-source', 'initiative');

      await pickTag(page, s.tagA);
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both, onlyA]);
      await pickTag(page, s.tagB);
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both]);
      await expect(page).toHaveURL(endsWith(`#/initiatives?tag=${s.tagA}&tag=${s.tagB}`));
      expect(await savedQuery(page, INITIATIVES_KEY)).toBe(`tag=${s.tagA}&tag=${s.tagB}`);

      await page.reload();
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both]);
      await openChips(page);
      await expect(tagChip(page, s.tagA)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, s.tagB)).toHaveAttribute('aria-pressed', 'true');

      // A bare #/initiatives after a visit elsewhere restores the saved filter.
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Tasks', exact: true }).click();
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Initiatives' }).click();
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both]);
      await expect(page).toHaveURL(endsWith(`#/initiatives?tag=${s.tagA}&tag=${s.tagB}`));

      // Taking one tag off widens the list (a fresh mount folds the phone's filter); Clear filters forgets the chips and the saved state.
      await openChips(page);
      await tagChip(page, s.tagB).click();
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both, onlyA]);
      await page.getByTestId('clear-filters').click();
      await expect(page).toHaveURL(/#\/initiatives$/);
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both, onlyB, onlyA, none]);
      expect(await page.evaluate((k) => localStorage.getItem(k), INITIATIVES_KEY)).toBeNull();
      await api.dispose();
    });

    test('Initiatives: the shared sort control renders and reorders the seeded rows, with the chips kept', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seedInitiatives(api, `sort-${vp.name}`);
      const { both, onlyB, onlyA, none } = s.slugs;
      await page.goto('/#/initiatives');
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both, onlyB, onlyA, none]);
      await expect(select(page)).toBeVisible();
      await expect(page.locator('th [data-testid^="sort-"]')).toHaveCount(0); // the headers are plain: the control is the one sort

      await select(page).selectOption('title:asc');
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([onlyB, none, both, onlyA]); // apple, cherry, Mango, Zebra
      await expect(page).toHaveURL(endsWith('#/initiatives?sort=title&dir=asc'));
      await select(page).selectOption('title:desc');
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([onlyA, both, none, onlyB]);

      await page.reload();
      await expect(select(page)).toHaveValue('title:desc');
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([onlyA, both, none, onlyB]);

      await pickTag(page, s.tagA);
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([onlyA, both]);
      await expect(page).toHaveURL(endsWith(`#/initiatives?tag=${s.tagA}&sort=title&dir=desc`));
      expect(await savedQuery(page, INITIATIVES_KEY)).toBe(`tag=${s.tagA}&sort=title&dir=desc`);
      await select(page).selectOption('');
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([both, onlyA]);
      await api.dispose();
    });

    test('Initiatives: a tag chip and the tag on a row have the one colour', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seedInitiatives(api, `hue-${vp.name}`);
      await page.goto(`/#/initiatives?tag=${s.tagB}`);
      await expect.poll(() => ownRows(page, s.prefix)).toEqual([s.slugs.both, s.slugs.onlyB]);
      await openChips(page);
      const row = page.locator(`[data-testid=initiative-row][data-slug=${s.slugs.onlyB}]`);
      const onRow = row.locator('[data-testid=tag]', { hasText: s.tagB });
      await expect(onRow).toHaveClass(/vh-tag/);
      const hue = await hueOf(onRow);
      expect(hue, 'the row tag carries its hue').toMatch(/^\d+$/);
      expect(await hueOf(tagChip(page, s.tagB))).toBe(hue);
      await api.dispose();
    });

    test('an initiative page: the tag chips narrow the tasks with AND, offer tags only, and the fragment, the saved view and a reload hold them', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seedPage(api, `and-${vp.name}`);
      const { both, onlyB, onlyA, none } = s.refs;
      await page.goto(`/#/i/${s.slug}`);
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyB, onlyA, none]);

      await openChips(page);
      await expect(page.getByTestId('chip-tags')).toBeVisible();
      await expect(page.getByTestId('chip-initiatives')).toHaveCount(0);
      await expect(page.getByTestId('chip-initiative')).toHaveCount(0);

      await pickTag(page, s.tagA);
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyA]);
      await pickTag(page, s.tagB);
      await expect.poll(() => tableRefs(page)).toEqual([both]);
      await expect(page).toHaveURL(endsWith(`#/i/${s.slug}?tag=${s.tagA}&tag=${s.tagB}`));
      expect(await savedQuery(page, PAGE_KEY)).toBe(`tag=${s.tagA}&tag=${s.tagB}`);

      await page.reload();
      await expect.poll(() => tableRefs(page)).toEqual([both]);
      await openChips(page);
      await expect(tagChip(page, s.tagA)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, s.tagB)).toHaveAttribute('aria-pressed', 'true');

      await tagChip(page, s.tagB).click();
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyA]);
      await expect(page).toHaveURL(endsWith(`#/i/${s.slug}?tag=${s.tagA}`));
      await page.getByTestId('clear-filters').click();
      await expect(page).toHaveURL(endsWith(`#/i/${s.slug}`));
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyB, onlyA, none]);
      expect(await page.evaluate((k) => localStorage.getItem(k), PAGE_KEY)).toBeNull();
      await api.dispose();
    });

    test('an initiative page: the shared sort control renders and reorders the seeded tasks, with the chips kept', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seedPage(api, `sort-${vp.name}`);
      const { both, onlyB, onlyA, none } = s.refs;
      await page.goto(`/#/i/${s.slug}`);
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyB, onlyA, none]);
      await expect(select(page)).toBeVisible();
      await expect(page.locator('th [data-testid^="sort-"]')).toHaveCount(0);

      await select(page).selectOption('title:asc');
      await expect.poll(() => tableRefs(page)).toEqual([onlyB, none, both, onlyA]); // apple, cherry, Mango, Zebra
      await expect(page).toHaveURL(endsWith(`#/i/${s.slug}?sort=title&dir=asc`));
      await select(page).selectOption('title:desc');
      await expect.poll(() => tableRefs(page)).toEqual([onlyA, both, none, onlyB]);

      await page.reload();
      await expect(select(page)).toHaveValue('title:desc');
      await expect.poll(() => tableRefs(page)).toEqual([onlyA, both, none, onlyB]);

      await pickTag(page, s.tagA);
      await expect.poll(() => tableRefs(page)).toEqual([onlyA, both]);
      expect(await savedQuery(page, PAGE_KEY)).toBe(`tag=${s.tagA}&sort=title&dir=desc`);
      await select(page).selectOption('');
      await expect.poll(() => tableRefs(page)).toEqual([both, onlyA]);
      await api.dispose();
    });

    test('an initiative page: a tag chip, the tag on a task and the tag on the initiative have the one colour', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seedPage(api, `hue-${vp.name}`);
      await page.goto(`/#/i/${s.slug}`);
      await expect.poll(() => tableRefs(page)).toHaveLength(4);
      await openChips(page);
      const onTask = page.locator(`[data-testid=task-row][data-ref=${s.refs.onlyB}] [data-testid=task-tag]`, { hasText: s.tagB });
      await expect(onTask).toHaveClass(/vh-tag/);
      const hue = await hueOf(onTask);
      expect(hue, 'the task tag carries its hue').toMatch(/^\d+$/);
      expect(await hueOf(tagChip(page, s.tagB))).toBe(hue);
      await api.dispose();
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`Initiatives and an initiative page pass axe in the ${theme} theme with the chip filter and the sort control`, async ({ page, playwright, baseURL }) => {
        const api = await apiAs(playwright, baseURL);
        const i = await seedInitiatives(api, `axe-${theme}-${vp.name}`);
        const p = await seedPage(api, `axe-${theme}-${vp.name}`);
        await page.addInitScript(([key, t]) => localStorage.setItem(key!, t!), ['vh.theme', theme]);
        const axe = async () => {
          await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
          const { violations } = await new AxeBuilder({ page }).analyze();
          return violations
            .filter((v) => v.impact === 'serious' || v.impact === 'critical')
            .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
        };

        await page.goto(`/#/initiatives?tag=${i.tagA}&sort=title&dir=asc`);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await expect.poll(() => ownRows(page, i.prefix)).toEqual([i.slugs.both, i.slugs.onlyA]);
        await openChips(page);
        await expect(tagChip(page, i.tagA)).toHaveAttribute('aria-pressed', 'true');
        expect(await axe(), 'serious or critical axe violations on Initiatives').toEqual([]);

        await page.goto(`/#/i/${p.slug}?tag=${p.tagA}&sort=title&dir=asc`);
        await expect.poll(() => tableRefs(page)).toEqual([p.refs.both, p.refs.onlyA]);
        await openChips(page);
        await expect(tagChip(page, p.tagA)).toHaveAttribute('aria-pressed', 'true');
        expect(await axe(), 'serious or critical axe violations on the initiative page').toEqual([]);
        await api.dispose();
      });
    }
  });
}
