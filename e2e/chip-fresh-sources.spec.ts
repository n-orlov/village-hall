import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, tagChip } from './chips';
import { logicViewports } from './viewports';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): a linked or restored tag chip names the sources the tag has
// NOW. The per-tag read a link or a restoration makes is the server's answer for that tag, so a source an earlier read showed and
// the server no longer reports goes from the chip's accessible name and hover text, even when the combined filter matches no row.
// Each test seeds two initiatives of its own on the shared board: one whose tag is under test and an empty one the filter names,
// so the combined result is always empty and only the per-tag answer can name the sources.
const views = [
  { id: 'tasks', key: 'vh.view.v1.tasks' },
  { id: 'now', key: 'vh.view.v1.attention' },
  { id: 'timeline', key: 'vh.view.v1.timeline' },
  { id: 'archive', key: 'vh.view.v1.archive' },
] as const;

type Api = Awaited<ReturnType<typeof actorApi>>;

// The tag `slug` on the initiative `slug` and on its one task; the initiative `${slug}-none` (tag `${slug}-none`) holds no task.
async function seed(api: Api, slug: string): Promise<string> {
  await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [slug] });
  await created(api, '/api/v1/initiatives', { slug: `${slug}-none`, title: `${slug}-none`, tags: [`${slug}-none`] });
  const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: `task of ${slug}`, tags: [slug] });
  return ref;
}

async function expectSources(page: Page, tag: string, sources: string) {
  const chip = tagChip(page, tag);
  await expect(chip).toHaveAccessibleName(`${tag}, ${sources}`);
  await expect(chip).toHaveAttribute('title', `${tag}: ${sources}`);
}

// Opens the view at a link naming the empty initiative and the tag, and checks the chip names both sources to begin with.
async function openLinked(page: Page, view: (typeof views)[number], slug: string) {
  await page.goto(`/#/${view.id}?initiative=${slug}-none&tag=${slug}`);
  await openChips(page);
  await expectSources(page, slug, 'task tag, initiative tag');
}

// Moves, in the same page, to a new link that also names the empty initiative's tag: a new per-tag read.
async function relink(page: Page, view: (typeof views)[number], slug: string) {
  await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}?initiative=${slug}-none&tag=${slug}&tag=${slug}-none`);
  await expect(tagChip(page, `${slug}-none`)).toHaveAttribute('aria-pressed', 'true');
}

for (const viewport of logicViewports) {
  for (const view of views) {
    test.describe(`${view.id} at ${viewport.name}`, () => {
      test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

      test('a new link drops the task source after the task loses its own tag', async ({ page, playwright, baseURL }) => {
        const slug = `fresh-src-task-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        try {
          const ref = await seed(api, slug);
          await openLinked(page, view, slug);
          expect((await api.delete(`/api/v1/tasks/${ref}/tags/${slug}`)).status()).toBe(200);
          await relink(page, view, slug);
          await expectSources(page, slug, 'initiative tag');
        } finally {
          await api.dispose();
        }
      });

      test('a new link drops the initiative source after the initiative loses the tag', async ({ page, playwright, baseURL }) => {
        const slug = `fresh-src-ini-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        try {
          await seed(api, slug);
          await openLinked(page, view, slug);
          expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [] } })).ok()).toBe(true);
          await relink(page, view, slug);
          await expectSources(page, slug, 'task tag');
        } finally {
          await api.dispose();
        }
      });

      test('a restored filter drops the task source after the task loses its own tag', async ({ page, playwright, baseURL }) => {
        const slug = `fresh-src-saved-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        try {
          const ref = await seed(api, slug);
          await openLinked(page, view, slug);
          expect((await api.delete(`/api/v1/tasks/${ref}/tags/${slug}`)).status()).toBe(200);
          // The saved query names one more tag than the link, so restoring it is a new filter (and a new per-tag read).
          const saved = `initiative=${slug}-none&tag=${slug}&tag=${slug}-none`;
          await page.evaluate(({ key, query }) => localStorage.setItem(key, JSON.stringify({ v: 1, query })), { key: view.key, query: saved });
          await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}`);
          await expect(tagChip(page, `${slug}-none`)).toHaveAttribute('aria-pressed', 'true');
          await expect(tagChip(page, slug)).toHaveAttribute('aria-pressed', 'true');
          await expectSources(page, slug, 'initiative tag');
        } finally {
          await api.dispose();
        }
      });
    });
  }
}
