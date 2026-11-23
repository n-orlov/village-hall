import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { expectInitiatives, openChips, revealInitiative, tagChip } from './chips';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): a read of a tag's sources belongs to the navigation that
// began it. Once a newer navigation is current (a chip the user clicks, a window the user picks), the older one's answer names
// nothing, however late it lands, and the newer one reads as its own every chosen tag the older one had not answered for. So a
// chosen chip names the sources its tag has now even when no row of the combined filter matches, and an abandoned link or
// restoration cannot take one away. Each test seeds initiatives of its own on the shared board; the combined filter never
// matches a row, so only the per-tag answers can name the chip's sources.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const views = ['tasks', 'now', 'timeline', 'archive'] as const;
type Api = Awaited<ReturnType<typeof actorApi>>;
const run = Date.now().toString(36).slice(-6);

function gate<T = void>() {
  let open!: (value: T) => void;
  const wait = new Promise<T>((resolve) => (open = resolve));
  return { open, wait };
}

// `outside` holds the one task carrying `tag` as its own; `empty`, `empty2` and `added` hold none, so a filter naming only them
// with the tag has no row. `later` is tagged, and given an open task, only once the older read is out: the tag then also has an
// initiative source, which no row of the combined filter shows.
async function seed(api: Api, prefix: string) {
  const s = { outside: `${prefix}-out`, empty: `${prefix}-empty`, empty2: `${prefix}-empty2`, added: `${prefix}-added`, later: `${prefix}-later`, tag: `${prefix}-x` };
  for (const slug of [s.outside, s.empty, s.empty2, s.added, s.later]) await created(api, '/api/v1/initiatives', { slug, title: slug });
  await created(api, '/api/v1/tasks', { initiative: s.outside, title: 'Own source', tags: [s.tag] });
  return s;
}

async function gainInitiativeSource(api: Api, s: Awaited<ReturnType<typeof seed>>) {
  expect((await api.patch(`/api/v1/initiatives/${s.later}`, { data: { tags: [s.tag] } })).status()).toBe(200);
  await created(api, '/api/v1/tasks', { initiative: s.later, title: 'Inherited source' });
  const fresh = await api.get(`/api/v1/tasks?status=all&limit=1000&tag=${s.tag}`);
  expect(fresh.status()).toBe(200);
  expect(sourcesOf(await fresh.json(), s.tag)).toEqual(['initiative', 'task']);
}

const sourcesOf = (body: { items: { tag_marks: { tag: string; source: string }[] }[] }, tag: string) =>
  [...new Set(body.items.flatMap((row) => row.tag_marks.filter((m) => m.tag === tag).map((m) => m.source)))].sort();

// Once armed, holds the first per-tag read of `tag` at the network (after the server answered) until `release()`.
async function holdLookup(page: Page, tag: string) {
  const snapshot = gate<string[]>();
  const released = gate();
  let armed = false;
  let held = false;
  const isLookup = (url: URL) => url.pathname === '/api/v1/tasks' && url.searchParams.get('limit') === '1000' && url.searchParams.get('tag') === tag && !url.searchParams.has('initiative');
  await page.route('**/api/v1/tasks?**', async (route) => {
    if (!armed || held || !isLookup(new URL(route.request().url()))) return route.continue();
    held = true;
    const response = await route.fetch();
    snapshot.open(sourcesOf(await response.json(), tag));
    await released.wait;
    await route.fulfill({ response });
  });
  return {
    arm: () => (armed = true),
    snapshot: snapshot.wait,
    // Lets the held answer land and waits until the page has handled it (two frames after the response finished).
    release: async () => {
      const answer = page.waitForResponse((r) => isLookup(new URL(r.url())));
      released.open();
      const response = await answer;
      expect(sourcesOf(await response.json(), tag)).toEqual(['task']);
      await response.finished();
      await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    },
    open: released.open,
  };
}

const labels = async (page: Page, tag: string) => ({ name: await tagChip(page, tag).getAttribute('aria-label'), hover: await tagChip(page, tag).getAttribute('title') });
const named = (tag: string, sources: string) => ({ name: `${tag}, ${sources}`, hover: `${tag}: ${sources}` });

for (const viewport of viewports) {
  test.describe(`chip source owner at ${viewport.name}`, () => {
    test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

    for (const view of views) {
      test(`${view}: a link a chip change supersedes cannot take away the source the change read for a chosen chip no row shows`, async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `so-${view}-${viewport.name}-${run}`);
        const held = await holdLookup(page, s.tag);
        try {
          await page.goto(`/#/${view}?initiative=${s.empty}&tag=${s.tag}`);
          await openChips(page);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));
          await revealInitiative(page, s.added);
          held.arm();
          // An explicit link (no saved restoration involved) whose per-tag read is held.
          await page.evaluate((hash) => (window.location.hash = hash), `#/${view}?initiative=${s.empty}&initiative=${s.empty2}&tag=${s.tag}`);
          expect(await held.snapshot).toEqual(['task']);
          await expectInitiatives(page, [s.empty, s.empty2]);
          await gainInitiativeSource(api, s);

          // The user's chip change is now the current navigation; the combined filter still has no row.
          await (await revealInitiative(page, s.added)).click();
          await expectInitiatives(page, [s.empty, s.empty2, s.added]);
          await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));

          await held.release();
          await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
          await expectInitiatives(page, [s.empty, s.empty2, s.added]);
          expect(await labels(page, s.tag), 'the abandoned link must not take away the source the current navigation read').toEqual(named(s.tag, 'task tag, initiative tag'));
        } finally {
          held.open();
          await api.dispose();
        }
      });
    }

    test('archive: a window picked while the saved filter restores reads the chosen tag as its own; the restoration answer names nothing', async ({ page, playwright, baseURL }) => {
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      const s = await seed(api, `so-window-${viewport.name}-${run}`);
      const held = await holdLookup(page, s.tag);
      try {
        // The link saves its query; the bare fragment then restores it, its per-tag lookup held.
        await page.goto(`/#/archive?initiative=${s.empty}&tag=${s.tag}`);
        await openChips(page);
        await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));
        await expect(page.getByTestId('archive-empty')).toBeVisible();
        held.arm();
        await page.evaluate(() => (window.location.hash = '#/archive'));
        expect(await held.snapshot).toEqual(['task']);
        await gainInitiativeSource(api, s);

        await page.getByTestId('archive-window').selectOption('90');
        await expect.poll(() => page.url()).toContain('window=90');
        await expect(page.getByTestId('archive-empty')).toBeVisible();
        await openChips(page);
        await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
        await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));

        await held.release();
        await expect(page.getByTestId('archive-window')).toHaveValue('90');
        await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
        await expectInitiatives(page, [s.empty]);
        expect(await labels(page, s.tag), 'the abandoned restoration must not take away the source the window read').toEqual(named(s.tag, 'task tag, initiative tag'));
      } finally {
        held.open();
        await api.dispose();
      }
    });
  });
}
