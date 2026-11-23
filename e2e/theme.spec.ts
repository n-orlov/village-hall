import { expect, type Page } from '@playwright/test';
import { test as base } from './fixtures';

// The theme setting (Auto, Light, Dark; Auto is the stored `system`) in the account menu, with the browser emulating a dark
// system. Signs in through the mock provider, so no API key rides along (see login.spec.ts).
const test = base;
test.use({ colorScheme: 'dark' });

const html = (page: Page) => page.locator('html');
const themeColor = (page: Page) => page.locator('meta[name="theme-color"]');
const background = (page: Page) => page.evaluate(() => getComputedStyle(document.body).backgroundColor);

async function choose(page: Page, name: 'Auto' | 'Light' | 'Dark') {
  await page.getByRole('button', { name: 'Account menu' }).click();
  const dialog = page.getByRole('dialog', { name: 'Account' });
  await dialog.getByRole('radio', { name }).check({ force: true });
  await expect(dialog.getByRole('radio', { name })).toBeChecked();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
}

test('Auto follows the dark system; Light overrides it, survives a reload and changes the background and theme-color', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Account menu' })).toBeVisible();

  // Auto (the default): the emulated dark preference.
  await expect(html(page)).toHaveAttribute('data-theme', 'dark');
  const darkBackground = await background(page);
  const darkColor = await themeColor(page).getAttribute('content');

  await choose(page, 'Light');
  await expect(html(page)).toHaveAttribute('data-theme', 'light');
  const lightBackground = await background(page);
  const lightColor = await themeColor(page).getAttribute('content');
  expect(lightBackground).not.toBe(darkBackground);
  expect(lightColor).not.toBe(darkColor);

  // The choice is persisted: the reload paints light although the system is dark.
  await page.reload();
  await expect(html(page)).toHaveAttribute('data-theme', 'light');
  expect(await background(page)).toBe(lightBackground);
  await expect(themeColor(page)).toHaveAttribute('content', lightColor!);

  // The control shows the stored setting, and Dark and Auto switch back.
  await page.getByRole('button', { name: 'Account menu' }).click();
  await expect(page.getByRole('dialog', { name: 'Account' }).getByRole('radio', { name: 'Light' })).toBeChecked();
  await page.keyboard.press('Escape');
  await choose(page, 'Dark');
  await expect(html(page)).toHaveAttribute('data-theme', 'dark');
  await expect(themeColor(page)).toHaveAttribute('content', darkColor!);
  await choose(page, 'Light');
  await choose(page, 'Auto');
  await expect(html(page)).toHaveAttribute('data-theme', 'dark');
  expect(await background(page)).toBe(darkBackground);
});

test('Auto follows a change of the system preference without a reload', async ({ page }) => {
  await page.goto('/');
  await expect(html(page)).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(html(page)).toHaveAttribute('data-theme', 'light');
  await expect(themeColor(page)).not.toHaveAttribute('content', '#14161a');
});

test('an unknown stored value falls back to Auto before first paint', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('vh.theme', 'neon'));
  await page.goto('/');
  await expect(html(page)).toHaveAttribute('data-theme', 'dark');
});
