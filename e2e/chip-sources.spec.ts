import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, pickTag, tagChip } from './chips';

// R7/R10 (SPEC 11, "Tag choices and saved filters on the task views"): Tasks, Now and Timeline offer every tag of the tasks they show
// (own, initiative and derived actor kind, with their sources), and a restored filter keeps a tag a task carries even when no
// initiative lists it, the same rule the server filters by. Each test seeds an initiative of its own on the shared board.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const views = [
  { id: 'tasks', key: 'vh.view.v1.tasks', endpoint: '/api/v1/tasks' },
  { id: 'now', key: 'vh.view.v1.attention', endpoint: '/api/v1/attention/now' },
  { id: 'timeline', key: 'vh.view.v1.timeline', endpoint: '/api/v1/timeline' },
] as const;

async function seed(playwright: Parameters<typeof actorApi>[0], baseURL: string | undefined, slug: string, own: string[], lease = true) {
  const api = await actorApi(playwright, baseURL, 'ralph:review');
  await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [`${slug}-ini`] });
  const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: `task of ${slug}`, tags: own });
  if (lease) await created(api, '/api/v1/leases', { task: ref, intent: 'review' });
  await api.dispose();
  return ref;
}

// Two tasks of one initiative, each with tags of its own; the first is leased when `lease` (a derived actor-kind tag).
async function seedTwo(playwright: Parameters<typeof actorApi>[0], baseURL: string | undefined, slug: string, first: string[], second: string[], lease = false) {
  const api = await actorApi(playwright, baseURL, 'ralph:review');
  await created(api, '/api/v1/initiatives', { slug, title: slug });
  const a = await created(api, '/api/v1/tasks', { initiative: slug, title: 'first', tags: first });
  await created(api, '/api/v1/tasks', { initiative: slug, title: 'second', tags: second });
  if (lease) await created(api, '/api/v1/leases', { task: a.ref, intent: 'review' });
  await api.dispose();
}

// A bare navigation to the view after another one: the saved query is restored, and the read it makes is returned.
async function restoredRead(page: Page, view: (typeof views)[number], slug: string, query: string) {
  await page.addInitScript(({ key, query }) => localStorage.setItem(key, JSON.stringify({ v: 1, query })), { key: view.key, query });
  await page.goto('/#/actors');
  await expect(page.getByTestId('actors-view')).toBeVisible();
  const read = page.waitForResponse((r) => new URL(r.url()).pathname === view.endpoint && new URL(r.url()).searchParams.get('initiative') === slug);
  await page.evaluate((id) => (window.location.hash = `#/${id}`), view.id);
  return new URL((await read).url());
}

for (const viewport of viewports) {
  for (const view of views) {
    test.describe(`${view.id} at ${viewport.name}`, () => {
      test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

      test('offers the task\'s own, its initiative\'s and its actor-kind tag, each with its source', async ({ page, playwright, baseURL }) => {
        const slug = `src-offer-${view.id}-${viewport.name}`;
        await seed(playwright, baseURL, slug, [`${slug}-own`]);
        await page.goto(`/#/${view.id}?initiative=${slug}`);
        await openChips(page);
        await expect(tagChip(page, `${slug}-ini`)).toBeVisible();
        await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('aria-label', /task tag/);
        await expect(tagChip(page, `${slug}-ini`)).toHaveAttribute('aria-label', /initiative tag/);
        await expect(tagChip(page, 'ralph')).toHaveAttribute('aria-label', /actor kind/);
      });

      test('keeps the other chips on offer after one is chosen', async ({ page, playwright, baseURL }) => {
        const slug = `src-keep-${view.id}-${viewport.name}`;
        await seed(playwright, baseURL, slug, [`${slug}-own`]);
        await page.goto(`/#/${view.id}?initiative=${slug}`);
        await openChips(page);
        await pickTag(page, `${slug}-own`);
        await expect(tagChip(page, 'ralph')).toBeVisible();
        await expect(tagChip(page, `${slug}-ini`)).toBeVisible();
      });

      test('restores a task-only tag, with its initiative', async ({ page, playwright, baseURL }) => {
        const slug = `src-own-${view.id}-${viewport.name}`;
        await seed(playwright, baseURL, slug, [`${slug}-own`]);
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug}-own`);
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-own`]);
      });

      test('restores an actor-kind tag no initiative lists', async ({ page, playwright, baseURL }) => {
        const slug = `src-actor-${view.id}-${viewport.name}`;
        await seed(playwright, baseURL, slug, []);
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=ralph`);
        expect(url.searchParams.getAll('tag')).toEqual(['ralph']);
      });

      // A chip names the sources its tag has on the tasks, not on the rows the combined filter keeps: with an intersection that keeps
      // none the view has no rows to read them from, so the sources come from the tasks that carry the tag.
      test('names the task source of two restored task-only tags whose intersection is empty', async ({ page, playwright, baseURL }) => {
        const slug = `src-empty-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-left`], [`${slug}-right`]);
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug}-left&tag=${slug}-right`);
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-left`, `${slug}-right`]);
        await openChips(page);
        for (const side of ['left', 'right']) {
          const chip = tagChip(page, `${slug}-${side}`);
          await expect(chip).toHaveAttribute('aria-pressed', 'true');
          await expect(chip).toHaveAttribute('aria-label', `${slug}-${side}, task tag`);
          await expect(chip).toHaveAttribute('title', `${slug}-${side}: task tag`);
        }
      });

      test('names every source of a restored tag an initiative lists and another task owns, with an empty intersection', async ({ page, playwright, baseURL }) => {
        const slug = `src-both-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        await created(api, '/api/v1/initiatives', { slug: `${slug}-x`, title: 'x', tags: [`${slug}-shared`] });
        await created(api, '/api/v1/initiatives', { slug, title: slug });
        await created(api, '/api/v1/tasks', { initiative: slug, title: 'shared', tags: [`${slug}-shared`] });
        await created(api, '/api/v1/tasks', { initiative: slug, title: 'other', tags: [`${slug}-other`] });
        await api.dispose();
        await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug}-shared&tag=${slug}-other`);
        await openChips(page);
        await expect(tagChip(page, `${slug}-shared`)).toHaveAttribute('aria-label', `${slug}-shared, task tag, initiative tag`);
        await expect(tagChip(page, `${slug}-other`)).toHaveAttribute('aria-label', `${slug}-other, task tag`);
      });

      test('names the actor kind of a restored derived tag next to a task-only tag, with an empty intersection', async ({ page, playwright, baseURL }) => {
        const slug = `src-kind-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-leased`], [`${slug}-bare`], true);
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=ralph&tag=${slug}-bare`);
        expect(url.searchParams.getAll('tag')).toEqual(['ralph', `${slug}-bare`]);
        await openChips(page);
        await expect(tagChip(page, 'ralph')).toHaveAttribute('aria-label', /^ralph, actor kind/);
        await expect(tagChip(page, `${slug}-bare`)).toHaveAttribute('aria-label', `${slug}-bare, task tag`);
      });

      test('names the sources of a linked (not restored) pair of tags with an empty intersection', async ({ page, playwright, baseURL }) => {
        const slug = `src-link-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-left`], [`${slug}-right`]);
        await page.goto(`/#/${view.id}?initiative=${slug}&tag=${slug}-left&tag=${slug}-right`);
        await openChips(page);
        await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('aria-label', `${slug}-left, task tag`);
        await expect(tagChip(page, `${slug}-right`)).toHaveAttribute('aria-label', `${slug}-right, task tag`);
      });

      test('keeps the sources of the restored chips across navigation away and back', async ({ page, playwright, baseURL }) => {
        const slug = `src-nav-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-left`], [`${slug}-right`]);
        await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug}-left&tag=${slug}-right`);
        await page.evaluate(() => (window.location.hash = '#/actors'));
        await expect(page.getByTestId('actors-view')).toBeVisible();
        await page.evaluate((id) => (window.location.hash = `#/${id}`), view.id);
        await openChips(page);
        await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('aria-label', `${slug}-left, task tag`);
        await expect(tagChip(page, `${slug}-right`)).toHaveAttribute('aria-pressed', 'true');
      });

      test('still drops a restored tag nothing carries beside a valid pair with an empty intersection', async ({ page, playwright, baseURL }) => {
        const slug = `src-ghost-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-left`], [`${slug}-right`]);
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug}-left&tag=${slug}-ghost&tag=${slug}-right`);
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-left`, `${slug}-right`]);
      });

      test('restores a task-only and an initiative tag together and still drops a tag nothing carries', async ({ page, playwright, baseURL }) => {
        const slug = `src-mix-${view.id}-${viewport.name}`;
        await seed(playwright, baseURL, slug, [`${slug}-own`]);
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug}-ini&tag=${slug}-own&tag=${slug}-nothing`);
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-ini`, `${slug}-own`]);
      });

      // The server reads a tag trimmed and in lower case (SPEC 6.4), so ` OWN ` and `RALPH` in a link or a saved filter name the tags
      // `own` and `ralph`: the chip, its sources and the restore check use that spelling, whatever the link or the saved query wrote.
      test('names the sources of a linked task-only and actor-kind tag spelled in upper case and padded, with an empty intersection', async ({ page, playwright, baseURL }) => {
        const slug = `src-case-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [], [`${slug}-own`], true);
        const read = page.waitForResponse((r) => new URL(r.url()).pathname === view.endpoint && new URL(r.url()).searchParams.get('initiative') === slug);
        await page.goto(`/#/${view.id}?initiative=${slug}&tag=${encodeURIComponent(` ${slug.toUpperCase()}-OWN `)}&tag=RALPH`);
        expect(new URL((await read).url()).searchParams.getAll('tag')).toEqual([`${slug}-own`, 'ralph']);
        await openChips(page);
        const own = tagChip(page, `${slug}-own`);
        await expect(own).toHaveAttribute('aria-pressed', 'true');
        await expect(own).toHaveAttribute('aria-label', `${slug}-own, task tag`);
        await expect(own).toHaveAttribute('title', `${slug}-own: task tag`);
        const kind = tagChip(page, 'ralph');
        await expect(kind).toHaveAttribute('aria-pressed', 'true');
        await expect(kind).toHaveAttribute('aria-label', /^ralph, (.+, )?actor kind, from the holder of a lease$/);
        await expect(kind).toHaveAttribute('title', /^ralph: (.+, )?actor kind, from the holder of a lease$/);
      });

      test('restores a task-only and an actor-kind tag saved in upper case, keeps them in the server\'s spelling and drops a tag nothing carries', async ({ page, playwright, baseURL }) => {
        const slug = `src-cased-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [], [`${slug}-own`], true);
        const query = new URLSearchParams([['initiative', slug], ['tag', ` ${slug.toUpperCase()}-OWN `], ['tag', 'RALPH'], ['tag', `${slug}-UNKNOWN`]]);
        const url = await restoredRead(page, view, slug, query.toString());
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-own`, 'ralph']);
        await expect.poll(() => new URLSearchParams(new URL(page.url()).hash.split('?')[1]).getAll('tag')).toEqual([`${slug}-own`, 'ralph']);
        await expect
          .poll(() => page.evaluate((key) => new URLSearchParams(JSON.parse(localStorage.getItem(key) ?? '{}').query ?? '').getAll('tag'), view.key))
          .toEqual([`${slug}-own`, 'ralph']);
        await openChips(page);
        await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('aria-label', `${slug}-own, task tag`);
        await expect(tagChip(page, 'ralph')).toHaveAttribute('aria-label', /^ralph, (.+, )?actor kind/);
      });

      test('restores an initiative tag saved in upper case beside a task-only tag, naming the initiative source', async ({ page, playwright, baseURL }) => {
        const slug = `src-caseini-${view.id}-${viewport.name}`;
        const api = await actorApi(playwright, baseURL, 'ralph:review');
        await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [`${slug}-ini`] });
        await created(api, '/api/v1/initiatives', { slug: `${slug}-x`, title: 'x' });
        await created(api, '/api/v1/tasks', { initiative: `${slug}-x`, title: 'own', tags: [`${slug}-own`] });
        await api.dispose();
        const url = await restoredRead(page, view, slug, `initiative=${slug}&tag=${slug.toUpperCase()}-INI&tag=${slug}-Own`);
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-ini`, `${slug}-own`]);
        await openChips(page);
        await expect(tagChip(page, `${slug}-ini`)).toHaveAttribute('aria-label', `${slug}-ini, initiative tag`);
        await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('aria-label', `${slug}-own, task tag`);
      });

      // The server trims what Go's `strings.TrimSpace` does (U+0085 and U+00A0 among them) and lower-cases rune by rune (U+0130 is `i`),
      // which is not what JavaScript's trim and lower-casing do: the chip, its sources and the restore check follow the server.
      test('names the task source of a linked tag the server reads through U+0085 padding, with an empty intersection', async ({ page, playwright, baseURL }) => {
        const slug = `src-nel-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-left`], [`${slug}-right`]);
        const read = page.waitForResponse((r) => new URL(r.url()).pathname === view.endpoint && new URL(r.url()).searchParams.get('initiative') === slug);
        await page.goto(`/#/${view.id}?initiative=${slug}&tag=${encodeURIComponent(`\u0085${slug}-left\u0085`)}&tag=${slug}-right`);
        expect(new URL((await read).url()).searchParams.getAll('tag')).toEqual([`${slug}-left`, `${slug}-right`]);
        await openChips(page);
        await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('aria-label', `${slug}-left, task tag`);
        await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('title', `${slug}-left: task tag`);
      });

      test('restores tags padded with U+0085 or U+00A0 and spelled with U+0130, naming their sources, and drops a U+FEFF-padded one the server refuses', async ({ page, playwright, baseURL }) => {
        const slug = `src-uni-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-left`], [`${slug}-right`, `${slug}-ivy`]);
        const query = new URLSearchParams([
          ['initiative', slug],
          ['tag', `\u0085${slug}-left\u0085`],
          ['tag', `\u00a0${slug}-right`],
          ['tag', `${slug}-\u0130vy`],
          ['tag', `\ufeff${slug}-left`],
        ]);
        const url = await restoredRead(page, view, slug, query.toString());
        expect(url.searchParams.getAll('tag')).toEqual([`${slug}-left`, `${slug}-right`, `${slug}-ivy`]);
        await openChips(page);
        for (const side of ['left', 'right', 'ivy']) {
          await expect(tagChip(page, `${slug}-${side}`)).toHaveAttribute('aria-label', `${slug}-${side}, task tag`);
        }
      });

      test('reads two spellings of one tag in a link as one chip and keeps it across navigation away and back', async ({ page, playwright, baseURL }) => {
        const slug = `src-casedup-${view.id}-${viewport.name}`;
        await seedTwo(playwright, baseURL, slug, [`${slug}-own`], [], false);
        const read = page.waitForResponse((r) => new URL(r.url()).pathname === view.endpoint && new URL(r.url()).searchParams.get('initiative') === slug);
        await page.goto(`/#/${view.id}?initiative=${slug}&tag=${slug.toUpperCase()}-OWN&tag=${encodeURIComponent(` ${slug}-own`)}`);
        expect(new URL((await read).url()).searchParams.getAll('tag')).toEqual([`${slug}-own`]);
        await page.evaluate(() => (window.location.hash = '#/actors'));
        await expect(page.getByTestId('actors-view')).toBeVisible();
        await page.evaluate((id) => (window.location.hash = `#/${id}`), view.id);
        await openChips(page);
        await expect(page.locator('[data-testid="chip-tag"][aria-pressed="true"]')).toHaveCount(1);
        await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('aria-label', `${slug}-own, task tag`);
        await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('title', `${slug}-own: task tag`);
      });
    });
  }
}

// Tasks has filters of its own besides the chips: a restored search that matches nothing leaves no row to read the sources from either.
for (const viewport of viewports) {
  test(`tasks at ${viewport.name}: names the sources of restored tags under a search that matches no task`, async ({ page, playwright, baseURL }) => {
    const slug = `src-text-tasks-${viewport.name}`;
    await seed(playwright, baseURL, slug, [`${slug}-own`]);
    await page.setViewportSize(viewport.size);
    const url = await restoredRead(page, views[0], slug, `initiative=${slug}&tag=${slug}-own&tag=ralph&q=${slug}-matches-nothing`);
    expect(url.searchParams.getAll('tag')).toEqual([`${slug}-own`, 'ralph']);
    await expect(page.getByTestId('tasks-skeleton')).toHaveCount(0);
    await openChips(page);
    await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('aria-label', `${slug}-own, task tag`);
    await expect(tagChip(page, `${slug}-own`)).toHaveAttribute('title', `${slug}-own: task tag`);
    await expect(tagChip(page, 'ralph')).toHaveAttribute('aria-label', /^ralph, (.+, )?actor kind/);
  });
}
