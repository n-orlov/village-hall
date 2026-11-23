import { expect, type APIRequestContext, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';
import { catalogRead } from './chips';

// The live indicator in the shell (SPEC 11): `live` while the stream is open, `offline` while the browser
// is offline, `live` again once it is back. Signs in through the mock provider like shell.spec.ts, so no
// API key rides along. No fixed sleep: every step waits on the indicator itself, at most 5 s.
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

// A request from the test itself, never from the page: the change a view must show without a reload.
async function apiAs(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<APIRequestContext> {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'live-spec' },
  });
}

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the indicator reads live, offline after the browser goes offline, live again when it is back', async ({
      page,
      context,
    }) => {
      await page.goto('/');
      // getByRole sees only the indicator of the layout on screen (the other two are display: none).
      const indicator = page.getByRole('status', { name: /^Live updates:/ });

      await expect(indicator).toBeVisible(FIVE_SECONDS);
      await expect(indicator).toHaveText('live', FIVE_SECONDS);
      await expect(indicator).toHaveAccessibleName('Live updates: live', FIVE_SECONDS);

      await context.setOffline(true);
      await expect(indicator).toHaveText('offline', FIVE_SECONDS);
      await expect(indicator).toHaveAccessibleName('Live updates: offline', FIVE_SECONDS);

      await context.setOffline(false);
      await expect(indicator).toHaveText('live', FIVE_SECONDS);
      await expect(indicator).toHaveAccessibleName('Live updates: live', FIVE_SECONDS);
    });

    test('the Initiatives list shows an initiative and its task counts changed through the API, without a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-list-${vp.name}`;
      // Each step waits for the live re-read of the whole catalog (a long run's shared board is many pages) to land with the
      // change before the usual bound for drawing it, so the bound never covers the read itself.
      const first = catalogRead(page);
      await page.goto('/#/initiatives');
      await first;
      await expect(page.getByTestId('initiatives-skeleton')).toBeHidden(FIVE_SECONDS);
      await expect(page.locator(`[data-testid="initiative-row"][data-slug="${slug}"]`)).toHaveCount(0);
      // The page is never reloaded: a marker set on it must survive to the end.
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      const created = catalogRead(page, slug);
      await post(api, '/api/v1/initiatives', { slug, title: `Live list ${vp.name}` }, 201);
      await created;
      const row = page.locator(`[data-testid="initiative-row"][data-slug="${slug}"]`);
      await expect(row).toBeVisible(FIVE_SECONDS);
      await expect(row.getByTestId('initiative-counts')).toHaveText('No tasks', FIVE_SECONDS);
      const before = await row.getByTestId('initiative-counts').innerText();

      const counted = catalogRead(page, (items) => items.some((i) => i.slug === slug && (i.counts?.open ?? 0) > 0));
      await post(api, '/api/v1/tasks', { initiative: slug, title: 'Counted through the API' }, 201);
      await counted;
      await expect(row.getByTestId('initiative-counts')).not.toHaveText(before, FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);

      const closed = catalogRead(page, (items) => !items.some((i) => i.slug === slug));
      await post(api, `/api/v1/initiatives/${slug}/close`, { force: true }, 200);
      await closed;
      await expect(row).toBeHidden(FIVE_SECONDS);
    });

    test('the initiative page shows a task added, finished and a title edited through the API, without a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-page-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live page ${vp.name}` }, 201);
      await page.goto(`/#/i/${slug}`);
      await expect(page.getByTestId('initiative-title')).toHaveText(`Live page ${vp.name}`, FIVE_SECONDS);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Added through the API' }, 201);
      const row = page.getByTestId('task-row').filter({ hasText: 'Added through the API' });
      await expect(row).toBeVisible(FIVE_SECONDS);
      await expect(row).toContainText('open');

      await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
      // A done task needs closing, so it moves from the table into Needs attention above it, with its badge.
      const needs = page.getByTestId('needs-row').filter({ hasText: 'Added through the API' });
      await expect(needs.getByTestId('attention-done')).toContainText('done, not closed', FIVE_SECONDS);
      await expect(row).toBeHidden(FIVE_SECONDS);

      const patched = await api.patch(`/api/v1/initiatives/${slug}`, { data: { title: `Renamed ${vp.name}` } });
      expect(patched.status(), await patched.text()).toBe(200);
      await expect(page.getByTestId('initiative-title')).toHaveText(`Renamed ${vp.name}`, FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    test('a refresh keeps an expanded note body and the scroll position of the initiative page', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-keep-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live keep ${vp.name}` }, 201);
      for (let n = 1; n <= 12; n += 1) await post(api, '/api/v1/tasks', { initiative: slug, title: `Filler task ${n}` }, 201);
      await post(api, '/api/v1/notes', { initiative: slug, summary: 'Kept open', body: 'Body **stays**' }, 201);
      await page.goto(`/#/i/${slug}`);
      const note = page.getByTestId('initiative-notes').getByTestId('note').first();
      await note.getByRole('button', { name: /body of/ }).click();
      await expect(note.getByTestId('note-body')).toBeVisible(FIVE_SECONDS);
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
      const scrolled = await page.evaluate(() => Math.round(window.scrollY));
      expect(scrolled).toBeGreaterThan(0);

      await post(api, '/api/v1/tasks', { initiative: slug, title: 'Arrives while reading' }, 201);
      await expect(page.getByTestId('task-row').filter({ hasText: 'Arrives while reading' })).toBeVisible(FIVE_SECONDS);
      await expect(note.getByTestId('note-body')).toBeVisible();
      expect(await page.evaluate(() => Math.round(window.scrollY))).toBeGreaterThanOrEqual(scrolled);
    });
    test('the Tasks view shows a task added and finished through the API, without a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-tasks-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live tasks ${vp.name}` }, 201);
      await page.goto(`/#/tasks?initiative=${slug}`);
      await expect(page.getByTestId('tasks-skeleton')).toBeHidden(FIVE_SECONDS);
      await expect(page.getByTestId('no-tasks')).toBeVisible(FIVE_SECONDS);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Added through the API' }, 201);
      const row = page.locator(`[data-testid="task-row"][data-ref="${ref}"]`);
      await expect(row).toBeVisible(FIVE_SECONDS);
      await expect(row.getByTestId('task-status')).toHaveText('open');

      await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
      // A done task needs closing, so it moves from the table into Needs attention above it, with its badge.
      const needs = page.locator(`[data-testid="needs-row"][data-ref="${ref}"]`);
      await expect(needs.getByTestId('attention-done')).toContainText('done, not closed', FIVE_SECONDS);
      await expect(row).toBeHidden(FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    test('the Actors view shows an actor that first acts, and the lease it takes, through the API within 5 s, without a reload', async ({ page, playwright, baseURL }) => {
      const handle = `live-actor-${vp.name}`;
      const slug = `e2e-live-actors-${vp.name}`;
      const api = await apiAs(playwright, baseURL);
      await post(api, '/api/v1/initiatives', { slug, title: `Live actors ${vp.name}` }, 201);
      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Taken by a new actor' }, 201);
      await page.goto('/#/actors');
      await expect(page.getByTestId('actors-view')).toBeVisible(FIVE_SECONDS);
      await expect(page.getByTestId('actor-row').first()).toBeVisible(FIVE_SECONDS);
      const row = page.locator(`[data-testid="actor-row"][data-handle="${handle}"]`);
      await expect(row).toHaveCount(0);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      const actor = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': handle },
      });
      await post(actor, '/api/v1/notes', { summary: `First sign of ${handle}` }, 201);
      await expect(row).toHaveAttribute('data-alive', 'false', FIVE_SECONDS);

      await post(actor, '/api/v1/leases', { task: ref, intent: 'live edit', resources: [`repo:${handle}`] }, 201);
      await expect(row).toHaveAttribute('data-alive', 'true', FIVE_SECONDS);
      await expect(row.getByTestId('lease-task')).toHaveText(ref, FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    test('the Archive shows a task and an initiative closed through the API within 5 s, and drops them again when reopened, without a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-archive-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live archive ${vp.name}` }, 201);
      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Closed while the Archive is open' }, 201);
      const taskRow = page.locator(`[data-testid="archive-row"][data-kind="task"][data-ref="${ref}"]`);
      const initiativeRow = page.locator(`[data-testid="archive-row"][data-kind="initiative"][data-ref="${slug}"]`);
      await page.goto('/#/archive');
      await expect(page.getByTestId('archive-view')).toBeVisible(FIVE_SECONDS);
      await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('live', FIVE_SECONDS);
      await expect(taskRow).toHaveCount(0);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      await post(api, `/api/v1/tasks/${ref}/close`, { resolution: 'dropped' }, 200);
      await expect(taskRow).toBeVisible(FIVE_SECONDS);
      await expect(taskRow.getByTestId('archive-resolution')).toHaveText('dropped');
      await post(api, `/api/v1/initiatives/${slug}/close`, {}, 200);
      await expect(initiativeRow).toBeVisible(FIVE_SECONDS);

      await post(api, `/api/v1/initiatives/${slug}/reopen`, {}, 200);
      await expect(initiativeRow).toHaveCount(0, FIVE_SECONDS);
      await post(api, `/api/v1/tasks/${ref}/reopen`, {}, 200);
      await expect(taskRow).toHaveCount(0, FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    test('a row closed through the API disappears from the Tasks view with no error toast', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-closed-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live closed ${vp.name}` }, 201);
      const gone = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Closed elsewhere' }, 201)).ref;
      const stays = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Stays' }, 201)).ref;
      await page.goto(`/#/tasks?initiative=${slug}`);
      const goneRow = page.locator(`[data-testid="task-row"][data-ref="${gone}"]`);
      await expect(goneRow).toBeVisible(FIVE_SECONDS);

      await post(api, `/api/v1/tasks/${gone}/close`, { resolution: 'dropped' }, 200);
      await expect(goneRow).toBeHidden(FIVE_SECONDS);
      await expect(page.locator(`[data-testid="task-row"][data-ref="${stays}"]`)).toBeVisible();
      await expect(page.getByRole('alert')).toHaveCount(0);
      await expect(page.getByTestId('toast')).toHaveCount(0);
    });

    test('text typed in the New task dialog survives a task created through the API', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-dialog-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live dialog ${vp.name}` }, 201);
      await page.goto(`/#/tasks?initiative=${slug}`);
      await expect(page.getByTestId('no-tasks')).toBeVisible(FIVE_SECONDS);
      await page.getByRole('button', { name: 'New task' }).click();
      const dialog = page.getByRole('dialog', { name: 'New task' });
      await expect(dialog).toBeVisible();
      await dialog.getByLabel('Title').fill('Typed and kept');
      await dialog.getByLabel('Description').fill('Half a description');

      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Arrives under the dialog' }, 201);
      await expect(page.locator(`[data-testid="task-row"][data-ref="${ref}"]`)).toBeAttached(FIVE_SECONDS);
      await expect(dialog).toBeVisible();
      await expect(dialog.getByLabel('Title')).toHaveValue('Typed and kept');
      await expect(dialog.getByLabel('Description')).toHaveValue('Half a description');
    });

    test('the Now panel shows a lease started and ended through the API, without a reload', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-now-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live now ${vp.name}` }, 201);
      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Leased through the API' }, 201);
      // The Tasks view under the test's own initiative: unfiltered it lists the whole shared board (thousands of rows on a
      // long run), whose reads and redraws, again on each live event, compete with the panel's own within its bound.
      await page.goto(`/#/tasks?initiative=${slug}`);
      await expect(page.getByTestId('now-panel')).toBeVisible(FIVE_SECONDS);
      // Below 768 px the panel starts folded to its summary line.
      const toggle = page.getByTestId('now-toggle');
      if (await toggle.isVisible()) await toggle.click();
      const lease = page.locator(`[data-testid="now-lease"][data-task="${ref}"]`);
      await expect(lease).toHaveCount(0);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      const started = await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: ref, intent: 'Shown live' }, 201);
      await expect(lease).toBeVisible(FIVE_SECONDS);
      await expect(lease).toContainText('Shown live');

      await post(api, `/api/v1/leases/${started.lease.ref}/end`, {}, 200);
      await expect(lease).toBeHidden(FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    test('the task page shows a milestone and a new status from the API, and keeps text typed in an open dialog', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-task-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live task ${vp.name}` }, 201);
      const { ref } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Watched page' }, 201);
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('task-status')).toHaveText('open', FIVE_SECONDS);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

      await page.getByRole('button', { name: 'Edit' }).click();
      const dialog = page.getByRole('dialog', { name: 'Edit task' });
      await expect(dialog).toBeVisible();
      await dialog.getByLabel('Description').fill('Typed while the task changes');

      await post(api, `/api/v1/tasks/${ref}/milestones`, { summary: 'Reached through the API' }, 200);
      await expect(page.getByTestId('task-page')).toContainText('Reached through the API', FIVE_SECONDS);
      await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
      await expect(page.getByTestId('task-status')).toHaveText('done', FIVE_SECONDS);

      await expect(dialog).toBeVisible();
      await expect(dialog.getByLabel('Description')).toHaveValue('Typed while the task changes');
      await expect(page.getByRole('alert')).toHaveCount(0);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    // R8, R9: an ask's state, and whether its asker is waiting, reaches an open task page and initiative page through
    // the stream alone. The held wait is a real `GET /asks/<ref>?wait=10`, started after the page showed "not waiting".
    for (const view of ['task page', 'initiative page'] as const) {
      test(`the ${view} shows an ask raised, its asker waiting while a wait is held, and the ask answered, without a reload`, async ({ page, playwright, baseURL }) => {
        const api = await apiAs(playwright, baseURL);
        const slug = `e2e-live-ask-${view === 'task page' ? 't' : 'i'}-${vp.name}`;
        await post(api, '/api/v1/initiatives', { slug, title: `Live ask ${vp.name}` }, 201);
        const { ref: task } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Asked live' }, 201);
        await page.goto(view === 'task page' ? `/#/t/${task}` : `/#/i/${slug}`);
        await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('live', FIVE_SECONDS);
        await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));

        const { ref } = await post<{ ref: string }>(api, '/api/v1/asks', { task, question: `Raised live on the ${view}?`, mode: 'poll' }, 201);
        const ask = page.locator(`[data-testid=ask][data-ref=${ref}]`);
        await expect(ask).toBeVisible(FIVE_SECONDS);
        await expect(ask.getByTestId('ask-line')).toHaveText('asker not waiting');

        const held = api.get(`/api/v1/asks/${ref}?wait=10`);
        try {
          await expect(ask.getByTestId('ask-line')).toHaveText('asker waiting', FIVE_SECONDS);
        } finally {
          await post(api, `/api/v1/asks/${ref}/answer`, { answer: 'Seen live' }, 200);
          await held;
        }
        if (view === 'task page') {
          await expect(ask.getByTestId('ask-state')).toHaveText('Answered', FIVE_SECONDS);
          await expect(ask.getByTestId('ask-answer')).toContainText('Seen live');
          await expect(ask.getByTestId('ask-line')).toContainText('answered');
        } else {
          await expect(ask).toBeHidden(FIVE_SECONDS); // the initiative page lists open asks only
        }
        expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
      });
    }

    test('the task page shows an ask withdrawn through the API while its wait is held, and a second ask on its own line', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-ask-w-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live ask withdrawn ${vp.name}` }, 201);
      const { ref: task } = await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Two asks' }, 201);
      const first = (await post<{ ref: string }>(api, '/api/v1/asks', { task, question: 'First?', mode: 'poll' }, 201)).ref;
      const second = (await post<{ ref: string }>(api, '/api/v1/asks', { task, question: 'Second?', mode: 'poll' }, 201)).ref;
      await page.goto(`/#/t/${task}`);
      const line = (ref: string) => page.locator(`[data-testid=ask][data-ref=${ref}]`).getByTestId('ask-line');
      await expect(line(first)).toHaveText('asker not waiting', FIVE_SECONDS);
      await expect(line(second)).toHaveText('asker not waiting');
      await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('live', FIVE_SECONDS);

      const held = api.get(`/api/v1/asks/${first}?wait=10`);
      try {
        await expect(line(first)).toHaveText('asker waiting', FIVE_SECONDS);
        await expect(line(second)).toHaveText('asker not waiting');
      } finally {
        await post(api, `/api/v1/asks/${first}/withdraw`, { reason: 'No longer needed' }, 200);
        await held;
      }
      await expect(line(first)).toHaveText('withdrawn: No longer needed', FIVE_SECONDS);
      await expect(line(second)).toHaveText('asker not waiting');
    });

    test('the Notes view shows a note posted through the API within 5 s, and one posted while offline once back online', async ({ page, context, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const slug = `e2e-live-notes-${vp.name}`;
      await post(api, '/api/v1/initiatives', { slug, title: `Live notes ${vp.name}` }, 201);
      await page.goto('/#/notes');
      await expect(page.getByTestId('notes-view')).toBeVisible(FIVE_SECONDS);
      await expect(page.getByTestId('notes-skeleton')).toBeHidden(FIVE_SECONDS);
      await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('live', FIVE_SECONDS);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));
      const noteWith = (summary: string) => page.getByTestId('notes-list').getByTestId('note-summary').filter({ hasText: summary });

      await post(api, '/api/v1/notes', { initiative: slug, summary: `Online note ${vp.name}`, body: 'Posted while the view is open' }, 201);
      await expect(noteWith(`Online note ${vp.name}`)).toBeVisible(FIVE_SECONDS);

      await context.setOffline(true);
      await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('offline', FIVE_SECONDS);
      await post(api, '/api/v1/notes', { initiative: slug, summary: `Offline note ${vp.name}`, body: 'Posted while the browser is offline' }, 201);
      await expect(noteWith(`Offline note ${vp.name}`)).toHaveCount(0);
      await context.setOffline(false);
      await expect(noteWith(`Offline note ${vp.name}`)).toBeVisible(FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });

    test('Settings -> API keys shows a key created and revoked through the API within 5 s, and one created while offline once back online', async ({
      page,
      context,
      playwright,
      baseURL,
    }) => {
      const api = await apiAs(playwright, baseURL);
      const row = (name: string) => page.locator(`[data-testid=key-row][data-name=${name}]`);
      const online = `e2e-live-key-${vp.name}`;
      const offline = `e2e-live-key-offline-${vp.name}`;
      await page.goto('/#/settings/keys');
      await expect(page.getByRole('heading', { level: 2, name: 'API keys' })).toBeVisible(FIVE_SECONDS);
      await expect(page.getByTestId('keys-skeleton')).toBeHidden(FIVE_SECONDS);
      await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('live', FIVE_SECONDS);
      await page.evaluate(() => ((window as unknown as { liveMark: boolean }).liveMark = true));
      await expect(row(online)).toHaveCount(0);

      await post(api, '/api/v1/keys', { name: online }, 201);
      await expect(row(online)).toBeVisible(FIVE_SECONDS);
      await expect(row(online).getByTestId('key-status')).toContainText('active');
      await post(api, `/api/v1/keys/${online}/revoke`, {}, 200);
      await expect(row(online).getByTestId('key-status')).toContainText('revoked', FIVE_SECONDS);

      await context.setOffline(true);
      await expect(page.getByRole('status', { name: /^Live updates:/ })).toHaveText('offline', FIVE_SECONDS);
      await post(api, '/api/v1/keys', { name: offline }, 201);
      await expect(row(offline)).toHaveCount(0);
      await context.setOffline(false);
      await expect(row(offline)).toBeVisible(FIVE_SECONDS);
      expect(await page.evaluate(() => (window as unknown as { liveMark?: boolean }).liveMark)).toBe(true);
    });
  });
}
