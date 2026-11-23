import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';

// Signs in through the mock provider, so no API key rides along (see login.spec.ts).
// The initiative page `#/i/<slug>` and the Initiatives list's links, counts, live leases and status filter,
// and the page's Edit, Close (with force) and Reopen dialogs, at the phone and the desktop size.
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

const row = (page: Page, slug: string) => page.locator(`[data-testid=initiative-row][data-slug=${slug}]`);
const taskRow = (page: Page, ref: string) => page.locator(`[data-testid=task-row][data-ref=${ref}]`);
// A task's row wherever the page lists it: in the table, or in Needs attention above it.
const listedRow = (page: Page, ref: string) => page.locator(`[data-testid=task-row][data-ref=${ref}], [data-testid=needs-row][data-ref=${ref}]`);
// Every task the view lists: the ones in Needs attention above the table, then the table's own (a task is in one of them).
const shownRefs = async (page: Page) => [...(await page.getByTestId('needs-ref').allTextContents()), ...(await page.getByTestId('task-ref').allTextContents())];

async function post(api: APIRequestContext, path: string, data: object, status: number) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as { ref: string };
}

// An open initiative with five tasks (open and leased, done, closed, open and blocked by the leased one,
// open with a second lease) and a closed one; returns the slugs.
async function seed(api: APIRequestContext, prefix: string) {
  const open = `${prefix}-open`;
  const closed = `${prefix}-closed`;
  await post(api, '/api/v1/initiatives', { slug: closed, title: `Closed ${prefix}` }, 201);
  await post(api, `/api/v1/initiatives/${closed}/close`, {}, 200);
  await post(
    api,
    '/api/v1/initiatives',
    {
      slug: open,
      title: `Open ${prefix}`,
      description: 'Plan with **bold** words and <b>raw html</b>.\n\n- first point\n- second point',
      tags: ['web', 'e2e'],
      target_start: '2026-03-01',
      target_end: '2026-04-15',
    },
    201,
  );
  const task = (title: string, extra: object = {}) => post(api, '/api/v1/tasks', { initiative: open, title, ...extra }, 201);
  const leased = await task('Leased work');
  const done = await task('Finished work');
  const dropped = await task('Dropped work');
  const blocked = await task('Blocked work', { blocked_by: [leased.ref] });
  await post(api, `/api/v1/leases`, { task: leased.ref, intent: 'working on it' }, 201);
  await post(api, `/api/v1/tasks/${done.ref}/done`, {}, 200);
  await post(api, `/api/v1/tasks/${dropped.ref}/close`, { resolution: 'dropped' }, 200);
  return { open, closed, leased: leased.ref, done: done.ref, dropped: dropped.ref, blocked: blocked.ref };
}

// A fresh open initiative per test (the dialog tests change it), with the given tags and open tasks; returns its slug.
async function fresh(api: APIRequestContext, slug: string, tasks = 0, extra: object = {}) {
  await post(api, '/api/v1/initiatives', { slug, title: `Fresh ${slug}`, description: 'Old words.', tags: ['old', 'keep'], target_start: '2026-03-01', ...extra }, 201);
  const refs: string[] = [];
  for (let n = 1; n <= tasks; n++) refs.push((await post(api, '/api/v1/tasks', { initiative: slug, title: `Task ${n}` }, 201)).ref);
  return refs;
}

async function apiContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
}

const dialog = (page: Page, name: string) => page.getByRole('dialog', { name });

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });
    let seeded: Awaited<ReturnType<typeof seed>>;

    test.beforeAll(async ({ playwright, baseURL }) => {
      const api = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      seeded = await seed(api, `e2e-ip-${vp.name}`);
      await api.dispose();
    });

    test('an initiative opens from the list into its page with the details, counts and tasks', async ({ page }) => {
      await page.goto('/#/initiatives');
      const listed = row(page, seeded.open);
      await expect(listed).toHaveCount(1);
      await expect(listed.getByTestId('initiative-counts')).toHaveText('2 open · 1 done · 1 closed');
      await expect(listed.getByTestId('initiative-leases')).toHaveText('1 live lease');
      await listed.getByRole('link', { name: `Open e2e-ip-${vp.name}` }).click();

      await expect(page).toHaveURL(new RegExp(`#/i/${seeded.open}$`));
      await expect(page.getByRole('heading', { level: 2, name: `Open e2e-ip-${vp.name}` })).toBeVisible();
      await expect(page.getByTestId('initiative-slug')).toHaveText(seeded.open);
      await expect(page.getByTestId('initiative-status')).toHaveText('open');
      await expect(page.getByTestId('tag')).toHaveText(['e2e', 'web']);
      await expect(page.getByTestId('initiative-targets')).toHaveText('Targets: 2026-03-01 to 2026-04-15');
      // The description is rendered Markdown, and raw HTML in it is text.
      const description = page.getByTestId('initiative-description');
      await expect(description.locator('strong')).toHaveText('bold');
      await expect(description.locator('li')).toHaveText(['first point', 'second point']);
      await expect(description.locator('b')).toHaveCount(0);
      await expect(description).toContainText('<b>raw html</b>');
      // The counts cover every task whatever the table shows.
      await expect(page.getByTestId('count-open')).toHaveText('2 open');
      await expect(page.getByTestId('count-done')).toHaveText('1 done');
      await expect(page.getByTestId('count-closed')).toHaveText('1 closed');
      await expect(page.getByTestId('initiative-page').getByTestId('initiative-leases')).toHaveText('1 live lease');

      // The tasks table starts on "not closed": open and done tasks.
      await expect(page.getByTestId('task-filter')).toHaveValue('not-closed');
      await expect.poll(async () => (await shownRefs(page)).length).toBe(3); // the done task is in Needs attention, the others in the table
      const leased = taskRow(page, seeded.leased);
      await expect(leased.getByTestId('task-title')).toHaveText('Leased work');
      await expect(leased.getByTestId('task-kind')).toHaveText('work');
      await expect(leased.getByTestId('task-status')).toHaveText('open');
      await expect(leased.getByTestId('flag-active')).toHaveText('active');
      await expect(leased.getByTestId('task-holder')).toHaveText('playwright');
      await expect(taskRow(page, seeded.blocked).getByTestId('flag-blocked')).toHaveText('blocked');
      await expect(page.locator(`[data-testid=needs-row][data-ref=${seeded.done}]`).getByTestId('attention-done')).toContainText('done, not closed');
      await expect(taskRow(page, seeded.dropped)).toHaveCount(0);

      const { scrollWidth, clientWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));
      expect(scrollWidth).toBeLessThanOrEqual(clientWidth);

      // Back returns to the list, and the page survives a reload on its own address.
      await page.getByTestId('back-link').click();
      await expect(page.getByRole('heading', { level: 2, name: 'Initiatives' })).toBeVisible();
      await page.goto(`/#/i/${seeded.open}`);
      await page.reload();
      await expect(page.getByTestId('initiative-title')).toHaveText(`Open e2e-ip-${vp.name}`);
    });

    test('the task filter offers not closed, done, closed and all, and shows the tasks each keeps', async ({ page }) => {
      await page.goto(`/#/i/${seeded.open}`);
      const filter = page.getByRole('combobox', { name: 'Tasks' });
      await expect(filter.locator('option')).toHaveText(['Not closed', 'Done', 'Closed', 'All']);
      expect(await filter.locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value))).toEqual([
        'not-closed',
        'done',
        'closed',
        'all',
      ]);

      await expect.poll(async () => (await shownRefs(page)).sort()).toEqual([seeded.leased, seeded.done, seeded.blocked].sort());

      await filter.selectOption('done');
      await expect.poll(async () => await shownRefs(page)).toEqual([seeded.done]);
      await filter.selectOption('closed');
      await expect.poll(async () => await shownRefs(page)).toEqual([seeded.dropped]);
      await expect(taskRow(page, seeded.dropped).getByTestId('task-status')).toHaveText('closed');
      await filter.selectOption('all');
      await expect.poll(async () => (await shownRefs(page)).sort()).toEqual([seeded.leased, seeded.done, seeded.dropped, seeded.blocked].sort());
      await filter.selectOption('not-closed');
      await expect.poll(async () => (await shownRefs(page)).length).toBe(3);
      // The counts above the table do not follow the filter.
      await expect(page.getByTestId('count-closed')).toHaveText('1 closed');
    });

    test('the Initiatives status filter shows the open ones, then the closed ones, then all', async ({ page }) => {
      await page.goto('/#/initiatives');
      const filter = page.getByRole('combobox', { name: 'Status' });
      expect(await filter.locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value))).toEqual(['open', 'closed', 'all']);

      await filter.selectOption('open');
      await expect(row(page, seeded.open)).toHaveCount(1);
      await expect(row(page, seeded.closed)).toHaveCount(0);

      await filter.selectOption('closed');
      await expect(row(page, seeded.closed)).toHaveCount(1);
      await expect(row(page, seeded.open)).toHaveCount(0);
      await expect(row(page, seeded.closed).getByTestId('initiative-status')).toHaveText('closed');
      await expect(row(page, seeded.closed).getByTestId('initiative-counts')).toHaveText('No tasks');

      await filter.selectOption('all');
      await expect(row(page, seeded.open)).toHaveCount(1);
      await expect(row(page, seeded.closed)).toHaveCount(1);
    });

    test('Edit changes the title, description, tags and targets in one dialog', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-ed-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      await page.goto(`/#/i/${slug}`);
      await expect(page.getByTestId('tag')).toHaveText(['keep', 'old']);
      await expect(page.getByTestId('initiative-targets')).toHaveText('Targets: from 2026-03-01');

      await page.getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit initiative');
      await expect(edit).toBeVisible();
      // The form starts from the initiative as it is, and its fields are phone-sized (16 px, 44 px).
      await expect(edit.getByLabel('Title')).toHaveValue(`Fresh ${slug}`);
      await expect(edit.getByLabel('Description')).toHaveValue('Old words.');
      await expect(edit.getByLabel('Tags')).toHaveValue('keep, old');
      await expect(edit.getByLabel('Target start')).toHaveValue('2026-03-01');
      await expect(edit.getByLabel('Target end')).toHaveValue('');
      const box = await edit.getByLabel('Title').boundingBox();
      // Layout snaps to 1/64 px, so a 44 px box can measure 43.99998 (touch.spec.ts allows the same 1/32 px).
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44 - 1 / 32);
      expect(await edit.getByLabel('Title').evaluate((el) => getComputedStyle(el).fontSize)).toBe('16px');

      await edit.getByLabel('Title').fill('Renamed plan');
      await edit.getByLabel('Description').fill('New **bold** words.');
      await edit.getByLabel('Tags').fill('web, fresh');
      await edit.getByLabel('Target start').fill('2026-05-01');
      await edit.getByLabel('Target end').fill('2026-06-30');
      await edit.getByRole('button', { name: 'Save' }).click();

      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveText('Initiative saved');
      await expect(page.getByTestId('initiative-title')).toHaveText('Renamed plan');
      await expect(page.getByTestId('initiative-description').locator('strong')).toHaveText('bold');
      await expect(page.getByTestId('tag')).toHaveText(['fresh', 'web']);
      await expect(page.getByTestId('initiative-targets')).toHaveText('Targets: 2026-05-01 to 2026-06-30');

      // Emptying a target clears it.
      await page.getByRole('button', { name: 'Edit' }).click();
      await dialog(page, 'Edit initiative').getByLabel('Target end').fill('');
      await dialog(page, 'Edit initiative').getByLabel('Target start').fill('');
      await dialog(page, 'Edit initiative').getByRole('button', { name: 'Save' }).click();
      await expect(page.getByTestId('initiative-targets')).toHaveCount(0);
      const saved = (await (await api.get(`/api/v1/initiatives/${slug}`)).json()) as Record<string, unknown>;
      expect(saved).toMatchObject({ title: 'Renamed plan', description: 'New **bold** words.', tags: ['fresh', 'web'] });
      expect(saved.target_start).toBeUndefined();
      expect(saved.target_end).toBeUndefined();
      await api.dispose();
    });

    test('a refused edit keeps the input and shows the refusal inside the dialog', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-rf-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      await page.goto(`/#/i/${slug}`);
      await page.getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit initiative');
      await edit.getByLabel('Title').fill('Typed title');
      await edit.getByLabel('Tags').fill('typed');
      await edit.getByLabel('Target start').fill('2026-09-01');
      await edit.getByLabel('Target end').fill('2026-08-01');
      await edit.getByRole('button', { name: 'Save' }).click();

      const refusal = edit.getByRole('alert');
      await expect(refusal).toBeVisible();
      await expect(refusal).toContainText('target_end');
      await expect(edit.getByLabel('Title')).toHaveValue('Typed title');
      await expect(edit.getByLabel('Tags')).toHaveValue('typed');
      await expect(edit.getByLabel('Target end')).toHaveValue('2026-08-01');
      // The page behind it is unchanged, and nothing was saved.
      await expect(page.getByTestId('initiative-title')).toHaveText(`Fresh ${slug}`);
      await expect(page.getByTestId('toast')).toHaveCount(0);

      // Correcting the dates lets the same dialog save.
      await edit.getByLabel('Target end').fill('2026-10-01');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('initiative-title')).toHaveText('Typed title');
      await expect(page.getByTestId('initiative-targets')).toHaveText('Targets: 2026-09-01 to 2026-10-01');

      // A blank title is refused as well; Cancel leaves the page as it was.
      await page.getByRole('button', { name: 'Edit' }).click();
      await dialog(page, 'Edit initiative').getByLabel('Title').fill('');
      await dialog(page, 'Edit initiative').getByRole('button', { name: 'Save' }).click();
      await expect(dialog(page, 'Edit initiative').getByRole('alert')).toBeVisible();
      await dialog(page, 'Edit initiative').getByRole('button', { name: 'Cancel' }).click();
      await expect(dialog(page, 'Edit initiative')).toHaveCount(0);
      await expect(page.getByTestId('initiative-title')).toHaveText('Typed title');
      await api.dispose();
    });

    test('Close with no tasks asks for confirmation, then closes; Cancel closes nothing', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-c0-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      await page.goto(`/#/i/${slug}`);
      await expect(page.getByTestId('initiative-status')).toHaveText('open');
      await expect(page.getByRole('button', { name: 'Reopen' })).toHaveCount(0);

      await page.getByRole('button', { name: 'Close', exact: true }).click();
      const confirm = dialog(page, 'Close initiative');
      await expect(confirm.getByTestId('dialog-body')).toHaveText('Closing stops new tasks and edits to its tasks until it is reopened.');
      await expect(confirm.getByRole('button', { name: 'Close initiative' })).toBeVisible();
      await confirm.getByRole('button', { name: 'Cancel' }).click();
      await expect(confirm).toHaveCount(0);
      await expect(page.getByTestId('initiative-status')).toHaveText('open');

      await page.getByRole('button', { name: 'Close', exact: true }).click();
      await dialog(page, 'Close initiative').getByRole('button', { name: 'Close initiative' }).click();
      await expect(dialog(page, 'Close initiative')).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveText(`Initiative ${slug} closed`);
      await expect(page.getByTestId('initiative-status')).toHaveText('closed');
      await expect(page.getByRole('button', { name: 'Close', exact: true })).toHaveCount(0);
      await expect(page.getByRole('button', { name: 'Reopen' })).toBeVisible();
      await api.dispose();
    });

    test('Close with an open task says how many and drops them through the force path', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-cf-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      const [open, done, gone] = await fresh(api, slug, 3);
      await post(api, `/api/v1/tasks/${done}/done`, {}, 200);
      await post(api, `/api/v1/tasks/${gone}/close`, { resolution: 'dropped' }, 200);
      await page.goto(`/#/i/${slug}`);
      await expect(page.getByTestId('count-open')).toHaveText('1 open');

      await page.getByRole('button', { name: 'Close', exact: true }).click();
      const confirm = dialog(page, 'Close initiative');
      // The open and the done task are not closed; the dropped one is not counted.
      await expect(confirm.getByTestId('dialog-body')).toHaveText('2 tasks not closed. Closing drops them too: each is closed as dropped.');
      await expect(confirm.getByRole('button', { name: 'Close initiative', exact: true })).toHaveCount(0);
      await confirm.getByRole('button', { name: 'Close and drop 2 tasks' }).click();

      await expect(confirm).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveText(`Initiative ${slug} closed, 2 tasks dropped`);
      await expect(page.getByTestId('initiative-status')).toHaveText('closed');
      await expect(page.getByTestId('count-open')).toHaveText('0 open');
      await expect(page.getByTestId('count-done')).toHaveText('0 done');
      await expect(page.getByTestId('count-closed')).toHaveText('3 closed');
      await expect(page.getByTestId('task-row')).toHaveCount(0);
      await page.getByRole('combobox', { name: 'Tasks' }).selectOption('closed');
      await expect(page.getByTestId('task-row')).toHaveCount(3);
      await expect(taskRow(page, open).getByTestId('task-status')).toHaveText('closed');
      const task = (await (await api.get(`/api/v1/tasks/${open}`)).json()) as { resolution: string };
      expect(task.resolution).toBe('dropped');
      await api.dispose();
    });

    test('Reopen asks for confirmation and opens a closed initiative again, its tasks staying closed', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-ro-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      const [task] = await fresh(api, slug, 1);
      await post(api, `/api/v1/initiatives/${slug}/close`, { force: true }, 200);
      await page.goto(`/#/i/${slug}`);
      await expect(page.getByTestId('initiative-status')).toHaveText('closed');
      await expect(page.getByTestId('count-closed')).toHaveText('1 closed');

      await page.getByRole('button', { name: 'Reopen' }).click();
      const confirm = dialog(page, 'Reopen initiative');
      await expect(confirm.getByTestId('dialog-body')).toContainText('closed tasks stay closed');
      await confirm.getByRole('button', { name: 'Cancel' }).click();
      await expect(page.getByTestId('initiative-status')).toHaveText('closed');

      await page.getByRole('button', { name: 'Reopen' }).click();
      await dialog(page, 'Reopen initiative').getByRole('button', { name: 'Reopen initiative' }).click();
      await expect(dialog(page, 'Reopen initiative')).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveText(`Initiative ${slug} reopened`);
      await expect(page.getByTestId('initiative-status')).toHaveText('open');
      await expect(page.getByRole('button', { name: 'Close', exact: true })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Reopen' })).toHaveCount(0);
      await expect(page.getByTestId('count-closed')).toHaveText('1 closed');
      await page.getByRole('combobox', { name: 'Tasks' }).selectOption('closed');
      await expect(taskRow(page, task).getByTestId('task-status')).toHaveText('closed');
      await api.dispose();
    });

    test('a close the server refuses shows its refusal in the dialog and keeps it open', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-st-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      await fresh(api, slug);
      await page.goto(`/#/i/${slug}`);
      await page.getByRole('button', { name: 'Close', exact: true }).click();
      // Someone else closes it while the dialog is open: the second close is a 409.
      await post(api, `/api/v1/initiatives/${slug}/close`, {}, 200);
      await dialog(page, 'Close initiative').getByRole('button', { name: 'Close initiative' }).click();
      await expect(dialog(page, 'Close initiative').getByRole('alert')).toContainText('already closed');
      await expect(dialog(page, 'Close initiative')).toBeVisible();
      await api.dispose();
    });

    test('the open asks of the initiative and its tasks are listed, and answering one from the page removes it', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-ia-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      const [task] = await fresh(api, slug, 1);
      const onTask = (await post(api, '/api/v1/asks', { task, question: 'Which database?', details: 'Pick **one**.', options: ['sqlite', 'postgres'], blocking: true }, 201)).ref;
      const onInitiative = (await post(api, '/api/v1/asks', { initiative: slug, question: 'Ship on Friday?' }, 201)).ref;
      const settled = (await post(api, '/api/v1/asks', { initiative: slug, question: 'Already settled?' }, 201)).ref;
      await post(api, `/api/v1/asks/${settled}/answer`, { answer: 'yes' }, 200);
      await post(api, '/api/v1/asks', { question: 'A board-wide question?' }, 201);
      await page.goto(`/#/i/${slug}`);

      const asks = page.getByTestId('initiative-asks').getByTestId('ask');
      await expect(asks).toHaveCount(2);
      const one = page.locator(`[data-testid=ask][data-ref=${onTask}]`);
      await expect(one.getByTestId('ask-question')).toHaveText('Which database?');
      await expect(one.getByTestId('ask-asker')).toHaveText('playwright');
      await expect(one.getByTestId('ask-blocking')).toHaveText('blocks its task');
      await expect(one.getByTestId('ask-details').locator('strong')).toHaveText('one');
      await expect(one.getByTestId('ask-task')).toHaveText(task);
      await expect(one.getByTestId('ask-task')).toHaveAttribute('href', `#/t/${task}`);
      const other = page.locator(`[data-testid=ask][data-ref=${onInitiative}]`);
      await expect(other.getByTestId('ask-question')).toHaveText('Ship on Friday?');
      await expect(other.getByTestId('ask-task')).toHaveCount(0);
      await expect(page.locator(`[data-testid=ask][data-ref=${settled}]`)).toHaveCount(0);
      await expect(listedRow(page, task).getByTestId('flag-blocked')).toHaveText('blocked');

      await one.getByRole('button', { name: `Answer ${onTask}` }).click();
      const answer = dialog(page, 'Answer ask');
      await answer.getByRole('button', { name: 'postgres' }).click();
      await answer.getByRole('button', { name: 'Send answer' }).click();
      await expect(answer).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Answered ${onTask}`);
      await expect(asks).toHaveCount(1);
      await expect(listedRow(page, task).getByTestId('flag-blocked')).toHaveCount(0);
      await expect(listedRow(page, task)).toHaveCount(1);

      await other.getByRole('button', { name: `Withdraw ${onInitiative}` }).click();
      const withdraw = dialog(page, 'Withdraw ask');
      await withdraw.getByRole('button', { name: 'Withdraw' }).click();
      await expect(withdraw).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Withdrew ${onInitiative}`);
      await expect(page.getByTestId('no-asks')).toHaveText('No open asks.');
      await api.dispose();
    });

    test('Post note puts a note on the initiative newest first, beside the notes on its tasks, and a body expands', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-in-${vp.name}`;
      const api = await apiContext(playwright, baseURL);
      const [task] = await fresh(api, slug, 1);
      await post(api, '/api/v1/notes', { task, summary: 'Note on the task' }, 201);
      await post(api, '/api/v1/notes', { summary: 'A broadcast' }, 201);
      await page.goto(`/#/i/${slug}`);
      const notes = page.getByTestId('initiative-notes').getByTestId('note');
      await expect(notes).toHaveCount(1);
      await expect(notes.first().getByTestId('note-task')).toHaveText(task);

      await page.getByRole('button', { name: 'Post note' }).click();
      const form = dialog(page, 'Post note');
      await expect(form).toBeVisible();
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form.getByTestId('dialog-error')).toHaveText('Write a summary.');
      await form.getByLabel('Summary').fill('Phase one is done');
      await form.getByLabel('Body (optional, Markdown)').fill('All **green**.');
      await form.getByLabel('Resource (optional)').fill('host:build-2');
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Note posted on ${slug}`);

      await expect(notes).toHaveCount(2);
      await expect(notes.first().getByTestId('note-summary')).toHaveText('Phase one is done');
      await expect(notes.nth(1).getByTestId('note-summary')).toHaveText('Note on the task');
      await expect(notes.first().getByTestId('note-author')).toHaveText('operator');
      await expect(notes.first().getByTestId('note-resource')).toHaveText('host:build-2');
      await expect(notes.first().getByTestId('note-task')).toHaveCount(0);
      const stored = await (await api.get(`/api/v1/notes?initiative=${slug}`)).json();
      expect(stored.items.map((n: { summary: string }) => n.summary)).toEqual(['Note on the task', 'Phase one is done']);

      const toggle = notes.first().getByRole('button', { name: /body of/ });
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');
      await toggle.click();
      await expect(notes.first().getByTestId('note-body').locator('strong')).toHaveText('green');
      await toggle.click();
      await expect(notes.first().getByTestId('note-body')).toBeHidden();
      await api.dispose();
    });
  });
}
