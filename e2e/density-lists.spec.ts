import { expect, type Locator, type Page } from '@playwright/test';
import { test } from './fixtures';
import { openFold, seedFlight } from './now';

// R6: the shared density preference changes the real row height on every list view at desktop, Now and Timeline included
// (Compact, the default, fits more rows; Sparse is the roomy layout), the Timeline's bars stay on their lanes of the row,
// and a phone keeps its 44 px targets in both densities. Each test seeds an initiative of its own and filters to it.
const DENSITY_KEY = 'vh.density.v1';
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };
const SNAP = 1 / 32;

const height = async (row: Locator) => (await row.boundingBox())!.height;

/** The row's height under Compact, then (switched with the sidebar's radio, no reload) under Sparse. */
async function compactThenSparse(page: Page, row: Locator): Promise<{ compact: number; sparse: number }> {
  await expect(page.locator('html')).toHaveAttribute('data-density', 'compact');
  await expect(row).toBeVisible();
  const compact = await height(row);
  await page.getByTestId('display-controls-sidebar').getByRole('radio', { name: 'Sparse' }).check({ force: true });
  await expect(page.locator('html')).toHaveAttribute('data-density', 'sparse');
  await expect.poll(() => height(row), { message: 'the row takes the Sparse height' }).not.toBe(compact);
  return { compact, sparse: await height(row) };
}

test.describe('desktop 1440x900', () => {
  test.use({ viewport: DESKTOP });

  test('Now: a flight row is shorter in Compact than in Sparse', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-flight');
    await page.goto(`/#/now?initiative=${seed.slug}`);
    const { compact, sparse } = await compactThenSparse(page, page.locator(`[data-testid="flight-row"][data-task="${seed.live}"]`));
    expect(compact, `compact ${compact}px, sparse ${sparse}px`).toBeLessThan(sparse - 8);
    await seed.api.dispose();
  });

  test('Now: a Recently touched row is shorter in Compact than in Sparse', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-recent');
    await page.goto(`/#/now?initiative=${seed.slug}`);
    const { compact, sparse } = await compactThenSparse(page, page.locator(`[data-testid="recent-row"][data-task="${seed.ended}"]`));
    expect(compact, `compact ${compact}px, sparse ${sparse}px`).toBeLessThan(sparse - 8);
    await seed.api.dispose();
  });

  test('Now: an open ask in Needs me is shorter in Compact than in Sparse', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-ask');
    const response = await seed.api.post('/api/v1/asks', { data: { task: seed.live, question: 'Which density?' } });
    expect(response.status(), await response.text()).toBe(201);
    await page.addInitScript(() => {
      const open = { asks: true, expired: true, watches: true, done: true };
      localStorage.setItem('vh.now.layout.v1', JSON.stringify({ columns: [{ width: 0.5, widgets: ['asks', 'expired', 'watches', 'done'] }, { width: 0.5, widgets: ['flight', 'recent'] }], heights: {}, open }));
    });
    await page.goto(`/#/now?initiative=${seed.slug}`);
    const { compact, sparse } = await compactThenSparse(page, page.locator('[data-testid="attention-item"][data-section="open_asks"]').first());
    expect(compact, `compact ${compact}px, sparse ${sparse}px`).toBeLessThan(sparse - 8);
    await seed.api.dispose();
  });

  test('Timeline: a task row and an initiative row are shorter in Compact, and the bars stay on their lanes', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-tl');
    await page.goto(`/#/timeline?initiative=${seed.slug}`);
    const label = page.locator('.vis-labelset .vis-label.tl-task').filter({ hasText: seed.live }).first();
    const initiative = page.locator('.vis-labelset .vis-label.tl-initiative').first();
    await expect(initiative).toBeVisible();
    const lane = async () => {
      const bar = page.locator(`.vis-item.tl-actual[data-id="act:${seed.live}"]`);
      await expect(bar).toBeVisible();
      return { top: (await bar.boundingBox())!.y - (await label.boundingBox())!.y, barHeight: await height(bar), row: await height(label) };
    };
    await expect(label).toBeVisible();
    const compactLane = await lane();
    const compactInitiative = await height(initiative);
    expect(compactLane.row).toBeCloseTo(40, 0);
    expect(compactInitiative).toBeCloseTo(32, 0);
    expect(compactLane.top).toBeCloseTo(8, 0); // the actual span rides the row's lane
    expect(compactLane.top + compactLane.barHeight).toBeLessThanOrEqual(compactLane.row);

    await page.getByTestId('display-controls-sidebar').getByRole('radio', { name: 'Sparse' }).check({ force: true });
    await expect.poll(() => height(label)).toBeCloseTo(52, 0);
    expect(await height(initiative)).toBeCloseTo(44, 0);
    const sparseLane = await lane();
    expect(sparseLane.top).toBeCloseTo(12, 0);
    expect(sparseLane.top + sparseLane.barHeight).toBeLessThanOrEqual(sparseLane.row);

    await page.getByTestId('display-controls-sidebar').getByRole('radio', { name: 'Compact' }).check({ force: true });
    await expect.poll(() => height(label)).toBeCloseTo(40, 0); // and back: the chart follows the switch both ways
    await seed.api.dispose();
  });

  test('Timeline: the labels line up with their rows in Compact (every label sits under the one above)', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-tl-align');
    await page.goto(`/#/timeline?initiative=${seed.slug}`);
    await expect(page.locator('.vis-labelset .vis-label.tl-task')).toHaveCount(2);
    const boxes = await page.locator('.vis-labelset .vis-label').evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => ({ top: r.top, bottom: r.bottom })));
    for (let i = 1; i < boxes.length; i += 1) expect(Math.abs(boxes[i]!.top - boxes[i - 1]!.bottom)).toBeLessThanOrEqual(1);
    const groups = await page.locator('.vis-panel.vis-center .vis-foreground > .vis-group').evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height));
    expect(groups.map((h) => Math.round(h))).toEqual(boxes.map((b) => Math.round(b.bottom - b.top)));
    await seed.api.dispose();
  });

  test('Timeline: its controls keep a target of at least 24 px (WCAG 2.5.8) in Compact', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-tl-targets');
    await page.goto(`/#/timeline?initiative=${seed.slug}`);
    await expect(page.locator('html')).toHaveAttribute('data-density', 'compact');
    for (const id of ['zoom-in', 'zoom-out']) {
      const box = (await page.getByTestId(id).boundingBox())!;
      expect(box.width, id).toBeGreaterThanOrEqual(24);
      expect(box.height, id).toBeGreaterThanOrEqual(24);
    }
    const label = page.locator('.vis-labelset .vis-label.tl-task').first();
    expect(await height(label)).toBeGreaterThanOrEqual(24);
    await seed.api.dispose();
  });

  test('Initiatives: a row is shorter in Compact than in Sparse', async ({ page, playwright, baseURL }) => {
    const seed = await seedFlight(playwright, baseURL, 'dens-init');
    await page.goto(`/#/initiatives`);
    const row = page.getByTestId('initiative-row').filter({ hasText: `Flight dens-init` });
    const { compact, sparse } = await compactThenSparse(page, row);
    expect(compact, `compact ${compact}px, sparse ${sparse}px`).toBeLessThan(sparse);
    await seed.api.dispose();
  });
});

test.describe('phone 390x844', () => {
  test.use({ viewport: PHONE, hasTouch: true });

  for (const density of ['compact', 'sparse'] as const) {
    test(`${density}: the Timeline keeps its 52 px task and 44 px initiative rows`, async ({ page, playwright, baseURL }) => {
      await page.addInitScript(([key, d]) => localStorage.setItem(key!, d!), [DENSITY_KEY, density]);
      const seed = await seedFlight(playwright, baseURL, `dens-tl-phone-${density}`);
      await page.goto(`/#/timeline?initiative=${seed.slug}`);
      await expect(page.locator('html')).toHaveAttribute('data-density', density);
      const label = page.locator('.vis-labelset .vis-label.tl-task').filter({ hasText: seed.live }).first();
      await expect(label).toBeVisible();
      expect(await height(label)).toBeCloseTo(52, 0);
      expect(await height(page.locator('.vis-labelset .vis-label.tl-initiative').first())).toBeCloseTo(44, 0);
      await seed.api.dispose();
    });

    test(`${density}: Now keeps 44 px links and buttons`, async ({ page, playwright, baseURL }) => {
      await page.addInitScript(([key, d]) => localStorage.setItem(key!, d!), [DENSITY_KEY, density]);
      const seed = await seedFlight(playwright, baseURL, `dens-now-phone-${density}`);
      await page.goto(`/#/now?initiative=${seed.slug}`);
      await openFold(page, 'flight');
      const link = page.locator(`[data-testid="flight-row"][data-task="${seed.live}"]`).getByTestId('flight-task');
      await expect(link).toBeVisible();
      const box = (await link.boundingBox())!;
      expect(box.height).toBeGreaterThanOrEqual(44 - SNAP);
      await seed.api.dispose();
    });
  }
});
