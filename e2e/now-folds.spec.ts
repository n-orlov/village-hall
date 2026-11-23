import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { BANDS_KEY, LAYOUT_KEY, band, seedFlight, seedNeedsMe } from './now';

// The narrow Now view folds In flight and Recently touched like the Needs me bands (PRD R1, R4, SPEC 11): counted accordion
// headers; only a nonempty Open asks or Expired leases and conflicts starts open, so In flight and Recently touched start
// closed however much they hold; and a choice that is remembered under `vh.now.bands.v1` apart from the wide choices and
// the desktop dock's own layout. Each test seeds an initiative of its own.
const FIVE_SECONDS = { timeout: 5000 };
const PHONE = { width: 390, height: 844 };
const TABLET = { width: 900, height: 900 };
const DESKTOP = { width: 1440, height: 900 };

const flight = (page: Page) => page.getByTestId('now-flight');
const recent = (page: Page) => page.getByTestId('now-recent');
const toggle = (page: Page, which: 'flight' | 'recent') => (which === 'flight' ? flight(page) : recent(page)).getByTestId('widget-toggle');
const stored = (page: Page, key: string) => page.evaluate((k) => JSON.parse(localStorage.getItem(k) ?? 'null') as unknown, key);

test.describe('phone 390x844', () => {
  test.use({ viewport: PHONE });

  test('In flight and Recently touched are accordion cards with a count, both closed though they hold something', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'fold-phone');
    await s.api.dispose();
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('flight-count')).toHaveText('1', FIVE_SECONDS);
    await expect(page.getByTestId('recent-count')).toHaveText('1', FIVE_SECONDS);

    await expect(toggle(page, 'flight')).toHaveText(/^\s*In flight\s*1\s*$/);
    await expect(toggle(page, 'recent')).toHaveText(/^\s*Recently touched\s*1\s*$/);
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false');
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'false');
    await expect(flight(page).getByTestId('flight-row')).toBeHidden();
    await expect(recent(page).getByTestId('recent-row')).toBeHidden();
    await expect(page.getByTestId('now-window')).toBeHidden();
    for (const which of ['flight', 'recent'] as const) {
      expect((await toggle(page, which).boundingBox())!.height).toBeGreaterThanOrEqual(44);
      const panel = await toggle(page, which).getAttribute('aria-controls');
      expect(panel).toBe(`band-panel-${which}`);
      await expect(page.locator(`#${panel}`)).toHaveAttribute('role', 'region');
      await expect(page.locator(`#${panel}`)).toHaveAttribute('aria-labelledby', `band-toggle-${which}`);
    }

    await toggle(page, 'flight').click();
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'true');
    await expect(flight(page).getByTestId('flight-row')).toBeVisible();
    await toggle(page, 'recent').click();
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'true');
    await expect(recent(page).getByTestId('recent-row')).toBeVisible();
    await expect(page.getByTestId('now-window')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });

  test('opening and closing the right-column parts is remembered across a reload, apart from the dock layout', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'reload-phone');
    await s.api.dispose();
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('flight-count')).toHaveText('1', FIVE_SECONDS);
    await toggle(page, 'flight').click(); // open
    await expect(flight(page).getByTestId('flight-row')).toBeVisible();
    await expect(recent(page).getByTestId('recent-row')).toBeHidden();
    expect(await stored(page, BANDS_KEY)).toEqual({ wide: {}, narrow: { flight: true } });
    expect(await stored(page, LAYOUT_KEY)).toBeNull();

    await page.reload();
    await expect(flight(page).getByTestId('flight-row')).toBeVisible(FIVE_SECONDS);
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'true');
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'false');
    await expect(recent(page).getByTestId('recent-row')).toBeHidden();

    // In flight closed and Recently touched opened by the keyboard, and the new choices survive a second reload.
    await toggle(page, 'flight').focus();
    await page.keyboard.press('Enter');
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false');
    await toggle(page, 'recent').focus();
    await page.keyboard.press('Space');
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'true');
    expect(await stored(page, BANDS_KEY)).toEqual({ wide: {}, narrow: { flight: false, recent: true } });
    await page.reload();
    await expect(recent(page).getByTestId('recent-row')).toBeVisible(FIVE_SECONDS);
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false');
    await expect(flight(page).getByTestId('flight-row')).toBeHidden();
    expect(await stored(page, LAYOUT_KEY)).toBeNull();
  });

  test('with nothing in flight and nothing touched both cards start closed, counted 0', async ({ page, playwright, baseURL }) => {
    const api = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'now-fold-quiet' } });
    expect((await api.post('/api/v1/initiatives', { data: { slug: 'e2e-now-fold-quiet', title: 'Quiet' } })).status()).toBe(201);
    await api.dispose();
    await page.goto('/#/now?initiative=e2e-now-fold-quiet');
    await expect(page.getByTestId('flight-count')).toHaveText('0', FIVE_SECONDS);
    await expect(page.getByTestId('recent-count')).toHaveText('0');
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false');
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'false');
    await toggle(page, 'flight').click();
    await expect(page.getByTestId('flight-empty')).toBeVisible();
    await toggle(page, 'recent').click();
    await expect(page.getByTestId('recent-empty')).toBeVisible();
  });

  test('with something in every part only Open asks and Expired leases and conflicts start open', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'fold-every');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('band-count')).toHaveText(['1', '1', '1', '1'], FIVE_SECONDS);
    await expect(page.getByTestId('flight-count')).toHaveText('2', FIVE_SECONDS); // the two conflicting leases
    const expanded = page.locator('[data-testid=band-toggle], [data-testid=widget-toggle]');
    await expect(expanded).toHaveCount(6);
    expect(await expanded.evaluateAll((all) => all.map((el) => el.getAttribute('aria-expanded')))).toEqual(['true', 'true', 'false', 'false', 'false', 'false']);
    await expect(band(page, 'watches').getByTestId('attention-item')).toBeHidden();
    await expect(flight(page).getByTestId('flight-row').first()).toBeHidden();
  });

  test('a saved wide choice and a narrow Recently touched choice leave a nonempty In flight closed on the phone', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'fold-saved');
    await s.api.dispose();
    await page.addInitScript((key) => localStorage.setItem(key, JSON.stringify({ wide: { flight: true, recent: false }, narrow: { recent: true } })), BANDS_KEY);
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('flight-count')).toHaveText('1', FIVE_SECONDS);
    await expect(recent(page).getByTestId('recent-row')).toBeVisible(FIVE_SECONDS);
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false');
    await expect(flight(page).getByTestId('flight-row')).toBeHidden();
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'true');
  });

  for (const theme of ['light', 'dark'] as const) {
    test(`the folded view is axe clean, ${theme}`, async ({ page, playwright, baseURL }) => {
      const s = await seedFlight(playwright, baseURL, `axe-${theme}`);
      await s.api.dispose();
      await page.emulateMedia({ colorScheme: theme });
      await page.addInitScript((key) => localStorage.setItem(key, JSON.stringify({ wide: {}, narrow: { flight: true, recent: true } })), BANDS_KEY);
      await page.goto(`/#/now?initiative=${s.slug}`);
      await expect(flight(page).getByTestId('flight-row')).toBeVisible(FIVE_SECONDS);
      await expect(recent(page).getByTestId('recent-row')).toBeVisible(FIVE_SECONDS);
      const results = await new AxeBuilder({ page }).include('[data-testid=now-view]').analyze();
      expect(results.violations).toEqual([]);
    });
  }

  test('a narrow choice stays on the phone, and the dock keeps its own open state', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'apart-phone');
    await s.api.dispose();
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('flight-count')).toHaveText('1', FIVE_SECONDS);
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false'); // narrow default
    await toggle(page, 'recent').click(); // narrow: opened
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'true');

    await page.setViewportSize(DESKTOP); // docked: both open by default, neither the narrow default nor choice applied
    await expect(flight(page).getByTestId('flight-row')).toBeVisible(FIVE_SECONDS);
    await expect(recent(page).getByTestId('recent-row')).toBeVisible();
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'true');
    await toggle(page, 'recent').click(); // dock: closed
    expect(await stored(page, LAYOUT_KEY)).toMatchObject({ open: { recent: false } });

    await page.setViewportSize(PHONE);
    await expect(toggle(page, 'flight')).toHaveAttribute('aria-expanded', 'false');
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'true');
    expect(await stored(page, BANDS_KEY)).toEqual({ wide: {}, narrow: { recent: true } });
  });
});

test.describe('tablet 900x900 (stacked, wide choices)', () => {
  test.use({ viewport: TABLET });

  test('both cards start open, and a choice here is the wide one', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'fold-tablet');
    await s.api.dispose();
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(flight(page).getByTestId('flight-row')).toBeVisible(FIVE_SECONDS);
    await expect(recent(page).getByTestId('recent-row')).toBeVisible();
    await toggle(page, 'recent').click();
    await expect(recent(page).getByTestId('recent-row')).toBeHidden();
    expect(await stored(page, BANDS_KEY)).toEqual({ wide: { recent: false }, narrow: {} });
    await page.reload();
    await expect(flight(page).getByTestId('flight-row')).toBeVisible(FIVE_SECONDS);
    await expect(toggle(page, 'recent')).toHaveAttribute('aria-expanded', 'false');
  });
});
