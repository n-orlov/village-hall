import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, tagChip } from './chips';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): only the navigation that is current reads the sources of
// its tags. A saved filter is restored once the board's list of initiatives has answered; when a link replaces it while that list
// is still out, the restoration is abandoned and must start no per-tag read: a read it began after the link's would be the newest
// for its tags and make the page drop the link's own answer, leaving the chip with a bare name and no hover text. Each test seeds
// initiatives and tasks of its own on the shared board: the combined filter of the link is always empty, so only the per-tag
// answers can name the sources of its chips.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const views = [
  { id: 'tasks', key: 'vh.view.v1.tasks' },
  { id: 'now', key: 'vh.view.v1.attention' },
  { id: 'timeline', key: 'vh.view.v1.timeline' },
  { id: 'archive', key: 'vh.view.v1.archive' },
] as const;

type Api = Awaited<ReturnType<typeof actorApi>>;
type Marks = { items: { tag_marks: { tag: string; source: string }[] }[] };

function gate() {
  let open!: () => void;
  const wait = new Promise<void>((resolve) => (open = resolve));
  return { open, wait };
}

const labels = async (page: Page, tag: string) => ({ name: await tagChip(page, tag).getAttribute('aria-label'), hover: await tagChip(page, tag).getAttribute('title') });
const named = (tag: string, sources: string) => ({ name: `${tag}, ${sources}`, hover: `${tag}: ${sources}` });

// The initiative `slug` carries one task whose own tag is `tag`; `${slug}-scope` is an empty initiative that holds the marker tag, so
// the combined filter (scope + tag + marker) has no row.
async function seed(api: Api, slug: string, tags: string[]): Promise<{ scope: string; marker: string }> {
  const scope = `${slug}-scope`;
  const marker = `${slug}-marker`;
  await created(api, '/api/v1/initiatives', { slug, title: slug });
  await created(api, '/api/v1/initiatives', { slug: scope, title: scope, tags: [marker] });
  for (const tag of tags) await created(api, '/api/v1/tasks', { initiative: slug, title: `Task carrying ${tag}`, tags: [tag] });
  return { scope, marker };
}

// Holds the board's list of initiatives at the network until `release()`, and counts (and optionally holds the first of) the per-tag reads.
async function hold(page: Page, holdFirstOf?: string) {
  const catalog = gate();
  const catalogStarted = gate();
  const firstHeld = gate();
  const firstStarted = gate();
  const reads = new Map<string, number>();
  await page.route('**/api/v1/initiatives*', async (route) => {
    if (new URL(route.request().url()).pathname !== '/api/v1/initiatives') return route.continue();
    const response = await route.fetch();
    catalogStarted.open();
    await catalog.wait;
    await route.fulfill({ response });
  });
  await page.route('**/api/v1/tasks?**', async (route) => {
    const url = new URL(route.request().url());
    const tag = url.searchParams.get('tag');
    const probe = url.pathname === '/api/v1/tasks' && url.searchParams.get('limit') === '1000' && tag !== null && !url.searchParams.has('initiative');
    if (!probe) return route.continue();
    reads.set(tag, (reads.get(tag) ?? 0) + 1);
    const response = await route.fetch();
    if (tag === holdFirstOf && reads.get(tag) === 1) {
      firstStarted.open();
      await firstHeld.wait;
    }
    await route.fulfill({ response });
  });
  return {
    reads,
    catalogStarted: catalogStarted.wait,
    firstStarted: firstStarted.wait,
    releaseCatalog: catalog.open,
    releaseFirst: firstHeld.open,
    sourcesOfFirst: async (tag: string, page: Page) => {
      const answer = await page.request.get(`/api/v1/tasks?status=all&limit=1000&tag=${tag}`);
      return [...new Set(((await answer.json()) as Marks).items.flatMap((i) => i.tag_marks.filter((m) => m.tag === tag).map((m) => m.source)))].sort();
    },
  };
}

// Opens the page on #/actors with `saved` as the view's saved query and starts the restoration (its list of initiatives is held).
async function startRestore(page: Page, view: (typeof views)[number], saved: string, held: Awaited<ReturnType<typeof hold>>) {
  await page.addInitScript(({ key, query }) => localStorage.setItem(key, JSON.stringify({ v: 1, query })), { key: view.key, query: saved });
  await page.goto('/#/actors');
  await expect(page.getByTestId('actors-view')).toBeVisible();
  await page.evaluate((id) => (window.location.hash = `#/${id}`), view.id);
  await held.catalogStarted;
}

// After the link: lets the abandoned restoration run (the list lands), then waits until the page has drawn two frames.
async function settle(page: Page, held: Awaited<ReturnType<typeof hold>>) {
  held.releaseCatalog();
  await page.waitForTimeout(800);
  held.releaseFirst();
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

for (const viewport of viewports) {
  for (const view of views) {
    test.describe(`${view.id} at ${viewport.name}`, () => {
      test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

      test('a restoration a link supersedes cannot invalidate the link\'s own source read', async ({ page, playwright, baseURL }) => {
        const slug = `rs-link-${view.id}-${viewport.name}`;
        const tag = `${slug}-own`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const held = await hold(page, tag);
        try {
          const { scope, marker } = await seed(api, slug, [tag]);
          await startRestore(page, view, `initiative=${scope}&tag=${tag}`, held);
          await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}?initiative=${scope}&tag=${tag}&tag=${marker}`);
          await held.firstStarted;
          expect(await held.sourcesOfFirst(tag, page)).toEqual(['task']);
          await openChips(page);
          await expect(tagChip(page, marker)).toHaveAttribute('aria-pressed', 'true');
          await settle(page, held);
          await expect.poll(() => labels(page, tag)).toEqual(named(tag, 'task tag'));
          // The abandoned restoration read nothing: the link's one read is the only one the tag had.
          expect(held.reads.get(tag)).toBe(1);
        } finally {
          held.releaseCatalog();
          held.releaseFirst();
          await api.dispose();
        }
      });

      test('a restoration a link with other tags supersedes reads none of its tags and the link\'s chip names its source', async ({ page, playwright, baseURL }) => {
        const slug = `rs-other-${view.id}-${viewport.name}`;
        const saved = `${slug}-saved`;
        const linked = `${slug}-linked`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const held = await hold(page, linked);
        try {
          const { scope, marker } = await seed(api, slug, [saved, linked]);
          await startRestore(page, view, `initiative=${scope}&tag=${saved}`, held);
          await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}?initiative=${scope}&tag=${linked}&tag=${marker}`);
          await held.firstStarted;
          await openChips(page);
          await expect(tagChip(page, marker)).toHaveAttribute('aria-pressed', 'true');
          await settle(page, held);
          await expect.poll(() => labels(page, linked)).toEqual(named(linked, 'task tag'));
          expect(held.reads.get(saved) ?? 0).toBe(0);
          expect(held.reads.get(linked)).toBe(1);
        } finally {
          held.releaseCatalog();
          held.releaseFirst();
          await api.dispose();
        }
      });

      test('a restoration a link naming no tag supersedes reads no tag at all', async ({ page, playwright, baseURL }) => {
        const slug = `rs-none-${view.id}-${viewport.name}`;
        const saved = `${slug}-saved`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const held = await hold(page);
        try {
          const { scope } = await seed(api, slug, [saved]);
          await startRestore(page, view, `initiative=${scope}&tag=${saved}`, held);
          await page.evaluate((hash) => (window.location.hash = hash), `#/${view.id}?initiative=${scope}`);
          await settle(page, held);
          expect(held.reads.get(saved) ?? 0).toBe(0);
          await openChips(page);
          // The link names no tag: whether or not the page offers the saved one, it is not selected.
          expect(await tagChip(page, saved).evaluateAll((chips) => chips.filter((chip) => chip.getAttribute('aria-pressed') === 'true').length)).toBe(0);
        } finally {
          held.releaseCatalog();
          held.releaseFirst();
          await api.dispose();
        }
      });
    });
  }
}
