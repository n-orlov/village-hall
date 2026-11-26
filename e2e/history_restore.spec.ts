import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { catalogRead, expectInitiatives, foldChips, initiativeChip, pickInitiative, pickTag, tagChip } from './chips';
import { showAllBands } from './now';

// Back and Forward restore a page the way it was left (PRD R7, SPEC 11): the Attention view (filters), the Initiatives
// list (status filter and sort), the initiative page (task filter and sort), the Tasks view (filters and sort), the task page
// (scroll only), the Actors view (tag chips and sort), the Archive view (window and sort), the Notes view (filter and scroll),
// Settings -> API keys (sort only) and the Timeline (filters, window and the chart's rows) each come back with the scroll position
// of the history entry, the filters in the fragment, the sort column and direction (the header's aria-sort at desktop,
// the Sort select at phone, the fragment's `sort` and `dir`) and the row order they had when a link was followed. Forward
// and then Back again restores the same. Every test seeds rows of its own (slugs start with `e2e-hr-`) and reads only those.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };
const SCROLL_TO = 300;

async function post<T = { ref: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const apiContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

const actorContext = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, handle: string) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': handle } });

const th = (page: Page, column: string): Locator => page.locator('th', { has: page.getByTestId(`sort-${column}`) });
const scrollY = (page: Page) => page.evaluate(() => window.scrollY);
const fragment = (page: Page) => new URL(page.url()).hash;
const queryOf = (page: Page) => new URLSearchParams(fragment(page).split('?')[1] ?? '');

async function sortBy(page: Page, phone: boolean, column: string, dir: 'asc' | 'desc') {
  if (phone) {
    await page.getByTestId('sort-select').selectOption(`${column}:${dir}`);
    return;
  }
  // The header cycles none -> ascending -> descending: click until the target is reached.
  const clicks = dir === 'asc' ? 1 : 2;
  for (let i = 0; i < clicks; i++) await page.getByTestId(`sort-${column}`).click();
}

/** The sort the page shows, the way a person reads it: the header's aria-sort at desktop, the Sort select at phone. */
async function shownSort(page: Page, phone: boolean, column: string): Promise<string> {
  return phone ? page.getByTestId('sort-select').inputValue() : ((await th(page, column).getAttribute('aria-sort')) ?? '');
}

/** Scrolls the page and waits until it stays there. */
async function scrollDown(page: Page, to: number) {
  const room = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight);
  expect(room, 'the seeded rows must make the page scroll').toBeGreaterThan(to);
  await page.evaluate((y) => window.scrollTo(0, y), to);
  await expect.poll(() => scrollY(page)).toBe(to);
}

/** Follows the first link of `links` that is fully inside the viewport, clear of the fixed bars: no scroll happens on the way. */
async function followVisibleLink(page: Page, links: Locator): Promise<number> {
  const at = await links.evaluateAll((els) => els.findIndex((el) => {
    const r = el.getBoundingClientRect();
    return r.top > 120 && r.bottom < window.innerHeight - 120 && r.width > 0;
  }));
  expect(at, 'a link inside the viewport after scrolling').toBeGreaterThanOrEqual(0);
  const before = await scrollY(page);
  await links.nth(at).click();
  return before;
}

async function leaveBy(page: Page, leave: (page: Page) => Promise<void>): Promise<number> {
  const before = await scrollY(page);
  await leave(page);
  return before;
}

interface Left {
  scrollY: number;
  fragment: string;
  rows: string[];
  sort: string | undefined;
}

interface Route {
  /** The page's own rows, in the order shown (other specs' rows left out). */
  rows: (page: Page) => Promise<string[]>;
  /** What must hold for the page to be shown as it was left. */
  shown: (page: Page, left: Left) => Promise<void>;
  /** The links of the page that leave it. */
  links: (page: Page) => Locator;
  /** Where the link followed lands. */
  landing: RegExp;
  sortColumn?: string;
  /** The page sorts through the shared sort control (a select at every width), not through its column headers. */
  sharedSort?: boolean;
  /** Leaves the page, for a route whose rows hold no link (the API keys table): the page is left by the navigation. */
  leave?: (page: Page) => Promise<void>;
  /** Where to scroll to, when the rows start further down than `SCROLL_TO` reaches (the Tasks view has the Now panel above its table, the Now view its chips). */
  scrollTo?: (page: Page) => Promise<number>;
}

/** Back, then Forward and Back again: both returns must show `left`. */
async function backAndForward(page: Page, route: Route, left: Left, phone: boolean) {
  const restored = async () => {
    await expect(page).toHaveURL((url) => url.hash === left.fragment, FIVE_SECONDS);
    await route.shown(page, left);
    await expect.poll(() => route.rows(page), FIVE_SECONDS).toEqual(left.rows);
    await expect.poll(async () => Math.abs((await scrollY(page)) - left.scrollY), FIVE_SECONDS).toBeLessThanOrEqual(2);
    if (route.sortColumn !== undefined) {
      expect(await shownSort(page, phone || route.sharedSort === true, route.sortColumn)).toBe(left.sort);
      expect(queryOf(page).get('sort')).toBe(route.sortColumn);
      expect(queryOf(page).get('dir')).toBe('desc');
    }
  };
  await page.goBack();
  await restored();
  await page.goForward();
  await expect(page).toHaveURL(route.landing, FIVE_SECONDS);
  await page.goBack();
  await restored();
}

/** Fills the route in, records how it looks, follows a link away and checks both returns. */
async function leaveAndReturn(page: Page, route: Route, phone: boolean) {
  await foldChips(page); // a fresh mount folds the filter: measure the page as tall as it comes back
  await scrollDown(page, (await route.scrollTo?.(page)) ?? SCROLL_TO);
  const rows = await route.rows(page);
  const sort = route.sortColumn === undefined ? undefined : await shownSort(page, phone || route.sharedSort === true, route.sortColumn);
  const left: Left = { scrollY: await scrollY(page), fragment: fragment(page), rows, sort };
  const before = route.leave === undefined ? await followVisibleLink(page, route.links(page)) : await leaveBy(page, route.leave);
  expect(Math.abs(before - left.scrollY)).toBeLessThanOrEqual(2);
  await expect(page).toHaveURL(route.landing, FIVE_SECONDS);
  await backAndForward(page, route, left, phone);
}

for (const vp of viewports) {
  const phone = vp.name === 'phone';
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the Now view comes back with its tag and initiative filters and its scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-att-${vp.name}`;
      const tag = `hr-att-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore attention ${vp.name}`, tags: [tag] }, 201);
      // Finished tasks wait for a review: each is a card of its own in the view.
      for (let i = 0; i < 16; i++) {
        const task = await post(api, '/api/v1/tasks', { initiative: slug, title: `Review ${String(i).padStart(2, '0')}` }, 201);
        await post(api, `/api/v1/tasks/${task.ref}/done`, {}, 200);
      }

      await showAllBands(page);
      const listed = catalogRead(page, slug);
      await page.goto('/#/now');
      await expect(page.getByTestId('chip-filter')).toBeVisible(FIVE_SECONDS);
      await pickTag(page, tag);
      await listed;
      await pickInitiative(page, slug);
      await expect(page).toHaveURL((url) => url.hash === `#/now?tag=${tag}&initiative=${slug}`);
      await foldChips(page); // a fresh mount folds the filter on a phone: measure the page as it comes back
      await expect(page.getByTestId('attention-item').first()).toBeVisible(FIVE_SECONDS);
      await expect.poll(() => page.getByTestId('attention-item').count(), FIVE_SECONDS).toBeGreaterThanOrEqual(16);

      const route: Route = {
        rows: (p) => p.getByTestId('item-link').allTextContents(),
        shown: async (p) => {
          await expect(tagChip(p, tag)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
          await expect(initiativeChip(p, slug)).toHaveAttribute('aria-pressed', 'true');
        },
        links: (p) => p.getByTestId('item-link'),
        landing: /#\/t\//,
        // Every spec's tags and initiatives are chips above the cards: on a grown store they push the cards below the fold.
        scrollTo: async (p) =>
          Math.max(SCROLL_TO, await p.getByTestId('item-link').first().evaluate((link) => Math.round(link.getBoundingClientRect().top + window.scrollY) - 300)),
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the Initiatives list comes back with its status filter, sort, row order and scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const prefix = `e2e-hr-ini-${vp.name}`;
      // Created in this order; no title sort gives it.
      const titles = ['Mango', 'apple', 'Zebra', 'cherry', 'Lemon', 'banana', 'Quince', 'date', 'Fig', 'grape', 'Kiwi', 'olive', 'Pear', 'jam', 'Nut', 'ice'];
      const slugs = titles.map((title, i) => `${prefix}-${String(i).padStart(2, '0')}`);
      for (const [i, title] of titles.entries()) await post(api, '/api/v1/initiatives', { slug: slugs[i], title: `${title} ${prefix}` }, 201);
      await post(api, `/api/v1/initiatives/${slugs[2]}/close`, {}, 200);
      const own = async (p: Page) =>
        (await p.getByTestId('initiative-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-slug') ?? ''))).filter((s) => s.startsWith(`${prefix}-`));

      await page.goto('/#/initiatives');
      await page.getByTestId('initiative-filter').selectOption('all');
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(16);
      const byDefault = await own(page);
      await sortBy(page, true, 'title', 'desc');
      await expect(page).toHaveURL((url) => url.hash === '#/initiatives?status=all&sort=title&dir=desc');
      const sorted = await own(page);
      expect(sorted, 'the picked sort must reorder the rows').not.toEqual(byDefault);
      expect(sorted).toEqual([...slugs].sort((a, b) => titles[slugs.indexOf(b)].toLowerCase().localeCompare(titles[slugs.indexOf(a)].toLowerCase())));

      const route: Route = {
        rows: own,
        shown: async (p) => {
          await expect(p.getByTestId('initiative-filter')).toHaveValue('all', FIVE_SECONDS);
        },
        links: (p) => p.getByTestId('initiative-row').locator('a'),
        landing: /#\/i\//,
        sortColumn: 'title',
        sharedSort: true,
        // This spec's first row sits 300 px below the top of the window: on a grown store the tag chips push the rows down.
        scrollTo: (p) =>
          p.locator(`[data-testid="initiative-row"][data-slug^="${prefix}-"]`).first().evaluate((row) => Math.max(0, Math.round(row.getBoundingClientRect().top + window.scrollY) - 300)),
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the initiative page comes back with its task filter, sort, row order and scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-page-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore page ${vp.name}` }, 201);
      const titles = ['Mango', 'apple', 'Zebra', 'cherry', 'Lemon', 'banana', 'Quince', 'date', 'Fig', 'grape', 'Kiwi', 'olive', 'Pear', 'jam', 'Nut', 'ice', 'Rye', 'oat', 'Tea', 'bean'];
      const refs: string[] = [];
      for (const title of titles) refs.push((await post(api, '/api/v1/tasks', { initiative: slug, title: `${title} ${slug}` }, 201)).ref);
      // Closed tasks only show under the All filter.
      for (const ref of refs.slice(2, 4)) {
        await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
        await post(api, `/api/v1/tasks/${ref}/close`, { resolution: 'completed' }, 200);
      }
      const own = (p: Page) => p.getByTestId('task-ref').allTextContents();

      await page.goto(`/#/i/${slug}`);
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(18);
      const byDefault = await own(page);
      await page.getByTestId('task-filter').selectOption('all');
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(20);
      await sortBy(page, true, 'title', 'desc');
      await expect(page).toHaveURL((url) => url.hash === `#/i/${slug}?status=all&sort=title&dir=desc`);
      const sorted = await own(page);
      expect(sorted, 'the picked sort must reorder the rows').not.toEqual([...byDefault, ...refs.slice(2, 4)]);
      expect(sorted).toHaveLength(20);

      const route: Route = {
        rows: own,
        shown: async (p) => {
          await expect(p.getByTestId('task-filter')).toHaveValue('all', FIVE_SECONDS);
        },
        links: (p) => p.getByTestId('task-ref').locator('a'),
        landing: /#\/t\//,
        sortColumn: 'title',
        sharedSort: true,
      };
      await leaveAndReturn(page, route, phone);
    });
    test('the Tasks view comes back with its filters, sort, row order and scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-tasks-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore tasks ${vp.name}` }, 201);
      const titles = ['Mango', 'apple', 'Zebra', 'cherry', 'Lemon', 'banana', 'Quince', 'date', 'Fig', 'grape', 'Kiwi', 'olive', 'Pear', 'jam', 'Nut', 'ice', 'Rye', 'oat', 'Tea', 'bean'];
      const refs: string[] = [];
      for (const title of titles) refs.push((await post(api, '/api/v1/tasks', { initiative: slug, title: `${title} ${slug}` }, 201)).ref);
      // Closed tasks only show under the All status filter (a closed task needs no review, so none sits in Needs attention).
      for (const ref of refs.slice(2, 4)) {
        await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
        await post(api, `/api/v1/tasks/${ref}/close`, { resolution: 'completed' }, 200);
      }
      const own = (p: Page) => p.getByTestId('task-title').allTextContents();

      const listed = catalogRead(page, slug);
      await page.goto('/#/tasks');
      await listed;
      await pickInitiative(page, slug);
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(18);
      const byDefault = await own(page);
      await page.getByTestId('status-filter').selectOption('all');
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(20);
      await sortBy(page, true, 'title', 'desc');
      await expect(page).toHaveURL((url) => new URLSearchParams(url.hash.split('?')[1] ?? '').get('sort') === 'title');
      const sorted = await own(page);
      expect(sorted, 'the picked sort must reorder the rows').not.toEqual([...byDefault, ...titles.slice(2, 4).map((t) => `${t} ${slug}`)]);
      expect(sorted).toEqual([...titles].map((t) => `${t} ${slug}`).sort((a, b) => b.toLowerCase().localeCompare(a.toLowerCase())));
      // The tag chips offered stay offered while the view is mounted (a narrower read takes none away), so the view that began on
      // the whole board offers every tag of the shared store, and a Back mounts it on the narrow read, with only the tags of the
      // chosen initiative's tasks: a page of another height. Mount it the way Back does before measuring.
      await page.reload();
      await expect.poll(() => own(page), FIVE_SECONDS).toEqual(sorted);

      const route: Route = {
        rows: own,
        shown: async (p) => {
          await expectInitiatives(p, [slug]);
          await expect(p.getByTestId('status-filter')).toHaveValue('all');
          expect(queryOf(p).get('status')).toBe('all');
          expect(queryOf(p).get('initiative')).toBe(slug);
        },
        links: (p) => p.getByTestId('task-ref').locator('a'),
        landing: /#\/t\//,
        sortColumn: 'title',
        sharedSort: true,
        // The first row sits 300 px below the top of the window, clear of the bars, wherever the Now panel above it ends.
        scrollTo: (p) => p.getByTestId('task-row').first().evaluate((row) => Math.round(row.getBoundingClientRect().top + window.scrollY) - 300),
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the task page comes back with its scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-task-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore task ${vp.name}` }, 201);
      const task = async (title: string) => (await post(api, '/api/v1/tasks', { initiative: slug, title }, 201)).ref;
      const ref = await task('The task under test');
      // Tasks that block it and that it blocks: the page's links to other tasks.
      for (let i = 0; i < 3; i++) {
        await post(api, `/api/v1/tasks/${ref}/deps/${await task(`Blocker ${i}`)}`, {}, 200);
        await post(api, `/api/v1/tasks/${await task(`Blocked ${i}`)}/deps/${ref}`, {}, 200);
      }
      for (let i = 0; i < 25; i++) await post(api, '/api/v1/notes', { task: ref, summary: `Note ${String(i).padStart(2, '0')} of the history restore` }, 201);

      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-title')).toHaveText('The task under test', FIVE_SECONDS);
      await expect.poll(() => page.getByTestId('event').count(), FIVE_SECONDS).toBeGreaterThanOrEqual(25);

      const route: Route = {
        rows: (p) => p.getByTestId('event-summary').allTextContents(),
        shown: async (p) => {
          await expect(p.getByTestId('task-title')).toHaveText('The task under test', FIVE_SECONDS);
        },
        links: (p) => p.locator('[data-testid=task-deps] a, [data-testid=task-blocking] a'),
        landing: /#\/t\//,
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the Actors view comes back with its sort, row order and scroll', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-hr-act-${vp.name}`;
      const prefix = `zzhr-${vp.name}-`;
      // Seen in this order, so neither the default order nor its reverse is the order by handle.
      const names = ['mike', 'bob', 'tess', 'amy', 'zed', 'carl', 'olga', 'dan', 'pam', 'eve', 'ivan', 'nina', 'ken', 'lou'];
      const handles = names.map((name) => `${prefix}${name}`);
      for (const [i, handle] of handles.entries()) {
        const api = await actorContext(playwright, baseURL, handle);
        if (i === 0) await post(api, '/api/v1/initiatives', { slug, title: `History restore actors ${vp.name}` }, 201);
        const task = (await post(api, '/api/v1/tasks', { initiative: slug, title: `Held by ${names[i]}` }, 201)).ref;
        await post(api, '/api/v1/leases', { task, intent: `work of ${names[i]}`, resources: [`repo:${prefix}${names[i]}`] }, 201);
        await api.dispose();
      }
      const own = async (p: Page) =>
        (await p.getByTestId('actor-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-handle') ?? ''))).filter((h) => h.startsWith(prefix));

      await page.goto('/#/actors');
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(names.length);
      const byDefault = await own(page);
      await sortBy(page, true, 'actor', 'desc'); // the shared sort control is a select at every width
      await expect(page).toHaveURL((url) => url.hash === '#/actors?sort=actor&dir=desc');
      const sorted = await own(page);
      expect(sorted, 'the picked sort must reorder the rows').not.toEqual(byDefault);
      expect(sorted).toEqual([...handles].sort().reverse());

      const route: Route = {
        rows: own,
        shown: async () => undefined, // the view has no filter
        links: (p) => p.getByTestId('lease-task'),
        landing: /#\/t\//,
        sortColumn: 'actor',
        sharedSort: true,
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the Archive view comes back with its window, sort, row order and scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-arc-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore archive ${vp.name}` }, 201);
      // Closed in this order, newest last: the default order (newest closed first) is the reverse of it, and no title sort gives either.
      const titles = ['Mango', 'apple', 'Zebra', 'cherry', 'Lemon', 'banana', 'Quince', 'date', 'Fig', 'grape', 'Kiwi', 'olive', 'Pear', 'jam', 'Nut', 'ice'];
      const refs: string[] = [];
      for (const title of titles) {
        const ref = (await post(api, '/api/v1/tasks', { initiative: slug, title: `${title} ${slug}` }, 201)).ref;
        await post(api, `/api/v1/tasks/${ref}/close`, { resolution: 'completed' }, 200);
        refs.push(ref);
      }
      const own = async (p: Page) =>
        (await p.getByTestId('archive-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-ref') ?? ''))).filter((ref) => refs.includes(ref));

      await page.goto('/#/archive');
      await page.getByTestId('archive-window').selectOption('90');
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(titles.length);
      const byDefault = await own(page);
      await sortBy(page, true, 'item', 'desc'); // the shared sort control is a select at every width
      await expect(page).toHaveURL((url) => url.hash === '#/archive?window=90&sort=item&dir=desc');
      const sorted = await own(page);
      expect(sorted, 'the picked sort must reorder the rows').not.toEqual(byDefault);
      expect(sorted).toEqual([...refs].sort((a, b) => titles[refs.indexOf(b)].toLowerCase().localeCompare(titles[refs.indexOf(a)].toLowerCase())));

      const route: Route = {
        rows: own,
        shown: async (p) => {
          await expect(p.getByTestId('archive-window')).toHaveValue('90', FIVE_SECONDS);
          expect(queryOf(p).get('window')).toBe('90');
        },
        // Only the task links: the shared store's Archive also lists closed initiatives (this file closes one), whose link lands on #/i/.
        links: (p) => p.locator('[data-testid="archive-link"][href^="#/t/"]'),
        landing: /#\/t\//,
        sortColumn: 'item',
        sharedSort: true,
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the Notes view comes back with its filter and scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-notes-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore notes ${vp.name}` }, 201);
      const ref = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'The task the notes are on' }, 201)).ref;
      for (let i = 0; i < 22; i++) await post(api, '/api/v1/notes', { task: ref, summary: `Note ${String(i).padStart(2, '0')} of ${slug}` }, 201);

      await page.goto('/#/notes');
      await page.getByLabel('Initiative').fill(slug);
      await page.getByTestId('apply-filters').click();
      await expect(page).toHaveURL((url) => url.hash === `#/notes?initiative=${slug}`);
      await expect.poll(() => page.getByTestId('note').count(), FIVE_SECONDS).toBe(20);

      const route: Route = {
        rows: (p) => p.getByTestId('note-summary').allTextContents(),
        shown: async (p) => {
          await expect(p.getByLabel('Initiative')).toHaveValue(slug, FIVE_SECONDS);
        },
        links: (p) => p.getByTestId('note-scope'),
        landing: /#\/t\//,
      };
      await leaveAndReturn(page, route, phone);
    });

    test('the Timeline comes back with its filters, window, the rows scrolled inside the chart and the page scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-hr-tl-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `History restore timeline ${vp.name}` }, 201);
      // More rows than the chart is tall: the rows scroll inside it.
      for (let i = 0; i < 30; i++) await post(api, '/api/v1/tasks', { initiative: slug, title: `Row ${String(i).padStart(2, '0')}` }, 201);

      const listed = catalogRead(page, slug);
      await page.goto('/#/timeline');
      await expect(page.getByTestId('timeline-view')).toBeVisible();
      await listed;
      await pickInitiative(page, slug);
      await foldChips(page); // a fresh mount folds a phone's filter, so fold it here: the page is then as tall coming back as it was going
      await page.getByTestId('closed-filter').selectOption('90');
      await expect(page).toHaveURL((url) => url.hash.startsWith(`#/timeline?initiative=${slug}`));
      // The tag chips offered stay offered while the view is mounted (a narrower read takes none away), so the view that began on
      // the whole board offers every tag of the shared store, and a Back mounts it on the narrow read, with only the tags of the
      // chosen initiative's rows: a page of another height. Mount it the way Back does before measuring.
      await page.reload();
      await expect(page.locator('.vis-labelset .vis-label')).toHaveCount(31, FIVE_SECONDS);
      await expect(page.locator('.vis-loading-screen')).toHaveCount(0, FIVE_SECONDS);

      // The window: a sideways wheel pans, then a zoom step; the span readout carries the range shown.
      const windowShown = () =>
        page.getByTestId('timeline-span').evaluate((el) => ({ from: Number(el.getAttribute('data-from-ms')), to: Number(el.getAttribute('data-to-ms')) }));
      const opening = await windowShown();
      await page.getByTestId('timeline-chart').scrollIntoViewIfNeeded(); // the chips above it push the chart toward the fold
      const centre = (await page.locator('.vis-panel.vis-center').boundingBox())!;
      await page.mouse.move(centre.x + centre.width / 2, centre.y + 60);
      await page.mouse.wheel(300, 0);
      await expect.poll(async () => (await windowShown()).from, FIVE_SECONDS).not.toBe(opening.from);
      await page.getByTestId('zoom-in').click();
      await expect.poll(async () => (await windowShown()).to - (await windowShown()).from, FIVE_SECONDS).toBeLessThan(opening.to - opening.from);
      // The settled window is in the fragment (from and to), and the readout and the fragment agree.
      await expect.poll(() => queryOf(page).has('from') && queryOf(page).has('to'), FIVE_SECONDS).toBe(true);
      await page.waitForTimeout(600); // the read of the settled window comes back and redraws the rows

      // The rows scroll inside the chart (the label column's scrollTop), never the page.
      const rowsScroll = () => page.locator('.vis-panel.vis-left').evaluate((el) => Math.round(el.scrollTop));
      await page.locator('.vis-panel.vis-left').evaluate((el) => {
        el.scrollTop = 400;
      });
      await expect.poll(rowsScroll, FIVE_SECONDS).toBe(400);
      await page.waitForTimeout(300); // the scroll is reported once it settles

      // The page scrolls as far as it goes: the chart is as tall as the screen allows, so a desktop has only a few pixels of room.
      const room = await page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight);
      expect(room, 'the page must scroll under the chart').toBeGreaterThan(4);
      await page.evaluate((y) => window.scrollTo(0, y), room);
      await expect.poll(() => scrollY(page)).toBe(room);

      const left = { scrollY: await scrollY(page), fragment: fragment(page), window: await windowShown(), rows: await rowsScroll() };
      expect(queryOf(page).get('initiative')).toBe(slug);
      await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Now' }).click();
      await expect(page).toHaveURL(/#\/now/, FIVE_SECONDS);

      const restored = async () => {
        await expect(page).toHaveURL((url) => url.hash === left.fragment, FIVE_SECONDS);
        await expectInitiatives(page, [slug]);
        await expect(page.getByTestId('closed-filter')).toHaveValue('90');
        await expect.poll(async () => Math.abs((await rowsScroll()) - left.rows), FIVE_SECONDS).toBeLessThanOrEqual(2);
        await expect.poll(async () => Math.abs((await windowShown()).from - left.window.from), FIVE_SECONDS).toBeLessThanOrEqual(1000);
        await expect.poll(async () => Math.abs((await windowShown()).to - left.window.to), FIVE_SECONDS).toBeLessThanOrEqual(1000);
        await expect.poll(async () => Math.abs((await scrollY(page)) - left.scrollY), FIVE_SECONDS).toBeLessThanOrEqual(2);
      };
      await page.goBack();
      await restored();
      await page.goForward();
      await expect(page).toHaveURL(/#\/now/, FIVE_SECONDS);
      await page.goBack();
      await restored();
      await api.dispose();
    });

    test('Settings -> API keys comes back with its sort, row order and scroll', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const prefix = `e2e-hr-key-${vp.name}-`;
      // Created in this order, so the default order (the oldest key first) is this one; no name sort gives it.
      const names = ['mango', 'apple', 'zebra', 'cherry', 'lemon', 'banana', 'quince', 'date', 'fig', 'grape', 'kiwi', 'olive', 'pear', 'jam', 'nut', 'ice', 'rye', 'oat', 'tea', 'bean', 'yam', 'plum', 'sage', 'leek'];
      const keys = names.map((name) => `${prefix}${name}`);
      for (const key of keys) await post(api, '/api/v1/keys', { name: key }, 201);
      const own = async (p: Page) =>
        (await p.getByTestId('key-row').evaluateAll((rows) => rows.map((row) => row.getAttribute('data-name') ?? ''))).filter((name) => name.startsWith(prefix));

      await page.goto('/#/settings/keys');
      await expect.poll(() => own(page), FIVE_SECONDS).toHaveLength(names.length);
      const byDefault = await own(page);
      await sortBy(page, phone, 'name', 'desc');
      await expect(page).toHaveURL((url) => url.hash === '#/settings/keys?sort=name&dir=desc');
      const sorted = await own(page);
      expect(sorted, 'the picked sort must reorder the rows').not.toEqual(byDefault);
      expect(sorted).toEqual([...keys].sort().reverse());

      const route: Route = {
        rows: own,
        shown: async () => undefined, // the view has no filter
        // The table holds no link: the page is left by the navigation (a fixed bar, so nothing scrolls on the way).
        links: (p) => p.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Now' }),
        leave: (p) => p.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Now' }).click(),
        landing: /#\/now/,
        sortColumn: 'name',
      };
      await leaveAndReturn(page, route, phone);
    });
  });
}
