import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test as base } from './fixtures';
import { actorApi, created } from './now';

// R9: the notices banner above every view's content. One live notice is started through the API as another actor; the
// banner must show on Now, on Tasks and on a task page at 390 px with the holder, the intent, the resources, the time left
// and a link to the notice's task, with no sideways scroll, with no serious or critical axe violation (the full rule set
// in the light theme, the colour rules in the dark one, as the other views are scanned), and it must be gone once the
// notice ends. A second test holds `GET /api/v1/now` back for 500 ms, so the banner arrives after the view is drawn, and
// measures that it moves nothing: the layout-shift entries add up to 0 and the view's content keeps its top y. Signs in through the mock provider, so no API key rides along. Each step waits on the banner itself.
const test = base;

const PHONE = { width: 390, height: 844 };
const FIVE_SECONDS = { timeout: 5000 };
const themes = ['light', 'dark'] as const;
const DARK_PASS_RULES = ['color-contrast', 'link-in-text-block'];
// The scan is scoped to the component under test (only a11y.spec.ts scans whole pages).
const BANNER = '[data-testid=notices]';
// The view's content container: the first thing in the main area that is not the banner.
const CONTENT = '#main > :not([data-testid=notices])';

const settle = (page: Page) =>
  page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));

async function expectNoSidewaysScroll(page: Page) {
  const m = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth }));
  expect(m.scrollWidth).toBeLessThanOrEqual(PHONE.width);
  expect(m.scrollWidth).toBeLessThanOrEqual(m.clientWidth);
}

// R9: the banner comes before the view's content in the document and lies wholly above it (its bottom edge is at or above the content's top edge).
async function expectBannerAboveContent(page: Page, name: string) {
  const geometry = await page.evaluate(
    ({ banner, content }) => {
      const b = document.querySelector(banner);
      const c = document.querySelector(content);
      if (b === null || c === null) return undefined;
      return {
        precedes: (b.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0,
        sameParent: b.parentElement === c.parentElement,
        bannerBottom: b.getBoundingClientRect().bottom,
        contentTop: c.getBoundingClientRect().top,
      };
    },
    { banner: BANNER, content: CONTENT },
  );
  expect(geometry, `${name}: the banner and the content are in the page`).toBeDefined();
  expect(geometry!.precedes, `${name}: the banner precedes the content in the document`).toBe(true);
  expect(geometry!.sameParent, `${name}: both are children of the main area`).toBe(true);
  expect(geometry!.bannerBottom, `${name}: the banner's bottom edge is above the content's top edge`).toBeLessThanOrEqual(geometry!.contentTop);
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

const NOW_DELAY_MS = 500;
const AFTER_BANNER_MS = 1000;

declare global {
  interface Window {
    __layoutShift: number;
  }
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
          await expectBannerAboveContent(page, target.name);
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

test.describe('notices banner, layout', () => {
  test.use({ viewport: PHONE });

  test('moves nothing when it arrives late: layout shift 0 and the content keeps its top y, on Now, Tasks and a task page', async ({ page, playwright, baseURL }) => {
    const slug = 'e2e-notice-shift';
    const holder = 'notice-ops-shift';
    const api = await actorApi(playwright, baseURL, holder);
    let lease = '';
    try {
      await created(api, '/api/v1/initiatives', { slug, title: 'Notices shift' });
      const task = (await created(api, '/api/v1/tasks', { initiative: slug, title: 'Freeze the deploys' })).ref;
      lease = ((await created<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent: 'deploy freeze for the release', resources: ['env:shift-prod'], notice: true })).lease).ref;

      // Every layout-shift entry from the start of each navigation is added up (the entries already buffered included).
      await page.addInitScript(() => {
        window.__layoutShift = 0;
        new PerformanceObserver((list) => {
          for (const entry of list.getEntries()) window.__layoutShift += (entry as unknown as { value: number }).value;
        }).observe({ type: 'layout-shift', buffered: true });
      });
      await page.route('**/api/v1/now', async (route) => {
        await new Promise((resolve) => setTimeout(resolve, NOW_DELAY_MS));
        await route.continue();
      });

      const content = page.locator(CONTENT).first();
      const top = () => content.evaluate((el) => el.getBoundingClientRect().top);
      const mine = page.getByTestId('notices').getByTestId('notice').filter({ hasText: holder });
      // Now and Tasks are scoped to this test's initiative: a parallel worker's tasks, arriving live, would otherwise add rows
      // (and their layout shift) to the very window this test measures.
      const targets = [
        { name: 'Now', hash: `#/now?initiative=${slug}` },
        { name: 'Tasks', hash: `#/tasks?initiative=${slug}` },
        { name: 'task page', hash: `#/t/${task}` },
      ];
      for (const target of targets) {
        // A navigation of its own (a change of the fragment alone keeps the page, its banner and its counter).
        await page.goto('about:blank');
        await page.goto(`/${target.hash}`);
        await expect(content, `${target.name}: the content is drawn`).toBeVisible(FIVE_SECONDS);
        const before = await top();
        await expect(mine, `${target.name}: the banner shows the notice`).toBeVisible(FIVE_SECONDS);
        await page.waitForTimeout(AFTER_BANNER_MS);
        const after = await top();
        const shift = await page.evaluate(() => window.__layoutShift);
        expect(shift, `${target.name}: the layout-shift entries add up to`).toBe(0);
        expect(after, `${target.name}: the top y of the content, first drawn against 1 s after the banner`).toBe(before);
        await expectBannerAboveContent(page, target.name);
      }
    } finally {
      if (lease !== '') await api.post(`/api/v1/leases/${lease}/end`, { data: {} });
      await api.dispose();
    }
  });

  test('is above the content of every other view too: Initiatives, an initiative, Timeline, Actors, Archive, Notes and API keys', async ({ page, playwright, baseURL }) => {
    const slug = 'e2e-notice-views';
    const holder = 'notice-ops-views';
    const api = await actorApi(playwright, baseURL, holder);
    let lease = '';
    try {
      await created(api, '/api/v1/initiatives', { slug, title: 'Notices views' });
      const task = (await created(api, '/api/v1/tasks', { initiative: slug, title: 'Freeze the views' })).ref;
      lease = ((await created<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent: 'freeze across every view', resources: ['env:views-prod'], notice: true })).lease).ref;
      await page.addInitScript(() => {
        window.__layoutShift = 0;
        new PerformanceObserver((list) => {
          for (const entry of list.getEntries()) window.__layoutShift += (entry as unknown as { value: number }).value;
        }).observe({ type: 'layout-shift', buffered: true });
      });
      await page.route('**/api/v1/now', async (route) => {
        await new Promise((resolve) => setTimeout(resolve, NOW_DELAY_MS));
        await route.continue();
      });
      const content = page.locator(CONTENT).first();
      const mine = page.getByTestId('notices').getByTestId('notice').filter({ hasText: holder });
      const targets = [
        { name: 'Initiatives', hash: '#/initiatives' },
        { name: 'an initiative', hash: `#/i/${slug}` },
        { name: 'Timeline', hash: '#/timeline' },
        { name: 'Actors', hash: '#/actors' },
        { name: 'Archive', hash: '#/archive' },
        { name: 'Notes', hash: '#/notes' },
        { name: 'API keys', hash: '#/settings/keys' },
      ];
      for (const target of targets) {
        await page.goto('about:blank');
        await page.goto(`/${target.hash}`);
        await expect(content, `${target.name}: the content is drawn`).toBeVisible(FIVE_SECONDS);
        await expect(mine, `${target.name}: the banner shows the notice`).toBeVisible(FIVE_SECONDS);
        await expectBannerAboveContent(page, target.name);
        await expectNoSidewaysScroll(page);
      }
    } finally {
      if (lease !== '') await api.post(`/api/v1/leases/${lease}/end`, { data: {} });
      await api.dispose();
    }
  });
});
