import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Timeline's row labels from the keyboard (PRD R9, SPEC 11): every label is in the Tab order in row order with a
// visible focus ring, Enter on a task label opens its preview and Escape gives the focus back to that label, Space on an
// initiative label collapses or expands it and flips aria-expanded. Keyboard only, at the desktop viewport.
// Each test seeds two initiatives of its own and filters the view to the first.
test.use({ viewport: { width: 1440, height: 900 } });
const FIVE_SECONDS = { timeout: 5000 };
const DAY = 86_400_000;

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

function dateTask(slug: string, ref: string, createdAt: number) {
  const seq = Number(ref.slice(slug.length + 1));
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db.prepare('UPDATE tasks SET created_at = ? WHERE seq = ? AND initiative_id = (SELECT id FROM initiatives WHERE slug = ?)').run(createdAt, seq, slug);
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
}

/** An initiative of two tasks, dated back so the chart has bars, in a view filtered to it. */
async function seed(page: Page, playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  const slug = `e2e-tlk-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `tlk-${name}` },
  });
  const now = Date.now();
  const refs: string[] = [];
  await post(api, '/api/v1/initiatives', { slug, title: `Keys ${name}` }, 201);
  for (let i = 0; i < 2; i++) {
    const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: `Keys ${name} task ${i}` }, 201);
    dateTask(slug, ref, now - (4 - i) * DAY);
    refs.push(ref);
  }
  await api.dispose();
  await page.goto(`/#/timeline?initiative=${slug}`);
  await expect(labels(page).filter({ hasText: `Keys ${name} task` })).toHaveCount(2, FIVE_SECONDS);
  // vis-timeline's first redraw after it draws the rows is what makes them controls (operableLabels).
  await expect(labels(page).first()).toHaveAttribute('tabindex', '0', FIVE_SECONDS);
  await expect(page.locator('.vis-labelset .vis-label:not([tabindex])')).toHaveCount(0);
  return { slug, refs, tasks: [`Keys ${name} task 0`, `Keys ${name} task 1`] };
}

/**
 * Tab from the zoom button, just before the chart, to its first row label. vis-timeline settles its rows a moment after the
 * first draw (it takes them out of the page and puts them back in order, and a label focused then loses the focus), so the
 * move is made again until it holds: only a settled chart is asserted on.
 */
async function tabIntoChart(page: Page) {
  const first = page.locator('.vis-labelset .vis-label.tl-initiative');
  await expect(async () => {
    await page.getByTestId('zoom-in').focus();
    await page.keyboard.press('Tab');
    await expect(first).toBeFocused({ timeout: 500 });
  }).toPass(FIVE_SECONDS);
}

const labels = (page: Page) => page.locator('.vis-labelset .vis-label');
const initiativeLabel = (page: Page) => page.locator('.vis-labelset .vis-label.tl-initiative');
const texts = (page: Page) => labels(page).evaluateAll((els) => els.map((el) => (el.textContent ?? '').trim().replace(/\s+/g, ' ')));
const ring = (label: Locator) => label.evaluate((el) => ({ style: getComputedStyle(el).outlineStyle, width: getComputedStyle(el).outlineWidth }));

test('Tab reaches the initiative label, then its task labels in row order, each with a visible focus ring', async ({ page, playwright, baseURL }) => {
  const s = await seed(page, playwright, baseURL, 'tab');
  const initiative = initiativeLabel(page);
  await tabIntoChart(page);
  expect(await ring(initiative)).toEqual({ style: 'solid', width: '2px' });
  for (const title of s.tasks) {
    await page.keyboard.press('Tab');
    const task = labels(page).filter({ hasText: title });
    await expect(task).toBeFocused();
    expect(await ring(task)).toEqual({ style: 'solid', width: '2px' });
  }
  const rows = await texts(page);
  expect(rows).toHaveLength(3);
  expect(rows.slice(1).map((text, i) => text.endsWith(s.tasks[i]!))).toEqual([true, true]); // each task's label, in row order
  // The labels are the controls the keyboard works: each is a button, and only the initiative's says whether it is expanded.
  await expect(labels(page).filter({ hasText: s.tasks[0]! })).toHaveAttribute('role', 'button');
  await expect(labels(page).filter({ hasText: s.tasks[0]! })).not.toHaveAttribute('aria-expanded', /.*/);
});

test('Enter on a task label opens its preview and Escape returns the focus to that label', async ({ page, playwright, baseURL }) => {
  const s = await seed(page, playwright, baseURL, 'enter');
  await tabIntoChart(page); // the initiative
  await page.keyboard.press('Tab'); // its first task
  await page.keyboard.press('Tab'); // its second task
  const task = labels(page).filter({ hasText: s.tasks[1]! });
  await expect(task).toBeFocused();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: `Task ${s.refs[1]}` });
  await expect(dialog.getByTestId('task-title')).toHaveText(s.tasks[1]!, FIVE_SECONDS);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(task).toBeFocused();
  // Space opens it too, and Close hands the focus back the same way.
  await page.keyboard.press('Space');
  await expect(dialog.getByTestId('task-title')).toHaveText(s.tasks[1]!, FIVE_SECONDS);
  await dialog.getByTestId('preview-close').click();
  await expect(dialog).toHaveCount(0);
  await expect(task).toBeFocused();
});

test('Space on an initiative label collapses it, flips aria-expanded, and Enter expands it again with the labels in row order', async ({ page, playwright, baseURL }) => {
  const s = await seed(page, playwright, baseURL, 'space');
  await tabIntoChart(page);
  const initiative = initiativeLabel(page);
  await expect(initiative).toHaveAttribute('aria-expanded', 'true');
  await page.keyboard.press('Space');
  await expect(initiative).toHaveAttribute('aria-expanded', 'false', FIVE_SECONDS);
  await expect(labels(page).filter({ hasText: 'task' })).toHaveCount(0, FIVE_SECONDS);
  await expect(page.getByRole('dialog')).toHaveCount(0); // an initiative's label opens nothing
  await expect(initiative).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(initiative).toHaveAttribute('aria-expanded', 'true', FIVE_SECONDS);
  await expect(labels(page).filter({ hasText: 'task' })).toHaveCount(2, FIVE_SECONDS);
  await expect(initiative).toBeFocused();
  // The redrawn rows are in the Tab order where they are on screen: initiative, task 0, task 1.
  await page.keyboard.press('Tab');
  await expect(labels(page).filter({ hasText: s.tasks[0]! })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(labels(page).filter({ hasText: s.tasks[1]! })).toBeFocused();
});
