import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';

// The task page `#/t/<ref>` at the phone and the desktop size: what it shows (header, description, planned window,
// links, dependencies, live leases, the event timeline) and the Edit, Done, Reopen and Close actions, each leaving its
// event in the timeline; then the dependency, Milestone, Record check, End lease and Acknowledge actions. Every test
// works on an initiative of its own.
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

const RESOLUTIONS = ['completed', 'dropped', 'superseded', 'duplicate'] as const;

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

// An open initiative with one task (and any extra fields); returns its ref.
async function seedTask(api: APIRequestContext, slug: string, extra: object = {}) {
  await post(api, '/api/v1/initiatives', { slug, title: `Page ${slug}` }, 201);
  return (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Write the plan', ...extra }, 201)).ref;
}

// Runs a lease out the way the Go scenarios do: its expiry is moved into the past in the e2e store, then an API read
// expires the leases that are due (the page's own read would race the timeline), so nothing sleeps.
// The read is filtered to the task: the shared store holds hundreds of expired leases and a list is paged at 100.
async function expireLease(api: APIRequestContext, ref: string, task: string) {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db.prepare('UPDATE leases SET expires_at = ? WHERE n = ?').run(Date.now() - 30_000, Number(ref.slice(1)));
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
  const listed = await api.get(`/api/v1/leases?state=expired&task=${task}`);
  expect(listed.status()).toBe(200);
  expect(JSON.stringify(await listed.json())).toContain(`"${ref}"`);
}

const events = (page: Page) => page.getByTestId('event');
const eventTypes = (page: Page) => events(page).evaluateAll((els) => els.map((el) => el.getAttribute('data-type')));
const dialog = (page: Page, name: string) => page.getByRole('dialog', { name });
const actions = (page: Page) => page.getByTestId('task-actions');

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the page shows the header, description, planned window, links, dependencies, live lease and timeline', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tp-show-${vp.name}`;
      const blocker = await seedTask(api, slug, {
        description: 'Plan with **bold** words and <b>raw html</b>.',
        planned_start: '2026-03-01T00:00:00Z',
        planned_end: '2026-04-15T00:00:00Z',
        external_refs: [{ label: 'Plan PR', url: 'https://example.com/pr/1' }],
      });
      const blocked = (await post(api, '/api/v1/tasks', { initiative: slug, title: 'Needs the plan', blocked_by: [blocker] }, 201)).ref;
      await post(api, '/api/v1/leases', { task: blocker, intent: 'drafting the plan', resources: ['docs/plan.md', 'docs/notes.md'] }, 201);

      // From the Tasks view the ref is a link to the page.
      await page.goto(`/#/tasks?initiative=${slug}`);
      await page.locator(`[data-testid=task-row][data-ref=${blocker}]`).getByRole('link', { name: blocker }).click();
      await expect(page).toHaveURL(new RegExp(`#/t/${blocker}$`));

      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(blocker);
      await expect(page.getByRole('heading', { level: 2, name: 'Write the plan' })).toBeVisible();
      await expect(page.getByTestId('task-status')).toHaveText('open');
      await expect(page.getByTestId('task-kind')).toHaveText('work');
      await expect(page.getByTestId('flag-active')).toHaveText('active');
      await expect(page.getByTestId('flag-blocked')).toHaveCount(0);
      await expect(page.getByTestId('task-initiative')).toHaveText(slug);
      const description = page.getByTestId('task-description');
      await expect(description.locator('strong')).toHaveText('bold');
      await expect(description.locator('b')).toHaveCount(0);
      await expect(description).toContainText('<b>raw html</b>');
      await expect(page.getByTestId('task-planned')).toHaveText('2026-03-01 to 2026-04-15');
      const link = page.getByTestId('task-links').getByRole('link', { name: 'Plan PR' });
      await expect(link).toHaveAttribute('href', 'https://example.com/pr/1');
      await expect(link).toHaveAttribute('rel', 'noopener noreferrer');

      // Blocking is linked and opens that task's page, where the dependency is shown the other way.
      await expect(page.getByTestId('no-deps')).toBeVisible();
      const blocking = page.getByTestId('task-blocking').locator(`[data-ref=${blocked}]`);
      await expect(blocking).toContainText('Needs the plan');
      await expect(blocking.getByRole('link')).toHaveAttribute('href', `#/t/${blocked}`);

      const lease = page.getByTestId('lease');
      await expect(lease).toHaveCount(1);
      await expect(lease.getByTestId('lease-holder')).toHaveText('playwright');
      await expect(lease.getByTestId('lease-intent')).toHaveText('drafting the plan');
      await expect(lease.getByTestId('lease-resources')).toHaveText('docs/notes.md, docs/plan.md');
      await expect(lease.getByTestId('lease-expires')).toHaveText(/^expires in (1h 59m|2h)$/);

      // The timeline: newest first, with relative time, summary and actor.
      await expect(eventTypes(page)).resolves.toEqual(['lease_started', 'task_created']);
      await expect(events(page).first().getByTestId('event-when')).toHaveText('just now');
      await expect(events(page).first().getByTestId('event-actor')).toHaveText('playwright');
      await expect(events(page).last().getByTestId('event-summary')).toHaveText(`task ${blocker} created`);

      await blocking.getByRole('link').click();
      await expect(page).toHaveURL(new RegExp(`#/t/${blocked}$`));
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(blocked);
      await expect(page.getByTestId('flag-blocked')).toHaveText('blocked');
      const dep = page.getByTestId('task-deps').locator(`[data-ref=${blocker}]`);
      await expect(dep).toContainText('Write the plan');
      await expect(page.getByTestId('no-blocking')).toBeVisible();
      await expect(page.getByTestId('no-leases')).toBeVisible();

      const { scrollWidth, clientWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));
      expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
      await api.dispose();
    });

    test('a task that does not exist says so, and the page survives a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-miss-${vp.name}`);
      await page.goto('/#/t/zz-99');
      await expect(page.getByRole('alert')).toBeVisible();
      await expect(page.getByTestId('task-title')).toHaveCount(0);
      await page.goto(`/#/t/${ref}`);
      await page.reload();
      await expect(page.getByTestId('task-title')).toHaveText('Write the plan');
      await api.dispose();
    });

    test('the main actions sit in a bar above the tab bar on the phone and in the header on desktop', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-bar-${vp.name}`);
      await page.goto(`/#/t/${ref}`);
      await expect(actions(page).getByRole('button')).toHaveText(['Edit', 'Done', 'Close']);
      const bar = (await actions(page).boundingBox())!;
      const viewport = page.viewportSize()!;
      if (vp.name === 'phone') {
        const tabbar = (await page.getByTestId('tabbar').boundingBox())!;
        expect(bar.y + bar.height).toBeLessThanOrEqual(tabbar.y + 1);
        expect(viewport.height - bar.y - bar.height).toBeLessThan(viewport.height / 4);
        for (const button of await actions(page).getByRole('button').all()) expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      } else {
        const header = (await page.getByTestId('task-title').boundingBox())!;
        expect(bar.y).toBeGreaterThan(header.y);
        expect(bar.y).toBeLessThan(viewport.height / 2);
      }
      await api.dispose();
    });

    test('Edit changes the title, description and planned window, and a refused edit keeps what was typed', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-edit-${vp.name}`, { description: 'Old words.', planned_start: '2026-03-01T00:00:00Z' });
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-planned')).toHaveText('from 2026-03-01');

      await actions(page).getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit task');
      await expect(edit).toBeVisible();
      await expect(edit.getByLabel('Title')).toHaveValue('Write the plan');
      await expect(edit.getByLabel('Description')).toHaveValue('Old words.');
      await expect(edit.getByLabel('Planned start')).toHaveValue('2026-03-01');
      await expect(edit.getByLabel('Cadence')).toHaveCount(0); // a work task has no cadence

      // A refusal (an empty title) shows inline, keeps the dialog open and the other input.
      await edit.getByLabel('Title').fill('');
      await edit.getByLabel('Description').fill('Kept words.');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit.getByTestId('dialog-error')).toBeVisible();
      await expect(edit.getByLabel('Description')).toHaveValue('Kept words.');

      await edit.getByLabel('Title').fill('Write the final plan');
      await edit.getByLabel('Planned end').fill('2026-03-09');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Task ${ref} saved`);
      await expect(page.getByTestId('task-title')).toHaveText('Write the final plan');
      await expect(page.getByTestId('task-description')).toHaveText('Kept words.');
      await expect(page.getByTestId('task-planned')).toHaveText('2026-03-01 to 2026-03-09');
      await expect(eventTypes(page)).resolves.toEqual(['task_updated', 'task_created']);
      await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} updated: title, description, planned_end`);
      await expect(page.getByRole('button', { name: 'Edit', exact: true })).toBeFocused();
      await api.dispose();
    });

    test('Edit on a watch task changes its cadence, and a cadence that is not a number and a unit is refused', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-watch-${vp.name}`, { kind: 'watch', cadence_seconds: 900 });
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-kind')).toHaveText('watch, every 15 minutes');

      await actions(page).getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit task');
      await expect(edit.getByLabel('Cadence')).toHaveValue('15m');
      await edit.getByLabel('Cadence').fill('soon');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit.getByTestId('dialog-error')).toContainText('15m');
      await edit.getByLabel('Cadence').fill('2h');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('task-kind')).toHaveText('watch, every 2 hours');
      await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} updated: cadence_seconds`);
      await api.dispose();
    });

    test('Edit adds, replaces and removes links, a refused link keeps what was typed, and the saved links and event show', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const pr = { label: 'PR 12', url: 'https://example.com/pr/12' };
      const ci = { label: 'CI run', url: 'https://example.com/ci/9' };
      const ref = await seedTask(api, `e2e-tp-links-${vp.name}`, { external_refs: [pr, ci] });
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-links').getByRole('link')).toHaveText(['PR 12', 'CI run']);

      await actions(page).getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit task');
      await expect(edit.getByLabel('Label of link 1')).toHaveValue('PR 12');
      await expect(edit.getByLabel('URL of link 2')).toHaveValue(ci.url);

      // A URL the API refuses shows inline, keeps the dialog open and every typed value.
      await edit.getByLabel('URL of link 1').fill('ftp://example.com/pr/12');
      await edit.getByLabel('New link label').fill('Docs');
      await edit.getByLabel('New link URL').fill('https://example.com/docs');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit.getByTestId('dialog-error')).toBeVisible();
      await expect(edit.getByLabel('URL of link 1')).toHaveValue('ftp://example.com/pr/12');
      await expect(edit.getByLabel('New link label')).toHaveValue('Docs');
      await expect(page.getByTestId('task-links').getByRole('link')).toHaveText(['PR 12', 'CI run']); // nothing was saved

      // Replace the first link, remove the second, and the typed Docs link is added by Save.
      await edit.getByLabel('Label of link 1').fill('PR 13');
      await edit.getByLabel('URL of link 1').fill('https://example.com/pr/13');
      await edit.getByRole('button', { name: 'Remove link 2' }).click();
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Task ${ref} saved`);
      const links = page.getByTestId('task-links').getByRole('link');
      await expect(links).toHaveText(['PR 13', 'Docs']);
      await expect(links.first()).toHaveAttribute('href', 'https://example.com/pr/13');
      await expect(links.last()).toHaveAttribute('href', 'https://example.com/docs');
      await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} updated: external_refs`);
      await expect(eventTypes(page)).resolves.toEqual(['task_updated', 'task_created']);
      const stored = await (await api.get(`/api/v1/tasks/${ref}`)).json();
      expect(stored.external_refs).toEqual([{ label: 'PR 13', url: 'https://example.com/pr/13' }, { label: 'Docs', url: 'https://example.com/docs' }]);

      // Removing every link leaves none; a link typed with only a label is refused before anything is sent.
      await actions(page).getByRole('button', { name: 'Edit' }).click();
      await edit.getByRole('button', { name: 'Remove link 1' }).click();
      await edit.getByRole('button', { name: 'Remove link 1' }).click();
      await edit.getByLabel('New link label').fill('Half');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit.getByTestId('dialog-error')).toContainText('label and a URL');
      await edit.getByLabel('New link label').fill('');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('no-links')).toBeVisible();
      await expect(eventTypes(page)).resolves.toEqual(['task_updated', 'task_updated', 'task_created']);
      await api.dispose();
    });

    test('Edit sets, moves and clears the next check of a watch task, and the page shows the new check time and event', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-nxt-${vp.name}`, { kind: 'watch', cadence_seconds: 900, next_check_at: '2099-01-02T03:04:05Z' });
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-next-check').locator('time')).toHaveAttribute('datetime', '2099-01-02T03:04:05Z');

      await actions(page).getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit task');
      await expect(edit.getByLabel('Next check (UTC)')).toHaveValue('2099-01-02T03:04');
      await edit.getByLabel('Next check (UTC)').fill('2099-02-03T10:20');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('task-next-check').locator('time')).toHaveAttribute('datetime', '2099-02-03T10:20:00Z');
      await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} updated: next_check_at`);
      expect((await (await api.get(`/api/v1/tasks/${ref}`)).json()).next_check_at).toBe('2099-02-03T10:20:00Z');

      // A check time in the past makes the watch task overdue.
      await actions(page).getByRole('button', { name: 'Edit' }).click();
      await edit.getByLabel('Next check (UTC)').fill('2020-01-01T00:00');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('task-next-check')).toContainText('overdue');
      await expect(page.getByTestId('flag-overdue')).toBeVisible();

      // Clearing the field clears the check time.
      await actions(page).getByRole('button', { name: 'Edit' }).click();
      await edit.getByLabel('Next check (UTC)').fill('');
      await edit.getByRole('button', { name: 'Save' }).click();
      await expect(edit).toHaveCount(0);
      await expect(page.getByTestId('task-next-check')).toHaveCount(0);
      await expect(eventTypes(page)).resolves.toEqual(['task_updated', 'task_updated', 'task_updated', 'task_created']);
      await api.dispose();
    });

    test('Edit on a work task has no next check field', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-nocheck-${vp.name}`);
      await page.goto(`/#/t/${ref}`);
      await actions(page).getByRole('button', { name: 'Edit' }).click();
      const edit = dialog(page, 'Edit task');
      await expect(edit.getByLabel('Title')).toBeVisible();
      await expect(edit.getByLabel('Next check (UTC)')).toHaveCount(0);
      await expect(edit.getByRole('button', { name: 'Add link' })).toBeVisible();
      await api.dispose();
    });

    test('Done and Reopen move the task and leave their events, and a refused Done shows inline', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-done-${vp.name}`);
      await page.goto(`/#/t/${ref}`);

      await actions(page).getByRole('button', { name: 'Done' }).click();
      await expect(page.getByTestId('toast')).toContainText(`Task ${ref} marked done`);
      await expect(page.getByTestId('task-status')).toHaveText('done');
      await expect(actions(page).getByRole('button')).toHaveText(['Edit', 'Reopen', 'Close']);
      await expect(eventTypes(page)).resolves.toEqual(['task_done', 'task_created']);
      await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} marked done`);

      await actions(page).getByRole('button', { name: 'Reopen' }).click();
      await expect(page.getByTestId('toast')).toContainText(`Task ${ref} reopened`);
      await expect(page.getByTestId('task-status')).toHaveText('open');
      await expect(eventTypes(page)).resolves.toEqual(['task_reopened', 'task_done', 'task_created']);
      await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} reopened`);

      // Someone else finishes the task while this page is open: the stale Done is refused, inline, and the page is unchanged.
      await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
      await actions(page).getByRole('button', { name: 'Done' }).click();
      await expect(page.getByTestId('action-error')).toBeVisible();
      await expect(page.getByTestId('action-error')).toContainText('done');
      await expect(page.getByTestId('task-status')).toHaveText('open');
      await api.dispose();
    });

    test('Add dependency picks an open task and Remove drops it, with blocked changing and both events in the timeline', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const slug = `e2e-tp-dep-${vp.name}`;
      const task = await seedTask(api, slug);
      const blocker = (await post(api, '/api/v1/tasks', { initiative: slug, title: `Review the plan ${vp.name}` }, 201)).ref;
      await page.goto(`/#/t/${task}`);
      await expect(page.getByTestId('no-deps')).toBeVisible();
      await expect(page.getByTestId('flag-blocked')).toHaveCount(0);

      await page.getByRole('button', { name: 'Add dependency' }).click();
      const add = dialog(page, 'Add dependency');
      await expect(add).toBeVisible();
      await expect(add.locator(`[data-ref=${task}]`)).toHaveCount(0); // a task is not offered as its own blocker
      await add.getByLabel('Find an open task').fill(`review the plan ${vp.name}`);
      await expect(add.getByTestId('dep-options').getByRole('button')).toHaveCount(1);
      await add.locator(`[data-ref=${blocker}]`).click();

      await expect(add).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Task ${task} is now blocked by ${blocker}`);
      await expect(page.getByTestId('flag-blocked')).toHaveText('blocked');
      await expect(page.getByTestId('task-deps').locator(`[data-ref=${blocker}]`)).toContainText(`Review the plan ${vp.name}`);
      await expect(eventTypes(page)).resolves.toEqual(['dep_added', 'task_created']);

      // The blocker's page shows the other side, and a dependency back onto it would close a cycle: refused, inline.
      await page.goto(`/#/t/${blocker}`);
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(blocker);
      await expect(page.getByTestId('task-blocking').locator(`[data-ref=${task}]`)).toBeVisible();
      await page.getByRole('button', { name: 'Add dependency' }).click();
      const cycle = dialog(page, 'Add dependency');
      await cycle.getByLabel('Find an open task').fill(task);
      await cycle.locator(`[data-ref=${task}]`).click();
      await expect(cycle.getByTestId('dialog-error')).toBeVisible();
      await expect(cycle).toBeVisible();
      await cycle.getByRole('button', { name: 'Cancel' }).click();
      await expect(cycle).toHaveCount(0);
      await expect(page.getByRole('button', { name: 'Add dependency' })).toBeFocused();

      await page.goto(`/#/t/${task}`);
      await expect(page.getByTestId('task-page').getByTestId('task-ref')).toHaveText(task);
      await page.getByRole('button', { name: `Remove dependency on ${blocker}` }).click();
      await expect(page.getByTestId('toast')).toContainText(`Task ${task} no longer waits for ${blocker}`);
      await expect(page.getByTestId('no-deps')).toBeVisible();
      await expect(page.getByTestId('flag-blocked')).toHaveCount(0);
      await expect(eventTypes(page)).resolves.toEqual(['dep_removed', 'dep_added', 'task_created']);

      // A removal that is stale (the dependency went already) is refused inline. The other removal goes through the API
      // as the page's own DELETE leaves, not before the click: the live refresh it sets off would otherwise take the
      // Remove button away whenever the click comes later than the stream's debounce (a loaded CI host).
      await page.getByRole('button', { name: 'Add dependency' }).click();
      const again = dialog(page, 'Add dependency');
      await again.getByLabel('Find an open task').fill(blocker);
      await again.locator(`[data-ref=${blocker}]`).click();
      await expect(again).toHaveCount(0);
      let removedFirst = 0;
      await page.route(`**/api/v1/tasks/${task}/deps/${blocker}`, async (route) => {
        if (route.request().method() === 'DELETE') removedFirst = (await api.delete(`/api/v1/tasks/${task}/deps/${blocker}`)).status();
        await route.continue();
      });
      await page.getByRole('button', { name: `Remove dependency on ${blocker}` }).click();
      await expect.poll(() => removedFirst).toBe(200);
      await expect(page.getByTestId('action-error')).toBeVisible();
      await api.dispose();
    });

    test('Milestone records a summary on any task, and an empty or over-long summary is refused inline', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-mile-${vp.name}`);
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByRole('button', { name: 'Record check' })).toHaveCount(0); // a work task has no checks

      await page.getByRole('button', { name: 'Milestone' }).click();
      const mile = dialog(page, 'Record milestone');
      await expect(mile).toBeVisible();
      await mile.getByRole('button', { name: 'Record milestone' }).click();
      await expect(mile.getByTestId('dialog-error')).toBeVisible();
      await mile.getByLabel('Summary').fill('x'.repeat(201));
      await mile.getByRole('button', { name: 'Record milestone' }).click();
      await expect(mile.getByTestId('dialog-error')).toContainText('200');
      await mile.getByLabel('Summary').fill('Plan approved');
      await mile.getByRole('button', { name: 'Record milestone' }).click();

      await expect(mile).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Milestone recorded on ${ref}`);
      await expect(eventTypes(page)).resolves.toEqual(['milestone', 'task_created']);
      await expect(events(page).first().getByTestId('event-summary')).toContainText('Plan approved');
      await expect(page.getByRole('button', { name: 'Milestone' })).toBeFocused();
      await api.dispose();
    });

    test('Record check is offered on a watch task, moves the next check and leaves its event', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-check-${vp.name}`, { kind: 'watch', cadence_seconds: 900 });
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-next-check')).toBeVisible();

      await page.getByRole('button', { name: 'Record check' }).click();
      const check = dialog(page, 'Record check');
      await check.getByRole('button', { name: 'Record check' }).click();
      await expect(check.getByTestId('dialog-error')).toBeVisible();
      await check.getByLabel('Summary').fill('Build is still green');
      await check.getByRole('button', { name: 'Record check' }).click();

      await expect(check).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Check recorded on ${ref}`);
      await expect(eventTypes(page)).resolves.toEqual(['check_recorded', 'task_created']);
      await expect(events(page).first().getByTestId('event-summary')).toContainText('Build is still green');
      await expect(page.getByTestId('task-next-check')).toHaveText(/^due in (14|15)m$/);

      // A closed watch task takes no more checks.
      expect((await api.post(`/api/v1/tasks/${ref}/close`, { data: { resolution: 'dropped' } })).status()).toBe(200);
      await page.reload();
      await expect(page.getByTestId('task-status')).toHaveText('closed');
      await expect(page.getByRole('button', { name: 'Record check' })).toHaveCount(0);
      await api.dispose();
    });

    test('End lease ends a live lease with its outcome, and a lease that is gone already is refused inline', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-end-${vp.name}`);
      const first = (await post(api, '/api/v1/leases', { task: ref, intent: 'drafting' }, 201).then((l) => (l as unknown as { lease: { ref: string } }).lease)).ref;
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('lease')).toHaveCount(1);

      await page.getByRole('button', { name: `End lease ${first}` }).click();
      const end = dialog(page, 'End lease');
      await expect(end).toBeVisible();
      await end.getByLabel('Outcome (optional)').fill('Draft is in review');
      await end.getByRole('button', { name: 'End lease' }).click();
      await expect(end).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Lease ${first} ended`);
      await expect(page.getByTestId('no-leases')).toBeVisible();
      await expect(eventTypes(page)).resolves.toEqual(['lease_ended', 'lease_started', 'task_created']);
      await expect(events(page).first().getByTestId('event-summary')).toContainText('Draft is in review');

      // The lease is ended through the API while the dialog is open: the refusal shows in the dialog.
      const second = (await post(api, '/api/v1/leases', { task: ref, intent: 'again' }, 201).then((l) => (l as unknown as { lease: { ref: string } }).lease)).ref;
      await page.reload();
      await page.getByRole('button', { name: `End lease ${second}` }).click();
      const stale = dialog(page, 'End lease');
      expect((await api.post(`/api/v1/leases/${second}/end`, { data: {} })).status()).toBe(200);
      await stale.getByRole('button', { name: 'End lease' }).click();
      await expect(stale.getByTestId('dialog-error')).toBeVisible();
      await expect(stale).toBeVisible();
      await stale.getByRole('button', { name: 'Cancel' }).click();
      await api.dispose();
    });

    test('Acknowledge clears a stranded lease from the page and leaves its event', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-ack-${vp.name}`);
      const lease = (await post(api, '/api/v1/leases', { task: ref, intent: 'forgotten work' }, 201).then((l) => (l as unknown as { lease: { ref: string } }).lease)).ref;
      await expireLease(api, lease, ref);
      await page.goto(`/#/t/${ref}`);

      await expect(page.getByTestId('no-leases')).toBeVisible();
      const stranded = page.getByTestId('stranded-panel');
      await expect(stranded).toBeVisible();
      await expect(stranded).toContainText('forgotten work');
      await expect(stranded.getByTestId('stranded-expired')).toHaveText(/^expired \d+s ago$/);
      await expect(eventTypes(page)).resolves.toEqual(['lease_expired', 'lease_started', 'task_created']);

      await page.getByRole('button', { name: `Acknowledge lease ${lease}` }).click();
      await page.getByRole('dialog', { name: `Acknowledge lease ${lease}` }).getByRole('button', { name: 'Acknowledge lease' }).click();
      await expect(page.getByTestId('toast')).toContainText(`Lease ${lease} acknowledged`);
      await expect(page.getByTestId('stranded-panel')).toHaveCount(0);
      await expect(eventTypes(page)).resolves.toEqual(['lease_acked', 'lease_expired', 'lease_started', 'task_created']);
      await api.dispose();
    });

    for (const resolution of RESOLUTIONS) {
      test(`Close with the resolution ${resolution} closes the task and leaves its event, and Reopen undoes it`, async ({ page, playwright, baseURL }) => {
        const api = await apiContext(playwright, baseURL);
        const ref = await seedTask(api, `e2e-tp-close-${resolution.slice(0, 4)}-${vp.name}`);
        await page.goto(`/#/t/${ref}`);

        await actions(page).getByRole('button', { name: 'Close' }).click();
        const close = dialog(page, 'Close task');
        await expect(close).toBeVisible();
        const select = close.getByLabel('Resolution');
        expect(await select.locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value))).toEqual([...RESOLUTIONS]);
        await expect(select).toHaveValue('completed');
        await select.selectOption(resolution);
        await close.getByRole('button', { name: 'Close task' }).click();

        await expect(close).toHaveCount(0);
        await expect(page.getByTestId('toast')).toContainText(`Task ${ref} closed as ${resolution}`);
        await expect(page.getByTestId('task-status')).toHaveText('closed');
        await expect(page.getByTestId('task-resolution')).toHaveText(resolution);
        await expect(actions(page).getByRole('button')).toHaveText(['Reopen']);
        await expect(eventTypes(page)).resolves.toEqual(['task_closed', 'task_created']);
        await expect(events(page).first().getByTestId('event-summary')).toHaveText(`task ${ref} closed as ${resolution}`);

        await actions(page).getByRole('button', { name: 'Reopen' }).click();
        await expect(page.getByTestId('task-status')).toHaveText('open');
        await expect(page.getByTestId('task-resolution')).toHaveCount(0);
        await expect(eventTypes(page)).resolves.toEqual(['task_reopened', 'task_closed', 'task_created']);
        await api.dispose();
      });
    }

    test('a refused Close shows inline in the confirmation and keeps it open', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-refuse-${vp.name}`);
      await page.goto(`/#/t/${ref}`);
      await actions(page).getByRole('button', { name: 'Close' }).click();
      const close = dialog(page, 'Close task');
      await close.getByLabel('Resolution').selectOption('duplicate');

      const closed = await api.post(`/api/v1/tasks/${ref}/close`, { data: { resolution: 'dropped' } });
      expect(closed.status()).toBe(200);
      await close.getByRole('button', { name: 'Close task' }).click();
      await expect(close.getByTestId('dialog-error')).toContainText('closed');
      await expect(close).toBeVisible();
      await expect(close.getByLabel('Resolution')).toHaveValue('duplicate');
      await close.getByRole('button', { name: 'Cancel' }).click();
      await expect(close).toHaveCount(0);
      await api.dispose();
    });

    test('an ask raised through the API is listed with the blocked reason and is answered from the task page', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-ask-${vp.name}`);
      const first = (await post(api, '/api/v1/asks', { task: ref, question: 'Which database?', details: 'Pick **one**.', options: ['sqlite', 'postgres'], mode: 'poll', blocking: true }, 201)).ref;
      const second = (await post(api, '/api/v1/asks', { task: ref, question: 'Ship on Friday?' }, 201)).ref;
      await page.goto(`/#/t/${ref}`);

      await expect(page.getByTestId('flag-blocked')).toHaveText('blocked');
      await expect(page.getByTestId('blocked-reason')).toHaveText(`blocked by ${first}: Which database?`);
      const asks = page.getByTestId('ask');
      await expect(asks).toHaveCount(2);
      const one = page.locator(`[data-testid=ask][data-ref=${first}]`);
      await expect(one.getByTestId('ask-question')).toHaveText('Which database?');
      await expect(one.getByTestId('ask-asker')).toHaveText('playwright');
      await expect(one.getByTestId('ask-mode')).toContainText('poll');
      await expect(one.getByTestId('ask-blocking')).toHaveText('blocks its task');
      await expect(one.getByTestId('ask-details').locator('strong')).toHaveText('one');

      await one.getByRole('button', { name: `Answer ${first}` }).click();
      const answer = dialog(page, 'Answer ask');
      await expect(answer).toBeVisible();
      await answer.getByRole('button', { name: 'postgres' }).click();
      await expect(answer.getByRole('textbox', { name: 'Answer' })).toHaveValue('postgres');
      await answer.getByRole('button', { name: 'Send answer' }).click();
      await expect(answer).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Answered ${first}`);

      // The answered ask goes below the open one and says who answered and how; nothing blocks the task any more.
      await expect(asks.first().getAttribute('data-ref')).resolves.toBe(second);
      await expect(asks.nth(1)).toHaveAttribute('data-ref', first);
      await expect(one.getByTestId('ask-state')).toHaveText('Answered');
      await expect(one.getByTestId('ask-answer')).toContainText('postgres');
      await expect(one.getByTestId('ask-line')).toContainText('answered by');
      await expect(one.getByTestId('ask-line')).toContainText('in the UI');
      await expect(one.getByRole('button')).toHaveCount(0);
      await expect(page.getByTestId('flag-blocked')).toHaveCount(0);
      await expect(page.getByTestId('blocked-reason')).toHaveCount(0);

      // Withdraw the other one with a reason; it goes below the answered one.
      await page.locator(`[data-testid=ask][data-ref=${second}]`).getByRole('button', { name: `Withdraw ${second}` }).click();
      const withdraw = dialog(page, 'Withdraw ask');
      await withdraw.getByLabel('Reason (optional)').fill('not needed');
      await withdraw.getByRole('button', { name: 'Withdraw' }).click();
      await expect(withdraw).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Withdrew ${second}`);
      await expect(asks.nth(1)).toHaveAttribute('data-ref', second);
      await expect(page.locator(`[data-testid=ask][data-ref=${second}]`).getByTestId('ask-line')).toHaveText('withdrawn: not needed');
      await api.dispose();
    });

    test('Post note adds a note newest first, and its body expands and collapses', async ({ page, playwright, baseURL }) => {
      const api = await apiContext(playwright, baseURL);
      const ref = await seedTask(api, `e2e-tp-note-${vp.name}`);
      await post(api, '/api/v1/notes', { task: ref, summary: 'Earlier note' }, 201);
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('note')).toHaveCount(1);
      await expect(page.getByTestId('note').getByRole('button')).toHaveCount(0);

      await page.getByRole('button', { name: 'Post note' }).click();
      const form = dialog(page, 'Post note');
      await expect(form).toBeVisible();
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form.getByTestId('dialog-error')).toHaveText('Write a summary.');
      await form.getByLabel('Summary').fill('Found the cause');
      await form.getByLabel('Body (optional, Markdown)').fill('It was **the cache**.');
      await form.getByLabel('Resource (optional)').fill('host:build-1');
      await form.getByRole('button', { name: 'Post note' }).click();
      await expect(form).toHaveCount(0);
      await expect(page.getByTestId('toast')).toContainText(`Note posted on ${ref}`);

      const notes = page.getByTestId('note');
      await expect(notes).toHaveCount(2);
      await expect(notes.first().getByTestId('note-summary')).toHaveText('Found the cause');
      await expect(notes.nth(1).getByTestId('note-summary')).toHaveText('Earlier note');
      await expect(notes.first().getByTestId('note-author')).not.toHaveText('');
      await expect(notes.first().getByTestId('note-when')).toHaveText(/ago|now/);
      await expect(notes.first().getByTestId('note-resource')).toHaveText('host:build-1');

      await expect(notes.first().getByTestId('note-body')).toHaveCount(0);
      const toggle = notes.first().getByRole('button', { name: /body of/ });
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');
      await toggle.click();
      await expect(toggle).toHaveAttribute('aria-expanded', 'true');
      await expect(notes.first().getByTestId('note-body').locator('strong')).toHaveText('the cache');
      await toggle.click();
      await expect(toggle).toHaveAttribute('aria-expanded', 'false');
      await expect(notes.first().getByTestId('note-body')).toBeHidden();
      await api.dispose();
    });
  });
}
