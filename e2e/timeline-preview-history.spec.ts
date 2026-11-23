import { expect, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { apiContext, FIVE_SECONDS, post, viewports } from './history-prefs-walk';

// How a Timeline preview closes (SPEC 11, Preview), measured on the history entry the browser keeps. A preview the page
// opened itself (a click, Enter) pushed an entry of its own, flagged `preview` in history.state: closing goes Back to the
// entry under it, and that flag survives a reload of the entry. A preview that came with the page (a link, a reload of a
// view that had none) has no such flag: closing rewrites the fragment in place, the entry and its key stay. Named
// timeline*.spec.ts so that it runs early, before the store has grown. Slugs are e2e-tph-<name>.
const entryKey = (page: Page) => page.evaluate(() => (history.state as { boardEntry?: string } | null)?.boardEntry ?? '');
const flagged = (page: Page) => page.evaluate(() => (history.state as { preview?: boolean } | null)?.preview === true);
const dialogOf = (page: Page, ref: string) => page.getByRole('dialog', { name: `Task ${ref}` });
const rowOf = (page: Page, ref: string) => page.locator('.vis-label.tl-task', { hasText: ref });

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  const slug = `e2e-tph-${name}`;
  const api = await apiContext(playwright, baseURL, `tph-${name}`);
  await post(api, '/api/v1/initiatives', { slug, title: `Preview history ${name}` }, 201);
  const a = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Task A' }, 201)).ref;
  const b = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Task B' }, 201)).ref;
  await api.dispose();
  return { slug, a, b, view: `/#/timeline?initiative=${slug}` };
}

async function openView(page: Page, url: string) {
  await page.goto('/#/tasks');
  await expect(page.getByTestId('tasks-view')).toBeVisible();
  await page.goto(url);
  await expect(page.getByTestId('timeline-view')).toBeVisible();
  await expect(page.locator('.vis-label.tl-task')).toHaveCount(2, FIVE_SECONDS);
  await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);
}

type Closer = 'Close' | 'Escape';
const close = async (page: Page, ref: string, how: Closer) => {
  if (how === 'Escape') await page.keyboard.press('Escape');
  else await dialogOf(page, ref).getByTestId('preview-close').click();
  await expect(dialogOf(page, ref)).toHaveCount(0);
};

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    for (const how of ['Close', 'Escape'] as const) {
      test(`a deep-linked preview closes in place with ${how}: the entry and its key stay, and Back leaves the view`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `link-${how.toLowerCase()}-${vp.name}`);
        await openView(page, `${s.view}&task=${s.a}`);
        await expect(dialogOf(page, s.a).getByTestId('task-title')).toHaveText('Task A', FIVE_SECONDS);
        expect(await flagged(page)).toBe(false);
        const key = await entryKey(page);
        expect(key).not.toBe('');
        await close(page, s.a, how);
        expect(page.url()).not.toContain('task=');
        expect(await entryKey(page)).toBe(key);
        await page.goBack();
        await expect(page).toHaveURL(/#\/tasks$/);
      });

      test(`a script-pushed preview reloaded keeps its flag and its key, and ${how} then goes Back to the view's own entry`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `push-${how.toLowerCase()}-${vp.name}`);
        await openView(page, s.view);
        const viewKey = await entryKey(page);
        await rowOf(page, s.a).click();
        await expect(dialogOf(page, s.a).getByTestId('task-title')).toHaveText('Task A', FIVE_SECONDS);
        const previewKey = await entryKey(page);
        expect(previewKey).not.toBe(viewKey);
        expect(await flagged(page)).toBe(true);

        await page.reload();
        await expect(dialogOf(page, s.a).getByTestId('task-title')).toHaveText('Task A', FIVE_SECONDS);
        expect(await flagged(page), 'the flag survives the reload').toBe(true);
        expect(await entryKey(page), 'the reload keeps the entry').toBe(previewKey);

        await close(page, s.a, how);
        expect(page.url()).not.toContain('task=');
        expect(await entryKey(page), 'closing went Back to the entry under the preview').toBe(viewKey);
        expect(await flagged(page)).toBe(false);
        await page.goBack();
        await expect(page).toHaveURL(/#\/tasks$/);
      });
    }

    test('a deep-linked preview that is reloaded still closes in place, and a second preview opened from the view then closes by Back', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `both-${vp.name}`);
      await openView(page, `${s.view}&task=${s.a}`);
      await page.reload();
      await expect(dialogOf(page, s.a).getByTestId('task-title')).toHaveText('Task A', FIVE_SECONDS);
      expect(await flagged(page)).toBe(false);
      const key = await entryKey(page);
      await close(page, s.a, 'Close');
      expect(await entryKey(page)).toBe(key);

      await rowOf(page, s.b).click();
      await expect(dialogOf(page, s.b).getByTestId('task-title')).toHaveText('Task B', FIVE_SECONDS);
      expect(await entryKey(page)).not.toBe(key);
      expect(await flagged(page)).toBe(true);
      await close(page, s.b, 'Close');
      expect(await entryKey(page)).toBe(key);
      await page.goBack();
      await expect(page).toHaveURL(/#\/tasks$/);
    });

    test('the browser Back from a reloaded script-pushed preview closes it and lands on the view, as the close controls do', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `back-${vp.name}`);
      await openView(page, s.view);
      const viewKey = await entryKey(page);
      await rowOf(page, s.b).click();
      await expect(dialogOf(page, s.b)).toBeVisible(FIVE_SECONDS);
      await page.reload();
      await expect(dialogOf(page, s.b).getByTestId('task-title')).toHaveText('Task B', FIVE_SECONDS);
      await page.goBack();
      await expect(dialogOf(page, s.b)).toHaveCount(0);
      expect(await entryKey(page)).toBe(viewKey);
      await expect(page.getByTestId('timeline-view')).toBeVisible();
    });
  });
}
