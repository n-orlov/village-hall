import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { BANDS } from '../web/src/lib/nowBands';

// The Now view's Needs me bands on wide screens (SPEC 11): the bands are bordered cards stacked in the §11 order, one per row,
// whatever the width, inside a block at most 2560 px wide and centred in the main column. Everything is measured by bounding
// box, never by pixels. The bands draw whether or not they hold items, so the spec needs no seeded data.
const FIVE_SECONDS = { timeout: 5000 };
const KEYS = BANDS.map((b) => b.key);

async function openNow(page: Page) {
  await page.goto('/#/now');
  await expect(page.getByTestId('band-toggle')).toHaveCount(KEYS.length, FIVE_SECONDS);
}

async function boxes(page: Page) {
  const out: { key: string; x: number; y: number; width: number; height: number }[] = [];
  for (const key of KEYS) out.push({ key, ...(await page.getByTestId(`band-${key}`).boundingBox())! });
  return out;
}

const cases = [
  { name: 'wide 2560x1080', size: { width: 2560, height: 1080 } },
  { name: 'ultrawide 3440x1440', size: { width: 3440, height: 1440 } },
  { name: 'desktop 1440x900', size: { width: 1440, height: 900 } },
] as const;

for (const c of cases) {
  test.describe(c.name, () => {
    test.use({ viewport: c.size });

    test('the bands stack one per row in band order, sharing one width', async ({ page }) => {
      await openNow(page);
      const list = await boxes(page);
      for (let n = 1; n < list.length; n++) {
        expect(list[n].y).toBeGreaterThanOrEqual(list[n - 1].y + list[n - 1].height);
        expect(Math.abs(list[n].x - list[0].x)).toBeLessThanOrEqual(2);
        expect(Math.abs(list[n].width - list[0].width)).toBeLessThanOrEqual(2);
      }
    });
  });
}

test.describe('wide 2560x1080 cards', () => {
  test.use({ viewport: { width: 2560, height: 1080 } });

  test('a band is a bordered card', async ({ page }) => {
    await openNow(page);
    const border = await page.getByTestId('band-asks').evaluate((el) => parseFloat(getComputedStyle(el).borderTopWidth));
    expect(border).toBeGreaterThanOrEqual(1);
  });
});

test.describe('ultrawide 3440x1440 cap', () => {
  test.use({ viewport: { width: 3440, height: 1440 } });

  test('the view (the docked columns) is at most 2560 px wide and centred in the main column', async ({ page }) => {
    await openNow(page);
    const block = (await page.getByTestId('now-view').boundingBox())!;
    expect(block.width).toBeLessThanOrEqual(2560);
    const bands = (await page.getByTestId('band-asks').boundingBox())!;
    const flight = (await page.getByTestId('now-flight').boundingBox())!;
    expect(bands.x).toBeGreaterThanOrEqual(block.x);
    expect(flight.x + flight.width).toBeLessThanOrEqual(block.x + block.width + 1);
    expect(flight.x).toBeGreaterThanOrEqual(bands.x + bands.width);
    const column = await page.locator('#main').evaluate((el) => {
      const box = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      return { left: box.left + parseFloat(style.paddingLeft), right: box.right - parseFloat(style.paddingRight) };
    });
    const left = block.x - column.left;
    const right = column.right - (block.x + block.width);
    expect(left).toBeGreaterThan(0);
    expect(Math.abs(left - right)).toBeLessThanOrEqual(2);
  });
});
