import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, tagChip } from './chips';

// The board read of an open Now page (`GET /attention/now`) carries the tag marks of the tasks it shows, and the chips take
// sources from them (PRD R1). A board reply that began before an event that changed a tag's sources must not put back a
// source (or a chip) that event removed, and the newer reply that follows does not bring it back either: the filter, the
// fragment and the still-valid sources stay as they are.
const streamUp = (page: Page) => page.waitForResponse((r) => r.headers()['content-type']?.includes('text/event-stream') === true);
const isBoard = (url: URL) => url.pathname === '/api/v1/attention/now';
const settle = (page: Page) => page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));

interface Held {
  /** The sources the reply names for `tag` on task `ref`. */
  captured: Promise<string[]>;
  /** Lets the reply land and waits until the page has drawn it. */
  release: () => Promise<void>;
  /** Lets the reply land without waiting (clean-up). */
  free: () => void;
}

// Holds the next `count` board replies, each until released, in the order the page asked for them.
async function holdBoards(page: Page, ref: string, tag: string, count: number): Promise<Held[]> {
  const gates = Array.from({ length: count }, () => ({ captured: Promise.withResolvers<string[]>(), gate: Promise.withResolvers<void>() }));
  let reads = 0;
  await page.route(isBoard, async (route) => {
    const held = gates[reads++];
    if (held === undefined) return route.fallback();
    const response = await route.fetch();
    const body = (await response.json()) as { task_marks: Record<string, { tag_marks: { tag: string; source: string }[] }> };
    held.captured.resolve((body.task_marks[ref]?.tag_marks ?? []).filter((m) => m.tag === tag).map((m) => m.source));
    await held.gate.promise;
    await route.fulfill({ response });
  });
  return gates.map((held) => ({
    captured: held.captured.promise,
    release: async () => {
      const landed = page.waitForResponse((r) => isBoard(new URL(r.url())));
      held.gate.resolve();
      await (await landed).finished();
      await settle(page);
    },
    free: () => held.gate.resolve(),
  }));
}

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a board reply begun before a task lost its tag cannot restore the task source, before or after the newer reply', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const scope = `e2e-boardsrc-a-${n}`;
      const catalog = `e2e-boardsrc-a-cat-${n}`;
      const tag = `boardsrc-shared-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:boardsrc');
      await created(api, '/api/v1/initiatives', { slug: catalog, title: catalog, tags: [tag] });
      await created(api, '/api/v1/initiatives', { slug: scope, title: scope });
      const { ref } = await created(api, '/api/v1/tasks', { initiative: scope, title: 'owns the shared tag', tags: [tag] });
      const fragment = `#/now?initiative=${scope}&tag=${tag}`;
      const up = streamUp(page);
      await page.goto(`/${fragment}`);
      await up;
      await openChips(page);
      const row = page.locator(`[data-testid="recent-row"][data-task="${ref}"]`);
      const chip = tagChip(page, tag);
      await expect(row).toHaveCount(1);
      await expect(chip).toHaveAttribute('aria-label', `${tag}, task tag, initiative tag`);
      const [older, newer] = await holdBoards(page, ref, tag, 2);
      try {
        await created(api, '/api/v1/notes', { task: ref, summary: 'a note reads the board again' });
        expect(await older!.captured).toEqual(['task']);
        expect((await api.delete(`/api/v1/tasks/${ref}/tags/${tag}`)).status()).toBe(200);
        expect(await newer!.captured).toEqual([]);
        await expect(chip).toHaveAttribute('aria-label', `${tag}, initiative tag`);
        await older!.release();
        expect(await chip.getAttribute('aria-label')).toBe(`${tag}, initiative tag`);
        await newer!.release();
        await expect(row).toHaveCount(0);
        await expect(chip).toHaveAttribute('aria-pressed', 'true');
        expect(await chip.getAttribute('aria-label')).toBe(`${tag}, initiative tag`);
        expect(new URL(page.url()).hash).toBe(fragment);
      } finally {
        older!.free();
        newer!.free();
        await api.dispose();
      }
    });

    test('a board reply begun before an initiative dropped a tag cannot restore the initiative source', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-boardsrc-b-${n}`;
      const tag = `boardsrc-inherited-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:boardsrc');
      await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [tag] });
      const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: 'inherits the tag' });
      const fragment = `#/now?initiative=${slug}&tag=${tag}`;
      const up = streamUp(page);
      await page.goto(`/${fragment}`);
      await up;
      await openChips(page);
      const row = page.locator(`[data-testid="recent-row"][data-task="${ref}"]`);
      const chip = tagChip(page, tag);
      await expect(row).toHaveCount(1);
      await expect(chip).toHaveAttribute('aria-label', `${tag}, initiative tag`);
      const [older, newer] = await holdBoards(page, ref, tag, 2);
      try {
        await created(api, '/api/v1/notes', { task: ref, summary: 'a note reads the board again' });
        expect(await older!.captured).toEqual(['initiative']);
        expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [] } })).ok()).toBe(true);
        expect(await newer!.captured).toEqual([]);
        await expect(chip).toHaveAttribute('aria-label', tag);
        await older!.release();
        expect(await chip.getAttribute('aria-label')).toBe(tag);
        await newer!.release();
        await expect(row).toHaveCount(0);
        await expect(chip).toHaveAttribute('aria-pressed', 'true');
        expect(await chip.getAttribute('aria-label')).toBe(tag);
        expect(new URL(page.url()).hash).toBe(fragment);
      } finally {
        older!.free();
        newer!.free();
        await api.dispose();
      }
    });

    test('a board reply begun before a task lost its last tag cannot bring back the unselected chip', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-boardsrc-c-${n}`;
      const tag = `boardsrc-gone-${n}`;
      const kept = `boardsrc-kept-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:boardsrc');
      await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [kept] });
      const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: 'owns the tag', tags: [tag] });
      const fragment = `#/now?initiative=${slug}`;
      const up = streamUp(page);
      await page.goto(`/${fragment}`);
      await up;
      await openChips(page);
      const row = page.locator(`[data-testid="recent-row"][data-task="${ref}"]`);
      await expect(row).toHaveCount(1);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', `${tag}, task tag`);
      await expect(tagChip(page, kept)).toHaveAttribute('aria-label', `${kept}, initiative tag`);
      const [older, newer] = await holdBoards(page, ref, tag, 2);
      try {
        await created(api, '/api/v1/notes', { task: ref, summary: 'a note reads the board again' });
        expect(await older!.captured).toEqual(['task']);
        expect((await api.delete(`/api/v1/tasks/${ref}/tags/${tag}`)).status()).toBe(200);
        expect(await newer!.captured).toEqual([]);
        await expect(tagChip(page, tag)).toHaveCount(0);
        await older!.release();
        expect(await tagChip(page, tag).count()).toBe(0);
        await newer!.release();
        await expect(row).toHaveCount(1);
        expect(await tagChip(page, tag).count()).toBe(0);
        await expect(tagChip(page, kept)).toHaveAttribute('aria-label', `${kept}, initiative tag`);
        expect(new URL(page.url()).hash).toBe(fragment);
      } finally {
        older!.free();
        newer!.free();
        await api.dispose();
      }
    });
  });
}
