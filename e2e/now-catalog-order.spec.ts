import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created, openFold } from './now';
import { initiativeChip, openChips, showAllInitiatives, tagChip } from './chips';

// Now reads the initiative catalog again on every initiative event (PRD R1), so two reads can overlap and land out of order.
// The filter choices follow the newest catalog: a read that lands after a newer one was shown is dropped, whether it is a live
// read or the page's first. Each test holds one read's answer by hand, lets a newer one land, then releases the held one.
// Each test seeds its own initiatives and tags (one shared store).
const streamUp = (page: Page) => page.waitForResponse((r) => r.headers()['content-type']?.includes('text/event-stream') === true);

// The catalog is read page by page (100 a page) and a new initiative sorts last, so the page that ends a read is the one
// that carries it: that is the page held. The browser counts the catalog reads it has finished parsing (the ending pages).
const COUNT_READS = () => {
  const w = window as unknown as { __catalogReads: number };
  w.__catalogReads = 0;
  const json = Response.prototype.json;
  Response.prototype.json = async function (this: Response) {
    const body = await json.call(this);
    if (new URL(this.url).pathname === '/api/v1/initiatives' && body?.next_after == null) w.__catalogReads += 1;
    return body;
  };
};
const readsDone = (page: Page) => page.evaluate(() => (window as unknown as { __catalogReads: number }).__catalogReads);

// The initiative chips fold behind "N more initiatives" once the catalog holds more than fit; until a read carrying them has
// been applied there is nothing to unfold. So unfold again until the chip is on show.
async function showInitiative(page: Page, slug: string): Promise<void> {
  await expect(async () => {
    await showAllInitiatives(page);
    await expect(initiativeChip(page, slug)).toBeVisible({ timeout: 500 });
  }).toPass({ timeout: 10_000 });
}

interface Held {
  /** The body of the held page, once the server answered it. */
  captured: Promise<{ items: { slug: string; tags: string[] }[] }>;
  /** Hands the held page to the page and waits until the read it ends has been parsed and its outcome applied. */
  release: () => Promise<void>;
}

async function holdNextRead(page: Page): Promise<Held> {
  const captured = Promise.withResolvers<{ items: { slug: string; tags: string[] }[] }>();
  const gate = Promise.withResolvers<void>();
  let armed = true;
  await page.route(
    (url) => url.pathname === '/api/v1/initiatives',
    async (route) => {
      // A later hold is registered on top of this one and answers first: once spent it falls back to the one below.
      if (!armed) return route.fallback();
      const response = await route.fetch();
      const body = await response.json();
      if (body.next_after != null) return route.fulfill({ response });
      armed = false;
      captured.resolve(body);
      await gate.promise;
      await route.fulfill({ response });
    },
  );
  return {
    captured: captured.promise,
    release: async () => {
      const before = await readsDone(page);
      gate.resolve();
      await page.waitForFunction((n) => (window as unknown as { __catalogReads: number }).__catalogReads > n, before);
      // A task boundary: the microtasks that apply the parsed answer to the view have all run.
      await page.evaluate(() => new Promise((done) => setTimeout(done, 0)));
    },
  };
}

// A bare #/now restores the saved query (vh.view.v1.attention), checked against the catalog before the board's first read.
async function saveQuery(page: Page, query: string): Promise<void> {
  await page.addInitScript((q) => localStorage.setItem('vh.view.v1.attention', JSON.stringify({ v: 1, query: q })), query);
}

async function openNow(page: Page, hash: string): Promise<void> {
  const ready = streamUp(page);
  await page.goto(hash);
  await ready;
  await openChips(page);
}

// Holds the page's first task-tag lookup of `tag` at the network (the read that says which sources a restored tag has and whether
// any task carries it): `captured` resolves with the sources it answered, `release()` lets it land and waits until it is applied.
const lookupOf = (tag: string) => `/api/v1/tasks?status=all&limit=1000&tag=${tag}`;
async function holdLookup(page: Page, tag: string) {
  const captured = Promise.withResolvers<string[]>();
  const gate = Promise.withResolvers<void>();
  let armed = true;
  await page.route('**/api/v1/tasks?**', async (route) => {
    const url = new URL(route.request().url());
    if (!armed || url.pathname + url.search !== lookupOf(tag)) return route.fallback();
    armed = false;
    const response = await route.fetch();
    const body = (await response.json()) as { items: { tag_marks: { tag: string; source: string }[] }[] };
    captured.resolve([...new Set(body.items.flatMap((i) => i.tag_marks.filter((m) => m.tag === tag).map((m) => m.source)))].sort());
    await gate.promise;
    await route.fulfill({ response });
  });
  return {
    captured: captured.promise,
    release: async () => {
      const landed = page.waitForResponse((r) => r.url().endsWith(lookupOf(tag)));
      gate.resolve();
      await (await landed).finished();
      await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
    },
  };
}
const savedQuery = (page: Page) => page.evaluate(() => JSON.parse(localStorage.getItem('vh.view.v1.attention') ?? 'null')?.query ?? null);
// The chip's accessible name with its sources in a fixed order: `<tag>, <source tag>, ...`.
const labelOf = async (page: Page, tag: string) => {
  const [name, ...sources] = ((await tagChip(page, tag).getAttribute('aria-label')) ?? '').split(', ');
  return [name, ...sources.sort()].join(', ');
};

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });
    test.beforeEach(async ({ page }) => {
      await page.addInitScript(COUNT_READS);
    });

    test('an older live catalog answer cannot bring back a dropped tag nor take away the current one', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-catorder-a-${n}`;
      const selected = `catorder-sel-${n}`;
      const stale = `catorder-stale-${n}`;
      const current = `catorder-cur-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [selected] });
      const task = await created(api, '/api/v1/tasks', { initiative: slug, title: 'changes filter membership' });
      const hash = `#/now?initiative=${slug}&tag=${selected}`;
      await openNow(page, `/${hash}`);
      await openFold(page, 'recent');
      const row = page.locator(`[data-testid="recent-row"][data-task="${task.ref}"]`);
      await expect(row).toBeVisible();
      await expect(tagChip(page, selected)).toHaveAttribute('aria-label', `${selected}, initiative tag`);
      const timeOrigin = await page.evaluate(() => performance.timeOrigin);

      const held = await holdNextRead(page);
      const readsBefore = await readsDone(page);
      expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [stale] } })).ok()).toBe(true);
      expect((await held.captured).items).toContainEqual(expect.objectContaining({ slug, tags: [stale] }));
      await expect(row).toHaveCount(0);
      expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [selected, current] } })).ok()).toBe(true);
      await expect(row).toBeVisible();
      await expect(tagChip(page, current)).toHaveAttribute('aria-label', `${current}, initiative tag`);
      await expect(tagChip(page, stale)).toHaveCount(0);
      // The chip's source can come from the tag's own lookup first: the held read is older only once a newer catalog read has
      // landed (the held one has not been parsed yet, so the count moving is the newer read's).
      await page.waitForFunction((n) => (window as unknown as { __catalogReads: number }).__catalogReads > n, readsBefore);

      await held.release();
      expect(await tagChip(page, stale).count()).toBe(0);
      await expect(tagChip(page, current)).toHaveAttribute('aria-label', `${current}, initiative tag`);
      await expect(tagChip(page, selected)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, selected)).toHaveAttribute('aria-label', `${selected}, initiative tag`);
      await expect(row).toBeVisible();
      expect(new URL(page.url()).hash).toBe(hash);
      expect(await page.evaluate(() => performance.timeOrigin)).toBe(timeOrigin);
      await api.dispose();
    });

    test('an older live catalog answer cannot take away an initiative made after it', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const chosen = `e2e-catorder-b-${n}`;
      const first = `e2e-catorder-c-${n}`;
      const second = `e2e-catorder-d-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug: chosen, title: chosen });
      const hash = `#/now?initiative=${chosen}`;
      await openNow(page, `/${hash}`);
      // The page's first read has ended, so the read held below is a live one (a long run's catalog takes many pages).
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 1);
      await showAllInitiatives(page);
      await expect(initiativeChip(page, chosen)).toHaveAttribute('aria-pressed', 'true');

      const held = await holdNextRead(page);
      await created(api, '/api/v1/initiatives', { slug: first, title: first });
      const old = (await held.captured).items.map((i) => i.slug);
      expect(old).toContain(first);
      expect(old).not.toContain(second);
      await created(api, '/api/v1/initiatives', { slug: second, title: second });
      // The newer read (begun on the second initiative's event) ends while the older one is still held.
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 2);
      await showInitiative(page, second);
      await expect(initiativeChip(page, first)).toBeVisible();

      await held.release();
      await showAllInitiatives(page);
      expect(await initiativeChip(page, second).count()).toBe(1);
      await expect(initiativeChip(page, second)).toHaveAttribute('aria-pressed', 'false');
      await expect(initiativeChip(page, first)).toBeVisible();
      await expect(initiativeChip(page, chosen)).toHaveAttribute('aria-pressed', 'true');
      expect(new URL(page.url()).hash).toBe(hash);
      await api.dispose();
    });

    test("the page's first catalog read landing after a live one cannot undo it", async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-catorder-e-${n}`;
      const before = `catorder-before-${n}`;
      const after = `catorder-after-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [before] });
      // Armed before the first navigation: the read held is the page's own first one.
      const held = await holdNextRead(page);
      const hash = `#/now?initiative=${slug}`;
      await openNow(page, `/${hash}`);
      expect((await held.captured).items).toContainEqual(expect.objectContaining({ slug, tags: [before] }));
      await expect(initiativeChip(page, slug)).toHaveAttribute('aria-pressed', 'true');

      expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [after] } })).ok()).toBe(true);
      await expect(tagChip(page, after)).toHaveAttribute('aria-label', `${after}, initiative tag`);
      await expect(tagChip(page, before)).toHaveCount(0);

      await held.release();
      expect(await tagChip(page, before).count()).toBe(0);
      await expect(tagChip(page, after)).toHaveAttribute('aria-label', `${after}, initiative tag`);
      await expect(initiativeChip(page, slug)).toHaveAttribute('aria-pressed', 'true');
      expect(new URL(page.url()).hash).toBe(hash);
      await api.dispose();
    });

    // A saved filter is restored against the page's first catalog read; an initiative event while that read is out is
    // read again too, and the restore and the choices follow whichever answer is the newest.
    test('an initiative edit while a saved filter is restored still reaches the choices', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const anchor = `e2e-catorder-f-${n}`;
      const edited = `e2e-catorder-g-${n}`;
      const stale = `catorder-rstale-${n}`;
      const current = `catorder-rcur-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug: anchor, title: anchor });
      await created(api, '/api/v1/initiatives', { slug: edited, title: edited, tags: [stale] });
      const task = await created(api, '/api/v1/tasks', { initiative: anchor, title: 'restored filter row' });
      const query = `initiative=${anchor}`;
      await saveQuery(page, query);
      const held = await holdNextRead(page);
      await openNow(page, '/#/now');
      expect((await held.captured).items).toContainEqual(expect.objectContaining({ slug: edited, tags: [stale] }));
      const timeOrigin = await page.evaluate(() => performance.timeOrigin);

      expect((await api.patch(`/api/v1/initiatives/${edited}`, { data: { tags: [current] } })).ok()).toBe(true);
      // The live read (the first to finish, the first one being held) has landed before the first read is released.
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 1);
      await held.release();
      await openFold(page, 'recent');
      await expect(page.locator(`[data-testid="recent-row"][data-task="${task.ref}"]`)).toBeVisible();
      await expect(initiativeChip(page, anchor)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, current)).toHaveAttribute('aria-label', `${current}, initiative tag`);
      expect(await tagChip(page, stale).count()).toBe(0);
      expect(new URL(page.url()).hash).toBe(`#/now?${query}`);
      expect(await page.evaluate(() => performance.timeOrigin)).toBe(timeOrigin);
      await api.dispose();
    });

    test('a saved initiative made while the first catalog read is out stays chosen', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const made = `e2e-catorder-h-${n}`;
      const query = `initiative=${made}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await saveQuery(page, query);
      const held = await holdNextRead(page);
      await openNow(page, '/#/now');
      expect((await held.captured).items.map((i) => i.slug)).not.toContain(made);

      await created(api, '/api/v1/initiatives', { slug: made, title: made });
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 1);
      await held.release();
      await showAllInitiatives(page);
      await expect(initiativeChip(page, made)).toHaveAttribute('aria-pressed', 'true');
      expect(new URL(page.url()).hash).toBe(`#/now?${query}`);
      await api.dispose();
    });

    test('a saved tag an initiative takes on while the first read is out stays chosen when the live read lands last', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const slug = `e2e-catorder-i-${n}`;
      const later = `catorder-later-${n}`;
      // No task carries the tag (the initiative has none): only the newest catalog makes the saved tag a valid choice.
      const query = `tag=${later}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug, title: slug });
      await saveQuery(page, query);
      const first = await holdNextRead(page);
      await openNow(page, '/#/now');
      expect((await first.captured).items).toContainEqual(expect.objectContaining({ slug, tags: [] }));

      const live = await holdNextRead(page);
      expect((await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [later] } })).ok()).toBe(true);
      expect((await live.captured).items).toContainEqual(expect.objectContaining({ slug, tags: [later] }));
      await first.release();
      // The restore waits for the live read: the saved tag is neither dropped nor read yet.
      expect(new URL(page.url()).hash).toBe(`#/now?${query}`);
      await live.release();
      await expect(tagChip(page, later)).toHaveAttribute('aria-pressed', 'true');
      await expect(tagChip(page, later)).toHaveAttribute('aria-label', `${later}, initiative tag`);
      expect(new URL(page.url()).hash).toBe(`#/now?${query}`);
      await api.dispose();
    });

    // R1: the saved filter is restored once a task-tag lookup answers; an initiative event while the lookup is out must not
    // make the restore forget what the lookup confirms, nor offer a source that event removed.
    test('a saved tag a task carries stays chosen when an event takes it off its last initiative while the lookup is out', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const owner = `e2e-catorder-j-${n}`;
      const holder = `e2e-catorder-k-${n}`;
      const tag = `catorder-last-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug: holder, title: holder, tags: [tag] });
      await created(api, '/api/v1/initiatives', { slug: owner, title: owner });
      const match = await created(api, '/api/v1/tasks', { initiative: owner, title: 'carries the tag itself', tags: [tag] });
      const other = await created(api, '/api/v1/tasks', { initiative: owner, title: 'carries no tag' });
      const viaInitiative = await created(api, '/api/v1/tasks', { initiative: holder, title: 'matches only through the initiative' });
      const query = `tag=${tag}`;
      await saveQuery(page, query);
      const lookup = await holdLookup(page, tag);
      await openNow(page, '/#/now');
      expect(await lookup.captured).toEqual(['initiative', 'task']);

      const timeOrigin = await page.evaluate(() => performance.timeOrigin);
      expect((await api.patch(`/api/v1/initiatives/${holder}`, { data: { tags: [] } })).ok()).toBe(true);
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 2);
      await lookup.release();

      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true');
      await expect.poll(() => labelOf(page, tag)).toBe(`${tag}, task tag`);
      expect(new URL(page.url()).hash).toBe(`#/now?${query}`);
      expect(await savedQuery(page)).toBe(query);
      await openFold(page, 'recent');
      await expect(page.locator(`[data-testid="recent-row"][data-task="${match.ref}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid="recent-row"][data-task="${other.ref}"]`)).toHaveCount(0);
      await expect(page.locator(`[data-testid="recent-row"][data-task="${viaInitiative.ref}"]`)).toHaveCount(0);
      expect(await page.evaluate(() => performance.timeOrigin)).toBe(timeOrigin);
      await api.dispose();
    });

    test('a saved tag a task carries offers the initiative source of an initiative an event gave it while the lookup is out', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const owner = `e2e-catorder-l-${n}`;
      const taker = `e2e-catorder-m-${n}`;
      const tag = `catorder-gain-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug: taker, title: taker });
      await created(api, '/api/v1/initiatives', { slug: owner, title: owner });
      const match = await created(api, '/api/v1/tasks', { initiative: owner, title: 'carries the tag itself', tags: [tag] });
      const query = `tag=${tag}`;
      await saveQuery(page, query);
      const lookup = await holdLookup(page, tag);
      await openNow(page, '/#/now');
      expect(await lookup.captured).toEqual(['task']);

      expect((await api.patch(`/api/v1/initiatives/${taker}`, { data: { tags: [tag] } })).ok()).toBe(true);
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 2);
      await lookup.release();

      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true');
      await expect.poll(() => labelOf(page, tag)).toBe(`${tag}, initiative tag, task tag`);
      expect(new URL(page.url()).hash).toBe(`#/now?${query}`);
      expect(await savedQuery(page)).toBe(query);
      await openFold(page, 'recent');
      await expect(page.locator(`[data-testid="recent-row"][data-task="${match.ref}"]`)).toBeVisible();
      await api.dispose();
    });

    test('a saved tag only the initiative gave a task is dropped when an event takes it off the initiative while the lookup is out', async ({ page, playwright, baseURL }, testInfo) => {
      const n = `${vp.name}${testInfo.repeatEachIndex}`;
      const holder = `e2e-catorder-p-${n}`;
      const tag = `catorder-gone-${n}`;
      const api = await actorApi(playwright, baseURL, 'pi:catorder');
      await created(api, '/api/v1/initiatives', { slug: holder, title: holder, tags: [tag] });
      await created(api, '/api/v1/tasks', { initiative: holder, title: 'matches only through the initiative' });
      await saveQuery(page, `tag=${tag}`);
      const lookup = await holdLookup(page, tag);
      await openNow(page, '/#/now');
      expect(await lookup.captured).toEqual(['initiative']);

      expect((await api.patch(`/api/v1/initiatives/${holder}`, { data: { tags: [] } })).ok()).toBe(true);
      await page.waitForFunction(() => (window as unknown as { __catalogReads: number }).__catalogReads >= 2);
      await lookup.release();

      // Nothing carries the tag any more: it is no choice, and the restored filter does not name it.
      await expect(tagChip(page, tag)).toHaveCount(0);
      expect(new URL(page.url()).hash).not.toContain(tag);
      expect((await savedQuery(page)) ?? '').not.toContain(tag);
      await api.dispose();
    });
  });
}
