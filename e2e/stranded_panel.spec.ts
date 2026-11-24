import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Stranded panel on the task page (PRD R2, SPEC 11): a task whose lease ran out and was not acknowledged shows its
// badges in the header and a panel with the holder, the intent, the last renewal, the expiry, a copy way back button
// and Acknowledge (after a Dialog confirm). Acknowledging removes the panel and, in another page of the same session,
// takes the task out of the Tasks view's Needs attention within five seconds, with no reload.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

// Runs a lease out the way needs-me.spec.ts does: its expiry moves into the past in the e2e store, then an API read expires it.
async function expireLease(api: APIRequestContext, ref: string) {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db.prepare('UPDATE leases SET expires_at = ? WHERE n = ?').run(Date.now() - 30_000, Number(ref.slice(1)));
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
  expect((await api.get('/api/v1/leases?state=expired')).status()).toBe(200);
}

// An initiative with one task that is made stranded through the API by a holder that left a way back.
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  const slug = `e2e-strp-${name}`;
  const holder = `strp-${name}`;
  const wayBack = `ralphctl status ${holder}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': holder, 'Board-Actor-Way-Back': wayBack },
  });
  await post(api, '/api/v1/initiatives', { slug, title: `Stranded ${name}` }, 201);
  const task = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Ran out of time' }, 201)).ref;
  const lease = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent: 'long edit', resources: [`repo:strp-${name}`] }, 201)).lease.ref;
  await expireLease(api, lease);
  return { slug, holder, wayBack, api, task, lease };
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size, permissions: ['clipboard-read', 'clipboard-write'] });

    test('a stranded task shows its badges and the panel with the lease, and Copy way back copies the holder\'s way back', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `panel-${vp.name}`);
      await page.goto(`/#/t/${s.task}`);
      await expect(page.getByTestId('task-title')).toHaveText('Ran out of time', FIVE_SECONDS);
      await expect(page.getByTestId('task-page').getByTestId('attention-stranded')).toContainText('stranded');

      const panel = page.getByTestId('stranded-panel');
      await expect(panel).toBeVisible(FIVE_SECONDS);
      await expect(panel.getByRole('heading', { name: 'Stranded' })).toBeVisible();
      await expect(panel.getByTestId('stranded-holder')).toHaveText(s.holder);
      await expect(panel.getByTestId('stranded-lease')).toHaveText(s.lease);
      await expect(panel.getByTestId('stranded-intent')).toHaveText('long edit');
      await expect(panel.getByTestId('stranded-renewed')).toHaveText(/^renewed .+/);
      await expect(panel.getByTestId('stranded-expired')).toHaveText(/^expired \d+s ago$/);
      await expect(panel.getByTestId('stranded-way-back')).toHaveText(s.wayBack);
      await expect(page.getByTestId('expired-lease')).toHaveCount(0); // the stranded lease is not listed twice

      await panel.getByRole('button', { name: `Copy way back of ${s.holder}` }).click();
      await expect(page.getByTestId('toast')).toHaveText(`Copied the way back of ${s.holder}`, FIVE_SECONDS);
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(s.wayBack);
      await s.api.dispose();
    });

    test('Acknowledge asks for confirmation, then the panel goes and the task leaves Needs attention without a reload', async ({ page, context, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `ack-${vp.name}`);
      // The Tasks view in a second page of the same session; it is never reloaded.
      const tasks = await context.newPage();
      await tasks.goto(`/#/tasks?initiative=${s.slug}`);
      const needs = tasks.getByTestId('needs-attention').locator(`[data-testid=needs-row][data-ref=${s.task}]`);
      await expect(needs).toBeVisible(FIVE_SECONDS);

      await page.goto(`/#/t/${s.task}`);
      const panel = page.getByTestId('stranded-panel');
      await expect(panel).toBeVisible(FIVE_SECONDS);
      await panel.getByRole('button', { name: `Acknowledge lease ${s.lease}` }).click();
      const dialog = page.getByRole('dialog', { name: `Acknowledge lease ${s.lease}` });
      await expect(dialog).toBeVisible();
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      await expect(dialog).toBeHidden();
      await expect(panel).toBeVisible();
      expect(await (await s.api.get(`/api/v1/leases/${s.lease}`)).json(), 'Cancel acknowledges nothing').not.toHaveProperty('acked_by');

      await panel.getByRole('button', { name: `Acknowledge lease ${s.lease}` }).click();
      await dialog.getByRole('button', { name: 'Acknowledge lease' }).click();
      await expect(page.getByTestId('toast')).toHaveText(`Lease ${s.lease} acknowledged`, FIVE_SECONDS);
      await expect(panel).toHaveCount(0, FIVE_SECONDS);
      await expect(page.getByTestId('attention-stranded')).toHaveCount(0, FIVE_SECONDS);
      await expect(needs).toHaveCount(0, FIVE_SECONDS);
      expect(await (await s.api.get(`/api/v1/leases/${s.lease}`)).json()).toMatchObject({ acked_by: 'operator' });
      await s.api.dispose();
    });
  });
}

// The panel and its confirmation sheet are axe clean (serious and critical) in both themes at both viewports.
for (const vp of viewports) {
  for (const theme of ['light', 'dark'] as const) {
    test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}, ${theme} theme`, () => {
      test.use({ viewport: vp.size, colorScheme: theme });

      test('the Stranded panel and the Acknowledge confirmation have no serious axe violation', async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `axe-${vp.name}-${theme}`);
        await page.goto(`/#/t/${s.task}`);
        await expect(page.getByTestId('stranded-panel')).toBeVisible(FIVE_SECONDS);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        const scan = async (scope: string) => {
          await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
          const { violations } = await new AxeBuilder({ page }).include(scope).analyze();
          const blocking = violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
          expect(blocking.map((v) => ({ rule: v.id, nodes: v.nodes.map((n) => n.target) })), 'serious or critical axe violations').toEqual([]);
        };
        await scan('[data-testid=stranded-panel]');
        await page.getByRole('button', { name: `Acknowledge lease ${s.lease}` }).click();
        await expect(page.getByRole('dialog', { name: `Acknowledge lease ${s.lease}` })).toBeVisible();
        await scan('[data-testid=dialog]');
        await s.api.dispose();
      });
    });
  }
}
