import { expect, type Page, type Route } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, tagChip } from './chips';
import { logicViewports } from './viewports';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): a linked or restored tag chip names the sources its tag has
// NOW, so the per-tag reads a link, a restoration or a hash change makes are applied in the order they began. An older read whose
// answer lands after a newer one (here: held at the network until the newer one has been shown) changes neither the chip's
// accessible name nor its hover text, whichever way the sources moved in between and whether the newer read was a link or a
// restored filter. Each test seeds two initiatives of its own on the shared board: one whose tag is under test and an empty one
// the filter names, so the combined result is always empty and only the per-tag answers can name the sources.
const views = [
  { id: 'tasks', key: 'vh.view.v1.tasks' },
  { id: 'now', key: 'vh.view.v1.attention' },
  { id: 'timeline', key: 'vh.view.v1.timeline' },
  { id: 'archive', key: 'vh.view.v1.archive' },
] as const;

type Api = Awaited<ReturnType<typeof actorApi>>;
type Sources = Record<string, string[]>;

// The initiative `slug` (tag `slug`) with one task, which carries `slug` as its own tag when `own`; the initiative `${slug}-none`
// (tag `${slug}-none`) holds no task.
async function seed(api: Api, slug: string, own: boolean): Promise<string> {
  await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [slug] });
  await created(api, '/api/v1/initiatives', { slug: `${slug}-none`, title: `${slug}-none`, tags: [`${slug}-none`] });
  const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: `task of ${slug}`, tags: own ? [slug] : [] });
  return ref;
}

// A read of the sources of tags is ONE request for all the tags of the round: this one asks about `tag`.
const isProbeOf = (tag: string, raw: string) => {
  const url = new URL(raw);
  return url.pathname === '/api/v1/tasks/tag-sources' && url.searchParams.getAll('tag').includes(tag);
};
const sourcesIn = (tag: string, body: Sources) => [...(body[tag] ?? [])].sort();

// Holds the page's first read of the sources of `tag` at the network: `held` resolves with the sources it answered, `release()` lets it land.
async function holdFirstRead(page: Page, tag: string) {
  let seen = false;
  let captured!: (sources: string[]) => void;
  let release!: () => void;
  const held = new Promise<string[]>((resolve) => (captured = resolve));
  const gate = new Promise<void>((resolve) => (release = resolve));
  await page.route('**/api/v1/tasks/tag-sources?**', async (route: Route) => {
    if (seen || !isProbeOf(tag, route.request().url())) return route.continue();
    seen = true;
    const response = await route.fetch();
    captured(sourcesIn(tag, (await response.json()) as Sources));
    await gate;
    await route.fulfill({ response });
  });
  return { held, release };
}

const labels = async (page: Page, tag: string) => ({ name: await tagChip(page, tag).getAttribute('aria-label'), hover: await tagChip(page, tag).getAttribute('title') });
const named = (tag: string, sources: string) => ({ name: `${tag}, ${sources}`, hover: `${tag}: ${sources}` });

// Lets the held older read land, waits until the page has its whole answer and has drawn two frames since (every step after the
// body is microtasks), then checks the chip still names what the newer read gave it.
async function releaseAndKeep(page: Page, tag: string, release: () => void, older: string[], expected: { name: string; hover: string }) {
  const landed = page.waitForResponse((response) => isProbeOf(tag, response.url()));
  release();
  const response = await landed;
  expect(sourcesIn(tag, (await response.json()) as Sources)).toEqual(older);
  await response.finished();
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  expect(await labels(page, tag)).toEqual(expected);
}

// Moves, in the same page, to a new link that also names the empty initiative's tag: a new per-tag read, which lands first.
async function relink(page: Page, view: (typeof views)[number], slug: string) {
  await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}?initiative=${slug}-none&tag=${slug}&tag=${slug}-none`);
  await openChips(page);
  await expect(tagChip(page, `${slug}-none`)).toHaveAttribute('aria-pressed', 'true');
}

for (const viewport of logicViewports) {
  for (const view of views) {
    test.describe(`${view.id} at ${viewport.name}`, () => {
      test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

      test('an older link read landing last does not bring back a task source the newer link dropped', async ({ page, playwright, baseURL }) => {
        const slug = `src-order-drop-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const { held, release } = await holdFirstRead(page, slug);
        try {
          const ref = await seed(api, slug, true);
          await page.goto(`/#/${view.id}?initiative=${slug}-none&tag=${slug}`);
          expect(await held).toEqual(['initiative', 'task']);
          expect((await api.delete(`/api/v1/tasks/${ref}/tags/${slug}`)).status()).toBe(200);
          await relink(page, view, slug);
          await expect.poll(() => labels(page, slug)).toEqual(named(slug, 'initiative tag'));
          await releaseAndKeep(page, slug, release, ['initiative', 'task'], named(slug, 'initiative tag'));
        } finally {
          release();
          await api.dispose();
        }
      });

      test('an older link read landing last does not take away a task source the newer link found', async ({ page, playwright, baseURL }) => {
        const slug = `src-order-gain-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const { held, release } = await holdFirstRead(page, slug);
        try {
          const ref = await seed(api, slug, false);
          await page.goto(`/#/${view.id}?initiative=${slug}-none&tag=${slug}`);
          expect(await held).toEqual(['initiative']);
          expect((await api.put(`/api/v1/tasks/${ref}/tags/${slug}`)).status()).toBe(200);
          await relink(page, view, slug);
          await expect.poll(() => labels(page, slug)).toEqual(named(slug, 'task tag, initiative tag'));
          await releaseAndKeep(page, slug, release, ['initiative'], named(slug, 'task tag, initiative tag'));
        } finally {
          release();
          await api.dispose();
        }
      });

      test('an older link read landing after a restored filter does not overwrite the restored chip', async ({ page, playwright, baseURL }) => {
        const slug = `src-order-saved-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const { held, release } = await holdFirstRead(page, slug);
        try {
          const ref = await seed(api, slug, true);
          await page.goto(`/#/${view.id}?initiative=${slug}-none&tag=${slug}`);
          expect(await held).toEqual(['initiative', 'task']);
          expect((await api.delete(`/api/v1/tasks/${ref}/tags/${slug}`)).status()).toBe(200);
          // The saved query names one more tag than the link, so restoring it is a new filter (and a new per-tag read).
          const saved = `initiative=${slug}-none&tag=${slug}&tag=${slug}-none`;
          await page.evaluate(({ key, query }) => localStorage.setItem(key, JSON.stringify({ v: 1, query })), { key: view.key, query: saved });
          await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}`);
          await openChips(page);
          await expect(tagChip(page, `${slug}-none`)).toHaveAttribute('aria-pressed', 'true');
          await expect(tagChip(page, slug)).toHaveAttribute('aria-pressed', 'true');
          await expect.poll(() => labels(page, slug)).toEqual(named(slug, 'initiative tag'));
          await releaseAndKeep(page, slug, release, ['initiative', 'task'], named(slug, 'initiative tag'));
        } finally {
          release();
          await api.dispose();
        }
      });
    });
  }
}
