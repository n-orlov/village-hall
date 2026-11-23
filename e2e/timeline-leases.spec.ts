import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test } from './fixtures';

// Overlapping leases stay readable and tappable (PRD R11, SPEC 11 "Lease slots"): on a task whose leases overlap, each
// lease takes a slot of its own below the actual span, stepping down so every bar has a strip no other bar's box covers,
// the row keeps its height (52 px in Sparse, 40 px in Compact), and leases that touch end to start share one slot. Three tasks are seeded, one per case: two
// overlapping leases, three where one lies inside another, two that touch end to start. At desktop.
test.use({ viewport: { width: 1440, height: 900 } });
const FIVE_SECONDS = { timeout: 5000 };
const HOUR = 3_600_000;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

/** Writes a lease's span into the e2e store (a lease starts now and cannot be dated through the API). */
function dateLease(ref: string, startedAt: number, endedAt: number) {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db
      .prepare('UPDATE leases SET started_at = ?, renewed_at = ?, expires_at = ?, ended_at = ? WHERE n = ?')
      .run(startedAt, startedAt, endedAt, endedAt, Number(ref.slice(1)));
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
}

/** The cases: each lease's start and end, in hours before now. The touching pair ends and starts at the same instant. */
const CASES = {
  overlap: { title: 'Two overlapping', spans: [[40, 20], [30, 10]] },
  nested: { title: 'One inside another', spans: [[40, 10], [32, 22], [26, 6]] },
  touch: { title: 'Touching', spans: [[40, 24], [24, 8]] },
} as const;
type CaseName = keyof typeof CASES;

interface Seeded {
  task: string;
  leases: string[];
}

async function seed(api: APIRequestContext, slug: string, now: number): Promise<Record<CaseName, Seeded>> {
  const seeded = {} as Record<CaseName, Seeded>;
  for (const name of Object.keys(CASES) as CaseName[]) {
    const task = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: CASES[name].title }, 201)).ref;
    const leases: string[] = [];
    for (const [n, [from, to]] of CASES[name].spans.entries()) {
      const started = await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent: `${name} ${n}`, resources: [`repo:${slug}-${name}-${n}`] }, 201);
      await post(api, `/api/v1/leases/${started.lease.ref}/end`, {}, 200);
      dateLease(started.lease.ref, now - from * HOUR, now - to * HOUR);
      leases.push(started.lease.ref);
    }
    seeded[name] = { task, leases };
  }
  return seeded;
}

interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}

const barOf = (page: Page, lease: string) => page.locator(`.vis-item.tl-lease[data-id="lease:${lease}"]`);

/** A point inside `box` that no box of `others` contains: the middle of the widest uncovered horizontal run of its top-to-bottom rows. */
function uniquePoint(box: Box, others: Box[]): { x: number; y: number } | undefined {
  const inside = (b: Box, x: number, y: number) => x >= b.x && x <= b.x + b.width && y >= b.y && y <= b.y + b.height;
  for (let y = box.y + 1; y < box.y + box.height; y += 1) {
    for (let x = box.x + 1; x < box.x + box.width; x += 2) {
      if (!others.some((o) => inside(o, x, y))) return { x, y };
    }
  }
  return undefined;
}

// The row is 52 px in Sparse and 40 px in Compact (the default) at desktop; the bars keep their strips in both.
for (const [density, rowHeight] of [['compact', 40], ['sparse', 52]] as const) {
test(`every lease bar of overlapping, nested and touching leases is visible and tappable on a ${density} row that stays ${rowHeight} px`, async ({ page, playwright, baseURL }) => {
  const slug = `e2e-tll-slots-${density}`;
  await page.addInitScript((d) => localStorage.setItem('vh.density.v1', d), density);
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'tll-slots' },
  });
  const now = Date.now();
  await post(api, '/api/v1/initiatives', { slug, title: 'Timeline lease slots' }, 201);
  const seeded = await seed(api, slug, now);

  await page.goto(`/#/timeline?initiative=${slug}`);
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-item.tl-lease')).toHaveCount(7, FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);

  for (const name of Object.keys(CASES) as CaseName[]) {
    const { task, leases } = seeded[name];
    const label = page.locator('.vis-labelset .vis-label.tl-task', { hasText: task });
    expect((await label.boundingBox())!.height, `${name}: the task row keeps its height`).toBeCloseTo(rowHeight, 0);

    const boxes: Box[] = [];
    for (const lease of leases) {
      await expect(barOf(page, lease), `${name} ${lease} is visible`).toBeVisible();
      boxes.push((await barOf(page, lease).boundingBox())!);
    }
    for (const [i, lease] of leases.entries()) {
      const others = boxes.filter((_, j) => j !== i);
      const point = uniquePoint(boxes[i]!, others);
      expect(point, `${name} ${lease} has an area no other lease bar covers`).toBeDefined();
      const hit = await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.closest('.vis-item')?.getAttribute('data-id'), point!);
      expect(hit, `${name}: the element at ${lease}'s own point`).toBe(`lease:${lease}`);
      await page.mouse.click(point!.x, point!.y);
      await expect(page.getByRole('dialog', { name: `Task ${task}` }), `${name}: a tap on ${lease} opens its task`).toBeVisible(FIVE_SECONDS);
      await page.keyboard.press('Escape');
      await expect(page.getByRole('dialog')).toHaveCount(0, FIVE_SECONDS);
    }
    // The touching pair shares a slot (one top), the overlapping ones do not.
    const tops = new Set(boxes.map((b) => Math.round(b.y)));
    expect(tops.size, `${name}: slots in use`).toBe(name === 'touch' ? 1 : name === 'nested' ? 3 : 2);
  }

  const rows = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.vis-labelset .vis-label.tl-task')].map((el) => el.getBoundingClientRect().height));
  expect(rows).toEqual([rowHeight, rowHeight, rowHeight]);
  await api.dispose();
});
}
