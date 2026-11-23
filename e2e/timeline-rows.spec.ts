import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Timeline's rows hold still (SPEC 11, R4): the view asks `GET /timeline` for rows=all, so the rows of a filter are
// the same however the chart is panned or zoomed, and every row has a fixed height (task 52 px, initiative 44 px; 40 and 32 in Compact at desktop) with a
// fixed lane per kind of item (planned on top, the actual span with its markers, the leases below), so nothing moves
// vertically. The seeded initiative has tasks whose spans lie inside the opening window and outside it.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

function store<T>(work: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    return work(db);
  } finally {
    db.close();
  }
}

/** Dates a task back (the e2e store is written to move history): its actual span becomes createdAt to closedAt. */
function dateTask(slug: string, ref: string, createdAt: number, closedAt: number | null) {
  const seq = Number(ref.slice(slug.length + 1));
  const changed = store((db) =>
    db
      .prepare('UPDATE tasks SET created_at = ?, closed_at = COALESCE(?, closed_at) WHERE seq = ? AND initiative_id = (SELECT id FROM initiatives WHERE slug = ?)')
      .run(createdAt, closedAt, seq, slug),
  );
  expect(changed.changes).toBe(1);
}

interface Seed {
  slug: string;
  refs: string[];
  api: APIRequestContext;
}

/**
 * Five tasks: R1 with a planned bar, a live lease and a milestone, all in the opening window; R2 open for a week; R3
 * planned for a month ahead; R4 and R5 closed 80 and 60 days ago, so their spans lie outside the opening window.
 */
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const slug = `e2e-tlr-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tlr-${name}` },
  });
  const now = Date.now();
  await post(api, '/api/v1/initiatives', { slug, title: `Timeline rows ${name}` }, 201);
  const task = async (title: string, extra: object = {}) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201)).ref;
  const r1 = await task('Row one', { planned_start: new Date(now - 5 * DAY).toISOString(), planned_end: new Date(now + 3 * DAY).toISOString() });
  const r2 = await task('Row two');
  const r3 = await task('Row three', { planned_start: new Date(now + 30 * DAY).toISOString(), planned_end: new Date(now + 40 * DAY).toISOString() });
  const r4 = await task('Row four');
  const r5 = await task('Row five');
  await post(api, `/api/v1/tasks/${r1}/milestones`, { summary: 'Halfway there' }, 200);
  await post(api, '/api/v1/leases', { task: r1, intent: 'holding row one', resources: [`repo:tlr-${name}`] }, 201);
  await post(api, `/api/v1/tasks/${r4}/close`, { resolution: 'dropped' }, 200);
  await post(api, `/api/v1/tasks/${r5}/close`, { resolution: 'dropped' }, 200);
  dateTask(slug, r1, now - 6 * DAY, null);
  dateTask(slug, r2, now - 7 * DAY, null);
  dateTask(slug, r3, now - 1 * DAY, null);
  dateTask(slug, r4, now - 90 * DAY, now - 80 * DAY);
  dateTask(slug, r5, now - 70 * DAY, now - 60 * DAY);
  return { slug, refs: [r1, r2, r3, r4, r5], api };
}

/** The GET /timeline requests the page makes. */
function timelineReads(page: Page): URL[] {
  const reads: URL[] = [];
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (request.method() === 'GET' && url.pathname.endsWith('/api/v1/timeline')) reads.push(url);
  });
  return reads;
}

const shown = (page: Page) =>
  page.getByTestId('timeline-span').evaluate((el) => ({ from: Number(el.getAttribute('data-from-ms')), to: Number(el.getAttribute('data-to-ms')) }));

interface Layout {
  /** The row labels in order with each row's top offset in the label column and its height. */
  rows: { label: string; top: number; height: number; initiative: boolean }[];
  /** Every drawn item's top offset from the top of its row, by kind. */
  lanes: Record<string, { top: number; height: number }[]>;
}

/** Reads the rows and the lanes of the chart from the DOM. */
function layout(page: Page): Promise<Layout> {
  return page.evaluate(() => {
    const column = document.querySelector('.vis-panel.vis-left')!.getBoundingClientRect().top;
    const labels = [...document.querySelectorAll<HTMLElement>('.vis-labelset .vis-label')];
    const rows = labels.map((el) => {
      const box = el.getBoundingClientRect();
      return { label: (el.textContent ?? '').trim(), top: box.top - column, height: box.height, initiative: el.classList.contains('tl-initiative') };
    });
    const kinds = ['planned', 'actual', 'lease', 'marker'];
    const lanes: Layout['lanes'] = {};
    const tops = new Map(labels.map((el) => [el.getBoundingClientRect().top, el]));
    for (const kind of kinds) {
      lanes[kind] = [...document.querySelectorAll<HTMLElement>(`.vis-item.tl-${kind}:not(.vis-dot)`)].map((item) => {
        const box = item.getBoundingClientRect();
        const row = [...tops.keys()].filter((t) => t <= box.top + 1).sort((a, b) => b - a)[0] ?? 0;
        return { top: box.top - row, height: box.height };
      });
    }
    return { rows, lanes };
  });
}

const drawn = async (page: Page) => {
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-labelset .vis-label')).toHaveCount(6, FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
};

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the rows, their order and every offset are the same after a pan far past every span, a zoom in and a zoom out, and every read asks for rows=all', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `hold-${vp.name}`);
      const reads = timelineReads(page);
      await page.goto(`/#/timeline?initiative=${s.slug}&closed=90`);
      await drawn(page);
      await expect.poll(() => reads.length, FIVE_SECONDS).toBeGreaterThanOrEqual(1);
      const before = await layout(page);
      expect(before.rows.map((r) => r.label)).toEqual([
        expect.stringContaining(`Timeline rows hold-${vp.name}`),
        ...s.refs.map((ref, i) => expect.stringContaining(`${ref} Row ${['one', 'two', 'three', 'four', 'five'][i]}`)),
      ]);
      // The closed tasks' spans lie outside the window, yet their rows are there; so is R3's planned span, ahead of it.
      const opening = await shown(page);
      expect(opening.from).toBeGreaterThan(Date.now() - 20 * DAY);

      // Pan far past every span: the sideways wheel, until the window starts more than 100 days ahead.
      await page.getByTestId('timeline-chart').scrollIntoViewIfNeeded();
      const box = (await page.locator('.vis-panel.vis-center').boundingBox())!;
      await page.mouse.move(box.x + box.width / 2, Math.min(box.y + 60, vp.size.height - 60));
      await expect
        .poll(
          async () => {
            await page.mouse.wheel(4000, 0);
            return (await shown(page)).from;
          },
          { timeout: 20_000 },
        )
        .toBeGreaterThan(Date.now() + 100 * DAY);
      const readsPanned = reads.length;
      await expect.poll(() => reads.length, FIVE_SECONDS).toBeGreaterThanOrEqual(2);
      await page.waitForTimeout(700); // the reads of the settled pan have come back
      await expect.poll(async () => JSON.stringify((await layout(page)).rows), FIVE_SECONDS).toBe(JSON.stringify(before.rows));
      expect((await layout(page)).rows).toEqual(before.rows);

      await page.getByTestId('zoom-in').click();
      await expect.poll(() => reads.length, FIVE_SECONDS).toBeGreaterThan(readsPanned);
      await page.waitForTimeout(700);
      expect((await layout(page)).rows).toEqual(before.rows);

      const readsZoomed = reads.length;
      await page.getByTestId('zoom-out').click();
      await expect.poll(() => reads.length, FIVE_SECONDS).toBeGreaterThan(readsZoomed);
      await page.waitForTimeout(700);
      const after = await layout(page);
      expect(after.rows.map((r) => r.label)).toEqual(before.rows.map((r) => r.label));
      for (const [i, row] of after.rows.entries()) {
        expect(Math.abs(row.top - before.rows[i]!.top)).toBeLessThanOrEqual(1);
        expect(Math.abs(row.height - before.rows[i]!.height)).toBeLessThanOrEqual(1);
      }

      expect(reads.length).toBeGreaterThanOrEqual(4);
      for (const url of reads) expect(url.searchParams.get('rows')).toBe('all');
      await s.api.dispose();
    });

    // Sparse, and any phone, draw a task row of 52 px and an initiative row of 44 px; Compact (the default) at desktop 40 and 32.
    for (const density of ['compact', 'sparse'] as const) {
      const [taskRow, initiativeRow] = density === 'compact' && vp.name === 'desktop' ? [40, 32] : [52, 44];
    test(`${density}: a task row is ${taskRow} px and an initiative row ${initiativeRow} px, and the planned strip, the actual span with its marker and the lease each keep their lane`, async ({ page, playwright, baseURL }) => {
      await page.addInitScript((d) => localStorage.setItem('vh.density.v1', d), density);
      const s = await seed(playwright, baseURL, `lane-${density}-${vp.name}`);
      await page.goto(`/#/timeline?initiative=${s.slug}&closed=90`);
      await drawn(page);
      await expect(page.locator('.vis-item.tl-lease')).toHaveCount(1, FIVE_SECONDS);
      await expect(page.locator('.vis-item.tl-planned')).toHaveCount(1, FIVE_SECONDS); // R1's strip is in the window; R3's, a month ahead, is not drawn
      const { rows, lanes } = await layout(page);
      expect(rows.map((r) => r.height)).toEqual([initiativeRow, ...Array(5).fill(taskRow)]);
      expect(rows.map((r) => r.top - rows[0]!.top)).toEqual(Array.from({ length: 6 }, (_, n) => (n === 0 ? 0 : initiativeRow + (n - 1) * taskRow))); // each row starts where the one above ends
      expect(rows.map((r) => r.initiative)).toEqual([true, false, false, false, false, false]);

      const planned = lanes.planned![0]!;
      const actual = lanes.actual![0]!;
      const lease = lanes.lease![0]!;
      const marker = lanes.marker![0]!;
      expect(planned.top + planned.height).toBeLessThanOrEqual(actual.top + 1); // planned on top
      expect(actual.top + actual.height).toBeLessThanOrEqual(lease.top + 1); // leases below the actual span
      expect(marker.top).toBeCloseTo(actual.top, 0); // markers ride the actual span's lane
      expect(lease.top + lease.height).toBeLessThanOrEqual(taskRow);
      await s.api.dispose();
    });
    }
  });
}
