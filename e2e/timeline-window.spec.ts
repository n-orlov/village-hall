import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { logicViewports } from './viewports';

// The Timeline's window lives in the URL fragment (SPEC 11, R3): a settled pan or zoom rewrites `from` and `to` in place
// (replaceState, so history.length does not grow), a reload opens on the same span, a fragment that names a window opens
// on it, and the window is never saved: vh.view.v1.timeline holds the filters alone.
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;
const KEY = 'vh.view.v1.timeline';

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<string> {
  const slug = `e2e-tlw-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tlw-${name}` },
  });
  await post(api, '/api/v1/initiatives', { slug, title: `Timeline window ${name}` }, 201);
  await post(api, '/api/v1/tasks', { initiative: slug, title: 'Task A' }, 201);
  await api.dispose();
  return slug;
}

const drawn = async (page: Page) => {
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-item.tl-actual')).toHaveCount(1, FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
};

/** The window the chart shows, from the readout the view keeps. */
const shown = (page: Page) =>
  page.getByTestId('timeline-span').evaluate((el) => ({ from: Number(el.getAttribute('data-from-ms')), to: Number(el.getAttribute('data-to-ms')) }));

/** The window the fragment names, or undefined when it names none. */
const fragmentWindow = (page: Page) =>
  page.evaluate(() => {
    const query = new URLSearchParams(window.location.hash.split('?')[1] ?? '');
    const from = query.get('from');
    const to = query.get('to');
    return from === null || to === null ? undefined : { from: Date.parse(from), to: Date.parse(to), keys: [...query.keys()] };
  });

const saved = (page: Page) => page.evaluate((key) => window.localStorage.getItem(key), KEY);
const historyLength = (page: Page) => page.evaluate(() => window.history.length);

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a settled zoom puts the new window in the fragment without a history entry, a reload opens on the same span, and nothing but the filters is saved', async ({ page, playwright, baseURL }) => {
      const slug = await seed(playwright, baseURL, `zoom-${vp.name}`);
      await page.goto(`/#/timeline?initiative=${slug}`);
      await drawn(page);
      expect(await fragmentWindow(page)).toBeUndefined(); // a fresh visit keeps the default window out of the URL
      const entries = await historyLength(page);
      const before = await shown(page);

      await page.getByTestId('zoom-in').click();
      await expect.poll(async () => (await fragmentWindow(page))?.to ?? 0, FIVE_SECONDS).not.toBe(0);
      await expect.poll(async () => {
        const w = await shown(page);
        const f = (await fragmentWindow(page))!;
        return Math.abs(f.from - w.from) < 1500 && Math.abs(f.to - w.to) < 1500;
      }, FIVE_SECONDS).toBe(true);
      const zoomed = (await fragmentWindow(page))!;
      expect(zoomed.to - zoomed.from).toBeLessThan((before.to - before.from) * 0.9); // zoomed in
      expect(zoomed.keys).toEqual(['initiative', 'from', 'to']);
      expect(await historyLength(page)).toBe(entries);

      // The saved query is the filter alone: no window.
      expect(JSON.parse((await saved(page))!)).toEqual({ v: 1, query: `initiative=${slug}` });

      await page.reload();
      await drawn(page);
      const reopened = await shown(page);
      expect(Math.abs(reopened.from - zoomed.from)).toBeLessThan(1500);
      expect(Math.abs(reopened.to - zoomed.to)).toBeLessThan(1500);
      expect(Math.abs(reopened.to - reopened.from - (zoomed.to - zoomed.from))).toBeLessThan(1500);
      expect(JSON.parse((await saved(page))!)).toEqual({ v: 1, query: `initiative=${slug}` });
    });

    test('a fragment that names a window opens on it, and a bare Timeline link opens on the default window, not on a saved one', async ({ page, playwright, baseURL }) => {
      const slug = await seed(playwright, baseURL, `link-${vp.name}`);
      const from = new Date(Date.now() - 3 * DAY);
      const to = new Date(Date.now() + 2 * DAY);
      const text = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, 'Z');
      await page.goto(`/#/timeline?initiative=${slug}&from=${text(from)}&to=${text(to)}`);
      await drawn(page);
      const w = await shown(page);
      expect(Math.abs(w.from - Math.floor(from.getTime() / 1000) * 1000)).toBeLessThan(1500);
      expect(Math.abs(w.to - Math.floor(to.getTime() / 1000) * 1000)).toBeLessThan(1500);
      expect(JSON.parse((await saved(page))!)).toEqual({ v: 1, query: `initiative=${slug}` });

      // Leaving and coming back by the bare link restores the filters and the default window of three weeks.
      await page.goto('/#/tasks');
      await expect(page.getByTestId('tasks-view')).toBeVisible();
      await page.goto('/#/timeline');
      await drawn(page);
      const back = await shown(page);
      expect(Math.round((back.to - back.from) / DAY)).toBe(21);
      expect(await fragmentWindow(page)).toBeUndefined();
      expect(await page.evaluate(() => window.location.hash)).toBe(`#/timeline?initiative=${slug}`);
    });
  });
}
