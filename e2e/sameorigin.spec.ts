import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';
import { expectInitiatives } from './chips';
import { showAllBands } from './now';
import { readsTheSame } from './settle';

// R1: the UI makes no cross-origin request (no web font, CDN, remote icon or script). Signs in through the
// mock provider (another port, so another origin: recording starts only afterwards, and no API key rides
// along), then records every request the page issues while it loads each of the views (Attention, Initiatives, Tasks, Settings -> API keys) at the desktop viewport
// (the origin of a request does not depend on the layout; the phone run was dropped in R9). R12 adds PHASE1_STATES, the finite list of the Phase 1
// views `#/tasks`, `#/i/<slug>` and `#/t/<ref>`, each loaded in full over a seeded watch task with a blocker, a
// dependent, a milestone, a lease and a conflict, and each again with a dialog opened on the page (edit initiative,
// new task, edit task, add dependency, end lease): 8 states = 8 more visits. Every recorded http(s)/ws(s) URL must have the board
// server's origin. R13 adds PHASE2_STATES, the 2 views `#/now` and `#/notes` and the six dialogs answer ask, withdraw ask, post note, close
// from attention, acknowledge and record check from attention: 8 states = 8 more visits, each of which must also
// record the `/api/v1/stream` request, so the EventSource is held to the same bar as every fetch. R12 adds PHASE3_STATES, the finite list of the 3 views
// `#/timeline`, `#/actors` and `#/archive`, the Needs attention sections of `#/tasks` and `#/i/<slug>`, the Stranded panel, the 3 attention chips and badges and the 3 new dialogs
// (acknowledge on the task page, archive Reopen, force-done confirm): 12 states = 12 more visits, each recording the stream request too, with vis-timeline's
// own script, stylesheet and DOM loading nothing outside the board server's origin. data: and blob: URLs never touch the network and are not origins; they are left out.
const test = base;

// The origin a view's requests go to does not depend on the layout, so the per-view loop runs at the desktop viewport
// only; the direction check below keeps the phone size it always used.
const phone = { name: 'phone', size: { width: 390, height: 844 } } as const;
const desktop = { name: 'desktop', size: { width: 1440, height: 900 } } as const;
const viewports = [desktop] as const;

const views = [
  {
    name: 'Now',
    hash: '#/',
    ready: async (page: Page) => {
      await expect(page.getByTestId('band-asks')).toBeVisible();
    },
  },
  {
    name: 'Initiatives',
    hash: '#/initiatives',
    ready: async (page: Page) => {
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
    },
  },
  {
    name: 'Tasks',
    hash: '#/tasks',
    ready: async (page: Page) => {
      await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
      await expect(page.getByTestId('tasks-skeleton')).toHaveCount(0);
    },
  },
  {
    name: 'Settings -> API keys',
    hash: '#/settings/keys',
    ready: async (page: Page) => {
      await expect(page.getByTestId('key-row').first()).toBeVisible();
    },
  },
  {
    name: 'Signed out',
    hash: '#/signed-out',
    ready: async (page: Page) => {
      await expect(page.getByTestId('signed-out')).toBeVisible();
    },
  },
] as const;

const P1_SLUG = 'e2e-sameorigin-p1';

type Seed = { watch: string; lease: string };
let seeded: Promise<Seed> | undefined;

async function apiContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, actor = 'playwright') {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor },
  });
}

async function send(api: APIRequestContext, path: string, data: object, status: number) {
  const res = await api.post(path, { data });
  expect(res.status(), `${path} ${await res.text()}`).toBe(status);
  return (await res.json()) as Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any -- seed responses are read for one field each
}

// One initiative with a watch task (the subject) that has a link, a blocker, a dependent, a milestone and a live
// lease; a second actor holds the same resource on the dependent, so the task page shows a conflict. Seeded once.
async function seedPhase1(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed> {
  const api = await apiContext(playwright, baseURL);
  const other = await apiContext(playwright, baseURL, 'sameorigin-other');
  const created = await api.post('/api/v1/initiatives', { data: { slug: P1_SLUG, title: 'Same origin Phase 1', description: 'Phase 1 origins', tags: ['origin'] } });
  expect([201, 409], `seed ${P1_SLUG}: ${created.status()}`).toContain(created.status());
  const listed = await api.get(`/api/v1/tasks?initiative=${P1_SLUG}`);
  expect(listed.status()).toBe(200);
  const existing = ((await listed.json()) as { tasks?: { ref: string; title: string }[] }).tasks ?? [];
  const watchRow = existing.find((t) => t.title === 'Origin watch task');
  let watch = watchRow?.ref as string | undefined;
  let lease = '';
  if (!watch) {
    const task = async (title: string, extra: object = {}) => (await send(api, '/api/v1/tasks', { initiative: P1_SLUG, title, ...extra }, 201)).ref as string;
    watch = await task('Origin watch task', { kind: 'watch', cadence_seconds: 900, external_refs: [{ label: 'Spec', url: 'https://example.invalid/spec' }] });
    const blocker = await task('Origin blocker');
    const dependent = await task('Origin dependent');
    await send(api, `/api/v1/tasks/${watch}/deps/${blocker}`, {}, 200);
    await send(api, `/api/v1/tasks/${dependent}/deps/${watch}`, {}, 200);
    await send(api, `/api/v1/tasks/${watch}/milestones`, { summary: 'Origin milestone' }, 200);
    const live = await send(api, '/api/v1/leases', { task: watch, intent: 'Origin lease', resources: ['repo:origin'] }, 201);
    lease = live.lease.ref as string;
    await send(other, '/api/v1/leases', { task: dependent, intent: 'Origin conflict', resources: ['repo:origin'] }, 201);
  }
  if (!lease) {
    const leases = await api.get('/api/v1/leases?state=active');
    expect(leases.status()).toBe(200);
    const rows = ((await leases.json()) as { leases?: { ref: string; task: string }[] }).leases ?? [];
    lease = rows.find((l) => l.task === watch)?.ref ?? '';
  }
  await api.dispose();
  await other.dispose();
  return { watch, lease };
}

const openDialog = async (page: Page, name: string) => {
  const dialog = page.getByRole('dialog', { name });
  await expect(dialog).toBeVisible();
  return dialog;
};

// A visible button of the page: on the phone the action bar sits above the tab bar, on the desktop in the header.
const button = (page: Page, name: string | RegExp, exact = false) => page.getByRole('button', { name, exact }).locator('visible=true').first();

type Phase1State = { name: string; entry: (seed: Seed) => string; ready: (page: Page, seed: Seed) => Promise<void>; dialog?: (page: Page, seed: Seed) => Promise<void> };

const taskPageReady = async (page: Page) => {
  await expect(page.getByTestId('task-title')).toHaveText('Origin watch task');
  await expect(page.getByTestId('conflict')).toHaveCount(1);
};
const tasksViewReady = async (page: Page, seed: Seed) => {
  await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
};
const initiativePageReady = async (page: Page, seed: Seed) => {
  await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
};

// The finite list of Phase 1 states this spec records: 3 views, then dialogs opened on them.
const PHASE1_STATES: Phase1State[] = [
  { name: 'view #/tasks', entry: () => `/#/tasks?initiative=${P1_SLUG}`, ready: tasksViewReady },
  { name: 'view #/i/<slug>', entry: () => `/#/i/${P1_SLUG}`, ready: initiativePageReady },
  { name: 'view #/t/<ref>', entry: (seed) => `/#/t/${seed.watch}`, ready: taskPageReady },
  {
    name: 'view #/tasks with the new-task dialog open',
    entry: () => `/#/tasks?initiative=${P1_SLUG}`,
    ready: tasksViewReady,
    dialog: async (page) => {
      await button(page, 'New task').click();
      const dialog = await openDialog(page, 'New task');
      await dialog.getByLabel('Kind').selectOption('watch');
      await dialog.getByLabel('Find an open task').fill('Origin');
      await expect(dialog.getByTestId('blocker-options')).toBeVisible();
    },
  },
  {
    name: 'view #/i/<slug> with the edit-initiative dialog open',
    entry: () => `/#/i/${P1_SLUG}`,
    ready: initiativePageReady,
    dialog: async (page) => {
      await button(page, 'Edit', true).click();
      await openDialog(page, 'Edit initiative');
    },
  },
  {
    name: 'view #/t/<ref> with the edit-task dialog open',
    entry: (seed) => `/#/t/${seed.watch}`,
    ready: taskPageReady,
    dialog: async (page) => {
      await button(page, 'Edit', true).click();
      await openDialog(page, 'Edit task');
    },
  },
  {
    name: 'view #/t/<ref> with the add-dependency dialog open',
    entry: (seed) => `/#/t/${seed.watch}`,
    ready: taskPageReady,
    dialog: async (page) => {
      await button(page, 'Add dependency', true).click();
      const dialog = await openDialog(page, 'Add dependency');
      await dialog.getByLabel('Find an open task').fill('Origin');
      await expect(dialog.getByTestId('dep-options')).toBeVisible();
    },
  },
  {
    name: 'view #/t/<ref> with the end-lease dialog open',
    entry: (seed) => `/#/t/${seed.watch}`,
    ready: taskPageReady,
    dialog: async (page, seed) => {
      await button(page, `End lease ${seed.lease}`, true).click();
      await openDialog(page, 'End lease');
    },
  },
];

// ---- Phase 2 (R13) -------------------------------------------------------------------------------------------------

const P2_SLUG = 'e2e-sameorigin-p2';
const P2_ACTOR = 'sameorigin-p2';

type Seed2 = { ask: string; doneTask: string; watch: string; note: string };
let seeded2: Promise<Seed2> | undefined;

// Runs a lease out: its expiry moves into the past in the e2e store, then an API read expires it.
async function expireLease(api: APIRequestContext, ref: string) {
  const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
  try {
    db.exec('PRAGMA busy_timeout = 5000');
    const changed = db.prepare('UPDATE leases SET expires_at = ? WHERE n = ?').run(Date.now() - 30_000, Number(ref.slice(1)));
    expect(changed.changes).toBe(1);
  } finally {
    db.close();
  }
  expect((await api.get('/api/v1/leases?state=expired')).status()).toBe(200);
}

// One initiative with an open blocking ask, a done task, an overdue watch task and a note, so the Attention view and the Notes view
// have an item for each dialog to open on. Seeded once.
async function seedPhase2(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed2> {
  const a = await apiContext(playwright, baseURL, P2_ACTOR);
  const created = await a.post('/api/v1/initiatives', { data: { slug: P2_SLUG, title: 'Same origin Phase 2', tags: ['origin'] } });
  expect([201, 409], `seed ${P2_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string, extra: object = {}) => (await send(a, '/api/v1/tasks', { initiative: P2_SLUG, title, ...extra }, 201)).ref as string;
  const askTask = await task('Origin p2 ask subject');
  const ask = (await send(a, '/api/v1/asks', { question: 'Is this same-origin?', options: ['Yes', 'No'], task: askTask, mode: 'poll', blocking: true }, 201)).ref as string;
  const doneTask = await task('Origin p2 done');
  await send(a, `/api/v1/tasks/${doneTask}/done`, {}, 200);
  const watch = await task('Origin p2 watch', { kind: 'watch', cadence_seconds: 900, next_check_at: '2020-01-01T00:00:00Z' });
  const note = (await send(a, '/api/v1/notes', { task: askTask, summary: 'Origin p2 note', body: 'Body of the note.' }, 201)).ref as string;
  await a.dispose();
  return { ask, doneTask, watch, note };
}

const attentionItem = (page: Page, text: string) => page.getByTestId('attention-item').filter({ hasText: text });

const openAttention = async (page: Page, seed: Seed2) => {
  await showAllBands(page);
  await page.goto(`/#/now?initiative=${P2_SLUG}`);
  await expect(attentionItem(page, seed.ask)).toBeVisible();
};

const openNotes = async (page: Page) => {
  await page.goto(`/#/notes?initiative=${P2_SLUG}`);
  await expect(page.getByTestId('notes-list').getByTestId('note').first()).toBeVisible();
};

type Phase2State = { name: string; open: (page: Page, seed: Seed2, api: APIRequestContext) => Promise<void> };

// The finite list of Phase 2 states this spec records: 2 views and the six dialogs opened on them.
const PHASE2_STATES: Phase2State[] = [
  { name: 'view #/now', open: openAttention },
  {
    name: 'view #/notes',
    open: async (page, seed) => {
      await openNotes(page);
      await page.getByRole('button', { name: `Show body of ${seed.note}` }).click();
      await expect(page.getByTestId('note-body').first()).toBeVisible();
    },
  },
  {
    name: 'dialog answer ask',
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.ask).getByRole('button', { name: `Answer ${seed.ask}` }).click();
      await openDialog(page, `Answer ${seed.ask}`);
    },
  },
  {
    name: 'dialog withdraw ask',
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.ask).getByRole('button', { name: `Withdraw ${seed.ask}` }).click();
      await openDialog(page, `Withdraw ${seed.ask}`);
    },
  },
  {
    name: 'dialog post note',
    open: async (page) => {
      await openNotes(page);
      await button(page, 'Post note', true).click();
      await openDialog(page, 'Post note');
    },
  },
  {
    name: 'dialog close from attention',
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.doneTask).getByRole('button', { name: `Close ${seed.doneTask}` }).click();
      await openDialog(page, 'Close task');
    },
  },
  {
    name: 'dialog acknowledge',
    open: async (page, seed, api) => {
      // Confirming removes the item, so each visit opens the sheet and acknowledges an expired lease of its own.
      const task = (await send(api, '/api/v1/tasks', { initiative: P2_SLUG, title: 'Origin p2 acknowledged lease' }, 201)).ref as string;
      const resource = `repo:origin-p2-${task}`;
      const lease = (await send(api, '/api/v1/leases', { task, intent: 'to acknowledge', resources: [resource] }, 201)).lease.ref as string;
      await expireLease(api, lease);
      await openAttention(page, seed);
      await attentionItem(page, lease).getByRole('button', { name: `Acknowledge ${lease}` }).click();
      const dialog = await openDialog(page, `Acknowledge ${lease}`);
      await dialog.getByRole('button', { name: 'Acknowledge lease' }).click();
      await expect(page.getByTestId('toast')).toHaveText(`Lease ${lease} acknowledged`);
    },
  },
  {
    name: 'dialog record check from attention',
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.watch).getByRole('button', { name: `Record check ${seed.watch}` }).click();
      await openDialog(page, 'Record check');
    },
  },
];

// ---- Phase 3 (R12) -------------------------------------------------------------------------------------------------

const P3_SLUG = 'e2e-sameorigin-p3';
const P3_CLOSED_SLUG = 'e2e-sameorigin-p3-closed';
const P3_ACTOR = 'sameorigin-p3-a';
const P3_HOLDER = 'sameorigin-p3-b';

type Seed3 = { stranded: string; lease: string; done: string; held: string; forced: string; closed: string };
let seeded3: Promise<Seed3> | undefined;

// One initiative with a stranded task (an expired lease nobody acknowledged), a done task, a task with an open ask under a live lease,
// a task under another actor's live lease (so Done asks first) and a task closed as superseded; a second initiative, closed, for the Archive.
async function seedPhase3(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed3> {
  const a = await apiContext(playwright, baseURL, P3_ACTOR);
  const b = await apiContext(playwright, baseURL, P3_HOLDER);
  // A 409 is a second seeding by a worker the runner restarted after a failure: the initiatives stay, their items are made again.
  for (const slug of [P3_SLUG, P3_CLOSED_SLUG]) {
    const created = await a.post('/api/v1/initiatives', { data: { slug, title: `Same origin Phase 3 ${slug}`, tags: ['origin'] } });
    expect([201, 409], `seed ${slug}: ${created.status()}`).toContain(created.status());
  }
  const task = async (title: string) => (await send(a, '/api/v1/tasks', { initiative: P3_SLUG, title }, 201)).ref as string;

  const stranded = await task('Origin p3 stranded');
  const old = await send(a, '/api/v1/leases', { task: stranded, intent: 'will strand', resources: ['repo:origin-p3-old'] }, 201);
  await expireLease(a, old.lease.ref);
  const done = await task('Origin p3 done');
  await send(a, `/api/v1/tasks/${done}/done`, {}, 200);
  const held = await task('Origin p3 held');
  await send(a, '/api/v1/leases', { task: held, intent: 'holding', resources: ['repo:origin-p3'] }, 201);
  await send(a, '/api/v1/asks', { question: 'Is this same-origin too?', task: held }, 201);
  const forced = await task('Origin p3 forced');
  await send(b, '/api/v1/leases', { task: forced, intent: 'also holding', resources: ['repo:origin-p3-b'] }, 201);
  const closed = await task('Origin p3 closed');
  await send(a, `/api/v1/tasks/${closed}/close`, { resolution: 'superseded' }, 200);
  const closedInitiative = await a.post(`/api/v1/initiatives/${P3_CLOSED_SLUG}/close`, { data: {} });
  expect([200, 409], `close ${P3_CLOSED_SLUG}: ${closedInitiative.status()}`).toContain(closedInitiative.status());
  await a.dispose();
  await b.dispose();
  return { stranded, lease: old.lease.ref as string, done, held, forced, closed };
}

const needsRow = (page: Page, ref: string) => page.locator(`[data-testid=needs-row][data-ref="${ref}"]`);

type Phase3State = { name: string; open: (page: Page, seed: Seed3) => Promise<void> };

// The finite list of Phase 3 states this spec records: 3 views, the Needs attention sections of the Tasks view and the initiative page,
// the Stranded panel, the 3 attention chips and badges and the 3 new dialogs.
const PHASE3_STATES: Phase3State[] = [
  {
    name: 'view #/timeline',
    open: async (page) => {
      await page.goto(`/#/timeline?initiative=${P3_SLUG}`);
      await expect(page.getByTestId('timeline-chart')).toBeVisible();
      // vis-timeline drew its labels from the bundled script and stylesheet.
      await expect(page.locator('.vis-labelset .vis-label').first()).toBeVisible();
      await expectInitiatives(page, [P3_SLUG]);
    },
  },
  {
    name: 'view #/actors',
    open: async (page) => {
      await page.goto('/#/actors');
      const row = page.locator(`[data-testid=actor-row][data-handle="${P3_ACTOR}"]`);
      await expect(row).toBeVisible();
      await expect(row.getByTestId('actor-lease').first()).toBeVisible();
    },
  },
  {
    name: 'view #/archive',
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      await expect(page.locator(`[data-testid=archive-row][data-kind=task][data-ref="${seed.closed}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=archive-row][data-kind=initiative][data-ref="${P3_CLOSED_SLUG}"]`)).toBeVisible();
    },
  },
  {
    name: 'Needs attention on #/tasks',
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}`);
      for (const ref of [seed.stranded, seed.done, seed.held]) await expect(needsRow(page, ref)).toBeVisible();
      await expect(needsRow(page, seed.held).getByTestId('needs-holder')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Needs attention on #/i/<slug>',
    open: async (page, seed) => {
      await page.goto(`/#/i/${P3_SLUG}`);
      for (const ref of [seed.stranded, seed.done, seed.held]) await expect(needsRow(page, ref)).toBeVisible();
      await expect(needsRow(page, seed.held).getByTestId('needs-holder')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Stranded panel on #/t/<ref>',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      const panel = page.getByTestId('stranded-panel');
      await expect(panel).toBeVisible();
      await expect(panel.getByTestId('stranded-holder')).toHaveText(P3_ACTOR);
      await expect(panel.getByRole('button', { name: `Acknowledge lease ${seed.lease}` })).toBeVisible();
    },
  },
  {
    name: 'attention chip on #/initiatives',
    open: async (page) => {
      await page.goto('/#/initiatives');
      const row = page.locator(`[data-testid=initiative-row][data-slug=${P3_SLUG}]`);
      await expect(row.getByTestId('initiative-attention')).toContainText(/need(s)? attention/);
    },
  },
  {
    name: 'attention badges on #/t/<ref>',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await expect(page.getByTestId('task-title')).toHaveText('Origin p3 stranded');
      await expect(page.getByTestId('task-page').getByTestId('attention-stranded')).toBeVisible();
    },
  },
  {
    name: 'attention badge in the navigation',
    open: async (page) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}`);
      await expect(page.locator('[data-testid=attention-badge]').locator('visible=true').first()).toBeVisible();
    },
  },
  {
    name: 'dialog acknowledge on #/t/<ref>',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await page.getByTestId('stranded-panel').getByRole('button', { name: `Acknowledge lease ${seed.lease}` }).click();
      await openDialog(page, `Acknowledge lease ${seed.lease}`);
    },
  },
  {
    name: 'dialog archive Reopen',
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      await page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`).getByRole('button', { name: `Reopen task ${seed.closed}` }).click();
      await openDialog(page, 'Reopen task');
    },
  },
  {
    name: 'dialog force-done confirm',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.forced}`);
      await expect(page.getByTestId('task-title')).toHaveText('Origin p3 forced');
      await button(page, 'Done', true).click();
      const dialog = await openDialog(page, `Mark ${seed.forced} done`);
      await expect(dialog.getByTestId('dialog-body')).toContainText(P3_HOLDER);
    },
  },
];

const networkSchemes = new Set(['http:', 'https:', 'ws:', 'wss:']);

/** The recorded URLs that go over the network to an origin other than `origin`. */
function foreignURLs(urls: string[], origin: string): string[] {
  return urls.filter((u) => {
    const url = new URL(u);
    return networkSchemes.has(url.protocol) && url.origin !== origin;
  });
}

/** Records the URL of every request the page issues (including those of its frames and workers). */
function record(page: Page): string[] {
  const urls: string[] = [];
  page.on('request', (request) => urls.push(request.url()));
  return urls;
}

// The initiative the views load, seeded once per worker (a 409 is the same worker's store seen again).
test.beforeAll(async ({ playwright, workerServer }) => {
  const seed = await apiContext(playwright, workerServer.baseURL);
  const res = await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-sameorigin', title: 'Same origin', tags: ['origin'] } });
  expect([201, 409]).toContain(res.status());
  await seed.dispose();
});

/**
 * Signs the page's browser context in through the mock provider (another port, so another origin) with the
 * context's own HTTP client: it follows /auth/login through the provider and the callback and keeps the session cookie
 * in the context. No page is loaded and the recording has not started, so every request the tests record is the
 * visit's own and none belongs to the sign-in.
 */
async function signIn(page: Page) {
  const res = await page.request.get('/auth/login', { headers: { Accept: 'text/html' } });
  expect(res.status(), `sign in through /auth/login: ${res.url()}`).toBe(200);
  expect((await page.context().cookies()).some((c) => c.name === 'board_session'), 'the session cookie').toBe(true);
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    // The sign-in only; every test makes its own full page load (no navigation here that the test would discard).
    test.beforeEach(async ({ page }) => {
      await signIn(page);
    });

    for (const view of views) {
      test(`${view.name} view requests only the board server's origin`, async ({ page, baseURL }) => {
        const origin = new URL(baseURL as string).origin;
        const urls = record(page);

        // A full page load of the view (not a hash change inside the loaded document).
        await page.goto('about:blank');
        await page.goto(`/${view.hash}`);
        await view.ready(page);
        await readsTheSame(() => urls.length, 'the recorded request list');

        const network = urls.filter((u) => networkSchemes.has(new URL(u).protocol));
        expect(network.length, 'the visit issued no network request at all').toBeGreaterThan(0);
        expect(network.some((u) => new URL(u).pathname === '/'), 'the document itself was recorded').toBe(true);
        expect(foreignURLs(urls, origin), 'requests to another origin').toEqual([]);
        for (const u of network) expect(new URL(u).origin).toBe(origin);
      });
    }

    for (const state of PHASE1_STATES) {
      test(`${state.name} requests only the board server's origin`, async ({ page, playwright, baseURL }) => {
        seeded ??= seedPhase1(playwright, baseURL);
        const seed = await seeded;
        const origin = new URL(baseURL as string).origin;
        const urls = record(page);

        // A full page load of the view, then the dialog opened on the loaded page.
        await page.goto('about:blank');
        await page.goto(state.entry(seed));
        await state.ready(page, seed);
        await state.dialog?.(page, seed);
        await readsTheSame(() => urls.length, 'the recorded request list');

        const network = urls.filter((u) => networkSchemes.has(new URL(u).protocol));
        expect(network.length, 'the visit issued no network request at all').toBeGreaterThan(0);
        expect(network.some((u) => new URL(u).pathname === '/'), 'the document itself was recorded').toBe(true);
        expect(foreignURLs(urls, origin), 'requests to another origin').toEqual([]);
        for (const u of network) expect(new URL(u).origin).toBe(origin);
      });
    }
    for (const state of PHASE2_STATES) {
      test(`${state.name} requests only the board server's origin, the stream included`, async ({ page, playwright, baseURL }) => {
        seeded2 ??= seedPhase2(playwright, baseURL);
        const seed = await seeded2;
        const api = await apiContext(playwright, baseURL, P2_ACTOR);
        const origin = new URL(baseURL as string).origin;
        const urls = record(page);
        try {
          // A full page load of the view, then the dialog opened on the loaded page.
          await page.goto('about:blank');
          await state.open(page, seed, api);
          await readsTheSame(() => urls.length, 'the recorded request list');
        } finally {
          await api.dispose();
        }

        const network = urls.filter((u) => networkSchemes.has(new URL(u).protocol));
        expect(network.some((u) => new URL(u).pathname === '/'), 'the document itself was recorded').toBe(true);
        expect(network.some((u) => new URL(u).pathname === '/api/v1/stream'), 'the live stream request was recorded').toBe(true);
        expect(foreignURLs(urls, origin), 'requests to another origin').toEqual([]);
        for (const u of network) expect(new URL(u).origin).toBe(origin);
      });
    }
    for (const state of PHASE3_STATES) {
      test(`${state.name} requests only the board server's origin, the stream included`, async ({ page, playwright, baseURL }) => {
        seeded3 ??= seedPhase3(playwright, baseURL);
        const seed = await seeded3;
        const origin = new URL(baseURL as string).origin;
        const urls = record(page);

        // A full page load of the view (the document is requested afresh), then the dialog opened on the loaded page.
        await page.goto('about:blank');
        await state.open(page, seed);
        await readsTheSame(() => urls.length, 'the recorded request list');

        const network = urls.filter((u) => networkSchemes.has(new URL(u).protocol));
        expect(network.some((u) => new URL(u).pathname === '/'), 'the document itself was recorded').toBe(true);
        expect(network.some((u) => new URL(u).pathname === '/api/v1/stream'), 'the live stream request was recorded').toBe(true);
        expect(foreignURLs(urls, origin), 'requests to another origin').toEqual([]);
        for (const u of network) expect(new URL(u).origin).toBe(origin);
      });
    }
  });
}

test('the Phase 3 list names the 3 views, the Needs attention sections, the Stranded panel, the chips and the 3 dialogs', () => {
  expect(PHASE3_STATES.map((s) => s.name)).toEqual([
    'view #/timeline',
    'view #/actors',
    'view #/archive',
    'Needs attention on #/tasks',
    'Needs attention on #/i/<slug>',
    'Stranded panel on #/t/<ref>',
    'attention chip on #/initiatives',
    'attention badges on #/t/<ref>',
    'attention badge in the navigation',
    'dialog acknowledge on #/t/<ref>',
    'dialog archive Reopen',
    'dialog force-done confirm',
  ]);
});

test('the Phase 2 list names the 2 views and the six dialogs', () => {
  const names = PHASE2_STATES.map((s) => s.name);
  expect(names.filter((n) => n.startsWith('view '))).toEqual(['view #/now', 'view #/notes']);
  expect(names.filter((n) => n.startsWith('dialog ')).map((n) => n.slice('dialog '.length))).toEqual([
    'answer ask',
    'withdraw ask',
    'post note',
    'close from attention',
    'acknowledge',
    'record check from attention',
  ]);
});

test('the Phase 1 list names the 3 views and the dialogs opened on them', () => {
  const names = PHASE1_STATES.map((s) => s.name);
  for (const view of ['#/tasks', '#/i/<slug>', '#/t/<ref>']) expect(names.some((n) => n.includes(view) && !n.includes('dialog'))).toBe(true);
  expect(names.filter((n) => n.includes('dialog open'))).toHaveLength(5);
});

// Direction check: the same recording and the same assertion must go red when a remote URL is on the page.
test.describe('direction check', () => {
  test.use({ viewport: phone.size });

  test('a planted remote image is recorded and rejected', async ({ page, baseURL }) => {
    const origin = new URL(baseURL as string).origin;
    const remote = 'http://remote.invalid/planted.png';
    // Answer it locally so the check never depends on the network; the request is still recorded.
    await page.route(remote, (route) => route.fulfill({ status: 200, contentType: 'image/png', body: '' }));
    await page.goto('/#/initiatives');
    await expect(page.getByTestId('initiative-row').first()).toBeVisible();
    const urls = record(page);
    await page.evaluate((src) => {
      const img = document.createElement('img');
      img.src = src;
      document.body.append(img);
    }, remote);
    await expect.poll(() => urls.includes(remote)).toBe(true);
    expect(foreignURLs(urls, origin)).toEqual([remote]);
  });

  test('same-origin, data: and blob: URLs are not foreign', () => {
    const origin = 'http://127.0.0.1:1234';
    expect(foreignURLs([`${origin}/a`, 'data:image/png;base64,AAAA', 'blob:http://127.0.0.1:1234/x'], origin)).toEqual([]);
    expect(foreignURLs(['http://127.0.0.1:1235/a', 'https://cdn.example/f.woff2'], origin)).toHaveLength(2);
  });
});
