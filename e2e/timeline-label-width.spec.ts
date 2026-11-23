import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test } from './fixtures';

// The Timeline's label column on desktop (SPEC 11, R8): a separator beside it resizes it by a mouse drag or the arrow keys
// (Home, End), double click resets it, the width is held between a minimum and 60 % of the chart and remembered under
// `vh.timeline.labelwidth.v1`, and each row of the chart stays level with its label after a resize. A phone has no handle.
const desktop = { width: 1440, height: 900 };
const phone = { width: 390, height: 844 };
const KEY = 'vh.timeline.labelwidth.v1';
const FIVE_SECONDS = { timeout: 5000 };

async function post(api: APIRequestContext, path: string, data: object) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(201);
  return (await response.json()) as { ref: string };
}

async function seed(playwright: Parameters<Parameters<typeof test>[2]>[0]['playwright'], baseURL: string | undefined, name: string) {
  const slug = `e2e-tlw-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tlw-${name}` },
  });
  await post(api, '/api/v1/initiatives', { slug, title: `Label width ${name}` });
  const day = 86_400_000;
  const now = Date.now();
  for (const title of ['First row', 'Second row with a rather long title to be cut', 'Third row']) {
    await post(api, '/api/v1/tasks', {
      initiative: slug,
      title,
      planned_start: new Date(now - 2 * day).toISOString(),
      planned_end: new Date(now + 2 * day).toISOString(),
    });
  }
  await api.dispose();
  return slug;
}

const drawn = async (page: Page) => {
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-labelset .vis-label')).toHaveCount(4, FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
};

/** The width of the label panel, and every label's top and height beside the top and height of the chart row it names. */
function geometry(page: Page) {
  return page.evaluate(() => {
    const labels = [...document.querySelectorAll<HTMLElement>('.vis-labelset .vis-label')].map((el) => el.getBoundingClientRect());
    const groups = [...document.querySelectorAll<HTMLElement>('.vis-panel.vis-center .vis-itemset .vis-foreground .vis-group')].map((el) =>
      el.getBoundingClientRect(),
    );
    const panel = document.querySelector('.vis-panel.vis-left')!.getBoundingClientRect();
    const center = document.querySelector('.vis-panel.vis-center')!.getBoundingClientRect();
    const handle = document.querySelector('[data-testid="label-resizer"]')!.getBoundingClientRect();
    return {
      panel: panel.width,
      centerLeft: center.left,
      handleMid: handle.left + handle.width / 2,
      panelRight: panel.right,
      rows: labels.map((l, i) => ({ labelTop: l.top, labelHeight: l.height, rowTop: groups[i]?.top ?? NaN, rowHeight: groups[i]?.height ?? NaN })),
      rowCount: groups.length,
    };
  });
}

async function expectAligned(page: Page) {
  await expect
    .poll(async () => {
      const g = await geometry(page);
      return g.rowCount === g.rows.length && g.rows.every((r) => Math.abs(r.labelTop - r.rowTop) <= 1 && Math.abs(r.labelHeight - r.rowHeight) <= 1);
    }, FIVE_SECONDS)
    .toBe(true);
  // An item drawn in a row sits inside the vertical span of the label of its row.
  const inside = await page.evaluate(() => {
    const labels = [...document.querySelectorAll<HTMLElement>('.vis-labelset .vis-label')].map((el) => el.getBoundingClientRect());
    const items = [...document.querySelectorAll<HTMLElement>('.vis-item.tl-planned')];
    return {
      count: items.length,
      ok: items.every((item) => {
        const b = item.getBoundingClientRect();
        return labels.some((l) => b.top >= l.top - 1 && b.bottom <= l.bottom + 1);
      }),
    };
  });
  expect(inside.count).toBeGreaterThan(0);
  expect(inside.ok).toBe(true);
}

const stored = (page: Page) => page.evaluate((k) => localStorage.getItem(k), KEY);

test.describe('desktop 1440x900', () => {
  test.use({ viewport: desktop });

  test('a width dragged with the mouse survives a reload, and each row stays level with its label', async ({ page, playwright, baseURL }) => {
    const slug = await seed(playwright, baseURL, 'drag');
    await page.goto(`/#/timeline?initiative=${slug}`);
    await drawn(page);
    await expectAligned(page);
    const before = await geometry(page);
    const handle = page.getByTestId('label-resizer');
    await expect(handle).toHaveAttribute('role', 'separator');
    await expect(handle).toHaveAttribute('aria-valuenow', '300');
    const box = (await handle.boundingBox())!;
    expect(Math.abs(box.x + box.width / 2 - before.panelRight)).toBeLessThanOrEqual(2);

    const y = box.y + 80;
    await page.mouse.move(box.x + box.width / 2, y);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width / 2 + 40, y, { steps: 4 });
    await page.mouse.move(box.x + box.width / 2 - 60, y, { steps: 6 });
    await page.mouse.up();
    // 300 px dragged 60 px to the left of where it began: 240 px (the drag follows the pointer from its start).
    await expect(handle).toHaveAttribute('aria-valuenow', '240');
    expect(await stored(page)).toBe('240');
    await expect.poll(async () => Math.round((await geometry(page)).panel), FIVE_SECONDS).toBeLessThan(Math.round(before.panel));
    const dragged = await geometry(page);
    expect(Math.abs(dragged.panel - before.panel + 60)).toBeLessThanOrEqual(2);
    expect(Math.abs(dragged.handleMid - dragged.panelRight)).toBeLessThanOrEqual(2);
    await expectAligned(page);

    await page.reload();
    await drawn(page);
    await expect(handle).toHaveAttribute('aria-valuenow', '240');
    await expect.poll(async () => Math.round((await geometry(page)).panel), FIVE_SECONDS).toBe(Math.round(dragged.panel));
    await expectAligned(page);
  });

  test('arrow keys, Home and End resize it, double click resets it, and it stays between the minimum and 60 % of the chart', async ({ page, playwright, baseURL }) => {
    const slug = await seed(playwright, baseURL, 'keys');
    await page.goto(`/#/timeline?initiative=${slug}`);
    await drawn(page);
    const handle = page.getByTestId('label-resizer');
    await handle.focus();
    await handle.press('ArrowRight');
    await expect(handle).toHaveAttribute('aria-valuenow', '316');
    await handle.press('ArrowLeft');
    await handle.press('ArrowLeft');
    await expect(handle).toHaveAttribute('aria-valuenow', '284');
    expect(await stored(page)).toBe('284');
    await expectAligned(page);

    await handle.press('Home');
    await expect(handle).toHaveAttribute('aria-valuenow', '120');
    await handle.press('ArrowLeft');
    await expect(handle).toHaveAttribute('aria-valuenow', '120');
    await expect.poll(async () => Math.round((await geometry(page)).panel), FIVE_SECONDS).toBeLessThanOrEqual(121 + 20);
    await expectAligned(page);

    const chartWidth = (await page.getByTestId('timeline-chart').boundingBox())!.width;
    const widest = Math.floor(chartWidth * 0.6);
    await handle.press('End');
    await expect(handle).toHaveAttribute('aria-valuenow', String(widest));
    await handle.press('ArrowRight');
    await expect(handle).toHaveAttribute('aria-valuenow', String(widest));
    expect(Number(await stored(page))).toBe(widest);
    await expectAligned(page);

    // A drag far past the edge of the chart is held at the same share.
    const box = (await handle.boundingBox())!;
    await page.mouse.move(box.x + box.width / 2, box.y + 80);
    await page.mouse.down();
    await page.mouse.move(box.x + 600, box.y + 80, { steps: 5 });
    await page.mouse.up();
    await expect(handle).toHaveAttribute('aria-valuenow', String(widest));

    await handle.dblclick();
    await expect(handle).toHaveAttribute('aria-valuenow', '300');
    expect(await stored(page)).toBe('300');
    await expectAligned(page);
  });

  test('a bad stored width is the default', async ({ page, playwright, baseURL }) => {
    const slug = await seed(playwright, baseURL, 'bad');
    await page.addInitScript((k) => localStorage.setItem(k, 'wide'), KEY);
    await page.goto(`/#/timeline?initiative=${slug}`);
    await drawn(page);
    await expect(page.getByTestId('label-resizer')).toHaveAttribute('aria-valuenow', '300');
    await expectAligned(page);
  });
});

test.describe('phone 390x844', () => {
  test.use({ viewport: phone });
  test('has no resize handle and keeps its capped label column', async ({ page, playwright, baseURL }) => {
    const slug = await seed(playwright, baseURL, 'phone');
    await page.goto(`/#/timeline?initiative=${slug}`);
    await drawn(page);
    await expect(page.getByTestId('label-resizer')).toBeHidden();
    const panel = (await page.locator('.vis-panel.vis-left').boundingBox())!;
    expect(panel.width).toBeLessThanOrEqual(0.28 * phone.width + 16);
  });
});
