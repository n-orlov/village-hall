import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test } from './fixtures';

// R8 and R9: the asker-waiting label of an open poll ask follows the board without a reload. Each test opens a view that
// shows the ask as "asker not waiting", then starts a real held wait (`GET /asks/<ref>?wait=10`) and requires the label to
// change within five seconds, with no navigation and no timer on the page. The Attention view, the task page and the
// initiative page all show the label.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

interface Raised {
  slug: string;
  ref: string;
  task: string;
}

async function seed(api: APIRequestContext, name: string, kind: string): Promise<Raised> {
  const slug = `aw-${kind}-${name}`;
  expect((await api.post('/api/v1/initiatives', { data: { slug, title: `Waiting ${kind}` } })).status()).toBe(201);
  const created = await api.post('/api/v1/tasks', { data: { initiative: slug, title: 'Needs an answer' } });
  expect(created.status()).toBe(201);
  const task = ((await created.json()) as { ref: string }).ref;
  const asked = await api.post('/api/v1/asks', { data: { task, question: `Is ${kind} waiting?`, mode: 'poll' } });
  expect(asked.status()).toBe(201);
  return { slug, task, ref: ((await asked.json()) as { ref: string }).ref };
}

// Holds a wait on the ask, shows that it is held (the API says asker_waiting), runs the check, then ends the wait.
async function whileHeld(api: APIRequestContext, ref: string, check: () => Promise<void>) {
  const held = api.get(`/api/v1/asks/${ref}?wait=10`);
  try {
    await expect
      .poll(async () => ((await (await api.get(`/api/v1/asks/${ref}`)).json()) as { asker_waiting: boolean }).asker_waiting, FIVE_SECONDS)
      .toBe(true);
    await check();
  } finally {
    await api.post(`/api/v1/asks/${ref}/withdraw`, { data: { reason: 'regression done' } });
    await held;
  }
}

async function noReload(page: Page) {
  await page.evaluate(() => {
    (window as unknown as { __aliveMarker: boolean }).__aliveMarker = true;
  });
  return async () => expect(await page.evaluate(() => (window as unknown as { __aliveMarker?: boolean }).__aliveMarker)).toBe(true);
}

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: viewport.size });

    test('Attention shows the asker waiting once a held wait starts', async ({ page, playwright, baseURL }) => {
      const api = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'aw-asker' },
      });
      try {
        const { slug, ref } = await seed(api, viewport.name, 'attention');
        await page.goto(`/#/now?initiative=${slug}`);
        const row = page.getByTestId('band-asks').getByTestId('attention-item');
        await expect(row).toContainText(ref, FIVE_SECONDS);
        await expect(row).toContainText('asker not waiting', FIVE_SECONDS);
        await expect(page.getByRole('status', { name: 'Live updates: live' })).toBeVisible(FIVE_SECONDS);
        const stillThere = await noReload(page);
        await whileHeld(api, ref, async () => {
          await expect(row).toContainText('asker waiting', FIVE_SECONDS);
          await expect(row).not.toContainText('asker not waiting');
          await stillThere();
        });
      } finally {
        await api.dispose();
      }
    });

    for (const view of ['task', 'initiative'] as const) {
      test(`the ${view} page shows the asker waiting once a held wait starts`, async ({ page, playwright, baseURL }) => {
        const api = await playwright.request.newContext({
          baseURL,
          extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'aw-asker' },
        });
        try {
          const { slug, task, ref } = await seed(api, viewport.name, view);
          await page.goto(view === 'task' ? `/#/t/${task}` : `/#/i/${slug}`);
          const line = page.locator(`[data-testid=ask][data-ref=${ref}]`).getByTestId('ask-line');
          await expect(line).toHaveText('asker not waiting', FIVE_SECONDS);
          await expect(page.getByRole('status', { name: 'Live updates: live' })).toBeVisible(FIVE_SECONDS);
          const stillThere = await noReload(page);
          await whileHeld(api, ref, async () => {
            await expect(line).toHaveText('asker waiting', FIVE_SECONDS);
            await stillThere();
          });
        } finally {
          await api.dispose();
        }
      });
    }
  });
}
