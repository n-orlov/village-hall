import { expect } from '@playwright/test';
import { test as base } from './fixtures';

// Signs in through the mock provider (no API key rides along), then reads what the page links:
// the manifest, the favicon and the apple-touch-icon. A second test fetches the six public assets
// in a fresh browser context that has no cookie and no key.
const test = base;

test('the page links a same-origin manifest and five icons that are served', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'Account menu' })).toBeVisible();

  const origin = new URL(page.url()).origin;
  const href = async (selector: string) => {
    const value = await page.locator(selector).getAttribute('href');
    expect(value, `no ${selector} in the page`).toBeTruthy();
    return new URL(value as string, page.url());
  };

  const manifestURL = await href('link[rel="manifest"]');
  expect(manifestURL.origin).toBe(origin);
  expect(await page.locator('link[rel="manifest"]').getAttribute('crossorigin')).toBeNull();
  const manifestResponse = await page.request.get(manifestURL.href);
  expect(manifestResponse.status()).toBe(200);
  expect(manifestResponse.headers()['content-type']).toContain('json');
  const manifest = await manifestResponse.json();

  expect(manifest.name).toBe('Village Hall');
  expect(manifest.short_name).toBe('Hall');
  await expect(page).toHaveTitle('Village Hall');
  expect(manifest.start_url).toBeTruthy();
  expect(manifest.start_url).not.toMatch(/^([a-z][a-z0-9+.-]*:|\/)/i); // relative
  expect(manifest.display).toBe('standalone');
  expect(manifest.theme_color).toBeTruthy();
  expect(manifest.background_color).toBeTruthy();

  const icons = manifest.icons as { src: string; sizes: string; type: string }[];
  const svg = icons.find((i) => i.type === 'image/svg+xml');
  const png192 = icons.find((i) => i.type === 'image/png' && i.sizes === '192x192');
  const png512 = icons.find((i) => i.type === 'image/png' && i.sizes === '512x512');
  expect(svg, 'manifest SVG icon').toBeTruthy();
  expect(png192, 'manifest 192 px icon').toBeTruthy();
  expect(png512, 'manifest 512 px icon').toBeTruthy();

  const urls = [
    new URL(svg!.src, manifestURL),
    new URL(png192!.src, manifestURL),
    new URL(png512!.src, manifestURL),
    await href('link[rel="apple-touch-icon"]'),
    await href('link[rel="icon"][type="image/svg+xml"]'),
  ];
  expect(urls).toHaveLength(5);
  for (const url of urls) {
    expect(url.origin).toBe(origin);
    const response = await page.request.get(url.href);
    expect(response.status(), url.pathname).toBe(200);
    expect(response.headers()['content-type'], url.pathname).toMatch(/^image\//);
  }

  // No service worker: the app is installable, not offline-capable.
  expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0);
});

test('the manifest and its icons load in a fresh browser context with no cookie', async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL });
  try {
    expect(await context.cookies()).toEqual([]);
    const assets: [string, RegExp][] = [
      ['manifest.json', /json/],
      ['icon-192.png', /^image\/png/],
      ['icon-512.png', /^image\/png/],
      ['icon.svg', /^image\/svg\+xml/],
      ['apple-touch-icon.png', /^image\/png/],
      ['favicon.svg', /^image\/svg\+xml/],
    ];
    for (const [name, type] of assets) {
      const response = await context.request.get(`/${name}`);
      expect(response.status(), name).toBe(200);
      expect(response.headers()['content-type'], name).toMatch(type);
    }
    // The exemption is for those six files only.
    expect((await context.request.get('/api/v1/tasks')).status()).toBe(401);
  } finally {
    await context.close();
  }
});
