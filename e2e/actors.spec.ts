import { expect, type APIRequestContext, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Actors view (PRD R7, SPEC 11): the actors that hold a live lease come first, each lease linking to its task, then
// the others by last seen, and every actor with a way back has a button that copies it. A change through the API (a
// lease started) shows within five seconds with no reload.
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

function actorApi(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, handle: string, wayBack: string) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': handle, 'Board-Actor-Way-Back': wayBack },
  });
}

// A task, and its holder's live lease started through the API; then a second actor that holds nothing and acts after it,
// so that it is seen last: only the lease puts the holder above it.
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  const slug = `e2e-act-${name}`;
  const holder = `act-holder-${name}`;
  const idle = `act-idle-${name}`;
  const holderWay = `ralphctl status ${holder}`;
  const idleWay = `tmux attach -t ${idle}`;
  const api = await actorApi(playwright, baseURL, holder, holderWay);
  await post(api, '/api/v1/initiatives', { slug, title: `Actors ${name}` }, 201);
  const task = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Held by an actor' }, 201)).ref;
  const lease = (await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent: 'long edit', resources: [`repo:act-${name}`] }, 201)).lease.ref;
  const other = await actorApi(playwright, baseURL, idle, idleWay);
  await post(other, '/api/v1/notes', { summary: 'The idle actor was here' }, 201);
  return { api, other, holder, idle, holderWay, idleWay, task, lease };
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size, permissions: ['clipboard-read', 'clipboard-write'] });

    test('an actor with a live lease is listed first, with the lease linking to its task, and its way back is copied', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `first-${vp.name}`);
      await page.goto('/#/actors');
      const rows = page.getByTestId('actor-row');
      await expect(rows.first()).toBeVisible(FIVE_SECONDS);
      const held = page.locator(`[data-testid=actor-row][data-handle="${s.holder}"]`);
      const idle = page.locator(`[data-testid=actor-row][data-handle="${s.idle}"]`);
      await expect(held).toBeVisible(FIVE_SECONDS);
      await expect(idle).toBeVisible();

      // The holder is the first row: it holds a live lease and was seen before the idle actor, which only the lease explains.
      await expect(rows.first()).toHaveAttribute('data-handle', s.holder);
      await expect(held).toHaveAttribute('data-alive', 'true');
      await expect(held.getByTestId('actor-alive')).toBeVisible();
      await expect(idle).toHaveAttribute('data-alive', 'false');
      const order = await rows.evaluateAll((els) => els.map((el) => [el.getAttribute('data-handle'), el.getAttribute('data-alive')]));
      const alive = order.map(([, a]) => a);
      expect(alive.lastIndexOf('true'), 'every actor with a live lease comes before every other').toBeLessThan(alive.indexOf('false') === -1 ? Infinity : alive.indexOf('false'));
      expect(order.findIndex(([h]) => h === s.holder)).toBeLessThan(order.findIndex(([h]) => h === s.idle));

      const lease = held.getByTestId('actor-lease');
      await expect(lease).toHaveCount(1);
      await expect(lease.getByTestId('lease-ref')).toHaveText(s.lease);
      await expect(lease.getByTestId('lease-intent')).toHaveText('long edit');
      await expect(lease.getByTestId('lease-expires')).toHaveText(/^expires in /);
      await expect(held.getByTestId('actor-last-seen')).toHaveText(/ago$|just now$/);
      await expect(idle.getByTestId('actor-idle')).toHaveText('no live lease');

      await held.getByRole('button', { name: `Copy way back of ${s.holder}` }).click();
      await expect(page.getByTestId('toast')).toHaveText(`Copied the way back of ${s.holder}`, FIVE_SECONDS);
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(s.holderWay);
      await expect(held.getByTestId('actor-way-back')).toHaveText(s.holderWay);

      await idle.getByRole('button', { name: `Copy way back of ${s.idle}` }).click();
      await expect(page.getByTestId('toast')).toHaveText(`Copied the way back of ${s.idle}`, FIVE_SECONDS);
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(s.idleWay);

      await lease.getByTestId('lease-task').click();
      await expect(page).toHaveURL(new RegExp(`#/t/${s.task}$`));
      await expect(page.getByTestId('task-title')).toHaveText('Held by an actor', FIVE_SECONDS);
      await s.api.dispose();
      await s.other.dispose();
    });

    test('a lease started through the API puts its actor first within five seconds, with no reload', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `live-${vp.name}`);
      await page.goto('/#/actors');
      const idle = page.locator(`[data-testid=actor-row][data-handle="${s.idle}"]`);
      await expect(idle).toBeVisible(FIVE_SECONDS);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      const task = (await post<{ ref: string }>(s.other, '/api/v1/tasks', { initiative: `e2e-act-live-${vp.name}`, title: 'Taken later' }, 201)).ref;
      await post(s.other, '/api/v1/leases', { task, intent: 'taking over', resources: [`repo:act-later-${vp.name}`] }, 201);
      await expect(idle).toHaveAttribute('data-alive', 'true', FIVE_SECONDS);
      await expect(page.getByTestId('actor-row').first()).toHaveAttribute('data-handle', s.idle, FIVE_SECONDS);
      await expect(idle.getByTestId('lease-intent')).toHaveText('taking over');
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
      await s.api.dispose();
      await s.other.dispose();
    });
  });
}
