import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';
import { expectInitiatives, pickInitiative } from './chips';
import { logicViewports } from './viewports';

// Signs in through the mock provider, so no API key rides along (see login.spec.ts).
// The Tasks view `#/tasks` (filters held in the fragment's query) and the New task dialog, opened from the Tasks
// view and from an initiative page with the initiative preset, at the desktop size (the logic viewport). The database is
// shared by the specs, so every test works on initiatives of its own and filters the view down to them.
const test = base;


const taskRow = (page: Page, ref: string) => page.locator(`[data-testid=task-row][data-ref=${ref}]`);
// Every task the view lists: the ones in Needs attention above the table, then the table's own (a task is in one of them).
const shownRefs = async (page: Page) => [...(await page.getByTestId('needs-ref').allTextContents()), ...(await page.getByTestId('task-ref').allTextContents())];
const dialog = (page: Page) => page.getByRole('dialog', { name: 'New task' });

async function post(api: APIRequestContext, path: string, data: object, status: number) {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as { ref: string };
}

async function apiContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
}

async function initiative(api: APIRequestContext, slug: string) {
  await post(api, '/api/v1/initiatives', { slug, title: `Tasks ${slug}` }, 201);
}

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('a work task and a watch task are created from the New task dialog', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tk-new-${vp.name}`;
      await initiative(api, slug);
      const blocker = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Blocker work' }, 201)).ref;

      await page.goto(`/#/tasks?initiative=${slug}`);
      await expect(taskRow(page, blocker)).toBeVisible();

      // The work task: every field, a blocker picked from the open tasks, a link added.
      await page.getByRole('button', { name: 'New task' }).click();
      await expect(dialog(page)).toBeVisible();
      await expect(dialog(page).getByLabel('Cadence')).toHaveCount(0); // a work task has no cadence
      await dialog(page).getByLabel('Initiative').selectOption(slug);
      await dialog(page).getByLabel('Title').fill('Write the plan');
      await dialog(page).getByLabel('Description').fill('Plan **it**.');
      await dialog(page).getByLabel('Planned start').fill('2026-03-01');
      await dialog(page).getByLabel('Planned end').fill('2026-03-09');
      await dialog(page).getByLabel('Find an open task').fill(blocker);
      await dialog(page).getByTestId('blocker-options').getByRole('button', { name: new RegExp(`${blocker} Blocker work`) }).click();
      await expect(dialog(page).getByTestId('blockers')).toContainText(blocker);
      await dialog(page).getByLabel('Link label').fill('Plan PR');
      await dialog(page).getByLabel('Link URL').fill('https://example.com/pr/1');
      await dialog(page).getByRole('button', { name: 'Add link' }).click();
      await expect(dialog(page).getByTestId('links')).toContainText('Plan PR');
      await dialog(page).getByRole('button', { name: 'Create task' }).click();
      await expect(dialog(page)).toHaveCount(0);
      const workRef = `${slug}-2`;
      await expect(page.getByText(`Task ${workRef} created`)).toBeVisible();
      const work = taskRow(page, workRef);
      await expect(work.getByTestId('task-title')).toHaveText('Write the plan');
      await expect(work.getByTestId('task-kind')).toHaveText('work');
      await expect(work.getByTestId('flag-blocked')).toHaveText('blocked');
      const made = await (await api.get(`/api/v1/tasks/${workRef}`)).json();
      expect(made).toMatchObject({
        description: 'Plan **it**.',
        planned_start: '2026-03-01T00:00:00Z',
        planned_end: '2026-03-09T00:00:00Z',
        external_refs: [{ label: 'Plan PR', url: 'https://example.com/pr/1' }],
      });
      expect(made.deps.map((d: { ref: string }) => d.ref)).toEqual([blocker]);

      // The watch task: the cadence field appears for a watch, a bad cadence is refused inline and keeps the input.
      await page.getByRole('button', { name: 'New task' }).click();
      await dialog(page).getByLabel('Initiative').selectOption(slug);
      await dialog(page).getByLabel('Title').fill('Check the build');
      await dialog(page).getByLabel('Kind').selectOption('watch');
      await dialog(page).getByLabel('Cadence').fill('often');
      await dialog(page).getByRole('button', { name: 'Create task' }).click();
      await expect(dialog(page).getByTestId('dialog-error')).toContainText('Cadence is a number and a unit');
      await expect(dialog(page).getByLabel('Title')).toHaveValue('Check the build');
      await dialog(page).getByLabel('Cadence').fill('15m');
      await dialog(page).getByRole('button', { name: 'Create task' }).click();
      await expect(dialog(page)).toHaveCount(0);
      const watchRef = `${slug}-3`;
      await expect(taskRow(page, watchRef).getByTestId('task-kind')).toHaveText('watch');
      const watch = await (await api.get(`/api/v1/tasks/${watchRef}`)).json();
      expect(watch).toMatchObject({ kind: 'watch', cadence_seconds: 900 });
      await api.dispose();
    });

    test('a refusal from the server shows inline and keeps what was typed', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tk-refuse-${vp.name}`;
      await initiative(api, slug);
      await page.goto(`/#/tasks?initiative=${slug}`);
      await page.getByRole('button', { name: 'New task' }).click();
      await dialog(page).getByLabel('Initiative').selectOption(slug);
      await dialog(page).getByLabel('Description').fill('kept words');
      await dialog(page).getByRole('button', { name: 'Create task' }).click(); // no title
      await expect(dialog(page).getByTestId('dialog-error')).toBeVisible();
      await expect(dialog(page).getByLabel('Description')).toHaveValue('kept words');
      await dialog(page).getByRole('button', { name: 'Cancel' }).click();
      await expect(dialog(page)).toHaveCount(0);
      await api.dispose();
    });

    test('the filters are held in the URL fragment and survive a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tk-filter-${vp.name}`;
      await initiative(api, slug);
      const task = (title: string, extra: object = {}) => post(api, '/api/v1/tasks', { initiative: slug, title, ...extra }, 201);
      const plain = await task('Plain needle work');
      const leased = await task('Leased work');
      const blocked = await task('Blocked work', { blocked_by: [leased.ref] });
      const watch = await task('Watching needle', { kind: 'watch', cadence_seconds: 3600 });
      const done = await task('Finished work');
      await post(api, '/api/v1/leases', { task: leased.ref, intent: 'busy' }, 201);
      await post(api, `/api/v1/tasks/${done.ref}/done`, {}, 200);

      await page.goto('/#/tasks');
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await expect(page.getByTestId('status-filter')).toHaveValue('not-closed');

      await pickInitiative(page, slug);
      await expect.poll(async () => (await shownRefs(page)).sort()).toEqual([plain.ref, leased.ref, blocked.ref, watch.ref, done.ref].sort());
      await expect(page).toHaveURL(new RegExp(`#/tasks\\?initiative=${slug}$`));

      await page.getByTestId('kind-filter').selectOption('watch');
      await expect.poll(() => shownRefs(page)).toEqual([watch.ref]);
      await page.getByTestId('kind-filter').selectOption('');
      await page.getByTestId('active-filter').selectOption('yes');
      await expect.poll(() => shownRefs(page)).toEqual([leased.ref]);
      await expect(taskRow(page, leased.ref).getByTestId('task-holder')).toHaveText('playwright');
      await page.getByTestId('active-filter').selectOption('');
      await page.getByTestId('blocked-filter').selectOption('yes');
      await expect.poll(() => shownRefs(page)).toEqual([blocked.ref]);
      await page.getByTestId('blocked-filter').selectOption('');
      await page.getByTestId('overdue-filter').selectOption('yes');
      await expect(page.getByTestId('no-tasks')).toHaveText('No tasks match these filters.');
      await page.getByTestId('overdue-filter').selectOption('');
      await page.getByTestId('status-filter').selectOption('done');
      await expect.poll(() => shownRefs(page)).toEqual([done.ref]);
      await page.getByTestId('status-filter').selectOption('not-closed');

      await page.getByTestId('text-filter').fill('NEEDLE');
      await page.getByTestId('text-filter').press('Enter');
      await expect.poll(async () => (await shownRefs(page)).sort()).toEqual([plain.ref, watch.ref].sort());
      await page.getByTestId('blocked-filter').selectOption('no');
      await page.getByTestId('kind-filter').selectOption('work');
      await expect.poll(() => shownRefs(page)).toEqual([plain.ref]);

      // The same filters after a reload: in the address, in the controls and in the rows.
      await expect(page).toHaveURL(new RegExp(`#/tasks\\?kind=work&initiative=${slug}&blocked=false&q=NEEDLE$`));
      await page.reload();
      await expectInitiatives(page, [slug]);
      await expect(page.getByTestId('kind-filter')).toHaveValue('work');
      await expect(page.getByTestId('blocked-filter')).toHaveValue('no');
      await expect(page.getByTestId('text-filter')).toHaveValue('NEEDLE');
      await expect(page.getByTestId('status-filter')).toHaveValue('not-closed');
      await expect.poll(() => shownRefs(page)).toEqual([plain.ref]);
      await expect(page.getByRole('link', { name: 'Tasks', exact: true })).toHaveAttribute('aria-current', 'page');

      // Clear filters returns to the bare address.
      await page.getByTestId('clear-filters').click();
      await expect(page).toHaveURL(/#\/tasks$/);
      await expect(page.getByTestId('clear-filters')).toHaveCount(0);
      await api.dispose();
    });

    test('New task on the initiative page opens the dialog with that initiative preset', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tk-page-${vp.name}`;
      await initiative(api, slug);
      await initiative(api, `${slug}-other`);
      await page.goto(`/#/i/${slug}`);
      await expect(page.getByTestId('initiative-title')).toBeVisible();
      await page.getByTestId('initiative-actions').getByRole('button', { name: 'New task' }).click();
      await expect(dialog(page)).toBeVisible();
      await expect(dialog(page).getByLabel('Initiative')).toHaveValue(slug);
      await expect(dialog(page).getByLabel('Title')).toBeFocused();
      await dialog(page).getByLabel('Title').fill('From the page');
      await dialog(page).getByRole('button', { name: 'Create task' }).click();
      await expect(dialog(page)).toHaveCount(0);
      await expect(page.getByText(`Task ${slug}-1 created`)).toBeVisible();
      await expect(taskRow(page, `${slug}-1`).getByTestId('task-title')).toHaveText('From the page');
      await expect(page.getByTestId('count-open')).toHaveText('1 open');
      await api.dispose();
    });
  });
}
