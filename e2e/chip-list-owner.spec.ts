import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { expectInitiatives, openChips, revealInitiative, tagChip } from './chips';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): on Archive only the rows of the navigation that is current
// may change what its chosen chips say about their tags' sources. A list a superseded navigation began (an explicit link, a window,
// a chip) names no source however it lands: before the current list, while it is out or after it. The held list here carries the
// rows of an initiative that listed the tag when it was read; the server then stops reporting that source, so the current
// navigation's per-tag read names only the task source and the current list has no row (or only rows without it).
// Rows belong to a navigation, not to a window and chips: a list an earlier visit began names no source on a later visit back to
// the same window and chips (A, then B, then A again), by an explicit link, a window or a chip.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
type Api = Awaited<ReturnType<typeof actorApi>>;
const run = Date.now().toString(36).slice(-6);

function gate() {
  let open!: () => void;
  const wait = new Promise<void>((resolve) => (open = resolve));
  return { open, wait };
}

// `own` holds the one task carrying `tag` as its own; `inh` lists the tag and holds an untagged closed task (the tag's initiative
// source on a closed row); `empty` and `other` hold nothing, so a filter naming only them and the tag has no row.
async function seed(api: Api, prefix: string) {
  const s = { own: `${prefix}-own`, inh: `${prefix}-inh`, empty: `${prefix}-empty`, tag: `${prefix}-x` };
  for (const slug of [s.own, s.empty]) await created(api, '/api/v1/initiatives', { slug, title: slug });
  await created(api, '/api/v1/initiatives', { slug: s.inh, title: s.inh, tags: [s.tag] });
  const own = await created(api, '/api/v1/tasks', { initiative: s.own, title: 'Own source', tags: [s.tag] });
  const inh = await created(api, '/api/v1/tasks', { initiative: s.inh, title: 'Inherited source' });
  for (const task of [own, inh]) expect((await api.post(`/api/v1/tasks/${task.ref}/close`, { data: { resolution: 'completed' } })).status()).toBe(200);
  return { ...s, ownRef: own.ref };
}

// The first request `mine` picks once armed, held at the network after the server answered: the answer is the one given before the
// source went.
async function holdRequest(page: Page, mine: (url: URL) => boolean) {
  const released = gate();
  const seen = gate();
  let armed = false;
  let held = false;
  await page.route('**/api/v1/tasks?**', async (route) => {
    if (!armed || held || !mine(new URL(route.request().url()))) return route.fallback();
    held = true;
    const response = await route.fetch();
    seen.open();
    await released.wait;
    await route.fulfill({ response });
  });
  return {
    arm: () => (armed = true),
    seen: seen.wait,
    release: async () => {
      released.open();
      await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    },
    open: released.open,
  };
}

// The closed-task lists (the per-tag reads carry `limit=1000`) of the requests `isHeld` picks.
const holdLists = (page: Page, isHeld: (query: URLSearchParams) => boolean) =>
  holdRequest(page, (url) => url.pathname === '/api/v1/tasks' && url.searchParams.get('status') === 'closed' && !url.searchParams.has('limit') && isHeld(url.searchParams));
// The per-tag read of `tag`.
const holdLookup = (page: Page, tag: string) =>
  holdRequest(page, (url) => url.pathname === '/api/v1/tasks' && url.searchParams.get('limit') === '1000' && url.searchParams.get('tag') === tag && !url.searchParams.has('initiative'));

// The board's list of initiatives (the catalog a restoration waits for), held at the network after the server answered.
async function holdBoard(page: Page) {
  const released = gate();
  const seen = gate();
  let armed = false;
  let held = false;
  await page.route('**/api/v1/initiatives?**', async (route) => {
    if (!armed || held || new URL(route.request().url()).searchParams.get('status') !== 'all') return route.fallback();
    held = true;
    const response = await route.fetch();
    seen.open();
    await released.wait;
    await route.fulfill({ response });
  });
  return {
    arm: () => (armed = true),
    seen: seen.wait,
    release: async () => {
      released.open();
      await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
    },
    open: released.open,
  };
}

// The task source goes (the held list still says it is there); the tag keeps its initiative source.
async function loseTaskSource(api: Api, s: Awaited<ReturnType<typeof seed>>) {
  // A closed task takes no edit: it is reopened, loses the tag and is closed again.
  expect((await api.post(`/api/v1/tasks/${s.ownRef}/reopen`)).status()).toBe(200);
  expect((await api.delete(`/api/v1/tasks/${s.ownRef}/tags/${s.tag}`)).status()).toBe(200);
  expect((await api.post(`/api/v1/tasks/${s.ownRef}/close`, { data: { resolution: 'completed' } })).status()).toBe(200);
}

const labels = async (page: Page, tag: string) => ({ name: await tagChip(page, tag).getAttribute('aria-label'), hover: await tagChip(page, tag).getAttribute('title') });
const named = (tag: string, sources: string) => ({ name: `${tag}, ${sources}`, hover: `${tag}: ${sources}` });
const initiativesOf = (query: URLSearchParams) => query.getAll('initiative').sort().join(',');

async function settle(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

for (const viewport of viewports) {
  test.describe(`archive list owner at ${viewport.name}`, () => {
    test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

    // The superseded explicit link names `inh` (its list is held); `supersede` makes the current navigation, whose list is empty.
    for (const order of ['before', 'after'] as const) {
      test(`a superseded explicit link's list landing ${order} the current empty list leaves the sources the current lookup confirmed`, async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `lo-${order}-${viewport.name}-${run}`);
        const older = await holdLists(page, (q) => initiativesOf(q) === s.inh);
        const current = await holdLists(page, (q) => initiativesOf(q) === s.empty && q.getAll('tag').includes(s.tag));
        try {
          await page.goto(`/#/archive?initiative=${s.own}&tag=${s.tag}`);
          await openChips(page);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));
          older.arm();
          await page.evaluate((hash) => (window.location.hash = hash), `#/archive?initiative=${s.inh}&tag=${s.tag}`);
          await older.seen;
          // The initiative stops listing the tag; the held list still says it does.
          expect((await api.patch(`/api/v1/initiatives/${s.inh}`, { data: { tags: [] } })).status()).toBe(200);
          current.arm();
          await page.evaluate((hash) => (window.location.hash = hash), `#/archive?initiative=${s.empty}&tag=${s.tag}`);
          await current.seen;
          await expectInitiatives(page, [s.empty]);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));

          if (order === 'before') {
            await older.release();
            expect(await labels(page, s.tag), 'the superseded link\'s list must name no source').toEqual(named(s.tag, 'task tag'));
            await current.release();
          } else {
            await current.release();
            await expect(page.getByTestId('archive-empty')).toBeVisible();
            await older.release();
          }
          await settle(page);
          await expect(page.getByTestId('archive-empty')).toBeVisible();
          await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
          expect(await labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));
        } finally {
          older.open();
          current.open();
          await api.dispose();
        }
      });
    }

    // The user's own change is the newest navigation too: a list the link began cannot name a source for the chips the user left.
    test('a window the user picks supersedes a held list: its rows name no source', async ({ page, playwright, baseURL }) => {
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      const s = await seed(api, `lo-win-${viewport.name}-${run}`);
      const older = await holdLists(page, (q) => initiativesOf(q) === s.own);
      const lookup = await holdLookup(page, s.tag);
      const current = await holdLists(page, (q) => initiativesOf(q) === s.own && q.get('closed_within') === '90d');
      try {
        await page.goto(`/#/archive?initiative=${s.empty}&tag=${s.tag}`);
        await openChips(page);
        await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));
        older.arm();
        lookup.arm();
        await page.evaluate((hash) => (window.location.hash = hash), `#/archive?initiative=${s.own}&tag=${s.tag}`);
        await Promise.all([older.seen, lookup.seen]);
        await loseTaskSource(api, s);
        current.arm();
        await page.getByTestId('archive-window').selectOption('90');
        await current.seen;
        await expect.poll(() => page.url()).toContain('window=90');
        await openChips(page);
        await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'initiative tag'));
        // The abandoned link's list lands while the window's own list is still out.
        await older.release();
        expect(await labels(page, s.tag), 'the abandoned link\'s list must name no source').toEqual(named(s.tag, 'initiative tag'));
        await lookup.release();
        await current.release();
        await expect(page.getByTestId('archive-empty')).toBeVisible();
        await expect(page.getByTestId('archive-window')).toHaveValue('90');
        expect(await labels(page, s.tag), 'the abandoned window\'s list must name no source').toEqual(named(s.tag, 'initiative tag'));
      } finally {
        older.open();
        lookup.open();
        current.open();
        await api.dispose();
      }
    });

    test('an initiative chip the user clicks supersedes a held list: its rows name no source', async ({ page, playwright, baseURL }) => {
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      const s = await seed(api, `lo-chip-${viewport.name}-${run}`);
      const older = await holdLists(page, (q) => initiativesOf(q) === s.own);
      const lookup = await holdLookup(page, s.tag);
      const current = await holdLists(page, (q) => initiativesOf(q) === [s.own, s.empty].sort().join(','));
      try {
        await page.goto(`/#/archive?initiative=${s.empty}&tag=${s.tag}`);
        await openChips(page);
        await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));
        older.arm();
        lookup.arm();
        await page.evaluate((hash) => (window.location.hash = hash), `#/archive?initiative=${s.own}&tag=${s.tag}`);
        await Promise.all([older.seen, lookup.seen]);
        await loseTaskSource(api, s);
        current.arm();
        await (await revealInitiative(page, s.empty)).click();
        await current.seen;
        await expectInitiatives(page, [s.own, s.empty]);
        await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'initiative tag'));
        // The abandoned link's list lands while the chip's own list is still out.
        await older.release();
        expect(await labels(page, s.tag), 'the abandoned link\'s list must name no source').toEqual(named(s.tag, 'initiative tag'));
        await lookup.release();
        await current.release();
        await expect(page.getByTestId('archive-empty')).toBeVisible();
        await expectInitiatives(page, [s.own, s.empty]);
        expect(await labels(page, s.tag), 'the abandoned link\'s list must name no source').toEqual(named(s.tag, 'initiative tag'));
      } finally {
        older.open();
        lookup.open();
        current.open();
        await api.dispose();
      }
    });

    // A, then B, then A again by explicit links. The first visit to A (`inh`, which then still lists the tag) asks for the same
    // window and chips as the current one; B's list is held too, so no newer list lands before the first visit's.
    for (const order of ['before', 'after'] as const) {
      test(`a first visit's list landing ${order} the empty list of the visit back to it (link A, B, A) leaves the confirmed sources`, async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `lo-back-${order}-${viewport.name}-${run}`);
        const linkA = `#/archive?initiative=${s.inh}&tag=${s.tag}`;
        const first = await holdLists(page, (q) => initiativesOf(q) === s.inh);
        const middle = await holdLists(page, (q) => initiativesOf(q) === s.empty);
        const back = await holdLists(page, (q) => initiativesOf(q) === s.inh);
        try {
          await page.goto(`/#/archive?initiative=${s.own}&tag=${s.tag}`);
          await openChips(page);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));
          first.arm();
          await page.evaluate((hash) => (window.location.hash = hash), linkA);
          await first.seen;
          middle.arm();
          await page.evaluate((hash) => (window.location.hash = hash), `#/archive?initiative=${s.empty}&tag=${s.tag}`);
          await middle.seen;
          // The initiative stops listing the tag; the first visit's held list still says it does.
          expect((await api.patch(`/api/v1/initiatives/${s.inh}`, { data: { tags: [] } })).status()).toBe(200);
          back.arm();
          await page.evaluate((hash) => (window.location.hash = hash), linkA);
          await back.seen;
          await expectInitiatives(page, [s.inh]);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));

          if (order === 'before') {
            await first.release();
            expect(await labels(page, s.tag), 'the first visit\'s list must name no source').toEqual(named(s.tag, 'task tag'));
            await back.release();
          } else {
            await back.release();
            await expect(page.getByTestId('archive-empty')).toBeVisible();
            await first.release();
          }
          await middle.release();
          await settle(page);
          await expect(page.getByTestId('archive-empty')).toBeVisible();
          await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
          expect(await labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));
        } finally {
          first.open();
          middle.open();
          back.open();
          await api.dispose();
        }
      });
    }

    // The same rule through the user's own controls: the link's list (window 30, `own`, which then still carries the tag) is held
    // with its per-tag read, the user leaves for another window or chip set and comes back to the link's own window and chips.
    type Seeded = Awaited<ReturnType<typeof seed>>;
    const windowSelect = (page: Page) => page.getByTestId('archive-window');
    const returns: Record<string, { away: (q: URLSearchParams) => boolean; go: (page: Page, s: Seeded) => Promise<unknown>; back: (page: Page, s: Seeded) => Promise<unknown>; shown: (page: Page, s: Seeded) => Promise<void> }> = {
      window: {
        away: (q) => q.get('closed_within') === '90d',
        go: (page) => windowSelect(page).selectOption('90'),
        back: (page) => windowSelect(page).selectOption('30'),
        shown: (page) => expect(windowSelect(page)).toHaveValue('30'),
      },
      chip: {
        away: (q) => q.getAll('initiative').length === 2,
        go: async (page, s) => (await revealInitiative(page, s.empty)).click(),
        back: async (page, s) => (await revealInitiative(page, s.empty)).click(),
        shown: (page, s) => expectInitiatives(page, [s.own]),
      },
    };
    for (const [how, r] of Object.entries(returns)) {
      test(`a ${how} the user changes and changes back supersedes the link's held list for the same scope: its rows name no source`, async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `lo-ret-${how}-${viewport.name}-${run}`);
        const linkScope = (q: URLSearchParams) => initiativesOf(q) === s.own && q.get('closed_within') === '30d';
        const first = await holdLists(page, linkScope);
        const lookup = await holdLookup(page, s.tag);
        const middle = await holdLists(page, r.away);
        const back = await holdLists(page, linkScope);
        try {
          await page.goto(`/#/archive?initiative=${s.empty}&tag=${s.tag}`);
          await openChips(page);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));
          first.arm();
          lookup.arm();
          await page.evaluate((hash) => (window.location.hash = hash), `#/archive?initiative=${s.own}&tag=${s.tag}`);
          await Promise.all([first.seen, lookup.seen]);
          await loseTaskSource(api, s);
          middle.arm();
          await r.go(page, s);
          await middle.seen;
          back.arm();
          await r.back(page, s);
          await back.seen;
          await r.shown(page, s);
          await openChips(page);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'initiative tag'));
          // The link's list lands while the list of the return to its own scope is still out.
          await first.release();
          expect(await labels(page, s.tag), 'the link\'s list must name no source on the return').toEqual(named(s.tag, 'initiative tag'));
          await lookup.release();
          await back.release();
          await expect(page.getByTestId('archive-empty')).toBeVisible();
          await middle.release();
          await settle(page);
          await r.shown(page, s);
          await expect(page.getByTestId('archive-empty')).toBeVisible();
          expect(await labels(page, s.tag), 'no superseded list may name a source').toEqual(named(s.tag, 'initiative tag'));
        } finally {
          first.open();
          lookup.open();
          middle.open();
          back.open();
          await api.dispose();
        }
      });
    }

    // A saved filter restored on a bare fragment is a navigation from the moment the fragment changes, not once its tags are checked:
    // the list the navigation it superseded began (the opening read, or a later explicit link) names no source and shows no row
    // while the restoration waits for the board's list or its per-tag lookup, nor after the restoration's own empty list landed.
    // That list (`inh`, which then still lists the tag) is held; the link's own lookup and board list are let through only once
    // the initiative has dropped the tag, so before the restoration the chip names the task source alone.
    const restoreCases = [
      { from: 'opening', at: 'lookup' },
      { from: 'opening', at: 'catalog' },
      { from: 'link', at: 'lookup' },
      { from: 'link', at: 'after' },
    ] as const;
    for (const { from, at } of restoreCases) {
      test(`a saved filter restored while the ${from} list is held supersedes it: released ${at === 'after' ? 'after the restored empty list' : `while the restoration's ${at} is out`}, it names no source`, async ({ page, playwright, baseURL }) => {
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        const s = await seed(api, `lo-rs-${from[0]}${at[0]}-${viewport.name}-${run}`);
        const link = `initiative=${s.inh}&tag=${s.tag}`;
        const saved = `initiative=${s.empty}&tag=${s.tag}`;
        const older = await holdLists(page, (q) => initiativesOf(q) === s.inh);
        const gateOpen = gate();
        let gated = false;
        // The link's per-tag read and board list wait for the source to go before they ask.
        await page.route(/\/api\/v1\/(tasks|initiatives)\?/, async (route) => {
          const url = new URL(route.request().url());
          const lookup = url.pathname === '/api/v1/tasks' && url.searchParams.get('limit') === '1000' && url.searchParams.get('tag') === s.tag;
          const board = url.pathname === '/api/v1/initiatives' && url.searchParams.get('status') === 'all';
          if (!gated || !(lookup || board)) return route.fallback();
          await gateOpen.wait;
          await route.fallback();
        });
        const restoreLookup = await holdLookup(page, s.tag);
        const restoreBoard = await holdBoard(page);
        try {
          if (from === 'opening') {
            older.arm();
            gated = true;
            await page.goto(`/#/archive?${link}`);
          } else {
            await page.goto(`/#/archive?initiative=${s.own}&tag=${s.tag}`);
            await openChips(page);
            await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag, initiative tag'));
            older.arm();
            gated = true;
            await page.evaluate((hash) => (window.location.hash = hash), `#/archive?${link}`);
          }
          await older.seen;
          expect((await api.patch(`/api/v1/initiatives/${s.inh}`, { data: { tags: [] } })).status()).toBe(200);
          gateOpen.open();
          await openChips(page);
          await expectInitiatives(page, [s.inh]);
          await expect.poll(() => labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));
          gated = false;

          if (at !== 'after') (at === 'catalog' ? restoreBoard : restoreLookup).arm();
          await page.evaluate((query) => {
            localStorage.setItem('vh.view.v1.archive', JSON.stringify({ v: 1, query }));
            window.location.hash = '#/archive';
          }, saved);
          if (at === 'after') {
            await expect.poll(() => page.url()).toContain(`#/archive?${saved}`);
            await expectInitiatives(page, [s.empty]);
            await expect(page.getByTestId('archive-empty')).toBeVisible();
            await older.release();
          } else {
            await (at === 'catalog' ? restoreBoard : restoreLookup).seen;
            await expect.poll(() => page.url()).toContain(`#/archive?${saved}`);
            await older.release();
            expect(await labels(page, s.tag), 'the superseded list must name no source while the restoration is checked').toEqual(named(s.tag, 'task tag'));
            await expect(page.getByText('Inherited source', { exact: true })).toHaveCount(0);
            await (at === 'catalog' ? restoreBoard : restoreLookup).release();
          }
          await expectInitiatives(page, [s.empty]);
          await expect(page.getByTestId('archive-empty')).toBeVisible();
          await settle(page);
          await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
          await expect(page.getByText('Inherited source', { exact: true })).toHaveCount(0);
          expect(await labels(page, s.tag)).toEqual(named(s.tag, 'task tag'));
        } finally {
          gateOpen.open();
          older.open();
          restoreLookup.open();
          restoreBoard.open();
          await api.dispose();
        }
      });
    }
  });
}
