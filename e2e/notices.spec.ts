import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test as base } from './fixtures';
import { actorApi, created } from './now';

// R9: the notices banner above every view's content. One live notice is started through the API as another actor; the
// banner must show on Now, on Tasks and on a task page at 390 px with the holder, the intent, the resources, the time left
// and a link to the notice's task, with no sideways scroll, with no serious or critical axe violation (the full rule set
// in the light theme, the colour rules in the dark one, as the other views are scanned), and it must be gone once the
// notice ends. Signs in through the mock provider, so no API key rides along. Each step waits on the banner itself.
const test = base;

const PHONE = { width: 390, height: 844 };
const FIVE_SECONDS = { timeout: 5000 };
const themes = ['light', 'dark'] as const;
const DARK_PASS_RULES = ['color-contrast', 'link-in-text-block'];
// The scan is scoped to the component under test (only a11y.spec.ts scans whole pages).
const BANNER = '[data-testid=notices]';

const settle = (page: Page) =>
  page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));

async function expectNoSidewaysScroll(page: Page) {
  const m = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }));
  expect(m.scrollWidth).toBeLessThanOrEqual(PHONE.width);
  expect(m.scrollWidth).toBeLessThanOrEqual(m.clientWidth);
}

async function scan(page: Page, theme: string) {
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
  await settle(page);
  const { violations } =
    theme === 'dark' ? await new AxeBuilder({ page }).include(BANNER).withRules(DARK_PASS_RULES).analyze() : await new AxeBuilder({ page }).include(BANNER).analyze();
  const blocking = violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
  expect(
    blocking.map((v) => ({ rule: v.id, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) })),
    'serious or critical axe violations',
  ).toEqual([]);
}

interface Seed {
  api: APIRequestContext;
  task: string;
  lease: string;
}

for (const theme of themes) {
  test.describe(`notices banner, ${theme} theme`, () => {
    test.use({ viewport: PHONE, colorScheme: theme });

    test('shows on Now, Tasks and a task page with no sideways scroll and no axe violation, and is gone once the notice ends', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-notice-${theme}`;
      const holder = `notice-ops-${theme}`;
      const api = await actorApi(playwright, baseURL, holder);
      const seed: Seed = { api, task: '', lease: '' };
      try {
        await created(api, '/api/v1/initiatives', { slug, title: `Notices ${theme}` });
        seed.task = (await created(api, '/api/v1/tasks', { initiative: slug, title: 'Reboot the build host' })).ref;
        const resource = `host:notice-${theme}-${'w'.repeat(60)}`;
        const intent = `rebooting the build host for the kernel update ${'i'.repeat(80)}`;
        seed.lease = ((await created<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: seed.task, intent, resources: [resource], notice: true })).lease).ref;

        const banner = page.getByTestId('notices');
        const mine = banner.getByTestId('notice').filter({ hasText: holder });
        const pages = [
          { name: 'Now', hash: '#/now' },
          { name: 'Tasks', hash: '#/tasks' },
          { name: 'task page', hash: `#/t/${seed.task}` },
        ];
        await page.goto('/');
        for (const target of pages) {
          await page.evaluate((h) => (window.location.hash = h), target.hash);
          await expect(mine, `${target.name}: the banner shows the notice`).toBeVisible(FIVE_SECONDS);
          await expect(mine.getByTestId('notice-holder')).toHaveText(holder);
          await expect(mine.getByTestId('notice-intent')).toContainText('rebooting the build host');
          await expect(mine.getByTestId('notice-resources')).toContainText(resource);
          await expect(mine.getByTestId('notice-left')).toHaveText(/^expires in /);
          await expect(mine.getByTestId('notice-link')).toHaveAttribute('href', `#/t/${seed.task}`);
          await expectNoSidewaysScroll(page);
          await scan(page, theme);
        }

        // The link leads to the task page.
        await page.evaluate(() => (window.location.hash = '#/now'));
        await expect(mine).toBeVisible(FIVE_SECONDS);
        await mine.getByTestId('notice-link').click();
        await expect(page).toHaveURL(new RegExp(`#/t/${seed.task}$`));

        // Ending the notice takes it from the banner on the page that is open, with no reload.
        const ended = await api.post(`/api/v1/leases/${seed.lease}/end`, { data: { outcome: 'rebooted' } });
        expect(ended.status(), await ended.text()).toBe(200);
        seed.lease = '';
        await expect(mine).toHaveCount(0, FIVE_SECONDS);
        for (const target of pages) {
          await page.evaluate((h) => (window.location.hash = h), target.hash);
          await expect(page.getByTestId('notices').filter({ hasText: holder }), `${target.name}: the banner is gone`).toHaveCount(0);
        }
      } finally {
        if (seed.lease !== '') await api.post(`/api/v1/leases/${seed.lease}/end`, { data: {} });
        await api.dispose();
      }
    });
  });
}
