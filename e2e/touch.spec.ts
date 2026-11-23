import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';
import { expectInitiatives } from './chips';
import { items, showAllBands } from './now';

// Signs in through the mock provider, so no API key rides along (see login.spec.ts).
// R4 at the phone viewport: on each view and with each dialog/sheet of the R4 set open, every visible
// `a, button, input, select, textarea, [role=button]` has a hit area of at least 44x44 CSS px and every
// visible input, select and textarea has a computed font-size of at least 16 px (no iOS zoom on focus).
// R12 extends it to the Phase 1 views and dialogs as a finite, named list of states (PHASE1_STATES below): the views
// `#/tasks`, `#/i/<slug>` and `#/t/<ref>`, and the 9 dialogs edit initiative, close initiative, new task, edit task,
// close task, milestone, record check, add dependency and end lease.
// R13 extends it to the Phase 2 views and dialogs the same way (PHASE2_STATES below): `#/now` with its five sections
// filled, `#/notes`, the six dialogs answer ask, withdraw ask, post note, close from attention, acknowledge and record check
// from attention, and the live indicator in its three states.
// R12 extends it to the Phase 3 views and dialogs (PHASE3_STATES below): `#/timeline`, `#/actors`, `#/archive`, the Needs attention
// sections of the Tasks view and the initiative page, the Stranded panel, the attention chips, the dialogs acknowledge, archive Reopen
// and force-done confirm, and the phone Sort select.
const test = base;

// The one exemption (R13): a Timeline bar (planned, actual or lease) is a data mark sized by its time span, so it can be a few px wide; its task's 44 px row label opens the same preview (timeline-equivalent.spec.ts).
const SELECTOR = 'a, button, input, select, textarea, [role=button]:not(.vis-item:is(.tl-planned, .tl-actual, .tl-lease))'; // exempts only the chart's task and lease bars: WCAG 2.5.8 equivalent control
const MIN_HIT = 44;
const MIN_FONT = 16;
// Layout snaps to 1/64 px, so a 44 px box can measure 43.99997.
const SNAP = 1 / 32;

type Measured = { target: string; width: number; height: number; fontSize: number; field: boolean };

/**
 * Every matched element that is visible: rendered (not display:none or visibility:hidden), with a
 * non-empty box, and not clipped away to nothing (the sr-only skip link, clip-path: inset(50%), until it takes focus).
 * An input is measured as itself, so a visually hidden radio does not escape by being 1x1.
 */
async function measure(page: Page): Promise<Measured[]> {
  return page.evaluate((selector) => {
    const describe = (el: Element): string => {
      const label = el.getAttribute('aria-label') ?? el.getAttribute('data-testid') ?? el.textContent?.trim().slice(0, 30) ?? '';
      return `${el.tagName.toLowerCase()}[${label}]`;
    };
    const out: Measured[] = [];
    for (const el of document.querySelectorAll(selector)) {
      if (!el.checkVisibility({ visibilityProperty: true })) continue;
      const rect = el.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) continue;
      if (getComputedStyle(el).clipPath !== 'none') continue;
      out.push({
        target: describe(el),
        width: rect.width,
        height: rect.height,
        fontSize: parseFloat(getComputedStyle(el).fontSize),
        field: ['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName),
      });
    }
    return out;
  }, SELECTOR) as Promise<Measured[]>;
}

async function expectTouchFriendly(page: Page, minimum: number) {
  const found = await measure(page);
  expect(found.length, 'the state shows interactive elements').toBeGreaterThanOrEqual(minimum);
  const small = found.filter((m) => m.width < MIN_HIT - SNAP || m.height < MIN_HIT - SNAP).map((m) => `${m.target} ${m.width}x${m.height}`);
  expect(small, 'elements with a hit area under 44x44').toEqual([]);
  const zoom = found.filter((m) => m.field && m.fontSize < MIN_FONT).map((m) => `${m.target} ${m.fontSize}px`);
  expect(zoom, 'fields with a font-size under 16px').toEqual([]);
}


// ---- Phase 1 (R12) -------------------------------------------------------------------------------------------------

const P1_SLUG = 'e2e-touch-p1';
const P1_RESOURCE = 'repo:touch-p1';

type Seed = { watch: string; blocker: string; lease: string };
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

// Runs a lease out the way the Go scenarios do: its expiry moves into the past in the e2e store, then an API read expires it.
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

// One initiative with a watch task (the subject: links, a blocker, a dependent, a milestone, a check, an expired lease and a live lease)
// and a second actor holding the same resource on the dependent, so the views show a conflict.
async function seedPhase1(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed> {
  const api = await apiContext(playwright, baseURL);
  const other = await apiContext(playwright, baseURL, 'touch-p1-other');
  const created = await api.post('/api/v1/initiatives', { data: { slug: P1_SLUG, title: 'Touch Phase 1', description: 'Phase 1 touch targets', tags: ['phone', 'ux'] } });
  expect([201, 409], `seed ${P1_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string, extra: object = {}) => (await send(api, '/api/v1/tasks', { initiative: P1_SLUG, title, ...extra }, 201)).ref as string;
  const watch = await task('Touch watch', {
    kind: 'watch',
    cadence_seconds: 900,
    description: 'A watch task to measure',
    external_refs: [{ label: 'PR', url: 'https://example.com/pr/1' }],
  });
  const blocker = await task('Touch p1 blocker');
  const dependent = await task('Touch p1 dependent');
  await send(api, `/api/v1/tasks/${watch}/deps/${blocker}`, {}, 200);
  await send(api, `/api/v1/tasks/${dependent}/deps/${watch}`, {}, 200);
  await send(api, `/api/v1/tasks/${watch}/milestones`, { summary: 'Halfway there' }, 200);
  await send(api, `/api/v1/tasks/${watch}/checks`, { summary: 'Nothing changed' }, 200);
  const old = await send(api, '/api/v1/leases', { task: watch, intent: 'first try', resources: [P1_RESOURCE] }, 201);
  await expireLease(api, old.lease.ref);
  const live = await send(api, '/api/v1/leases', { task: watch, intent: 'second try', resources: [P1_RESOURCE, 'repo:touch-p1-b'] }, 201);
  await send(other, '/api/v1/leases', { task: dependent, intent: 'also here', resources: [P1_RESOURCE] }, 201);
  await api.dispose();
  await other.dispose();
  return { watch, blocker, lease: live.lease.ref as string };
}

// A visible button of the page: on the phone the action bar sits above the tab bar.
// Settings sits under the tab bar's More entry on the phone.
const openSettings = async (page: Page) => {
  await page.getByTestId('nav-tabbar').getByRole('button', { name: 'More' }).click();
  await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: 'Settings' }).click();
};
const button = (page: Page, name: string | RegExp, exact = false) => page.getByRole('button', { name, exact }).locator('visible=true').first();

const openDialog = async (page: Page, name: string) => {
  const dialog = page.getByRole('dialog', { name });
  await expect(dialog).toBeVisible();
  return dialog;
};

const taskPage = async (page: Page, seed: Seed) => {
  await page.goto(`/#/t/${seed.watch}`);
  await expect(page.getByTestId('task-title')).toHaveText('Touch watch');
  await expect(page.getByTestId('lease')).toHaveCount(1);
};

type Phase1State = { name: string; minimum: number; open: (page: Page, seed: Seed) => Promise<void> };

// The finite list of Phase 1 states this spec measures at the phone: 3 views and 9 dialogs.
const PHASE1_STATES: Phase1State[] = [
  {
    name: 'view #/tasks',
    minimum: 8,
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P1_SLUG}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
    },
  },
  {
    name: 'view #/i/<slug>',
    minimum: 6,
    open: async (page, seed) => {
      await page.goto(`/#/i/${P1_SLUG}`);
      await expect(page.getByTestId('initiative-title')).toHaveText('Touch Phase 1');
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
    },
  },
  { name: 'view #/t/<ref>', minimum: 10, open: taskPage },
  {
    name: 'dialog edit initiative',
    minimum: 8,
    open: async (page) => {
      await page.goto(`/#/i/${P1_SLUG}`);
      await expect(page.getByTestId('initiative-title')).toHaveText('Touch Phase 1');
      await button(page, 'Edit', true).click();
      await openDialog(page, 'Edit initiative');
    },
  },
  {
    name: 'dialog close initiative',
    minimum: 6,
    open: async (page) => {
      await page.goto(`/#/i/${P1_SLUG}`);
      await expect(page.getByTestId('initiative-title')).toHaveText('Touch Phase 1');
      await button(page, 'Close', true).click();
      await openDialog(page, 'Close initiative');
    },
  },
  {
    name: 'dialog new task',
    minimum: 14,
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P1_SLUG}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
      await button(page, 'New task').click();
      const dialog = await openDialog(page, 'New task');
      await dialog.getByLabel('Kind').selectOption('watch');
      await dialog.getByLabel('Find an open task').fill(seed.blocker);
      await dialog.getByTestId('blocker-options').getByRole('button').first().click(); // a chosen blocker has its remove button
      await expect(dialog.getByTestId('blockers')).toBeVisible();
      await dialog.getByLabel('Link label').fill('PR');
      await dialog.getByLabel('Link URL').fill('https://example.com/1');
      await dialog.getByRole('button', { name: 'Add link' }).click();
      await expect(dialog.getByTestId('links')).toBeVisible();
    },
  },
  {
    name: 'dialog edit task',
    minimum: 8,
    open: async (page, seed) => {
      await taskPage(page, seed);
      await button(page, 'Edit', true).click();
      await openDialog(page, 'Edit task');
    },
  },
  {
    name: 'dialog close task',
    minimum: 6,
    open: async (page, seed) => {
      await taskPage(page, seed);
      await button(page, 'Close', true).click();
      await openDialog(page, 'Close task');
    },
  },
  {
    name: 'dialog milestone',
    minimum: 3,
    open: async (page, seed) => {
      await taskPage(page, seed);
      await button(page, 'Milestone', true).click();
      await openDialog(page, 'Record milestone');
    },
  },
  {
    name: 'dialog record check',
    minimum: 3,
    open: async (page, seed) => {
      await taskPage(page, seed);
      await button(page, 'Record check', true).click();
      await openDialog(page, 'Record check');
    },
  },
  {
    name: 'dialog add dependency',
    minimum: 3,
    open: async (page, seed) => {
      await taskPage(page, seed);
      await button(page, 'Add dependency', true).click();
      const dialog = await openDialog(page, 'Add dependency');
      await dialog.getByLabel('Find an open task').fill(P1_SLUG);
      await expect(dialog.getByTestId('dep-options')).toBeVisible();
    },
  },
  {
    name: 'dialog end lease',
    minimum: 3,
    open: async (page, seed) => {
      await taskPage(page, seed);
      await button(page, `End lease ${seed.lease}`, true).click();
      await openDialog(page, 'End lease');
    },
  },
];

// ---- Phase 2 (R13) -------------------------------------------------------------------------------------------------

const P2_SLUG = 'e2e-touch-p2';
const P2_RESOURCE = 'repo:touch-p2';
const P2_ACTOR = 'touch-p2-a';
const P2_HOLDER = 'touch-p2-b';

type Seed2 = { ask: string; doneTask: string; lease: string; watch: string; note: string };
let seeded2: Promise<Seed2> | undefined;

async function actorApi(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, actor: string) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor, 'Board-Actor-Way-Back': `ralphctl status ${actor}` },
  });
}

// One initiative with one item in each of the five Attention sections (an open blocking ask with options, a done task, an expired
// lease, an overdue watch and a conflict between two actors) and a note on a task, on the initiative and as a broadcast.
async function seedPhase2(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed2> {
  const a = await actorApi(playwright, baseURL, P2_ACTOR);
  const b = await actorApi(playwright, baseURL, P2_HOLDER);
  // A 409 is a second seeding by a worker the runner restarted after a failure: the initiative stays, its items are made again.
  const created = await a.post('/api/v1/initiatives', { data: { slug: P2_SLUG, title: 'Touch Phase 2', tags: ['phone'] } });
  expect([201, 409], `seed ${P2_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string, extra: object = {}) => (await send(a, '/api/v1/tasks', { initiative: P2_SLUG, title, ...extra }, 201)).ref as string;

  const askTask = await task('Touch p2 ask subject');
  const ask = (await send(a, '/api/v1/asks', { question: 'Is this big enough?', details: 'Measure it.', options: ['Yes', 'No', 'Maybe'], task: askTask, mode: 'poll', blocking: true }, 201)).ref as string;
  const doneTask = await task('Touch p2 done');
  await send(a, `/api/v1/tasks/${doneTask}/done`, {}, 200);
  const leaseTask = await task('Touch p2 lease');
  const old = await send(a, '/api/v1/leases', { task: leaseTask, intent: 'will expire', resources: ['repo:touch-p2-old'] }, 201);
  await expireLease(a, old.lease.ref);
  const watch = await task('Touch p2 watch', { kind: 'watch', cadence_seconds: 900, next_check_at: '2020-01-01T00:00:00Z' });
  const first = await task('Touch p2 first holder');
  const second = await task('Touch p2 second holder');
  await send(a, '/api/v1/leases', { task: first, intent: 'holding', resources: [P2_RESOURCE] }, 201);
  await send(b, '/api/v1/leases', { task: second, intent: 'also holding', resources: [P2_RESOURCE] }, 201);

  const onTask = (await send(a, '/api/v1/notes', { task: askTask, summary: 'A note on the task', body: 'Body of the note.' }, 201)).ref as string;
  await send(a, '/api/v1/notes', { initiative: P2_SLUG, summary: 'A note on the initiative', body: 'More body.' }, 201);
  await send(a, '/api/v1/notes', { summary: 'A broadcast', body: 'To everyone.', resource: P2_RESOURCE }, 201);
  await a.dispose();
  await b.dispose();
  return { ask, doneTask, lease: old.lease.ref as string, watch, note: onTask };
}

const attentionItem = (page: Page, text: string) => page.getByTestId('attention-item').filter({ hasText: text });

const openAttention = async (page: Page, seed: Seed2) => {
  await showAllBands(page);
  await page.goto(`/#/now?initiative=${P2_SLUG}`);
  await expect(page.getByTestId('band-count')).toHaveCount(4);
  await expect(attentionItem(page, seed.ask)).toBeVisible();
};

type Phase2State = { name: string; minimum: number; open: (page: Page, seed: Seed2) => Promise<void> };

// The finite list of Phase 2 states this spec measures at the phone: 2 views, the six dialogs and the live indicator in 3 states.
// Acknowledge's state is its confirmation sheet.
const PHASE2_STATES: Phase2State[] = [
  {
    name: 'view #/now',
    minimum: 15,
    open: async (page, seed) => {
      await openAttention(page, seed);
      for (const key of ['open_asks', 'done_not_closed', 'expired_leases', 'overdue_watches', 'conflicts'] as const) {
        await expect(items(page, key).first()).toBeVisible();
      }
      await expect(page.getByTestId('copy-way-back').first()).toBeVisible();
    },
  },
  {
    name: 'view #/notes',
    minimum: 12,
    open: async (page, seed) => {
      await page.goto(`/#/notes?initiative=${P2_SLUG}`);
      await expect(page.getByTestId('notes-list').getByTestId('note').first()).toBeVisible();
      await page.getByRole('button', { name: `Show body of ${seed.note}` }).click();
      await expect(page.getByTestId('note-body').first()).toBeVisible();
    },
  },
  {
    name: 'dialog answer ask',
    minimum: 6,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.ask).getByRole('button', { name: `Answer ${seed.ask}` }).click();
      const dialog = await openDialog(page, `Answer ${seed.ask}`);
      await expect(page.getByTestId('answer-option')).toHaveCount(3);
      await dialog.getByRole('textbox', { name: 'Answer' }).fill('Yes');
    },
  },
  {
    name: 'dialog withdraw ask',
    minimum: 3,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.ask).getByRole('button', { name: `Withdraw ${seed.ask}` }).click();
      await openDialog(page, `Withdraw ${seed.ask}`);
    },
  },
  {
    name: 'dialog post note',
    minimum: 6,
    open: async (page) => {
      await page.goto(`/#/notes?initiative=${P2_SLUG}`);
      await expect(page.getByTestId('notes-list').getByTestId('note').first()).toBeVisible();
      await button(page, 'Post note', true).click();
      await openDialog(page, 'Post note');
    },
  },
  {
    name: 'dialog close from attention',
    minimum: 3,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.doneTask).getByRole('button', { name: `Close ${seed.doneTask}` }).click();
      await openDialog(page, 'Close task');
    },
  },
  {
    name: 'dialog acknowledge',
    minimum: 10,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.lease).getByRole('button', { name: `Acknowledge ${seed.lease}` }).click();
      await openDialog(page, `Acknowledge ${seed.lease}`);
    },
  },
  {
    name: 'dialog record check from attention',
    minimum: 3,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.watch).getByRole('button', { name: `Record check ${seed.watch}` }).click();
      await openDialog(page, 'Record check');
    },
  },
];

// ---- Phase 3 (R12) -------------------------------------------------------------------------------------------------

const P3_SLUG = 'e2e-touch-p3';
const P3_CLOSED_SLUG = 'e2e-touch-p3-closed';
const P3_ACTOR = 'touch-p3-a';
const P3_HOLDER = 'touch-p3-b';

type Seed3 = { stranded: string; lease: string; done: string; held: string; forced: string; closed: string };
let seeded3: Promise<Seed3> | undefined;

// One initiative with a stranded task (an expired lease nobody acknowledged), a done task, a task with an open ask under a live lease,
// a task under another actor's live lease (so Done asks first) and a task closed as superseded; a second initiative, closed, for the Archive.
async function seedPhase3(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed3> {
  const a = await actorApi(playwright, baseURL, P3_ACTOR);
  const b = await actorApi(playwright, baseURL, P3_HOLDER);
  // A 409 is a second seeding by a worker the runner restarted after a failure: the initiatives stay, their items are made again.
  for (const slug of [P3_SLUG, P3_CLOSED_SLUG]) {
    const created = await a.post('/api/v1/initiatives', { data: { slug, title: `Touch Phase 3 ${slug}`, tags: ['phone'] } });
    expect([201, 409], `seed ${slug}: ${created.status()}`).toContain(created.status());
  }
  const task = async (title: string) => (await send(a, '/api/v1/tasks', { initiative: P3_SLUG, title }, 201)).ref as string;

  const stranded = await task('Touch p3 stranded');
  const old = await send(a, '/api/v1/leases', { task: stranded, intent: 'will strand', resources: ['repo:touch-p3-old'] }, 201);
  await expireLease(a, old.lease.ref);
  const done = await task('Touch p3 done');
  await send(a, `/api/v1/tasks/${done}/done`, {}, 200);
  const held = await task('Touch p3 held');
  await send(a, '/api/v1/leases', { task: held, intent: 'holding', resources: ['repo:touch-p3'] }, 201);
  await send(a, '/api/v1/asks', { question: 'Is this big enough?', task: held }, 201);
  const forced = await task('Touch p3 forced');
  await send(b, '/api/v1/leases', { task: forced, intent: 'also holding', resources: ['repo:touch-p3-b'] }, 201);
  const closed = await task('Touch p3 closed');
  await send(a, `/api/v1/tasks/${closed}/close`, { resolution: 'superseded' }, 200);
  const closedInitiative = await a.post(`/api/v1/initiatives/${P3_CLOSED_SLUG}/close`, { data: {} });
  expect([200, 409], `close ${P3_CLOSED_SLUG}: ${closedInitiative.status()}`).toContain(closedInitiative.status());
  await a.dispose();
  await b.dispose();
  return { stranded, lease: old.lease.ref as string, done, held, forced, closed };
}

const needsRow = (page: Page, ref: string) => page.locator(`[data-testid=needs-row][data-ref="${ref}"]`);

type Phase3State = { name: string; minimum: number; open: (page: Page, seed: Seed3) => Promise<void> };

// The finite list of Phase 3 states this spec measures at the phone: 3 views, the Needs attention sections of the Tasks view and the
// initiative page, the Stranded panel, the 3 attention chips and badges, 3 dialogs and the Sort select of 2 views.
const PHASE3_STATES: Phase3State[] = [
  {
    name: 'view #/timeline',
    minimum: 4,
    open: async (page) => {
      await page.goto(`/#/timeline?initiative=${P3_SLUG}`);
      await expect(page.getByTestId('timeline-chart')).toBeVisible();
      await expect(page.locator('.vis-labelset .vis-label').first()).toBeVisible();
      await expectInitiatives(page, [P3_SLUG]);
    },
  },
  {
    name: 'view #/actors',
    minimum: 3,
    open: async (page) => {
      await page.goto('/#/actors');
      await expect(page.locator(`[data-testid=actor-row][data-handle="${P3_ACTOR}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=actor-row][data-handle="${P3_ACTOR}"]`).getByTestId('actor-lease').first()).toBeVisible();
    },
  },
  {
    name: 'view #/archive',
    minimum: 4,
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      await expect(page.locator(`[data-testid=archive-row][data-kind=task][data-ref="${seed.closed}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=archive-row][data-kind=initiative][data-ref="${P3_CLOSED_SLUG}"]`)).toBeVisible();
    },
  },
  {
    name: 'Needs attention on #/tasks',
    minimum: 6,
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}`);
      for (const ref of [seed.stranded, seed.done, seed.held]) await expect(needsRow(page, ref)).toBeVisible();
      await expect(needsRow(page, seed.held).getByTestId('needs-holder')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Needs attention on #/i/<slug>',
    minimum: 6,
    open: async (page, seed) => {
      await page.goto(`/#/i/${P3_SLUG}`);
      for (const ref of [seed.stranded, seed.done, seed.held]) await expect(needsRow(page, ref)).toBeVisible();
      await expect(needsRow(page, seed.held).getByTestId('needs-holder')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Stranded panel on #/t/<ref>',
    minimum: 5,
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
    minimum: 4,
    open: async (page) => {
      await page.goto('/#/initiatives');
      const row = page.locator(`[data-testid=initiative-row][data-slug=${P3_SLUG}]`);
      await expect(row.getByTestId('initiative-attention')).toContainText(/need(s)? attention/);
    },
  },
  {
    name: 'attention badges on #/t/<ref>',
    minimum: 5,
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await expect(page.getByTestId('task-title')).toHaveText('Touch p3 stranded');
      await expect(page.getByTestId('task-page').getByTestId('attention-stranded')).toBeVisible();
    },
  },
  {
    name: 'attention badge in the navigation',
    minimum: 4,
    open: async (page) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}`);
      await expect(page.locator('[data-testid=attention-badge]').locator('visible=true').first()).toBeVisible();
    },
  },
  {
    name: 'dialog acknowledge on #/t/<ref>',
    minimum: 3,
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await page.getByTestId('stranded-panel').getByRole('button', { name: `Acknowledge lease ${seed.lease}` }).click();
      await openDialog(page, `Acknowledge lease ${seed.lease}`);
    },
  },
  {
    name: 'dialog archive Reopen',
    minimum: 3,
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      await page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`).getByRole('button', { name: `Reopen task ${seed.closed}` }).click();
      await openDialog(page, 'Reopen task');
    },
  },
  {
    name: 'dialog force-done confirm',
    minimum: 3,
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.forced}`);
      await expect(page.getByTestId('task-title')).toHaveText('Touch p3 forced');
      await button(page, 'Done', true).click();
      const dialog = await openDialog(page, `Mark ${seed.forced} done`);
      await expect(dialog.getByTestId('dialog-body')).toContainText(P3_HOLDER);
    },
  },
  {
    name: 'phone Sort select on #/tasks',
    minimum: 6,
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}&sort=holder&dir=desc`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.forced}]`)).toBeVisible();
      await expect(page.getByTestId('sort-select')).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('holder:desc');
    },
  },
  {
    name: 'phone Sort select on #/archive',
    minimum: 4,
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7&sort=item&dir=desc');
      await expect(page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`)).toBeVisible();
      await expect(page.getByTestId('sort-select')).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('item:desc');
    },
  },
];

const LIVE_STATES = ['live', 'offline', 'reconnecting'] as const;

test.describe('phone 390x844', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test.beforeEach(async ({ page, playwright, baseURL }) => {
    // Rows to measure: an initiative with tags (the key list already holds global-setup.ts's own key).
    const seed = await playwright.request.newContext({
      baseURL,
      extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
    });
    const res = await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-touch', title: 'Touch targets', tags: ['phone', 'ux'] } });
    expect([201, 409]).toContain(res.status());
    if (res.status() === 201) {
      expect((await seed.post('/api/v1/tasks', { data: { initiative: 'e2e-touch', title: 'Touch task' } })).status()).toBe(201);
      expect((await seed.post('/api/v1/tasks', { data: { initiative: 'e2e-touch', title: 'Touch blocker' } })).status()).toBe(201); // for the Add dependency sheet
    }
    await seed.dispose();
    await page.goto('/#/initiatives');
    await expect(page.getByTestId('initiative-row').first()).toBeVisible();
  });

  test('the Attention view with an ask, its way back button and the filter chips', async ({ page, playwright, baseURL }) => {
    const seed = await playwright.request.newContext({
      baseURL,
      extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'touch-att', 'Board-Actor-Way-Back': 'ralphctl status touch-att' },
    });
    const made = await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-touch-att', title: 'Touch Attention', tags: ['touch-att'] } });
    expect([201, 409]).toContain(made.status());
    if (made.status() === 201) {
      expect((await seed.post('/api/v1/asks', { data: { question: 'Is this big enough?', initiative: 'e2e-touch-att' } })).status()).toBe(201);
    }
    await seed.dispose();
    await page.goto('/#/now?initiative=e2e-touch-att');
    await expect(page.getByTestId('copy-way-back').first()).toBeVisible();
    await expectTouchFriendly(page, 6);
  });

  test('the Notes view with a note, its filters and the Post note sheet', async ({ page, playwright, baseURL }) => {
    const seed = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'touch-notes' } });
    expect([201, 409]).toContain((await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-touch-notes', title: 'Touch Notes' } })).status());
    expect((await seed.post('/api/v1/notes', { data: { initiative: 'e2e-touch-notes', summary: 'Big enough?', body: 'Yes.' } })).status()).toBe(201);
    await seed.dispose();
    await page.goto('/#/notes?initiative=e2e-touch-notes');
    await expect(page.getByTestId('note').first()).toBeVisible();
    await expectTouchFriendly(page, 8);
    await page.getByTestId('notes-view').getByRole('button', { name: 'Post note' }).click();
    await expect(page.getByRole('dialog', { name: 'Post note' })).toBeVisible();
    await expectTouchFriendly(page, 5);
  });

  test('the Initiatives view', async ({ page }) => {
    await expectTouchFriendly(page, 4);
  });

  test('the initiative page', async ({ page }) => {
    await page.goto('/#/i/e2e-touch');
    await expect(page.getByTestId('initiative-title')).toHaveText('Touch targets');
    await expectTouchFriendly(page, 4);
  });

  test('the Tasks view', async ({ page }) => {
    await page.goto('/#/tasks?initiative=e2e-touch');
    await expect(page.getByRole('heading', { level: 2, name: 'Tasks' })).toBeVisible();
    await expect(page.getByTestId('task-row').first()).toBeVisible();
    await expectTouchFriendly(page, 8);
  });

  test('the Tasks view with the Now panel open and a conflict', async ({ page, playwright, baseURL }) => {
    const seed = await playwright.request.newContext({
      baseURL,
      extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'touch-a' },
    });
    expect((await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-touch-now', title: 'Touch Now' } })).status()).toBe(201);
    for (const [actor, title] of [['touch-a', 'Now one'], ['touch-b', 'Now two']]) {
      const made = await seed.post('/api/v1/tasks', { data: { initiative: 'e2e-touch-now', title } });
      expect(made.status()).toBe(201);
      const { ref } = (await made.json()) as { ref: string };
      const lease = await seed.post('/api/v1/leases', { data: { task: ref, intent: 'touching', resources: ['repo:touch'] }, headers: { 'Board-Actor': actor! } });
      expect(lease.status()).toBe(201);
    }
    await seed.dispose();

    await page.goto('/#/tasks?initiative=e2e-touch-now');
    await expect(page.getByTestId('task-row').first()).toBeVisible();
    await page.getByTestId('now-toggle').click();
    await expect(page.locator('[data-testid=now-conflict][data-resource="repo:touch"]')).toBeVisible();
    await expectTouchFriendly(page, 10);
  });

  test('the new-task sheet', async ({ page }) => {
    await page.goto('/#/tasks?initiative=e2e-touch');
    await expect(page.getByTestId('task-row').first()).toBeVisible();
    await page.getByRole('button', { name: 'New task' }).click();
    const dialog = page.getByRole('dialog', { name: 'New task' });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel('Kind').selectOption('watch');
    await dialog.getByLabel('Find an open task').fill('e2e-touch');
    await dialog.getByTestId('blocker-options').getByRole('button').first().click(); // a chosen blocker has its remove button
    await expect(dialog.getByTestId('blockers')).toBeVisible();
    await dialog.getByLabel('Link label').fill('PR');
    await dialog.getByLabel('Link URL').fill('https://example.com/1');
    await dialog.getByRole('button', { name: 'Add link' }).click();
    await expect(dialog.getByTestId('links')).toBeVisible();
    await expectTouchFriendly(page, 14);
  });

  test('the Settings -> API keys view', async ({ page }) => {
    await openSettings(page);
    await expect(page.getByTestId('key-row').first()).toBeVisible();
    await expectTouchFriendly(page, 5);
  });

  test('the Signed out view', async ({ page }) => {
    await page.getByRole('button', { name: 'Account menu' }).click();
    await page.getByRole('dialog', { name: 'Account' }).getByRole('button', { name: 'Sign out' }).click();
    await expect(page).toHaveURL(/#\/signed-out$/);
    await expect(page.getByTestId('signed-out')).toBeVisible();
    await expectTouchFriendly(page, 1);
  });

  test('the new-initiative sheet', async ({ page }) => {
    await page.getByRole('button', { name: 'New initiative' }).click();
    const dialog = page.getByRole('dialog', { name: 'New initiative' });
    await expect(dialog).toBeVisible();
    expect((await dialog.locator('input, textarea').count()) >= 3).toBe(true);
    await expectTouchFriendly(page, 8);
  });

  test('the new-key sheet', async ({ page }) => {
    await openSettings(page);
    await page.getByRole('button', { name: 'New API key' }).click();
    const dialog = page.getByRole('dialog', { name: 'New API key' });
    await expect(dialog).toBeVisible();
    await expectTouchFriendly(page, 6);
  });

  test('the revoke confirmation', async ({ page }) => {
    await openSettings(page);
    await page.getByTestId('key-row').first().getByRole('button', { name: 'Revoke' }).click();
    const dialog = page.getByRole('dialog', { name: 'Revoke API key' });
    await expect(dialog).toBeVisible();
    await expectTouchFriendly(page, 6);
  });

  test('the account menu', async ({ page }) => {
    await page.getByRole('button', { name: 'Account menu' }).click();
    const dialog = page.getByRole('dialog', { name: 'Account' });
    await expect(dialog).toBeVisible();
    // Density (2) and theme (3) switches.
    expect(await dialog.locator('input[type=radio]').count()).toBe(5);
    await expectTouchFriendly(page, 8);
  });

  for (const state of PHASE1_STATES) {
    test(`${state.name} has 44 px hit areas and 16 px inputs`, async ({ page, playwright, baseURL }) => {
      seeded ??= seedPhase1(playwright, baseURL);
      const seed = await seeded;
      await state.open(page, seed);
      await expectTouchFriendly(page, state.minimum);
    });
  }

  for (const state of PHASE2_STATES) {
    test(`${state.name} has 44 px hit areas and 16 px inputs`, async ({ page, playwright, baseURL }) => {
      seeded2 ??= seedPhase2(playwright, baseURL);
      const seed = await seeded2;
      await state.open(page, seed);
      await expectTouchFriendly(page, state.minimum);
    });
  }

  for (const state of PHASE3_STATES) {
    test(`${state.name} has 44 px hit areas and 16 px inputs`, async ({ page, playwright, baseURL }) => {
      seeded3 ??= seedPhase3(playwright, baseURL);
      const seed = await seeded3;
      await state.open(page, seed);
      await expectTouchFriendly(page, state.minimum);
    });
  }

  for (const word of LIVE_STATES) {
    test(`the live indicator reading ${word} sits on a page of 44 px hit areas`, async ({ page, context }) => {
      if (word === 'reconnecting') {
        // The stream never opens: the indicator stays on `reconnecting`.
        await page.addInitScript(() => {
          class Silent {
            readyState = 0;
            onopen = null;
            onerror = null;
            addEventListener() {}
            close() {}
          }
          (window as unknown as { EventSource: unknown }).EventSource = Silent;
        });
      }
      await page.goto('/#/now');
      if (word === 'reconnecting') await page.reload(); // beforeEach already loaded the app: the stand-in applies to a fresh document
      const indicator = page.getByRole('status', { name: /^Live updates:/ });
      await expect(indicator).toBeVisible();
      if (word === 'offline') await context.setOffline(true);
      await expect(indicator).toHaveAccessibleName(`Live updates: ${word}`);
      await expect(indicator).toHaveText(word);
      await expectTouchFriendly(page, 4);
      await context.setOffline(false);
    });
  }

  test('the Phase 3 list names the 3 views, the Needs attention sections, the Stranded panel, the chips, the 3 dialogs and the Sort select', () => {
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
      'phone Sort select on #/tasks',
      'phone Sort select on #/archive',
    ]);
  });

  test('the Phase 2 list names the views, the six dialogs and the three live indicator states', () => {
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
    expect(LIVE_STATES).toEqual(['live', 'offline', 'reconnecting']);
  });

  test('the Phase 1 list names the 3 views and the 9 dialogs', () => {
    const names = PHASE1_STATES.map((s) => s.name);
    expect(names.filter((n) => n.startsWith('view '))).toEqual(['view #/tasks', 'view #/i/<slug>', 'view #/t/<ref>']);
    expect(names.filter((n) => n.startsWith('dialog ')).map((n) => n.slice('dialog '.length))).toEqual([
      'edit initiative',
      'close initiative',
      'new task',
      'edit task',
      'close task',
      'milestone',
      'record check',
      'add dependency',
      'end lease',
    ]);
  });
});
