import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { openChips, tagChip } from './chips';
import { logicViewports } from './viewports';

// R7/R10 on Archive (SPEC 11, "Tag choices and saved filters on the task views"): a chosen tag names the sources it has on the tasks
// (of any status) and on the initiatives, whatever the combined filter returns, so a linked or restored pair of tags with an empty
// intersection still shows them; a restored tag nothing carries is dropped before the list is read, and a tag is read the way the
// server reads it (trimmed as Go does, lower case). Each test seeds initiatives of its own and closes their tasks.
const KEY = 'vh.view.v1.archive';

function scope(initiatives: string[], tags: string[]): URLSearchParams {
  const query = new URLSearchParams();
  for (const slug of initiatives) query.append('initiative', slug);
  for (const tag of tags) query.append('tag', tag);
  return query;
}

async function closedTask(api: Awaited<ReturnType<typeof actorApi>>, slug: string, tag: string, initiativeTags: string[] = []) {
  await created(api, '/api/v1/initiatives', { slug, title: slug, tags: initiativeTags });
  const task = await created(api, '/api/v1/tasks', { initiative: slug, title: slug, tags: [tag] });
  const result = await api.post(`/api/v1/tasks/${task.ref}/close`, { data: { resolution: 'completed' } });
  expect(result.status()).toBe(200);
}

async function restoredRead(page: Page, slug: string, query: string) {
  await page.addInitScript(({ key, query }) => localStorage.setItem(key, JSON.stringify({ v: 1, query })), { key: KEY, query });
  await page.goto('/#/actors');
  await expect(page.getByTestId('actors-view')).toBeVisible();
  const read = page.waitForResponse((r) => {
    const url = new URL(r.url());
    return url.pathname === '/api/v1/tasks' && url.searchParams.get('status') === 'closed' && url.searchParams.get('initiative') === slug;
  });
  await page.evaluate(() => (window.location.hash = '#/archive'));
  return new URL((await read).url());
}

for (const viewport of logicViewports) {
  test.describe(`archive at ${viewport.name}`, () => {
    test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));

    test('names the task source of two linked task-only tags whose closed intersection is empty', async ({ page, playwright, baseURL }) => {
      const slug = `arch-src-link-${viewport.name}`;
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      await closedTask(api, `${slug}-a`, `${slug}-left`);
      await closedTask(api, `${slug}-b`, `${slug}-right`);
      await api.dispose();
      await page.goto(`/#/archive?${scope([`${slug}-a`, `${slug}-b`], [`${slug}-left`, `${slug}-right`])}`);
      await expect(page.getByTestId('archive-empty')).toContainText('No closed item matches these chips');
      await openChips(page);
      for (const side of ['left', 'right']) {
        await expect(tagChip(page, `${slug}-${side}`)).toHaveAttribute('aria-pressed', 'true');
        await expect(tagChip(page, `${slug}-${side}`)).toHaveAttribute('aria-label', `${slug}-${side}, task tag`);
        await expect(tagChip(page, `${slug}-${side}`)).toHaveAttribute('title', `${slug}-${side}: task tag`);
      }
    });

    test('restores task-only tags with an empty intersection, naming their sources, and drops a tag nothing carries before reading', async ({ page, playwright, baseURL }) => {
      const slug = `arch-src-rest-${viewport.name}`;
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      await closedTask(api, `${slug}-a`, `${slug}-left`);
      await closedTask(api, `${slug}-b`, `${slug}-right`);
      await api.dispose();
      const query = scope([`${slug}-a`, `${slug}-b`], [`${slug}-left`, `${slug}-ghost`, `${slug}-right`]).toString();
      const url = await restoredRead(page, `${slug}-a`, query);
      expect(url.searchParams.getAll('tag')).toEqual([`${slug}-left`, `${slug}-right`]);
      await expect.poll(() => page.evaluate((key) => new URLSearchParams(JSON.parse(localStorage.getItem(key) ?? '{}').query ?? '').getAll('tag'), KEY)).toEqual([`${slug}-left`, `${slug}-right`]);
      await openChips(page);
      for (const side of ['left', 'right']) {
        await expect(tagChip(page, `${slug}-${side}`)).toHaveAttribute('aria-label', `${slug}-${side}, task tag`);
        await expect(tagChip(page, `${slug}-${side}`)).toHaveAttribute('title', `${slug}-${side}: task tag`);
      }
    });

    test('reads a linked tag the way the server does: U+0085 padding and upper case are one chip with its source', async ({ page, playwright, baseURL }) => {
      const slug = `arch-src-uni-${viewport.name}`;
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      await closedTask(api, `${slug}-a`, `${slug}-left`);
      await closedTask(api, `${slug}-b`, `${slug}-right`);
      await api.dispose();
      await page.goto(`/#/archive?${scope([`${slug}-a`, `${slug}-b`], [`\u0085${slug}-left\u0085`, `${slug}-RIGHT`])}`);
      await openChips(page);
      await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('aria-label', `${slug}-left, task tag`);
      await expect(tagChip(page, `${slug}-right`)).toHaveAttribute('title', `${slug}-right: task tag`);
    });

    test('restores a tag saved with U+0085 padding and an initiative tag, and drops a U+FEFF-padded one the server refuses', async ({ page, playwright, baseURL }) => {
      const slug = `arch-src-fe-${viewport.name}`;
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      await closedTask(api, `${slug}-a`, `${slug}-left`, [`${slug}-ini`]);
      await closedTask(api, `${slug}-b`, `${slug}-right`);
      await api.dispose();
      const saved = scope([`${slug}-a`, `${slug}-b`], [`\u0085${slug}-left`, `${slug}-ini`, `\ufeff${slug}-right`]);
      const url = await restoredRead(page, `${slug}-a`, saved.toString());
      expect(url.searchParams.getAll('tag')).toEqual([`${slug}-left`, `${slug}-ini`]);
      await openChips(page);
      await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('aria-label', `${slug}-left, task tag`);
      await expect(tagChip(page, `${slug}-ini`)).toHaveAttribute('aria-label', `${slug}-ini, initiative tag`);
    });

    test('keeps the sources of the restored chips across navigation away and back', async ({ page, playwright, baseURL }) => {
      const slug = `arch-src-nav-${viewport.name}`;
      const api = await actorApi(playwright, baseURL, 'ralph:review');
      await closedTask(api, `${slug}-a`, `${slug}-left`);
      await closedTask(api, `${slug}-b`, `${slug}-right`);
      await api.dispose();
      await restoredRead(page, `${slug}-a`, scope([`${slug}-a`, `${slug}-b`], [`${slug}-left`, `${slug}-right`]).toString());
      await page.evaluate(() => (window.location.hash = '#/actors'));
      await expect(page.getByTestId('actors-view')).toBeVisible();
      await page.evaluate(() => (window.location.hash = '#/archive'));
      await openChips(page);
      await expect(tagChip(page, `${slug}-left`)).toHaveAttribute('aria-label', `${slug}-left, task tag`);
      await expect(tagChip(page, `${slug}-right`)).toHaveAttribute('aria-pressed', 'true');
    });
  });
}
