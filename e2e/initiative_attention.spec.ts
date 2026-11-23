import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { logicViewports } from './viewports';

// The attention chip and order on the Initiatives list (PRD R2, SPEC 11): an initiative with a task that needs a person
// shows a count chip and moves above the initiatives with nothing waiting, which keep their order. The board is shared
// with the other specs, so each test makes two initiatives of its own and compares only those two.
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

// The slugs of the rows now on the page, top to bottom.
const slugs = (page: Page) => page.locator('[data-testid=initiative-row]').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-slug')));

async function order(page: Page, first: string, second: string): Promise<'first-before-second' | 'second-before-first'> {
  const all = (await slugs(page)) as string[];
  return all.indexOf(first) < all.indexOf(second) ? 'first-before-second' : 'second-before-first';
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `initattn-${name}` },
  });
  const quiet = `e2e-initattn-${name}-quiet`;
  const busy = `e2e-initattn-${name}-busy`;
  const refs: Record<string, string> = {};
  for (const slug of [quiet, busy]) {
    await post(api, '/api/v1/initiatives', { slug, title: `Attention ${slug}` }, 201);
    refs[slug] = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'A task' }, 201)).ref;
  }
  return { api, quiet, busy, busyTask: refs[busy]! };
}

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a later initiative whose task needs attention shows the count chip and moves above the quiet one', async ({ page, playwright, baseURL }) => {
      const { api, quiet, busy, busyTask } = await seed(playwright, baseURL, vp.name);
      await page.goto('/#/initiatives');
      const row = (slug: string) => page.locator(`[data-testid=initiative-row][data-slug=${slug}]`);
      await expect(row(busy)).toBeVisible();
      await expect(row(quiet)).toBeVisible();

      // Nothing needs attention yet: no chip, and the initiatives are in the order they were made.
      await expect(row(busy).getByTestId('initiative-attention')).toHaveCount(0);
      expect(await order(page, quiet, busy)).toBe('first-before-second');

      // A done task needs a person; the open page follows the live event without a reload.
      await post(api, `/api/v1/tasks/${busyTask}/done`, {}, 200);
      await expect(row(busy).getByTestId('initiative-attention-count')).toHaveText('1', FIVE_SECONDS);
      await expect(row(busy).getByTestId('initiative-attention')).toContainText('needs attention');
      await expect(row(quiet).getByTestId('initiative-attention')).toHaveCount(0);
      await expect.poll(() => order(page, quiet, busy), FIVE_SECONDS).toBe('second-before-first');

      // A reload reads the same order from the server's counts.
      await page.reload();
      await expect(row(busy).getByTestId('initiative-attention-count')).toHaveText('1');
      expect(await order(page, quiet, busy)).toBe('second-before-first');

      // Closing the task takes the chip away and the order returns.
      await post(api, `/api/v1/tasks/${busyTask}/close`, { resolution: 'completed' }, 200);
      await expect(row(busy).getByTestId('initiative-attention')).toHaveCount(0, FIVE_SECONDS);
      await expect.poll(() => order(page, quiet, busy), FIVE_SECONDS).toBe('first-before-second');
      await api.dispose();
    });

    test('a task with a blocking ask counts, and answering the ask takes the chip away and the order returns', async ({ page, playwright, baseURL }) => {
      const { api, quiet, busy, busyTask } = await seed(playwright, baseURL, `${vp.name}-ask`);
      await page.goto('/#/initiatives');
      const row = (slug: string) => page.locator(`[data-testid=initiative-row][data-slug=${slug}]`);
      await expect(row(busy)).toBeVisible();
      expect(await order(page, quiet, busy)).toBe('first-before-second');

      const ask = (await post<{ ref: string }>(api, '/api/v1/asks', { question: 'Which way?', task: busyTask, mode: 'poll', blocking: true }, 201)).ref;
      await expect(row(busy).getByTestId('initiative-attention-count')).toHaveText('1', FIVE_SECONDS);
      await expect(row(quiet).getByTestId('initiative-attention')).toHaveCount(0);
      await expect.poll(() => order(page, quiet, busy), FIVE_SECONDS).toBe('second-before-first');

      await post(api, `/api/v1/asks/${ask}/answer`, { answer: 'Go left' }, 200);
      await expect(row(busy).getByTestId('initiative-attention')).toHaveCount(0, FIVE_SECONDS);
      await expect.poll(() => order(page, quiet, busy), FIVE_SECONDS).toBe('first-before-second');
      await api.dispose();
    });

    test('the chip counts every task that needs a person and lowers live when one is closed', async ({ page, playwright, baseURL }) => {
      const { api, quiet, busy, busyTask } = await seed(playwright, baseURL, `${vp.name}-many`);
      const second = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: busy, title: 'Another task' }, 201)).ref;
      await post(api, '/api/v1/tasks', { initiative: busy, title: 'A third task' }, 201); // nobody touches it
      await post(api, `/api/v1/tasks/${busyTask}/done`, {}, 200);
      await post(api, '/api/v1/asks', { question: 'Which way?', task: second, mode: 'poll', blocking: false }, 201);
      await page.goto('/#/initiatives');
      const row = (slug: string) => page.locator(`[data-testid=initiative-row][data-slug=${slug}]`);
      await expect(row(busy).getByTestId('initiative-attention-count')).toHaveText('2');
      expect(await order(page, quiet, busy)).toBe('second-before-first');

      // The untouched third task does not count; closing a counted one lowers the count live.
      await post(api, `/api/v1/tasks/${busyTask}/close`, { resolution: 'completed' }, 200);
      await expect(row(busy).getByTestId('initiative-attention-count')).toHaveText('1', FIVE_SECONDS);
      await api.dispose();
    });
  });
}
