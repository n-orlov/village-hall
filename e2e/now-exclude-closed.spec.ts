import AxeBuilder from '@axe-core/playwright';
import { expect, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { openChips } from './chips';
import { actorApi, created, openFold } from './now';

// "Exclude closed" on Now's Recently touched (SPEC 11, The In flight column on Now): a checkbox, off by default, that hides
// the closed tasks of the band in the browser while a done task stays; the band's count follows the shown rows, and the
// choice lives in the fragment (`exclude=closed`) and the saved view state, so a reload, a link and a bare `#/now` keep it.
// The board is shared, so each test filters to an initiative of its own.
const FIVE_SECONDS = { timeout: 5000 };
const PHONE = { width: 390, height: 844 };
const DESKTOP = { width: 1440, height: 900 };

interface Seed {
  slug: string;
  open: string;
  done: string;
  closed: string;
}

/** One initiative with three tasks touched just now and no live lease: one open, one done and one closed. */
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const slug = `e2e-excl-${name}`;
  const api = await actorApi(playwright, baseURL, `excl-${name}`);
  await created(api, '/api/v1/initiatives', { slug, title: `Exclude closed ${name}` });
  const task = async (title: string) => (await created(api, '/api/v1/tasks', { initiative: slug, title })).ref;
  const open = await task('Still open');
  const done = await task('Done, waiting to be verified');
  expect((await api.post(`/api/v1/tasks/${done}/done`, { data: {} })).status()).toBe(200);
  const closed = await task('Closed already');
  const close = await api.post(`/api/v1/tasks/${closed}/close`, { data: { resolution: 'completed' } });
  expect(close.status(), await close.text()).toBe(200);
  await api.dispose();
  return { slug, open, done, closed };
}

const recentRow = (page: Page, ref: string) => page.locator(`[data-testid=recent-row][data-task="${ref}"]`);
const checkbox = (page: Page) => page.getByTestId('now-recent').getByRole('checkbox', { name: 'Exclude closed' });
const count = (page: Page) => page.getByTestId('now-recent').getByTestId('recent-count');

/** The band shows the open and the done task, and the closed one only when `closedShown`. */
async function expectRows(page: Page, s: Seed, closedShown: boolean): Promise<void> {
  await expect(recentRow(page, s.open)).toBeVisible(FIVE_SECONDS);
  await expect(recentRow(page, s.done)).toBeVisible();
  await expect(recentRow(page, s.closed)).toHaveCount(closedShown ? 1 : 0);
  await expect(page.getByTestId('recent-row')).toHaveCount(closedShown ? 3 : 2);
  await expect(count(page)).toHaveText(closedShown ? '3' : '2');
}

for (const vp of [
  { name: 'phone', size: PHONE },
  { name: 'desktop', size: DESKTOP },
]) {
  test.describe(`Exclude closed on ${vp.name}`, () => {
    test.use({ viewport: vp.size });

    test('hides the closed task, keeps the done one, and survives a reload, a new page and the keyboard', async ({ page, context, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, vp.name);
      await page.goto(`/#/now?initiative=${s.slug}`);
      await openFold(page, 'recent');
      // Off by default: every touched task shows.
      await expect(checkbox(page)).not.toBeChecked();
      await expectRows(page, s, true);

      await checkbox(page).click();
      await expectRows(page, s, false);
      await expect(page).toHaveURL(new RegExp(`#/now\\?initiative=${s.slug}&exclude=closed$`));

      // A reload reads it back from the fragment.
      await page.reload();
      await openFold(page, 'recent');
      await expect(checkbox(page)).toBeChecked();
      await expectRows(page, s, false);

      // A bare #/now in a new page restores it from the saved view state, with the filter.
      const other = await context.newPage();
      await other.goto('/#/now');
      await expect(other).toHaveURL(new RegExp(`#/now\\?initiative=${s.slug}&exclude=closed$`));
      await openFold(other, 'recent');
      await expect(checkbox(other)).toBeChecked();
      await expectRows(other, s, false);
      await other.close();

      // The keyboard turns it off again: the closed task comes back and the fragment drops it.
      await checkbox(page).focus();
      await page.keyboard.press('Space');
      await expect(checkbox(page)).not.toBeChecked();
      await expectRows(page, s, true);
      await expect(page).toHaveURL(new RegExp(`#/now\\?initiative=${s.slug}$`));
    });

    test('Clear chips keeps it, and an empty band says the closed tasks are hidden', async ({ page, playwright, baseURL }) => {
      const api = await actorApi(playwright, baseURL, `excl-empty-${vp.name}`);
      const slug = `e2e-excl-empty-${vp.name}`;
      await created(api, '/api/v1/initiatives', { slug, title: 'Only closed' });
      const ref = (await created(api, '/api/v1/tasks', { initiative: slug, title: 'Closed only' })).ref;
      expect((await api.post(`/api/v1/tasks/${ref}/close`, { data: { resolution: 'dropped' } })).status()).toBe(200);
      await api.dispose();
      await page.goto(`/#/now?initiative=${slug}&exclude=closed`);
      await openFold(page, 'recent');
      await expect(checkbox(page)).toBeChecked();
      await expect(page.getByTestId('recent-empty')).toHaveText('Nothing open or done was touched in this window; 1 closed task is hidden.', FIVE_SECONDS);
      await expect(count(page)).toHaveText('0');

      // Clear chips drops the initiative and keeps the setting, in the fragment and the saved view state.
      await openChips(page);
      await page.getByTestId('chip-clear').click();
      await expect(page).toHaveURL(/#\/now\?exclude=closed$/);
      await expect(checkbox(page)).toBeChecked();
      expect(await page.evaluate(() => JSON.parse(localStorage.getItem('vh.view.v1.attention') ?? '{}').query)).toBe('exclude=closed');
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`the checkbox passes axe in ${theme}`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `axe-${vp.name}-${theme}`);
        await page.addInitScript((t) => localStorage.setItem('vh.theme', t), theme);
        // The 44 px targets are the phone's and Sparse's; Compact (the default) from 768 px lowers them to 28 px.
        await page.addInitScript(() => localStorage.setItem('vh.density.v1', 'sparse'));
        await page.goto(`/#/now?initiative=${s.slug}&exclude=closed`);
        await openFold(page, 'recent');
        await expectRows(page, s, false);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        // A 44 px target on one line with the window switch beside it. Both rectangles are read in one task, so a layout
        // shift between two reads (the In flight band settling to the initiative filter above) cannot read as a
        // misalignment, and the alignment is awaited like any other text or count until the layout has settled.
        const control = () =>
          page.evaluate(() => {
            const rect = (selector: string) => {
              const { y, height } = document.querySelector(selector)!.getBoundingClientRect();
              return { y, height };
            };
            return { box: rect('[data-testid=exclude-closed-control]'), radio: rect('[data-testid=now-window] label') };
          });
        await expect
          .poll(async () => {
            const { box, radio } = await control();
            return Math.abs(box.y + box.height / 2 - (radio.y + radio.height / 2));
          }, FIVE_SECONDS)
          .toBeLessThanOrEqual(2);
        expect((await control()).box.height).toBeGreaterThanOrEqual(44);
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
        const { violations } = await new AxeBuilder({ page }).include('[data-testid=now-recent]').analyze();
        const found = violations.filter((v) => v.impact === 'serious' || v.impact === 'critical').map((v) => ({ rule: v.id, nodes: v.nodes.map((n) => n.target) }));
        expect(found, 'serious or critical axe violations').toEqual([]);
      });
    }
  });
}
