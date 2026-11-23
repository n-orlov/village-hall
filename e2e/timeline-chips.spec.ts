import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { expectInitiatives, expectTags, openChips, pickInitiative, pickTag, tagChip } from './chips';

// R7 on Timeline (SPEC 11, "Timeline"): the shared chip filter replaces the single-tag dropdown. Tag chips narrow with AND,
// initiative chips widen with OR; the filter goes to `GET /timeline` as repeated `tag` and `initiative`, lives in the fragment
// and the saved view state, and a single-tag fragment from before the chips still opens as one chosen tag. Each test seeds
// initiatives and tags of its own on the shared board and tells its own rows apart by task ref.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };
const KEY = 'vh.view.v1.timeline';

async function post<T = { ref: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const apiAs = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

// Timeline sits under the phone tab bar's More entry.
const goToTimeline = async (page: Page) => {
  const more = page.getByRole('button', { name: 'More' });
  if (await more.isVisible()) {
    await more.click();
    await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: 'Timeline' }).click();
  } else {
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Timeline' }).click();
  }
};

const row = (page: Page, ref: string) => page.locator('.vis-labelset .vis-label').filter({ hasText: `${ref} ` });

interface Seed {
  /** The slug of the initiative tagged A and B. */
  both: string;
  tagA: string;
  tagB: string;
  /** The tasks: in the initiative tagged A and B, in the one tagged A only, in the one tagged B only. */
  refs: { both: string; onlyA: string; onlyB: string };
}

async function seed(api: APIRequestContext, name: string): Promise<Seed> {
  const tagA = `tlc-${name}-a`;
  const tagB = `tlc-${name}-b`;
  const make = async (suffix: string, tags: string[]) => {
    const slug = `e2e-tlc-${name}-${suffix}`;
    await post(api, '/api/v1/initiatives', { slug, title: `Timeline chips ${name} ${suffix}`, tags }, 201);
    return { slug, ref: (await post(api, '/api/v1/tasks', { initiative: slug, title: `Row ${suffix}` }, 201)).ref };
  };
  const both = await make('both', [tagA, tagB]);
  const onlyA = await make('a', [tagA]);
  const onlyB = await make('b', [tagB]);
  return { both: both.slug, tagA, tagB, refs: { both: both.ref, onlyA: onlyA.ref, onlyB: onlyB.ref } };
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the shared chip filter renders and two picked tags narrow the rows to their intersection', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `and-${vp.name}`);
      const { both, onlyA, onlyB } = s.refs;
      const sent: string[][] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname === '/api/v1/timeline') sent.push(url.searchParams.getAll('tag'));
      });

      await page.goto('/#/timeline');
      await expect(page.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
      // The one shared filter is there; the single-tag dropdown and the initiative dropdown are not.
      await expect(page.getByTestId('timeline-filters').getByTestId('chip-filter')).toHaveCount(1);
      await expect(page.getByTestId('tag-filter')).toHaveCount(0);
      await expect(page.getByTestId('initiative-filter')).toHaveCount(0);
      await openChips(page);
      await expect(tagChip(page, s.tagA)).toBeVisible();
      await expect(tagChip(page, s.tagA)).toHaveAttribute('style', /--h:\s*\d+/);
      await expect(row(page, both)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, onlyA)).toHaveCount(1);
      await expect(row(page, onlyB)).toHaveCount(1);

      await pickTag(page, s.tagA);
      await expect(row(page, onlyB)).toHaveCount(0, FIVE_SECONDS);
      await expect(row(page, both)).toHaveCount(1);
      await expect(row(page, onlyA)).toHaveCount(1);
      await pickTag(page, s.tagB);
      await expect(row(page, onlyA)).toHaveCount(0, FIVE_SECONDS);
      await expect(row(page, both)).toHaveCount(1);
      await expect(row(page, onlyB)).toHaveCount(0);
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?tag=${s.tagA}&tag=${s.tagB}$`));
      expect(sent.at(-1)).toEqual([s.tagA, s.tagB]); // the server filters: both tags, repeated
      expect(JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string).query).toBe(`tag=${s.tagA}&tag=${s.tagB}`);

      await page.reload();
      await expect(row(page, both)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, onlyA)).toHaveCount(0);
      await openChips(page);
      await expectTags(page, [s.tagA, s.tagB]);

      // Initiatives are an OR on top of the tags: the A-only initiative adds nothing while B is chosen, and Clear chips empties.
      await pickInitiative(page, s.both);
      await expectInitiatives(page, [s.both]);
      await expect(row(page, both)).toHaveCount(1, FIVE_SECONDS);
      await page.getByTestId('chip-clear').click();
      await expect(page).toHaveURL(/#\/timeline$/);
      await expect(row(page, onlyA)).toHaveCount(1, FIVE_SECONDS);
      await api.dispose();
    });

    test('a single-tag fragment from before the chips opens as one chosen tag, and the saved view restores it', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `old-${vp.name}`);
      const { both, onlyA, onlyB } = s.refs;

      await page.goto(`/#/timeline?tag=${s.tagB}`);
      await expect(row(page, onlyB)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, onlyA)).toHaveCount(0);
      await expect(row(page, both)).toHaveCount(1);
      await openChips(page);
      await expectTags(page, [s.tagB]);

      // Away and back: the bare route restores the same single tag from the saved state.
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      await goToTimeline(page);
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?tag=${s.tagB}$`));
      await expect(row(page, onlyB)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, onlyA)).toHaveCount(0);
      await api.dispose();
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`the chip filter on Timeline passes axe in the ${theme} theme, chips chosen and not`, async ({ page, playwright, baseURL }) => {
        const api = await apiAs(playwright, baseURL);
        const s = await seed(api, `axe-${theme}-${vp.name}`);
        await page.addInitScript(([key, t]) => localStorage.setItem(key!, t!), ['vh.theme', theme]);
        await page.goto(`/#/timeline?initiative=${s.both}&tag=${s.tagA}`);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await expect(row(page, s.refs.both)).toHaveCount(1, FIVE_SECONDS);
        await openChips(page);
        await expect(tagChip(page, s.tagA)).toHaveAttribute('aria-pressed', 'true');
        await expect(tagChip(page, s.tagB)).toHaveAttribute('aria-pressed', 'false');
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
        const { violations } = await new AxeBuilder({ page }).include('[data-testid=timeline-filters]').analyze();
        const blocking = violations
          .filter((v) => v.impact === 'serious' || v.impact === 'critical')
          .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
        expect(blocking, 'serious or critical axe violations').toEqual([]);
        await api.dispose();
      });
    }
  });
}
