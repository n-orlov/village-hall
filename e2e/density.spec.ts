import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// R6 (SPEC 11, "Display: density, theme and the sidebar"): the density preference (Compact by default, Sparse) changes a
// list page's row height and is kept under `vh.density.v1`; the sidebar collapses to the icon rail at lg and wider and
// stays collapsed (`vh.sidebar.v1`); the density and theme switches sit together in the sidebar foot and in the account
// menu; and the Tasks view passes axe in both themes and both densities, with 44 px targets and 16 px inputs on a phone
// in both densities. Each test seeds an initiative of its own on the shared board and filters the view down to it.
const DENSITY_KEY = 'vh.density.v1';
const SIDEBAR_KEY = 'vh.sidebar.v1';
const PHONE = { width: 390, height: 844 };
const DESKTOP = { width: 1440, height: 900 };
const SNAP = 1 / 32; // layout snaps to 1/64 px, so a 44 px box can measure 43.99997

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

/** One initiative with three open tasks; a second call for the same slug leaves the first seed as it is. */
async function seed(api: APIRequestContext, slug: string): Promise<void> {
  const created = await api.post('/api/v1/initiatives', { data: { slug, title: `Density ${slug}` } });
  if (created.status() === 409) return;
  expect(created.status(), await created.text()).toBe(201);
  for (const title of ['Alpha row', 'Bravo row', 'Charlie row']) {
    const response = await api.post('/api/v1/tasks', { data: { initiative: slug, title } });
    expect(response.status(), await response.text()).toBe(201);
  }
}

const html = (page: Page) => page.locator('html');
const firstRowHeight = async (page: Page) => {
  const row = page.getByTestId('task-ref').first().locator('xpath=ancestor::tr');
  await expect(row).toBeVisible();
  return (await row.boundingBox())!.height;
};

test.describe('desktop 1440x900', () => {
  test.use({ viewport: DESKTOP });

  test('Sparse makes a Tasks row taller than Compact (the default) and survives a reload', async ({ page, playwright, baseURL }) => {
    const slug = 'e2e-density-rows';
    await seed(await apiContext(playwright, baseURL), slug);
    await page.goto(`/#/tasks?initiative=${slug}`);
    await expect(html(page)).toHaveAttribute('data-density', 'compact');
    const compact = await firstRowHeight(page);

    const switches = page.getByTestId('display-controls-sidebar');
    await switches.getByRole('radio', { name: 'Sparse' }).check({ force: true });
    await expect(html(page)).toHaveAttribute('data-density', 'sparse');
    const sparse = await firstRowHeight(page);
    expect(sparse, `compact ${compact}px, sparse ${sparse}px`).toBeGreaterThan(compact + 8);
    expect(await page.evaluate((key) => localStorage.getItem(key), DENSITY_KEY)).toBe('sparse');

    await page.reload();
    await expect(html(page)).toHaveAttribute('data-density', 'sparse');
    await expect(switches.getByRole('radio', { name: 'Sparse' })).toBeChecked();
    expect(await firstRowHeight(page)).toBeCloseTo(sparse, 0);

    await switches.getByRole('radio', { name: 'Compact' }).check({ force: true });
    await expect(html(page)).toHaveAttribute('data-density', 'compact');
    expect(await firstRowHeight(page)).toBeCloseTo(compact, 0);
  });

  test('a bad stored density is ignored and Compact shows', async ({ page }) => {
    await page.addInitScript((key) => localStorage.setItem(key, 'cozy'), DENSITY_KEY);
    await page.goto('/#/tasks');
    await expect(html(page)).toHaveAttribute('data-density', 'compact');
    await expect(page.getByTestId('display-controls-sidebar').getByRole('radio', { name: 'Compact' })).toBeChecked();
  });

  test('the sidebar foot holds the density and theme switches together, theme as Auto, Light, Dark', async ({ page }) => {
    await page.goto('/#/tasks');
    const foot = page.getByTestId('sidebar-foot');
    await expect(foot.getByRole('group', { name: 'Density' })).toBeVisible();
    await expect(foot.getByRole('group', { name: 'Theme' })).toBeVisible();
    for (const name of ['Auto', 'Light', 'Dark']) await expect(foot.getByRole('radio', { name })).toHaveCount(1);
    await foot.getByRole('radio', { name: 'Dark' }).check({ force: true });
    await expect(html(page)).toHaveAttribute('data-theme', 'dark');
    expect(await page.evaluate(() => localStorage.getItem('vh.theme'))).toBe('dark');
  });

  test('the sidebar collapses to the icon rail, stays collapsed after a reload and expands again', async ({ page }) => {
    await page.goto('/#/tasks');
    await expect(page.getByTestId('nav-sidebar')).toBeVisible();
    await expect(page.getByTestId('nav-rail')).toBeHidden();
    const wide = (await page.getByTestId('sidebar').boundingBox())!.width;

    await page.getByRole('button', { name: 'Collapse sidebar' }).click();
    await expect(page.getByTestId('nav-sidebar')).toBeHidden();
    await expect(page.getByTestId('nav-rail')).toBeVisible();
    const rail = (await page.getByTestId('rail').boundingBox())!.width;
    expect(rail).toBeLessThan(wide / 2);
    expect(await page.evaluate((key) => localStorage.getItem(key), SIDEBAR_KEY)).toBe('collapsed');

    await page.reload();
    await expect(page.getByTestId('nav-rail')).toBeVisible();
    await expect(page.getByTestId('nav-sidebar')).toBeHidden();
    await expect(page.getByRole('button', { name: 'Expand sidebar' })).toBeVisible();
    // The rail still reaches every view and the account menu (the switches live there).
    await page.getByRole('button', { name: 'Account menu' }).click();
    await expect(page.getByRole('dialog', { name: 'Account' }).getByRole('group', { name: 'Density' })).toBeVisible();
    await page.keyboard.press('Escape');

    await page.getByRole('button', { name: 'Expand sidebar' }).click();
    await expect(page.getByTestId('nav-sidebar')).toBeVisible();
    await page.reload();
    await expect(page.getByTestId('nav-sidebar')).toBeVisible();
    await expect(page.getByTestId('nav-rail')).toBeHidden();
  });

  test('a bad stored sidebar value leaves the sidebar expanded', async ({ page }) => {
    await page.addInitScript((key) => localStorage.setItem(key, 'true'), SIDEBAR_KEY);
    await page.goto('/#/tasks');
    await expect(page.getByTestId('nav-sidebar')).toBeVisible();
  });
});

test.describe('phone 390x844', () => {
  test.use({ viewport: PHONE, hasTouch: true });

  test('both switches are reachable from the account menu', async ({ page }) => {
    await page.goto('/#/tasks');
    await page.getByRole('button', { name: 'Account menu' }).click();
    const dialog = page.getByRole('dialog', { name: 'Account' });
    await expect(dialog.getByRole('group', { name: 'Density' })).toBeVisible();
    await expect(dialog.getByRole('group', { name: 'Theme' })).toBeVisible();
    await dialog.getByRole('radio', { name: 'Sparse' }).check({ force: true });
    await dialog.getByRole('radio', { name: 'Dark' }).check({ force: true });
    await expect(html(page)).toHaveAttribute('data-density', 'sparse');
    await expect(html(page)).toHaveAttribute('data-theme', 'dark');
    await page.keyboard.press('Escape');
    await page.reload();
    await expect(html(page)).toHaveAttribute('data-density', 'sparse');
    await expect(html(page)).toHaveAttribute('data-theme', 'dark');
  });
});

/** Every visible interactive element: its hit area and, for a field, its font size. */
async function measure(page: Page) {
  return page.evaluate(() => {
    const out: { target: string; width: number; height: number; fontSize: number; field: boolean }[] = [];
    for (const el of document.querySelectorAll('a, button, input, select, textarea, [role=button]')) {
      if (!el.checkVisibility({ visibilityProperty: true })) continue;
      const rect = el.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) continue;
      if (getComputedStyle(el).clipPath !== 'none') continue;
      const label = el.getAttribute('aria-label') ?? el.getAttribute('data-testid') ?? el.textContent?.trim().slice(0, 30) ?? '';
      out.push({
        target: `${el.tagName.toLowerCase()}[${label}]`,
        width: rect.width,
        height: rect.height,
        fontSize: parseFloat(getComputedStyle(el).fontSize),
        field: ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName),
      });
    }
    return out;
  });
}

for (const vp of [
  { name: 'phone', size: PHONE },
  { name: 'desktop', size: DESKTOP },
]) {
  for (const theme of ['light', 'dark'] as const) {
    for (const density of ['compact', 'sparse'] as const) {
      test.describe(`${vp.name} ${theme} ${density}`, () => {
        test.use({ viewport: vp.size, hasTouch: vp.name === 'phone' });

        test('Tasks passes axe' + (vp.name === 'phone' ? ', with 44 px targets and 16 px inputs' : ''), async ({ page, playwright, baseURL }) => {
          const slug = `e2e-density-axe-${vp.name}`;
          await seed(await apiContext(playwright, baseURL), slug);
          await page.addInitScript(
            ([themeKey, densityKey, t, d]) => {
              localStorage.setItem(themeKey, t);
              localStorage.setItem(densityKey, d);
            },
            ['vh.theme', DENSITY_KEY, theme, density],
          );
          await page.goto(`/#/tasks?initiative=${slug}`);
          await expect(page.getByTestId('task-ref').first()).toBeVisible();
          await expect(html(page)).toHaveAttribute('data-theme', theme);
          await expect(html(page)).toHaveAttribute('data-density', density);
          await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));

          const { violations } = await new AxeBuilder({ page }).include('[data-testid=tasks-view]').analyze();
          const blocking = violations
            .filter((v) => v.impact === 'serious' || v.impact === 'critical')
            .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
          expect(blocking, 'serious or critical axe violations').toEqual([]);

          if (vp.name === 'phone') {
            const found = await measure(page);
            expect(found.length).toBeGreaterThan(5);
            expect(found.filter((m) => m.width < 44 - SNAP || m.height < 44 - SNAP).map((m) => `${m.target} ${m.width}x${m.height}`), 'hit area under 44x44').toEqual([]);
            expect(found.filter((m) => m.field && m.fontSize < 16).map((m) => `${m.target} ${m.fontSize}px`), 'fields under 16px').toEqual([]);
          }
        });
      });
    }
  }
}
