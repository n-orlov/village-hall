import { expect, type APIRequestContext } from '@playwright/test';
import { test } from './fixtures';

// Origin and owner in the UI (PRD R13, SPEC 11). Signs in through the mock provider (no API key rides along), makes a key
// on the Settings -> API keys page so the signed-in operator owns it, acts as an agent through that key, and checks the
// three displays: the key's owner in Settings, the agent's origin on the Actors page, and "via <operator>" on the agent's
// event in the task timeline (and none on the operator's own event).
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

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a key shows its owner, its agent shows the origin, and the agent\'s event says via the operator', async ({ page, playwright, baseURL }) => {
      const keyName = `e2e-origin-${vp.name}-key`;
      const agent = `e2e-origin-${vp.name}-agent`;
      const slug = `e2e-origin-${vp.name}`;
      await page.goto('/');
      const operator = (await (await page.request.get('/auth/session')).json()).actor as string;
      expect(operator).not.toBe('');

      // Settings -> API keys: the new key is owned by the signed-in operator.
      await page.goto('/#/settings/keys');
      await page.getByRole('button', { name: 'New API key' }).click();
      const dialog = page.getByRole('dialog', { name: 'New API key' });
      await dialog.getByLabel('Name').fill(keyName);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      const key = (await page.getByTestId('new-key-value').textContent()) ?? '';
      expect(key).toMatch(/^brd_/);
      const row = page.locator(`[data-testid=key-row][data-name=${keyName}]`);
      await expect(row.getByTestId('key-owner')).toContainText(operator, FIVE_SECONDS);

      // The agent acts through that key: an initiative and a task, as the agent.
      const api = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${key}`, 'Board-Actor': agent } });
      await post(api, '/api/v1/initiatives', { slug, title: `Origin ${vp.name}` }, 201);
      const ref = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Made by an agent' }, 201)).ref;

      // Actors: the agent's origin is the key's owner.
      await page.goto('/#/actors');
      const actorRow = page.locator(`[data-testid=actor-row][data-handle="${agent}"]`);
      await expect(actorRow).toBeVisible(FIVE_SECONDS);
      await expect(actorRow.getByTestId('actor-origin')).toHaveText(`origin ${operator}`);

      // The task's timeline: the agent's event says via the operator.
      await page.goto(`/#/t/${ref}`);
      const events = page.getByTestId('event');
      await expect(events.first()).toBeVisible(FIVE_SECONDS);
      const agentEvent = events.filter({ has: page.locator(`[data-testid=event-actor]`, { hasText: agent }) }).first();
      await expect(agentEvent.getByTestId('event-via')).toHaveText(`via ${operator}`);

      // An operator's own write has the operator as its origin, so it says nothing.
      await page.getByRole('button', { name: 'Milestone' }).click();
      const mile = page.getByRole('dialog', { name: 'Record milestone' });
      await mile.getByLabel('Summary').fill(`milestone by ${operator}`);
      await mile.getByRole('button', { name: 'Record milestone' }).click();
      const own = events.filter({ has: page.locator(`[data-testid=event-actor]`, { hasText: new RegExp(`^${operator}$`) }) }).first();
      await expect(own).toBeVisible(FIVE_SECONDS);
      await expect(own.getByTestId('event-via')).toHaveCount(0);

      await api.dispose();
    });
  });
}
