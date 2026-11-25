import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, tagChip } from './chips';
import { logicViewports } from './viewports';

// The chips of an open Now page follow the sources the board has now (PRD R1): an event that changes a tag's sources (an
// initiative's tags, a task's tags) replaces the source information the page kept from earlier answers, also for a chip whose
// filter returns no row, and a reply that began before the event cannot put back a source the event removed.
const streamUp = (page: Page) => page.waitForResponse((r) => r.headers()['content-type']?.includes('text/event-stream') === true);
const aria = (page: Page, tag: string) => tagChip(page, tag).getAttribute('aria-label');

// The page's own per-tag lookup of `tag` (the read that names a link's or a restored tag's sources): held until released,
// `captured` is the sources it answered.
async function holdLookup(page: Page, tag: string) {
  const isLookup = (url: URL) => url.pathname === '/api/v1/tasks/tag-sources' && url.searchParams.getAll('tag').includes(tag);
  const captured = Promise.withResolvers<string[]>();
  const gate = Promise.withResolvers<void>();
  let armed = true;
  await page.route(isLookup, async (route) => {
    if (!armed) return route.fallback();
    armed = false;
    const response = await route.fetch();
    const body = (await response.json()) as Record<string, string[]>;
    captured.resolve([...(body[tag] ?? [])].sort());
    await gate.promise;
    await route.fulfill({ response });
  });
  return {
    captured: captured.promise,
    release: async () => {
      const landed = page.waitForResponse((r) => isLookup(new URL(r.url())));
      gate.resolve();
      await (await landed).finished();
      await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
    },
  };
}


for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a delayed linked lookup cannot reinstate an initiative tag an event removed', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-livesrc-a-${n}`;
      const old = `livesrc-old-${n}`;
      const latest = `livesrc-latest-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:livesrc');
      await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [old] });
      await created(api, '/api/v1/tasks', { initiative: slug, title: 'inherits only' });
      const held = await holdLookup(page, old);
      await page.goto(`/#/now?tag=${old}&tag=livesrc-nomatch-${n}`);
      await streamUp(page);
      await openChips(page);
      expect(await held.captured).toEqual(['initiative']);
      await expect(tagChip(page, old)).toHaveAttribute('aria-label', `${old}, initiative tag`);
      expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [latest] } })).ok()).toBe(true);
      await expect(tagChip(page, latest)).toHaveAttribute('aria-label', `${latest}, initiative tag`);
      await expect(tagChip(page, old)).toHaveAttribute('aria-label', old);
      await held.release();
      await expect(tagChip(page, old)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, old)).toHaveAttribute('aria-label', old, { timeout: 2000 });
      await expect(tagChip(page, old)).not.toHaveAttribute('title', /initiative tag/);
      await api.dispose();
    });

    test('a selected chip reflects removal of its last task source while Now stays open (no rows)', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-livesrc-b-${n}`;
      const tag = `livesrc-task-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:livesrc');
      await created(api, '/api/v1/initiatives', { slug, title: slug });
      const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: 'owns tag', tags: [tag] });
      await page.goto(`/#/now?tag=${tag}`);
      await streamUp(page);
      await openChips(page);
      const row = page.locator(`[data-testid="recent-row"][data-task="${ref}"]`);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', `${tag}, task tag`);
      expect((await api.delete(`/api/v1/tasks/${ref}/tags/${tag}`)).status()).toBe(200);
      await expect(row).toHaveCount(0);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', tag, { timeout: 5000 });
      await expect(tagChip(page, tag)).not.toHaveAttribute('title', /task tag/);
      await api.dispose();
    });

    test('an unselected chip nothing carries any more goes when its last task loses the tag', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-livesrc-c-${n}`;
      const tag = `livesrc-gone-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:livesrc');
      await created(api, '/api/v1/initiatives', { slug, title: slug });
      const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: 'owns tag', tags: [tag] });
      await page.goto(`/#/now?initiative=${slug}`);
      await streamUp(page);
      await openChips(page);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', `${tag}, task tag`);
      expect((await api.delete(`/api/v1/tasks/${ref}/tags/${tag}`)).status()).toBe(200);
      await expect(tagChip(page, tag)).toHaveCount(0);
      await api.dispose();
    });

    test('a delayed linked lookup cannot reinstate a task tag a later event removed', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-livesrc-d-${n}`;
      const tag = `livesrc-late-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:livesrc');
      await created(api, '/api/v1/initiatives', { slug, title: slug });
      const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: 'owns tag', tags: [tag] });
      const held = await holdLookup(page, tag);
      await page.goto(`/#/now?tag=${tag}`);
      await streamUp(page);
      await openChips(page);
      expect(await held.captured).toEqual(['task']);
      expect((await api.delete(`/api/v1/tasks/${ref}/tags/${tag}`)).status()).toBe(200);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', tag, { timeout: 5000 });
      await held.release();
      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', tag, { timeout: 2000 });
      expect(await aria(page, tag)).toBe(tag);
      await api.dispose();
    });
  });
}
