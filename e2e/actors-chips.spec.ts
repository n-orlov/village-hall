import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { expectTags, openChips, pickTag, tagChip } from './chips';
import { logicViewports } from './viewports';

// R7 on Actors (SPEC 11, "The chip filter on Actors"): the shared chip filter (tags only) keeps the actors whose live leases are on
// tasks that carry every chosen tag (own, initiative or derived), the filter lives in the fragment and in the saved view state, and the
// shared sort control reorders the narrowed rows. Each test seeds its own initiatives, tags and actors on the shared board and reads
// only its own rows by handle.
const KEY = 'vh.view.v1.actors';

async function post<T = { ref: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const actorApi = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, handle: string) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': handle } });

interface Seed {
  prefix: string;
  tagA: string;
  tagB: string;
  tagI: string;
  /** Default order (leases first, newest seen first): dora (task with own A and B), carl (task with own B, in an initiative tagged I), idle (no lease). */
  dora: string;
  carl: string;
  idle: string;
  all: string[];
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const prefix = `actchip-${name}-`;
  const [dora, carl, idle] = [`${prefix}dora`, `${prefix}carl`, `${prefix}idle`];
  const [tagA, tagB, tagI] = [`actchip-${name}-a`, `actchip-${name}-b`, `actchip-${name}-i`];
  const plain = `e2e-actchip-${name}-plain`;
  const tagged = `e2e-actchip-${name}-tagged`;
  const holder = async (handle: string, slug: string, tags: string[], init?: object) => {
    const api = await actorApi(playwright, baseURL, handle);
    if (init !== undefined) await post(api, '/api/v1/initiatives', init, 201);
    const task = (await post(api, '/api/v1/tasks', { initiative: slug, title: `chip ${handle}`, tags }, 201)).ref;
    await post(api, '/api/v1/leases', { task, intent: `edit ${handle}`, resources: [`repo:${handle}`] }, 201);
    await api.dispose();
  };
  await holder(carl, tagged, [tagB], { slug: tagged, title: `Actors chips ${name} tagged`, tags: [tagI] });
  await holder(dora, plain, [tagA, tagB], { slug: plain, title: `Actors chips ${name} plain` });
  const quiet = await actorApi(playwright, baseURL, idle);
  await post(quiet, '/api/v1/notes', { summary: 'The idle actor was here' }, 201);
  await quiet.dispose();
  return { prefix, tagA, tagB, tagI, dora, carl, idle, all: [dora, carl, idle] };
}

const order = (page: Page, prefix: string) =>
  page.locator(`[data-testid=actor-row][data-handle^="${prefix}"]`).evaluateAll((rows) => rows.map((row) => row.getAttribute('data-handle')));
const saved = async (page: Page) => (JSON.parse((await page.evaluate((k) => localStorage.getItem(k), KEY)) as string) as { query: string }).query;
const endsWith = (fragment: string) => new RegExp(`${fragment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`);

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('tag chips narrow the actors by the tasks they lease, the fragment and the saved view hold them, and a reload keeps them', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `tag-${vp.name}`);
      await page.goto('/#/actors');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora, s.carl, s.idle]);
      await openChips(page);
      await expect(page.locator('[data-testid="chip-initiative"]')).toHaveCount(0); // tags only
      await expect(tagChip(page, s.tagA)).toHaveAttribute('data-source', 'task');
      await expect(tagChip(page, s.tagI)).toHaveAttribute('data-source', 'initiative');

      await pickTag(page, s.tagB);
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora, s.carl]);
      await pickTag(page, s.tagA);
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora]);
      await expect(page).toHaveURL(endsWith(`#/actors?tag=${s.tagB}&tag=${s.tagA}`));
      expect(await saved(page)).toBe(`tag=${s.tagB}&tag=${s.tagA}`);

      await page.reload();
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora]);
      await openChips(page);
      await expectTags(page, [s.tagA, s.tagB]);

      // An initiative's tag counts too, and taking the tags off widens the list again.
      await tagChip(page, s.tagA).click();
      await tagChip(page, s.tagB).click();
      await pickTag(page, s.tagI);
      await expect.poll(() => order(page, s.prefix)).toEqual([s.carl]);
    });

    test('a bare #/actors after a visit elsewhere restores the saved filter', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `keep-${vp.name}`);
      await page.goto('/#/actors');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora, s.carl, s.idle]);
      await pickTag(page, s.tagA);
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora]);
      await page.goto('/#/tasks');
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await page.goto('/#/actors');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora]);
      await expect(page).toHaveURL(endsWith(`#/actors?tag=${s.tagA}`));
      await openChips(page);
      await expectTags(page, [s.tagA]);
    });

    test('the shared sort control renders and reorders the seeded actors, with the chosen tag kept', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `sort-${vp.name}`);
      await page.goto('/#/actors');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora, s.carl, s.idle]);
      const select = page.getByTestId('sort-select');
      await expect(select).toBeVisible();
      await expect(page.locator('th [data-testid^="sort-"]')).toHaveCount(0); // the headers are plain: the control is the one sort
      await select.selectOption('actor:asc');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.carl, s.dora, s.idle]);
      await select.selectOption('actor:desc');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.idle, s.dora, s.carl]);
      await pickTag(page, s.tagB);
      await expect.poll(() => order(page, s.prefix)).toEqual([s.dora, s.carl]);
      await expect(page).toHaveURL(endsWith(`#/actors?tag=${s.tagB}&sort=actor&dir=desc`));
      expect(await saved(page)).toBe(`tag=${s.tagB}&sort=actor&dir=desc`);
      await select.selectOption('actor:asc');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.carl, s.dora]);
      await page.reload();
      await expect(select).toHaveValue('actor:asc');
      await expect.poll(() => order(page, s.prefix)).toEqual([s.carl, s.dora]);
    });

    test('tags nobody holds a lease on match no actor and the view says so', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `none-${vp.name}`);
      await page.goto(`/#/actors?tag=${s.tagA}&tag=${s.tagI}`);
      await expect(page.getByTestId('actors-none')).toBeVisible();
      await expect.poll(() => order(page, s.prefix)).toEqual([]);
    });
  });
}
