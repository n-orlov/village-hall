import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { chosenTags, openChips, tagChip } from './chips';
import { logicViewports } from './viewports';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): only the navigation that is current reads the sources of
// its tags or sets the filter. A chip the user clicks (or, on Archive, a window the user picks) is newer than a saved filter still
// being restored: the restoration, waiting on the board's list of initiatives, is abandoned. It must start no per-tag read, send
// no request under its own filter and leave the chips, the fragment and the saved query as the user left them. The user's change
// is then the navigation that reads: it reads, as its own, the sources of each tag it keeps chosen that the abandoned
// restoration had named but not yet answered for, so that chip still names its source. Each test seeds
// initiatives and tasks of its own on the shared board: the combined filter is always empty, so only the per-tag answers can name
// the sources of its chips.
const views = [
  { id: 'tasks', key: 'vh.view.v1.tasks' },
  { id: 'now', key: 'vh.view.v1.attention' },
  { id: 'timeline', key: 'vh.view.v1.timeline' },
  { id: 'archive', key: 'vh.view.v1.archive' },
] as const;
type View = (typeof views)[number];
type Api = Awaited<ReturnType<typeof actorApi>>;

function gate() {
  let open!: () => void;
  const wait = new Promise<void>((resolve) => (open = resolve));
  return { open, wait };
}

const labels = async (page: Page, tag: string) => ({ name: await tagChip(page, tag).getAttribute('aria-label'), hover: await tagChip(page, tag).getAttribute('title') });
const named = (tag: string, sources: string) => ({ name: `${tag}, ${sources}`, hover: `${tag}: ${sources}` });

// The initiative `slug` carries one task with both own tags `drop` and `keep`; `${slug}-scope` is an empty initiative, so the
// combined filter (scope + drop + keep) has no row.
async function seed(api: Api, slug: string) {
  const scope = `${slug}-scope`;
  const drop = `${slug}-drop`;
  const keep = `${slug}-keep`;
  await created(api, '/api/v1/initiatives', { slug, title: slug });
  await created(api, '/api/v1/initiatives', { slug: scope, title: scope });
  await created(api, '/api/v1/tasks', { initiative: slug, title: 'Task carrying both tags', tags: [drop, keep] });
  return { scope, drop, keep, filter: `initiative=${scope}&tag=${drop}&tag=${keep}` };
}

// Once armed, holds the restoration at the network until `release()`: on `catalog`, the board's list of initiatives (`status=all`);
// on `lookup`, the restoration's own per-tag reads (Now restores in place against the list it already holds, then looks its tags
// up). From then on it counts the reads of each tag's sources, and once `changed()` marks the user's change, the per-tag reads and every request
// sent under a filter that names `scope` together with `stale` that begin after it.
async function hold(page: Page, scope: string, stale: string, on: 'catalog' | 'lookup' = 'catalog') {
  const released = gate();
  const started = gate();
  const reads = new Map<string, number>();
  const readsAfter: string[] = [];
  const staleRequests: string[] = [];
  let armed = false;
  let after = false;
  // A read of the sources of tags is ONE request for all the tags of the round, so it names the tags it reads (none for any other request).
  const lookup = (url: URL) => (url.pathname === '/api/v1/tasks/tag-sources' ? url.searchParams.getAll('tag') : undefined);
  await page.route(on === 'catalog' ? '**/api/v1/initiatives*' : '**/api/v1/tasks/tag-sources?**', async (route) => {
    const url = new URL(route.request().url());
    const held = on === 'catalog' ? url.pathname === '/api/v1/initiatives' && url.searchParams.get('status') === 'all' : lookup(url) !== undefined;
    if (!armed || !held) return route.continue();
    const response = await route.fetch();
    started.open();
    await released.wait;
    await route.fulfill({ response });
  });
  page.on('request', (request) => {
    if (!armed) return;
    const url = new URL(request.url());
    for (const tag of lookup(url) ?? []) {
      reads.set(tag, (reads.get(tag) ?? 0) + 1);
      if (after) readsAfter.push(tag);
    }
    if (after && url.searchParams.getAll('initiative').includes(scope) && url.searchParams.getAll('tag').includes(stale)) staleRequests.push(url.pathname + url.search);
  });
  return {
    reads,
    readsAfter,
    staleRequests,
    arm: () => (armed = true),
    changed: () => (after = true),
    catalogStarted: started.wait,
    releaseCatalog: released.open,
  };
}

// Lets the abandoned restoration run (the list lands), then waits until the page has drawn two frames.
async function settle(page: Page, held: Awaited<ReturnType<typeof hold>>) {
  held.releaseCatalog();
  await page.waitForTimeout(800);
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

const savedQuery = (page: Page, key: string) => page.evaluate((k) => (JSON.parse(localStorage.getItem(k) ?? '{}') as { query?: string }).query ?? '', key);

// Opens the view on the link `filter` (which saves it) and waits for both chips to name their source; then arms the hold and
// returns to the bare fragment, which restores the saved filter (its list of initiatives held).
async function restoreInPlace(page: Page, view: View, s: Awaited<ReturnType<typeof seed>>, held: Awaited<ReturnType<typeof hold>>) {
  await page.goto(`/#/${view.id}?${s.filter}`);
  await openChips(page);
  await expect.poll(() => labels(page, s.drop)).toEqual(named(s.drop, 'task tag'));
  await expect.poll(() => labels(page, s.keep)).toEqual(named(s.keep, 'task tag'));
  held.arm();
  await page.evaluate(() => (window.location.hash = window.location.hash.split('?')[0]));
  await held.catalogStarted;
}

// The per-tag reads of `tag` begun before the user's change.
const readsBefore = (held: Awaited<ReturnType<typeof hold>>, tag: string) => (held.reads.get(tag) ?? 0) - held.readsAfter.filter((t) => t === tag).length;

async function expectUserChipsKept(page: Page, view: View, s: Awaited<ReturnType<typeof seed>>, held: Awaited<ReturnType<typeof hold>>) {
  expect(await chosenTags(page)).toEqual([s.keep]);
  expect(page.url()).not.toContain(`tag=${s.drop}`);
  expect(await savedQuery(page, view.key)).not.toContain(`tag=${s.drop}`);
  // The only read after the change is the user's own, of the tag it keeps chosen: the restoration reads nothing.
  expect(held.readsAfter).toEqual([s.keep]);
  // Held at its list of initiatives, the restoration had begun no read before the change either.
  if (view.id !== 'now') expect([readsBefore(held, s.drop), readsBefore(held, s.keep)]).toEqual([0, 0]);
  expect(held.staleRequests).toEqual([]);
  await expect.poll(() => labels(page, s.keep)).toEqual(named(s.keep, 'task tag'));
}

for (const viewport of logicViewports) {
  for (const view of views) {
    test.describe(`${view.id} at ${viewport.name}`, () => {
      test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

      test('a chip the user removes while the saved filter is restored stays removed and the restoration reads nothing', async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `cs-place-${view.id}-${viewport.name}`);
        const held = await hold(page, s.scope, s.drop, view.id === 'now' ? 'lookup' : 'catalog');
        try {
          await restoreInPlace(page, view, s, held);
          await openChips(page);
          held.changed();
          await tagChip(page, s.drop).click();
          await expect.poll(() => chosenTags(page)).toEqual([s.keep]);
          await expect.poll(() => page.url()).not.toContain(`tag=${s.drop}`);
          await settle(page, held);
          await expectUserChipsKept(page, view, s, held);
        } finally {
          held.releaseCatalog();
          await api.dispose();
        }
      });

      test('a chip the user removes while the opening restores the saved filter stays removed and the restoration reads nothing', async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `cs-open-${view.id}-${viewport.name}`);
        const held = await hold(page, s.scope, s.drop);
        try {
          await page.addInitScript(({ key, query }) => localStorage.setItem(key, JSON.stringify({ v: 1, query })), { key: view.key, query: s.filter });
          held.arm();
          await page.goto(`/#/${view.id}`);
          await held.catalogStarted;
          await openChips(page);
          await expect(tagChip(page, s.drop)).toHaveAttribute('aria-pressed', 'true');
          held.changed();
          await tagChip(page, s.drop).click();
          await expect.poll(() => chosenTags(page)).toEqual([s.keep]);
          await settle(page, held);
          expect(await chosenTags(page)).toEqual([s.keep]);
          expect(page.url()).not.toContain(`tag=${s.drop}`);
          expect(await savedQuery(page, view.key)).not.toContain(`tag=${s.drop}`);
          expect(held.reads.get(s.drop) ?? 0).toBe(0);
          // The user's change reads the tag it keeps as its own; the restoration that named it reads nothing.
          expect(held.readsAfter).toEqual([s.keep]);
          expect(readsBefore(held, s.keep)).toBe(0);
          expect(held.staleRequests).toEqual([]);
          // No combined row matches, and the restoration that named the chip was abandoned: the chip still names its source.
          await expect.poll(() => labels(page, s.keep)).toEqual(named(s.keep, 'task tag'));
        } finally {
          held.releaseCatalog();
          await api.dispose();
        }
      });
    });
  }

  test.describe(`archive window at ${viewport.name}`, () => {
    test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

    test('a window the user picks while the saved filter is restored is kept and the restoration reads nothing', async ({ page, playwright, baseURL }) => {
      const view = views[3];
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      const s = await seed(api, `cs-window-${viewport.name}`);
      const held = await hold(page, s.scope, s.drop);
      try {
        await restoreInPlace(page, view, s, held);
        const picked = page.waitForRequest((request) => {
          const url = new URL(request.url());
          return url.pathname === '/api/v1/tasks' && url.searchParams.get('status') === 'closed' && url.searchParams.get('closed_within') === '90d';
        });
        held.changed();
        await page.getByTestId('archive-window').selectOption('90');
        await expect.poll(() => page.url()).toContain('window=90');
        await picked;
        await settle(page, held);
        await expect(page.getByTestId('archive-window')).toHaveValue('90');
        expect(page.url()).toContain('window=90');
        expect(await savedQuery(page, view.key)).toContain('window=90');
        expect(await chosenTags(page)).toEqual([s.drop, s.keep].sort());
        // The restoration read nothing; the window the user picked read both chosen tags as its own.
        expect([readsBefore(held, s.drop), readsBefore(held, s.keep)]).toEqual([0, 0]);
        expect([...held.readsAfter].sort()).toEqual([s.drop, s.keep].sort());
        await expect.poll(() => labels(page, s.drop)).toEqual(named(s.drop, 'task tag'));
        await expect.poll(() => labels(page, s.keep)).toEqual(named(s.keep, 'task tag'));
      } finally {
        held.releaseCatalog();
        await api.dispose();
      }
    });
  });
}
