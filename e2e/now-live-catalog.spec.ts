import { expect, type Page } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';
import { initiativeChip, openChips, showAllInitiatives, tagChip } from './chips';
import { logicViewports } from './viewports';

// The filter controls of Now follow the initiative catalog live (PRD R1): an initiative made, edited or closed while the page
// is open changes the choices with no reload, and the chips chosen, their sources and the filter in the fragment stay.
// Each test seeds its own initiatives and tags (one shared store).
const FIVE_SECONDS = { timeout: 5000 };
const streamUp = (page: Page) => page.waitForResponse((r) => r.headers()['content-type']?.includes('text/event-stream') === true);

// Resolves once a catalog read has ended (its last page, `next_after` absent) whose last page lists `slug`, or any read's last
// page when no slug is named. A new initiative sorts last, so the read that carries it ends with it. The catalog is read page
// by page and a long run grows it to many pages, so a test waits for the read itself, never for a time.
function catalogRead(page: Page, slug?: string) {
  return page.waitForResponse(async (r) => {
    if (r.request().method() !== 'GET' || new URL(r.url()).pathname !== '/api/v1/initiatives') return false;
    const body = (await r.json()) as { items: { slug: string }[]; next_after?: string };
    return body.next_after === undefined && (slug === undefined || body.items.some((i) => i.slug === slug));
  });
}

async function openNow(page: Page, hash: string): Promise<void> {
  const ready = streamUp(page);
  const listed = catalogRead(page);
  await page.goto(hash);
  await ready;
  await listed;
  await openChips(page);
}

// The initiative chips fold behind "N more initiatives" once the catalog holds more than fit; until the catalog read has been
// applied there is nothing to unfold. So unfold again until the chip is on show (the catalog read that carries it has ended).
async function showInitiative(page: Page, slug: string): Promise<void> {
  await expect(async () => {
    await showAllInitiatives(page);
    await expect(initiativeChip(page, slug)).toBeVisible({ timeout: 500 });
  }).toPass(FIVE_SECONDS);
}


for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a new initiative becomes a choice on the open page', async ({ page, playwright, baseURL }, testInfo) => {
      const first = `e2e-livecat-a-${vp.name}${testInfo.repeatEachIndex}`;
      const second = `e2e-livecat-b-${vp.name}${testInfo.repeatEachIndex}`;
      const api = await actorApi(playwright, baseURL, 'pi:livecat');
      await created(api, '/api/v1/initiatives', { slug: first, title: first });
      await openNow(page, `/#/now?initiative=${first}`);
      await showAllInitiatives(page);
      await expect(initiativeChip(page, first)).toHaveAttribute('aria-pressed', 'true');
      expect(await initiativeChip(page, second).count()).toBe(0);
      const reread = catalogRead(page, second);
      await created(api, '/api/v1/initiatives', { slug: second, title: second });
      await reread;
      await showInitiative(page, second);
      // The chosen one stays chosen and the fragment keeps the filter.
      await expect(initiativeChip(page, first)).toHaveAttribute('aria-pressed', 'true');
      await expect(initiativeChip(page, second)).toHaveAttribute('aria-pressed', 'false');
      expect(new URL(page.url()).hash).toBe(`#/now?initiative=${first}`);
      await api.dispose();
    });

    test('an initiative tag added and then removed comes and goes as a choice with its source', async ({ page, playwright, baseURL }, testInfo) => {
      const slug = `e2e-livecat-c-${vp.name}${testInfo.repeatEachIndex}`;
      const tag = `livecat-tag-c-${vp.name}${testInfo.repeatEachIndex}`;
      const api = await actorApi(playwright, baseURL, 'pi:livecat');
      await created(api, '/api/v1/initiatives', { slug, title: slug });
      await created(api, '/api/v1/tasks', { initiative: slug, title: 'carries the initiative tag' });
      await openNow(page, `/#/now?initiative=${slug}`);
      await expect(page.getByTestId('chip-filter')).toBeVisible();
      expect(await tagChip(page, tag).count()).toBe(0);
      const added = await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [tag] } });
      expect(added.ok()).toBe(true);
      await expect(tagChip(page, tag)).toBeVisible(FIVE_SECONDS);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', `${tag}, initiative tag`);
      expect(new URL(page.url()).hash).toBe(`#/now?initiative=${slug}`);
      const removed = await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [] } });
      expect(removed.ok()).toBe(true);
      await expect(tagChip(page, tag)).toHaveCount(0, FIVE_SECONDS);
      await api.dispose();
    });

    test('a tag chosen stays on offer after its initiative stops carrying it', async ({ page, playwright, baseURL }, testInfo) => {
      const slug = `e2e-livecat-d-${vp.name}${testInfo.repeatEachIndex}`;
      const tag = `livecat-tag-d-${vp.name}${testInfo.repeatEachIndex}`;
      const api = await actorApi(playwright, baseURL, 'pi:livecat');
      await created(api, '/api/v1/initiatives', { slug, title: slug, tags: [tag] });
      await created(api, '/api/v1/tasks', { initiative: slug, title: 'tagged by its initiative' });
      await openNow(page, `/#/now?initiative=${slug}&tag=${tag}`);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', `${tag}, initiative tag`);
      const removed = await api.patch(`/api/v1/initiatives/${slug}`, { data: { tags: [] } });
      expect(removed.ok()).toBe(true);
      // The chosen tag is still chosen (the server still filters by it), but it no longer claims a source.
      await expect(tagChip(page, tag)).toHaveAttribute('aria-label', tag, FIVE_SECONDS);
      await expect(tagChip(page, tag)).toHaveAttribute('aria-pressed', 'true');
      await api.dispose();
    });

    test('a closed initiative stays a choice and a reopened one is unchanged', async ({ page, playwright, baseURL }, testInfo) => {
      const slug = `e2e-livecat-e-${vp.name}${testInfo.repeatEachIndex}`;
      const other = `e2e-livecat-f-${vp.name}${testInfo.repeatEachIndex}`;
      const api = await actorApi(playwright, baseURL, 'pi:livecat');
      await created(api, '/api/v1/initiatives', { slug, title: slug });
      await openNow(page, `/#/now?initiative=${slug}`);
      await showAllInitiatives(page);
      await expect(initiativeChip(page, slug)).toBeVisible();
      expect((await api.post(`/api/v1/initiatives/${slug}/close`, { data: {} })).ok()).toBe(true);
      // A later initiative is the proof the catalog was read again after the close.
      const reread = catalogRead(page, other);
      await created(api, '/api/v1/initiatives', { slug: other, title: other });
      await reread;
      await showInitiative(page, other);
      await expect(initiativeChip(page, slug)).toHaveAttribute('aria-pressed', 'true');
      await api.dispose();
    });
  });
}
