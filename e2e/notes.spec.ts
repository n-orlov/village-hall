import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { logicViewports } from './viewports';

// The Notes view `#/notes` (PRD R10, SPEC 11): notes newest first, Post note with a task, an initiative or neither,
// the six filters in the fragment's query, a body that expands and Load more. The board is shared with the other
// specs, so every assertion is scoped by a filter that only this test's notes satisfy.

async function apiContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
}

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

async function fresh(api: APIRequestContext, slug: string): Promise<string> {
  await post(api, '/api/v1/initiatives', { slug, title: `Notes ${slug}` }, 201);
  return (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Task for notes' }, 201)).ref;
}

const dialog = (page: Page, name: string) => page.getByRole('dialog', { name });
const notes = (page: Page) => page.getByTestId('notes-list').getByTestId('note');
const summaries = (page: Page) => page.getByTestId('notes-list').getByTestId('note-summary').allTextContents();

// Posts through the Post note sheet; `scope` fills the task and initiative fields, none of them is a broadcast.
async function postFromUi(page: Page, note: { summary: string; task?: string; initiative?: string; resource?: string; body?: string; mentions?: string }) {
  await page.getByTestId('notes-view').getByRole('button', { name: 'Post note' }).click();
  const form = dialog(page, 'Post note');
  await expect(form).toBeVisible();
  if (note.task !== undefined) await form.getByLabel('Task (optional)').fill(note.task);
  if (note.initiative !== undefined) await form.getByLabel('Initiative (optional)').fill(note.initiative);
  await form.getByLabel('Summary').fill(note.summary);
  if (note.body !== undefined) await form.getByLabel('Body (optional, Markdown)').fill(note.body);
  if (note.resource !== undefined) await form.getByLabel('Resource (optional)').fill(note.resource);
  if (note.mentions !== undefined) await form.getByLabel('Mentions (optional)').fill(note.mentions);
  await form.getByRole('button', { name: 'Post note' }).click();
  await expect(form).toHaveCount(0);
  await expect(page.getByTestId('toast')).toHaveText('Note posted');
}

async function filterBy(page: Page, fields: Record<string, string>, broadcast = false) {
  const form = page.getByTestId('notes-filters');
  const toggle = form.getByTestId('broadcast-filter');
  if (((await toggle.getAttribute('aria-pressed')) === 'true') !== broadcast) await toggle.click();
  for (const label of ['Task', 'Initiative', 'Resource', 'Author', 'Mention']) await form.getByLabel(label, { exact: true }).fill(fields[label.toLowerCase()] ?? '');
  await page.getByTestId('apply-filters').click();
}

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('three notes posted from the UI (on a task, on an initiative, a broadcast) are found under their filters', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-notes-${vp.name}`;
      const resource = `host:notes-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      const task = await fresh(api, slug);
      await page.goto('/#/notes');
      await expect(page.getByRole('heading', { name: 'Notes', level: 2 })).toBeVisible();

      await postFromUi(page, { summary: `On the task ${vp.name}`, task, mentions: 'alice' });
      await postFromUi(page, { summary: `On the initiative ${vp.name}`, initiative: slug });
      await postFromUi(page, { summary: `Broadcast ${vp.name}`, resource });

      // The task filter: only the note on the task, labelled with the task.
      await filterBy(page, { task });
      await expect(page).toHaveURL(new RegExp(`#/notes\\?task=${task}$`));
      await expect(notes(page)).toHaveCount(1);
      await expect(notes(page).first().getByTestId('note-summary')).toHaveText(`On the task ${vp.name}`);
      await expect(notes(page).first().getByTestId('note-scope')).toHaveText(task);
      await expect(notes(page).first().getByTestId('note-author')).toHaveText('operator');

      // The initiative filter: the note on the initiative and the one on its task, newest first.
      await filterBy(page, { initiative: slug });
      await expect(page).toHaveURL(new RegExp(`#/notes\\?initiative=${slug}$`));
      await expect.poll(() => summaries(page)).toEqual([`On the initiative ${vp.name}`, `On the task ${vp.name}`]);
      await expect(notes(page).first().getByTestId('note-scope')).toHaveText(slug);

      // The broadcast filter (with the resource that makes it this test's): the note has no task and no initiative.
      await filterBy(page, { resource }, true);
      await expect(page).toHaveURL(new RegExp(`#/notes\\?broadcast=only&resource=${encodeURIComponent(resource)}$`));
      await expect.poll(() => summaries(page)).toEqual([`Broadcast ${vp.name}`]);
      await expect(notes(page).first().getByTestId('note-scope')).toHaveText('broadcast');
      await expect(notes(page).first().getByTestId('note-resource')).toHaveText(resource);

      // A reload keeps the filters; the author and mention filters find the notes too.
      await page.reload();
      await expect(page.getByTestId('broadcast-filter')).toHaveAttribute('aria-pressed', 'true');
      await expect.poll(() => summaries(page)).toEqual([`Broadcast ${vp.name}`]);
      await filterBy(page, { author: 'operator', mention: 'alice', initiative: slug });
      await expect.poll(() => summaries(page)).toEqual([`On the task ${vp.name}`]);
      await page.getByTestId('clear-filters').click();
      await expect(page).toHaveURL(/#\/notes$/);
      await expect(page.getByTestId('notes-filters').getByLabel('Author')).toHaveValue('');

      // Stored as posted: the task note belongs to the task's initiative, the broadcast to neither.
      const stored = await (await api.get(`/api/v1/notes?initiative=${slug}`)).json();
      expect(stored.items.map((n: { author: string }) => n.author)).toEqual(['operator', 'operator']);
      await api.dispose();
    });

    test('a refused post keeps the sheet open with the reason, and a body expands', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-notes-body-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      await page.goto(`/#/notes?initiative=${slug}`);
      await expect(page.getByTestId('no-notes')).toHaveText('No notes match these filters.');

      await page.getByTestId('notes-view').getByRole('button', { name: 'Post note' }).click();
      const form = dialog(page, 'Post note');
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form.getByTestId('dialog-error')).toHaveText('Write a summary.');
      await form.getByLabel('Task (optional)').fill('no-such-1');
      await form.getByLabel('Summary').fill('Has a body');
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form.getByTestId('dialog-error')).toBeVisible();
      await expect(form.getByLabel('Summary')).toHaveValue('Has a body');
      await form.getByLabel('Task (optional)').fill('');
      await form.getByLabel('Initiative (optional)').fill(slug);
      await form.getByLabel('Body (optional, Markdown)').fill('All **green**.');
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form).toHaveCount(0);

      await expect.poll(() => summaries(page)).toEqual(['Has a body']);
      const toggle = notes(page).first().getByRole('button', { name: /body of/ });
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');
      await toggle.click();
      await expect(notes(page).first().getByTestId('note-body').locator('strong')).toHaveText('green');
      await toggle.click();
      await expect(notes(page).first().getByTestId('note-body')).toBeHidden();
      await api.dispose();
    });

    test('a failed live refetch shows the inline error beside the notes, keeping the expanded body, and the next good one clears it', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-notes-fail-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      await post(api, '/api/v1/notes', { initiative: slug, summary: 'Kept note', body: 'Still **here**.' }, 201);
      await page.goto(`/#/notes?initiative=${slug}`);
      await expect.poll(() => summaries(page)).toEqual(['Kept note']);
      await notes(page).first().getByRole('button', { name: /body of/ }).click();
      await expect(notes(page).first().getByTestId('note-body').locator('strong')).toHaveText('here');

      // The list read fails from now on; a note posted through the API (not the page) sends the live event.
      let failing = true;
      await page.route(
        (url) => url.pathname === '/api/v1/notes',
        (route) => (failing ? route.fulfill({ status: 500, contentType: 'application/problem+json', body: JSON.stringify({ title: 'Boom', status: 500, detail: 'notes are down' }) }) : route.continue()),
      );
      await post(api, '/api/v1/notes', { initiative: slug, summary: 'Second note' }, 201);
      const error = page.getByTestId('notes-error');
      await expect(error).toBeVisible();
      await expect(page.getByTestId('notes-list')).toBeVisible();
      await expect(notes(page)).toHaveCount(1);
      await expect(notes(page).first().getByTestId('note-body').locator('strong')).toHaveText('here');
      await expect(notes(page).first().getByRole('button', { name: /body of/ })).toHaveAttribute('aria-expanded', 'true');

      // The next successful refetch shows the new note on top, drops the error and still has the body open.
      failing = false;
      await post(api, '/api/v1/notes', { initiative: slug, summary: 'Third note' }, 201);
      await expect.poll(() => summaries(page)).toEqual(['Third note', 'Second note', 'Kept note']);
      await expect(error).toHaveCount(0);
      await expect(notes(page).nth(2).getByTestId('note-body').locator('strong')).toHaveText('here');
      await api.dispose();
    });

    test('a failed body read keeps the list and its other expanded body, with the inline error', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-notes-bodyfail-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      const good = await post<{ ref: string }>(api, '/api/v1/notes', { initiative: slug, summary: 'Good body', body: 'Fine.' }, 201);
      const bad = await post<{ ref: string }>(api, '/api/v1/notes', { initiative: slug, summary: 'Bad body', body: 'Never read.' }, 201);
      await page.goto(`/#/notes?initiative=${slug}`);
      await expect.poll(() => summaries(page)).toEqual(['Bad body', 'Good body']);
      await page.getByRole('button', { name: `Show body of ${good.ref}` }).click();
      await expect(page.getByTestId('note-body')).toHaveText('Fine.');

      await page.route(`**/api/v1/notes/${bad.ref}*`, (route) => route.fulfill({ status: 500, contentType: 'application/problem+json', body: JSON.stringify({ title: 'Boom', status: 500, detail: 'body is down' }) }));
      await page.getByRole('button', { name: `Show body of ${bad.ref}` }).click();
      await expect(page.getByTestId('notes-error')).toBeVisible();
      await expect(notes(page)).toHaveCount(2);
      await expect(page.getByTestId('note-body')).toHaveText('Fine.');
      await api.dispose();
    });

    test('Load more shows the next page of an older tail, newest first', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-notes-page-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      const total = 45;
      for (let n = 1; n <= total; n++) await post(api, '/api/v1/notes', { initiative: slug, summary: `Note ${n}` }, 201);
      await page.goto(`/#/notes?initiative=${slug}`);

      await expect(notes(page)).toHaveCount(20);
      expect((await summaries(page))[0]).toBe('Note 45');
      expect((await summaries(page))[19]).toBe('Note 26');
      const more = page.getByTestId('load-more');
      await expect(more).toHaveText('Load more (20 next)');
      await more.click();
      await expect(notes(page)).toHaveCount(40);
      expect((await summaries(page))[20]).toBe('Note 25');
      await expect(more).toHaveText('Load more (5 next)');
      await more.click();
      await expect(notes(page)).toHaveCount(total);
      expect((await summaries(page))[total - 1]).toBe('Note 1');
      await expect(more).toHaveCount(0);
      await api.dispose();
    });
  });
}
