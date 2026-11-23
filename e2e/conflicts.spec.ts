import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// Live leases that share a resource (PRD R11): two leases started through the API show as a conflict on both task pages
// and in the Now panel of the Tasks view, and ending one clears it. Leases of other specs stay live on the shared board,
// so every assertion is scoped to a resource and to lease refs this test made.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

async function apiAs(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, actor: string) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor },
  });
}

async function post<T>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

async function startLease(api: APIRequestContext, task: string, intent: string, resource: string): Promise<string> {
  const started = await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task, intent, resources: [resource] }, 201);
  return started.lease.ref;
}

// The Now panel is folded to its summary on the phone; it is always open from 768 px.
async function openNow(page: Page) {
  const toggle = page.getByTestId('now-toggle');
  if (await toggle.isVisible()) {
    if ((await toggle.getAttribute('aria-expanded')) !== 'true') await toggle.click();
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  }
  await expect(page.getByTestId('now-body')).toBeVisible();
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('two leases sharing a resource show as a conflict on both task pages and in the Now panel until one ends', async ({ page, playwright, baseURL }) => {
      const alice = await apiAs(playwright, baseURL, `alice-${vp.name}`);
      const bob = await apiAs(playwright, baseURL, `bob-${vp.name}`);
      const slug = `e2e-lc-${vp.name}`;
      const resource = `repo:cf-${vp.name}`;
      await post(alice, '/api/v1/initiatives', { slug, title: `Conflicts ${slug}` }, 201);
      const first = (await post<{ ref: string }>(alice, '/api/v1/tasks', { initiative: slug, title: 'First task' }, 201)).ref;
      const second = (await post<{ ref: string }>(alice, '/api/v1/tasks', { initiative: slug, title: 'Second task' }, 201)).ref;

      // One lease alone is no conflict.
      const aliceLease = await startLease(alice, first, 'editing the first', resource);
      await page.goto(`/#/t/${first}`);
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(first);
      await expect(page.getByTestId('lease')).toHaveCount(1);
      await expect(page.getByTestId('task-conflicts')).toHaveCount(0);

      // A second lease on the same resource, on another task and by another actor.
      const bobLease = await startLease(bob, second, 'editing the second', resource);

      // The first task page names the resource, the other holder and links the other task. The page is read again
      // (the address is the same, so a goto would not reload it).
      await page.reload();
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(first);
      const onFirst = page.getByTestId('conflict');
      await expect(onFirst).toHaveCount(1);
      await expect(onFirst).toHaveAttribute('data-resource', resource);
      await expect(onFirst.getByTestId('conflict-text')).toHaveText(`${resource}: also held by bob-${vp.name} on ${second}`);
      await expect(onFirst.getByTestId('conflict-task')).toHaveAttribute('href', `#/t/${second}`);

      // The link leads to the second task page, which names the first as the other holder.
      await onFirst.getByTestId('conflict-task').click();
      await expect(page).toHaveURL(new RegExp(`#/t/${second}$`));
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(second);
      const onSecond = page.getByTestId('conflict');
      await expect(onSecond).toHaveCount(1);
      await expect(onSecond.getByTestId('conflict-text')).toHaveText(`${resource}: also held by alice-${vp.name} on ${first}`);
      await expect(onSecond.getByTestId('conflict-task')).toHaveAttribute('href', `#/t/${first}`);

      // The Now panel lists both live leases, flagged, and the conflict on the resource.
      await page.goto(`/#/tasks?initiative=${slug}`);
      await openNow(page);
      const conflict = page.locator(`[data-testid=now-conflict][data-resource="${resource}"]`);
      await expect(conflict).toHaveCount(1);
      await expect(conflict).toContainText(`${resource}: held by alice-${vp.name} on ${first}, bob-${vp.name} on ${second}`);
      for (const [lease, holder, task] of [
        [aliceLease, `alice-${vp.name}`, first],
        [bobLease, `bob-${vp.name}`, second],
      ] as const) {
        const row = page.locator(`[data-testid=now-lease][data-ref=${lease}]`);
        await expect(row).toHaveAttribute('data-task', task);
        await expect(row.getByTestId('now-holder')).toHaveText(holder);
        await expect(row.getByTestId('now-flag')).toBeVisible();
      }
      await expect(page.getByTestId('now-summary')).toContainText('conflict');
      // The panel never makes the page scroll sideways.
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);

      // Ending one lease clears the conflict: the panel after Refresh, and the task page after a reload.
      await post(bob, `/api/v1/leases/${bobLease}/end`, {}, 200);
      await page.getByTestId('now-refresh').click();
      await expect(page.locator(`[data-testid=now-lease][data-ref=${bobLease}]`)).toHaveCount(0);
      await expect(conflict).toHaveCount(0);
      const remaining = page.locator(`[data-testid=now-lease][data-ref=${aliceLease}]`);
      await expect(remaining).toBeVisible();
      await expect(remaining.getByTestId('now-flag')).toHaveCount(0);

      await page.goto(`/#/t/${first}`);
      await page.reload();
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(first);
      await expect(page.getByTestId('lease')).toHaveCount(1);
      await expect(page.getByTestId('task-conflicts')).toHaveCount(0);
    });

    test('the Now panel folds on the phone and stays open on the desktop', async ({ page }) => {
      await page.goto('/#/tasks');
      await expect(page.getByTestId('now-panel')).toBeVisible();
      const toggle = page.getByTestId('now-toggle');
      if (vp.name === 'phone') {
        await expect(toggle).toHaveAttribute('aria-expanded', 'false');
        await expect(page.getByTestId('now-body')).toBeHidden();
        const box = await toggle.boundingBox();
        expect(box!.height).toBeGreaterThanOrEqual(44);
        await toggle.click();
        await expect(page.getByTestId('now-body')).toBeVisible();
        await toggle.click();
        await expect(page.getByTestId('now-body')).toBeHidden();
      } else {
        await expect(toggle).toBeHidden();
        await expect(page.getByTestId('now-body')).toBeVisible();
      }
    });
  });
}
