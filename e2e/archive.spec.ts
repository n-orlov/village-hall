import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { backdateInitiative, backdateTask } from './store';

// The Archive view (PRD R7, SPEC 11): the closed tasks and closed initiatives of a 7, 30, 90 day or all window (the
// default is 30), the window held in the fragment query, newest closed first. Reopen confirms in a dialog and brings the
// item back to the Tasks view or the Initiatives list. The e2e store moves a `closed_at` back with e2e/store.ts, because
// the API stamps the server's own clock and a spec cannot wait thirty days.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

const archiveRow = (page: Page, kind: 'task' | 'initiative', ref: string) => page.locator(`[data-testid=archive-row][data-kind=${kind}][data-ref="${ref}"]`);
const dialog = (page: Page, name: string) => page.getByRole('dialog', { name });

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

interface Created {
  id: string;
  ref: string;
}

// An open initiative holding a task closed just now and a task closed through the API and then moved 30 days back.
async function seedTasks(api: APIRequestContext, name: string) {
  const slug = `e2e-arc-t-${name}`;
  await post(api, '/api/v1/initiatives', { slug, title: `Archive tasks ${name}` });
  const old = await post<Created>(api, '/api/v1/tasks', { initiative: slug, title: 'Closed long ago' });
  const fresh = await post<Created>(api, '/api/v1/tasks', { initiative: slug, title: 'Closed just now' });
  await post(api, `/api/v1/tasks/${old.ref}/close`, { resolution: 'superseded' }, 200);
  backdateTask(old.id, 30);
  await post(api, `/api/v1/tasks/${fresh.ref}/close`, { resolution: 'dropped' }, 200);
  return { slug, old: old.ref, fresh: fresh.ref };
}

// Two closed initiatives with no tasks: one closed just now, one closed and then moved 30 days back.
async function seedInitiatives(api: APIRequestContext, name: string) {
  const old = `e2e-arc-io-${name}`;
  const fresh = `e2e-arc-if-${name}`;
  const oldId = (await post<Created>(api, '/api/v1/initiatives', { slug: old, title: `Archive old initiative ${name}` })).id;
  await post(api, '/api/v1/initiatives', { slug: fresh, title: `Archive fresh initiative ${name}` });
  await post(api, `/api/v1/initiatives/${old}/close`, {}, 200);
  backdateInitiative(oldId, 30);
  await post(api, `/api/v1/initiatives/${fresh}/close`, {}, 200);
  return { old, fresh };
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a task closed now is in the 7 day window and one closed 30 days ago only in all, newest first, with its resolution', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const s = await seedTasks(api, vp.name);

      await page.goto('/#/archive?window=7');
      await expect(archiveRow(page, 'task', s.fresh)).toBeVisible(FIVE_SECONDS);
      await expect(archiveRow(page, 'task', s.fresh).getByTestId('archive-resolution')).toHaveText('dropped');
      await expect(archiveRow(page, 'task', s.old)).toHaveCount(0);
      await expect(page.getByTestId('archive-window')).toHaveValue('7');

      // The window select rewrites the fragment, and the rows follow it.
      await page.getByTestId('archive-window').selectOption('all');
      await expect(page).toHaveURL(/#\/archive\?window=all$/);
      await expect(archiveRow(page, 'task', s.old)).toBeVisible(FIVE_SECONDS);
      await expect(archiveRow(page, 'task', s.old).getByTestId('archive-resolution')).toHaveText('superseded');
      await expect(archiveRow(page, 'task', s.fresh)).toBeVisible();

      // Newest closed first: the task closed now is above the one closed 30 days ago.
      const refs = await page.locator(`[data-testid=archive-row][data-kind=task]`).evaluateAll((rows) => rows.map((r) => r.getAttribute('data-ref')));
      expect(refs.indexOf(s.fresh)).toBeGreaterThanOrEqual(0);
      expect(refs.indexOf(s.fresh)).toBeLessThan(refs.indexOf(s.old));

      // Back to 7 days by the fragment alone.
      await page.getByTestId('archive-window').selectOption('7');
      await expect(archiveRow(page, 'task', s.old)).toHaveCount(0);
      await expect(archiveRow(page, 'task', s.fresh)).toBeVisible();
    });

    test('a bare #/archive is the 30 day window, and the window is read from the link', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const s = await seedTasks(api, `def-${vp.name}`);
      await page.goto('/#/archive');
      await expect(page.getByTestId('archive-window')).toHaveValue('30');
      await expect(archiveRow(page, 'task', s.fresh)).toBeVisible(FIVE_SECONDS);
      await page.goto('/#/archive?window=90');
      await expect(page.getByTestId('archive-window')).toHaveValue('90');
      await expect(archiveRow(page, 'task', s.old)).toBeVisible(FIVE_SECONDS);
      await page.goto('/#/archive?window=bogus');
      await expect(page.getByTestId('archive-window')).toHaveValue('30');
    });

    test('an initiative closed now is in the 7 day window and one closed 30 days ago only in all', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const s = await seedInitiatives(api, vp.name);

      await page.goto('/#/archive?window=7');
      await expect(archiveRow(page, 'initiative', s.fresh)).toBeVisible(FIVE_SECONDS);
      await expect(archiveRow(page, 'initiative', s.fresh).getByTestId('archive-kind')).toHaveText('initiative');
      await expect(archiveRow(page, 'initiative', s.old)).toHaveCount(0);

      await page.goto('/#/archive?window=all');
      await expect(archiveRow(page, 'initiative', s.old)).toBeVisible(FIVE_SECONDS);
      await expect(archiveRow(page, 'initiative', s.fresh)).toBeVisible();
    });

    test('Reopen on a task confirms in a dialog and brings it back to the Tasks view', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const s = await seedTasks(api, `rt-${vp.name}`);

      await page.goto('/#/archive?window=7');
      await archiveRow(page, 'task', s.fresh).getByTestId('archive-reopen').click();
      await expect(dialog(page, 'Reopen task')).toBeVisible();

      // Cancel changes nothing.
      await dialog(page, 'Reopen task').getByRole('button', { name: 'Cancel' }).click();
      await expect(dialog(page, 'Reopen task')).toHaveCount(0);
      await expect(archiveRow(page, 'task', s.fresh)).toBeVisible();

      await archiveRow(page, 'task', s.fresh).getByTestId('archive-reopen').click();
      await dialog(page, 'Reopen task').getByRole('button', { name: 'Reopen task' }).click();
      await expect(dialog(page, 'Reopen task')).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveText(`Task ${s.fresh} reopened`);
      await expect(archiveRow(page, 'task', s.fresh)).toHaveCount(0, FIVE_SECONDS);

      // Back in the Tasks view, open again.
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${s.fresh}]`)).toBeVisible(FIVE_SECONDS);
    });

    test('Reopen on an initiative confirms in a dialog and brings it back to the Initiatives list', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const s = await seedInitiatives(api, `ri-${vp.name}`);

      await page.goto('/#/archive?window=7');
      await archiveRow(page, 'initiative', s.fresh).getByTestId('archive-reopen').click();
      await expect(dialog(page, 'Reopen initiative')).toBeVisible();
      await dialog(page, 'Reopen initiative').getByRole('button', { name: 'Reopen initiative' }).click();
      await expect(dialog(page, 'Reopen initiative')).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveText(`Initiative ${s.fresh} reopened`);
      await expect(archiveRow(page, 'initiative', s.fresh)).toHaveCount(0, FIVE_SECONDS);

      // Back in the Initiatives list (open initiatives), and no longer archived.
      await page.goto('/#/initiatives');
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${s.fresh}]`)).toBeVisible(FIVE_SECONDS);
    });

    test('Reopen on a task of a closed initiative is refused in the dialog, and the task stays archived', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-arc-ref-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Archive refused ${vp.name}` });
      const task = await post<Created>(api, '/api/v1/tasks', { initiative: slug, title: 'In a closed initiative' });
      await post(api, `/api/v1/tasks/${task.ref}/close`, { resolution: 'completed' }, 200);
      await post(api, `/api/v1/initiatives/${slug}/close`, {}, 200);

      await page.goto('/#/archive?window=7');
      await archiveRow(page, 'task', task.ref).getByTestId('archive-reopen').click();
      await dialog(page, 'Reopen task').getByRole('button', { name: 'Reopen task' }).click();
      await expect(dialog(page, 'Reopen task').getByTestId('dialog-error')).toBeVisible(FIVE_SECONDS);
      await dialog(page, 'Reopen task').getByRole('button', { name: 'Cancel' }).click();
      await expect(archiveRow(page, 'task', task.ref)).toBeVisible();
    });
  });
}
