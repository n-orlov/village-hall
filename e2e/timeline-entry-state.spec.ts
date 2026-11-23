import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test } from './fixtures';

// The Timeline's collapsed initiatives and its rows' scroll belong to the history entry they were displayed on (PRD R7, R8):
// a Back, Forward or reload that comes before the chart's own deferred reports (the 100 ms scroll debounce, the redraw after
// a collapse) still saves them to the entry being left, never to the one being shown, and shows each entry as it was left.
// Every test seeds initiatives of its own (slugs start with `e2e-tes-`) and filters the view to them.
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

const taskRows = (page: Page) => page.locator('.vis-labelset .vis-label.tl-task');
const initiativeLabel = (page: Page, title: string) => page.locator('.vis-labelset .vis-label.tl-initiative', { hasText: title });
const rowsScroll = (page: Page) => page.locator('.vis-panel.vis-left').evaluate((el) => Math.round(el.scrollTop));

async function api(playwright: { request: { newContext: (o: object) => Promise<APIRequestContext> } }, baseURL: string | undefined, actor: string) {
  return playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor } });
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('two Timeline entries each keep their own collapse over a link, Back and Forward, and a fresh entry opens expanded', async ({ page, playwright, baseURL }) => {
      const first = `e2e-tes-${vp.name}-a`;
      const second = `e2e-tes-${vp.name}-b`;
      const client = await api(playwright, baseURL, `tes-${vp.name}`);
      const now = Date.now();
      for (const [slug, title] of [[first, `Entry ${vp.name} first`], [second, `Entry ${vp.name} second`]] as const) {
        await post(client, '/api/v1/initiatives', { slug, title }, 201);
        for (let i = 0; i < 2; i++) {
          const { ref } = await post<{ ref: string }>(client, '/api/v1/tasks', { initiative: slug, title: `${title} task ${i}` }, 201);
          dateTask(slug, ref, now - (4 - i) * DAY);
        }
      }
      const label = (which: 'first' | 'second') => initiativeLabel(page, `Entry ${vp.name} ${which}`);
      const state = async (firstCollapsed: boolean, secondCollapsed: boolean) => {
        await expect(taskRows(page)).toHaveCount((firstCollapsed ? 0 : 2) + (secondCollapsed ? 0 : 2), FIVE_SECONDS);
        if (firstCollapsed) await expect(label('first')).toHaveClass(/collapsed/);
        else await expect(label('first')).not.toHaveClass(/collapsed/);
        if (secondCollapsed) await expect(label('second')).toHaveClass(/collapsed/);
        else await expect(label('second')).not.toHaveClass(/collapsed/);
      };
      const both = `initiative=${first}&initiative=${second}`;

      await page.goto(`/#/timeline?${both}`);
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      await state(false, false);

      // Collapse the first initiative and follow a link to another Timeline entry within the redraw's reach: the collapse
      // is saved to the entry it was made on, not to the one the link opens.
      await label('first').click();
      await state(true, false);
      await page.evaluate((query) => {
        window.location.hash = `#/timeline?${query}&closed=90`;
      }, both);
      await expect(page).toHaveURL((url) => url.hash.endsWith('&closed=90'), FIVE_SECONDS);
      await state(false, false); // a link's entry has nothing saved: every initiative expanded

      await label('second').click();
      await state(false, true);

      await page.goBack();
      await expect(page).toHaveURL((url) => !url.hash.includes('closed=90'), FIVE_SECONDS);
      await state(true, false); // the first entry as it was left
      await page.goForward();
      await expect(page).toHaveURL((url) => url.hash.endsWith('&closed=90'), FIVE_SECONDS);
      await state(false, true); // the second entry as it was left
      await page.reload();
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      await state(false, true);
      await page.goBack();
      await state(true, false);
      await client.dispose();
    });

    test('a collapse made in the same turn as Back is shown again on Forward', async ({ page, playwright, baseURL }) => {
      const first = `e2e-tes-${vp.name}-c`;
      const second = `e2e-tes-${vp.name}-d`;
      const client = await api(playwright, baseURL, `tes2-${vp.name}`);
      const now = Date.now();
      for (const [slug, title] of [[first, `Prompt ${vp.name} first`], [second, `Prompt ${vp.name} second`]] as const) {
        await post(client, '/api/v1/initiatives', { slug, title }, 201);
        for (let i = 0; i < 2; i++) {
          const { ref } = await post<{ ref: string }>(client, '/api/v1/tasks', { initiative: slug, title: `${title} task ${i}` }, 201);
          dateTask(slug, ref, now - (4 - i) * DAY);
        }
      }
      await page.goto('/#/now');
      await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
      await page.evaluate((query) => {
        window.location.hash = `#/timeline?${query}`;
      }, `initiative=${first}&initiative=${second}`);
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      await expect(taskRows(page)).toHaveCount(4, FIVE_SECONDS);

      // The collapse and the Back to Attention happen in one turn of the page, before the chart's redraw has been seen.
      // Enter on the label toggles the initiative at once, in vis-timeline's data; its redraw is a frame away.
      await page.evaluate(() => {
        const label = document.querySelector<HTMLElement>('.vis-labelset .vis-label.tl-initiative')!;
        label.focus();
        label.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
        window.history.back();
      });
      await expect(page).toHaveURL(/#\/now/, FIVE_SECONDS);
      await page.goForward();
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      await expect(taskRows(page)).toHaveCount(2, FIVE_SECONDS);
      await expect(initiativeLabel(page, `Prompt ${vp.name} first`)).toHaveClass(/collapsed/);
      await expect(initiativeLabel(page, `Prompt ${vp.name} second`)).not.toHaveClass(/collapsed/);
      await client.dispose();
    });

    test('the rows scrolled just before Back, Forward or a reload come back as the entry left them', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-tes-${vp.name}-rows`;
      const client = await api(playwright, baseURL, `tes3-${vp.name}`);
      await post(client, '/api/v1/initiatives', { slug, title: `Rows ${vp.name}` }, 201);
      // More rows than the chart is tall: the rows scroll inside it.
      for (let i = 0; i < 30; i++) await post(client, '/api/v1/tasks', { initiative: slug, title: `Row ${String(i).padStart(2, '0')}` }, 201);
      const labels = page.locator('.vis-labelset .vis-label');

      await page.goto('/#/now');
      await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
      await page.evaluate((query) => {
        window.location.hash = `#/timeline?${query}`;
      }, `initiative=${slug}&closed=90`);
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      await expect(labels).toHaveCount(31, FIVE_SECONDS);
      await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
      await page.waitForTimeout(600); // vis-timeline settles its rows after the first draw

      // Scroll and leave in one turn: neither the scroll event nor its 100 ms report has had a chance to run.
      await page.evaluate(() => {
        document.querySelector<HTMLElement>('.vis-panel.vis-left')!.scrollTop = 400;
        window.history.back();
      });
      await expect(page).toHaveURL(/#\/now/, FIVE_SECONDS);
      await page.goForward();
      await expect(labels).toHaveCount(31, FIVE_SECONDS);
      await expect.poll(async () => Math.abs((await rowsScroll(page)) - 400), FIVE_SECONDS).toBeLessThanOrEqual(2);

      // A reload in the same turn as a scroll keeps the latest offset too.
      await page.waitForTimeout(300);
      await page.evaluate(() => {
        document.querySelector<HTMLElement>('.vis-panel.vis-left')!.scrollTop = 250;
        window.location.reload();
      });
      await expect(labels).toHaveCount(31, FIVE_SECONDS);
      await expect.poll(async () => Math.abs((await rowsScroll(page)) - 250), FIVE_SECONDS).toBeLessThanOrEqual(2);

      // A link to another Timeline entry starts at the top, and Back shows this one's rows where they were left.
      await page.waitForTimeout(300);
      await page.evaluate(() => {
        document.querySelector<HTMLElement>('.vis-panel.vis-left')!.scrollTop = 330;
        window.location.hash = '#/timeline?initiative=' + new URLSearchParams(window.location.hash.split('?')[1]).get('initiative') + '&closed=30';
      });
      await expect(page).toHaveURL((url) => url.hash.endsWith('&closed=30'), FIVE_SECONDS);
      await expect(labels).toHaveCount(31, FIVE_SECONDS);
      await expect.poll(() => rowsScroll(page), FIVE_SECONDS).toBe(0);
      await page.goBack();
      await expect(page).toHaveURL((url) => url.hash.endsWith('&closed=90'), FIVE_SECONDS);
      await expect.poll(async () => Math.abs((await rowsScroll(page)) - 330), FIVE_SECONDS).toBeLessThanOrEqual(2);
      await client.dispose();
    });
  });
}
