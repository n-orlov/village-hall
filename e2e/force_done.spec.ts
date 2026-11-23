import { expect, type APIRequestContext } from '@playwright/test';
import { test } from './fixtures';

// Done over another actor's lease (PRD R14 UI, SPEC 11): Done on a task another actor holds opens a Dialog confirm that
// names the holder; Cancel changes nothing; confirming sends force and the task ends up done with no live lease.
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

test.describe('desktop 1440x900', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('Done on a task another actor holds names the holder, and confirming marks it done with no live lease', async ({ page, playwright, baseURL }) => {
    const holder = 'fd-holder';
    const api = await playwright.request.newContext({
      baseURL,
      extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': holder },
    });
    await post(api, '/api/v1/initiatives', { slug: 'e2e-force-done', title: 'Force done' }, 201);
    const task = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: 'e2e-force-done', title: 'Held by another' }, 201)).ref;
    const lease = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent: 'long refactor' }, 201)).lease.ref;

    await page.goto(`/#/t/${task}`);
    await expect(page.getByTestId('task-title')).toHaveText('Held by another', FIVE_SECONDS);
    await page.getByRole('button', { name: 'Done', exact: true }).click();

    const dialog = page.getByRole('dialog', { name: `Mark ${task} done` });
    await expect(dialog).toBeVisible(FIVE_SECONDS);
    await expect(dialog.getByTestId('dialog-body')).toContainText(`${holder} holds a live lease on this task`);

    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    expect((await (await api.get(`/api/v1/tasks/${task}`)).json()).status, 'Cancel marks nothing done').toBe('open');

    await page.getByRole('button', { name: 'Done', exact: true }).click();
    await dialog.getByRole('button', { name: 'Mark done anyway' }).click();
    await expect(page.getByTestId('toast')).toHaveText(`Task ${task} marked done`, FIVE_SECONDS);
    await expect(dialog).toBeHidden();

    const after = await (await api.get(`/api/v1/tasks/${task}`)).json();
    expect(after.status).toBe('done');
    expect(after.live_leases).toEqual([]);
    expect(await (await api.get(`/api/v1/leases/${lease}`)).json()).toMatchObject({ state: 'ended', ended_by: 'operator' });
    await api.dispose();
  });
});
