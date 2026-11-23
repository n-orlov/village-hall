import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test } from './fixtures';
import { expectInitiatives, foldChips, pickInitiative, pickTag } from './chips';

// The Timeline on a phone (PRD R10, SPEC 11): opening #/timeline at 390x844 shows the first task row in full, above the
// tab bar, with the page not scrolled, with the filters at their defaults and with all four set (values as long as the
// board lets a name be, which is what makes the filters wrap the most). The controls above the chart stay 44 px targets
// (touch.spec.ts covers the Timeline's).
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

test.use({ viewport: { width: 390, height: 844 } });

/** The boxes the rule compares, read together so one layout pass answers all of them. */
async function boxes(page: Page) {
  return page.evaluate(() => {
    const box = (selector: string) => {
      const r = document.querySelector(selector)!.getBoundingClientRect();
      return { top: r.top, bottom: r.bottom };
    };
    return {
      scrollY: window.scrollY,
      height: window.innerHeight,
      row: box('.vis-labelset .vis-label.tl-task'),
      tabBar: box('[data-testid="tabbar"]'),
    };
  });
}

async function expectFirstRowInView(page: Page) {
  const row = page.locator('.vis-labelset .vis-label.tl-task').first();
  await expect(row).toBeVisible(FIVE_SECONDS);
  // vis-timeline lays its rows out again about half a second after the first draw: read the boxes until they settle in view.
  await expect(async () => {
    const seen = await boxes(page);
    expect(seen.scrollY).toBe(0);
    expect(seen.row.top).toBeGreaterThanOrEqual(0);
    expect(seen.row.bottom).toBeLessThanOrEqual(seen.height);
    expect(seen.row.bottom).toBeLessThanOrEqual(seen.tabBar.top);
  }).toPass(FIVE_SECONDS);
}

async function seed(page: Page, api: APIRequestContext, slug: string, tag: string) {
  await post(api, '/api/v1/initiatives', { slug, title: `Phone ${slug}`, tags: [tag] }, 201);
  const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Phone first row' }, 201);
  await post(api, '/api/v1/leases', { task: ref, intent: 'phone', resources: [`repo:${slug}`] }, 201);
}

test('with the filters at their defaults the first task row is in the viewport above the tab bar, unscrolled', async ({ page, playwright, baseURL }) => {
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'tlp-default' },
  });
  await seed(page, api, 'e2e-tlp-default', 'tlp-default');
  await page.goto('/#/timeline');
  await expectInitiatives(page, []);
  await expectFirstRowInView(page);
});

test('with all four filters set the first task row is in the viewport above the tab bar, unscrolled', async ({ page, playwright, baseURL }) => {
  const slug = 'e2e-tlp-filtered-with-a-long-initiative';
  const tag = 'tlp-filtered-with-a-long-tag-name';
  const actor = 'tlp-filtered-with-a-long-handle';
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor },
  });
  await seed(page, api, slug, tag);
  await page.goto('/#/timeline');
  await expect(page.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
  await pickInitiative(page, slug);
  await pickTag(page, tag);
  await page.getByTestId('actor-filter').selectOption(actor);
  await page.getByTestId('closed-filter').selectOption('30');
  await expect(page.getByTestId('clear-filters')).toBeVisible();
  await expect(page).toHaveURL(/closed=30/);
  // Picking chips left the phone's folded filter open and scrolled the page to reach them: fold it again and go back to the top.
  await foldChips(page);
  await page.evaluate(() => window.scrollTo(0, 0));
  await expectFirstRowInView(page);
  // A reload opens the same view, unscrolled.
  await page.reload();
  await expectFirstRowInView(page);
});
