import { mkdirSync } from 'node:fs';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { logicViewports } from './viewports';

// R3: the app moves the browser storage of the earlier name (`board.*`) to `vh.*` before it reads any view state or
// the theme. The test seeds the old keys before the first load (addInitScript, once: the guard in sessionStorage keeps
// a reload from seeding them again), then asserts that the saved filter and the dark theme apply, that the board.* keys
// are gone and that the vh.* ones hold the values. It also shoots the migrated page at the logic viewport (desktop) in both themes
// when E2E_SHOTS_DIR names a directory.

async function post(api: APIRequestContext, path: string, data: object, status: number): Promise<{ ref: string }> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as { ref: string };
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

const stored = (page: Page, area: 'localStorage' | 'sessionStorage') =>
  page.evaluate((a) => Object.fromEntries(Object.keys(window[a]).map((k) => [k, window[a].getItem(k)])), area);

const keysStartingWith = (all: Record<string, string | null>, prefix: string) => Object.keys(all).filter((k) => k.startsWith(prefix));

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size, colorScheme: 'light' });

    test('board.view.v1.tasks and board.theme seeded before the first load are applied and moved to vh.*', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-mig-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Migration ${slug}` }, 201);
      const alpha = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Alpha migrated' }, 201)).ref;
      const beta = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Beta migrated' }, 201)).ref;
      const query = `initiative=${slug}&sort=title&dir=desc`;

      await page.addInitScript(
        ([q]) => {
          if (sessionStorage.getItem('e2e-seeded') !== null) return;
          sessionStorage.setItem('e2e-seeded', '1');
          localStorage.setItem('board.view.v1.tasks', JSON.stringify({ v: 1, query: q }));
          localStorage.setItem('board.theme', 'dark');
          sessionStorage.setItem('board.entry.e2e-mig', JSON.stringify({ t: 1, values: {} }));
        },
        [query],
      );

      const assertMigrated = async () => {
        // The filter applied: the bare #/tasks was rewritten to the saved query and the rows follow it (Beta before Alpha).
        await expect(page).toHaveURL(new RegExp(`#/tasks\\?${query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`));
        await expect.poll(() => page.getByTestId('task-ref').allTextContents()).toEqual([beta, alpha]);
        // The dark theme applied although the system is light.
        await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
        const local = await stored(page, 'localStorage');
        expect(keysStartingWith(local, 'board.'), 'board.* keys left in localStorage').toEqual([]);
        expect(JSON.parse(local['vh.view.v1.tasks'] as string)).toEqual({ v: 1, query });
        expect(local['vh.theme']).toBe('dark');
        const session = await stored(page, 'sessionStorage');
        expect(keysStartingWith(session, 'board.'), 'board.* keys left in sessionStorage').toEqual([]);
        expect(session['vh.entry.e2e-mig']).toBeTruthy();
      };

      await page.goto('/#/tasks');
      await assertMigrated();
      await page.reload();
      await assertMigrated();

      const shots = process.env.E2E_SHOTS_DIR;
      if (shots) {
        mkdirSync(shots, { recursive: true });
        await page.screenshot({ path: `${shots}/migrated-${vp.name}-dark.png` });
        await page.getByRole('button', { name: 'Account menu' }).click();
        await page.getByRole('dialog', { name: 'Account' }).getByRole('radio', { name: 'Light' }).check({ force: true });
        await page.keyboard.press('Escape');
        await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
        await page.screenshot({ path: `${shots}/migrated-${vp.name}-light.png` });
      }
    });
  });
}
