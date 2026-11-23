import { expect } from '@playwright/test';
import { test } from './fixtures';
import { actorApi, created } from './now';

// A note bound to a task is that task's last touch, so Now's Recently touched rows follow it live (PRD R1): the row's
// last event and summary change and the touched task moves to the top, with no reload. Each test seeds its own
// initiative and filters Now to it (one shared store).
const FIVE_SECONDS = { timeout: 5000 };
const streamUp = (page: import('@playwright/test').Page) =>
  page.waitForResponse((r) => r.headers()['content-type']?.includes('text/event-stream') === true);

test('a note on a task changes that row’s last event with no reload', async ({ page, playwright, baseURL }, testInfo) => {
  const slug = `e2e-livenote-a${testInfo.repeatEachIndex}`;
  const api = await actorApi(playwright, baseURL, 'pi:livenote');
  await created(api, '/api/v1/initiatives', { slug, title: slug });
  const { ref } = await created(api, '/api/v1/tasks', { initiative: slug, title: 'one' });
  const ready = streamUp(page);
  await page.goto(`/#/now?initiative=${slug}`);
  await ready;
  const row = page.locator(`[data-testid="recent-row"][data-task="${ref}"]`);
  await expect(row.getByTestId('recent-last')).toContainText('task_created', FIVE_SECONDS);
  await created(api, '/api/v1/notes', { task: ref, summary: 'first live note' });
  await expect(row.getByTestId('recent-last')).toContainText('note_posted', FIVE_SECONDS);
  await expect(row.getByTestId('recent-summary')).toContainText('first live note', FIVE_SECONDS);
  await created(api, '/api/v1/notes', { task: ref, summary: 'second live note' });
  await expect(row.getByTestId('recent-summary')).toContainText('second live note', FIVE_SECONDS);
  await api.dispose();
});

test('a note on the older task moves it to the top of Recently touched', async ({ page, playwright, baseURL }, testInfo) => {
  const slug = `e2e-livenote-b${testInfo.repeatEachIndex}`;
  const api = await actorApi(playwright, baseURL, 'pi:livenote');
  const other = await actorApi(playwright, baseURL, 'pi:livenoteb');
  await created(api, '/api/v1/initiatives', { slug, title: slug });
  const older = await created(api, '/api/v1/tasks', { initiative: slug, title: 'older' });
  const newer = await created(api, '/api/v1/tasks', { initiative: slug, title: 'newer' });
  const ready = streamUp(page);
  await page.goto(`/#/now?initiative=${slug}`);
  await ready;
  const refs = page.locator('[data-testid="recent-row"]');
  await expect(refs).toHaveCount(2, FIVE_SECONDS);
  await expect(refs.first()).toHaveAttribute('data-task', newer.ref);
  await created(other, '/api/v1/notes', { task: older.ref, summary: 'bump the older one' });
  await expect(refs.first()).toHaveAttribute('data-task', older.ref, FIVE_SECONDS);
  await expect(refs.first().getByTestId('recent-last')).toContainText('note_posted', FIVE_SECONDS);
  await expect(refs.first().getByTestId('recent-last')).toContainText('pi:livenoteb', FIVE_SECONDS);
  await api.dispose();
  await other.dispose();
});
