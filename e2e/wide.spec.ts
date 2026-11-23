import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// R13 caps (SPEC 11, Wide layout): at 2560x1080 and 3440x1440 the single-table views keep their content at most 1280 px
// wide, centred in the main column (left and right margins equal within 2 px), the Timeline frame takes the whole main
// column, and long text (a task description) is held to 75 characters a line. Everything is measured by bounding box and
// computed style, never by pixels.
const viewports = [
  { name: 'wide', size: { width: 2560, height: 1080 } },
  { name: 'ultrawide', size: { width: 3440, height: 1440 } },
] as const;
const CAP = 1280;
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status = 201): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

function apiContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
}

// The content box of the main column: its bounding box less its padding.
async function mainColumn(page: Page) {
  return page.locator('#main').evaluate((el) => {
    const box = el.getBoundingClientRect();
    const style = getComputedStyle(el);
    const left = box.left + parseFloat(style.paddingLeft);
    const right = box.right - parseFloat(style.paddingRight);
    return { left, right, width: right - left };
  });
}

async function expectCappedAndCentred(page: Page, table: string) {
  const box = (await page.locator(table).boundingBox())!;
  const column = await mainColumn(page);
  expect(box.width).toBeLessThanOrEqual(CAP);
  // The column is wider than the cap, so a capped table leaves a margin on each side.
  expect(column.width).toBeGreaterThan(CAP);
  const left = box.x - column.left;
  const right = column.right - (box.x + box.width);
  expect(left).toBeGreaterThan(0);
  expect(Math.abs(left - right)).toBeLessThanOrEqual(2);
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the Initiatives table is at most 1280 px wide and centred in the main column', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.locator('section[aria-labelledby=initiatives-heading] table')).toBeVisible(FIVE_SECONDS);
      await expectCappedAndCentred(page, 'section[aria-labelledby=initiatives-heading] table');
    });

    test('the Archive table is at most 1280 px wide and centred in the main column', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-wide-arc-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Wide archive ${vp.name}` });
      const task = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Closed for the wide archive' });
      await post(api, `/api/v1/tasks/${task.ref}/close`, { resolution: 'dropped' }, 200);
      await page.goto('/#/archive?window=7');
      await expect(page.getByTestId('archive-view').locator('table')).toBeVisible(FIVE_SECONDS);
      await expectCappedAndCentred(page, '[data-testid=archive-view] table');
    });

    test('Notes, API keys and Actors keep their content at most 1280 px wide and centred', async ({ page }) => {
      for (const [hash, section] of [
        ['#/notes', '[data-testid=notes-view]'],
        ['#/actors', '[data-testid=actors-view]'],
        ['#/settings/keys', 'section[aria-labelledby=keys-heading]'],
      ]) {
        await page.goto(`/${hash}`);
        await expect(page.locator(section)).toBeVisible(FIVE_SECONDS);
        await expectCappedAndCentred(page, section);
      }
    });

    test('the Timeline frame is as wide as the main column', async ({ page }) => {
      await page.goto('/#/timeline');
      const chart = page.getByTestId('timeline-chart');
      await expect(chart).toBeVisible(FIVE_SECONDS);
      const box = (await chart.boundingBox())!;
      const column = await mainColumn(page);
      expect(column.width).toBeGreaterThan(CAP);
      expect(Math.abs(box.width - column.width)).toBeLessThanOrEqual(2);
      expect(Math.abs(box.x - column.left)).toBeLessThanOrEqual(2);
    });

    test('a long task description has a max-width of at most 75ch and wraps inside it', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-wide-desc-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Wide description ${vp.name}` });
      const words = Array.from({ length: 300 }, (_, i) => `word${i}`).join(' ');
      const task = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Long description', description: words });
      await page.goto(`/#/t/${task.ref}`);
      const description = page.getByTestId('task-description');
      await expect(description).toBeVisible(FIVE_SECONDS);
      // max-width is computed in px; one `ch` is the width of "0" in the element's own font.
      const measured = await description.evaluate((el) => {
        const probe = document.createElement('span');
        probe.style.cssText = 'position:absolute;visibility:hidden;width:1ch';
        el.appendChild(probe);
        const ch = probe.getBoundingClientRect().width;
        probe.remove();
        return { maxWidth: getComputedStyle(el).maxWidth, ch };
      });
      expect(measured.maxWidth).not.toBe('none');
      expect(parseFloat(measured.maxWidth)).toBeLessThanOrEqual(75 * measured.ch + 0.5);
      const width = (await description.boundingBox())!.width;
      expect(width).toBeLessThanOrEqual(75 * measured.ch + 0.5);
    });
  });
}
