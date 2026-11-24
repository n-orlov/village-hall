import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';
import { expectInitiatives } from './chips';
import { items, showAllBands } from './now';

// R3: at 390, 768 and 1440 px (and 1024 px for the Now view) the page never scrolls sideways, whatever text the API accepts. Signs in through the mock provider.
// Every case seeds the longest unbroken text the server takes (no space to wrap at): a 200-character
// title, a 40-character slug, 20 tags of 40 characters, a 64-character key name; wide glyphs (W) and
// narrow ones (i) both, since they stress the line breaking differently.
// R12 extends it to the Phase 1 views and dialogs as a finite, named list of states (PHASE1_STATES below): the views
// `#/tasks`, `#/i/<slug>` and `#/t/<ref>`, and the 9 dialogs edit initiative, close initiative, new task, edit task,
// close task, milestone, record check, add dependency and end lease, each filled with the longest values the API takes.
// R13 extends it to the Phase 2 views and dialogs the same way (PHASE2_STATES below): `#/now` with its five
// sections filled, `#/notes`, the six dialogs answer ask, withdraw ask, post note, close from attention, acknowledge
// and record check from attention, and the live indicator in its three states.
// R12 extends it to the Phase 3 views, dialogs and tables the same way (PHASE3_STATES below): `#/timeline`, `#/actors` and
// `#/archive`, the Needs attention sections (Tasks view and initiative page), the Stranded panel, the attention chips and badges,
// the three new dialogs (acknowledge on the task page, archive Reopen, force-done confirm) and the sorted Initiatives, Tasks,
// initiative page Tasks, API keys, Actors and Archive tables. WIDE_STATES holds the Tasks view, the task page, the initiative
// page and Attention to the same bar at 2560x1080 and 3440x1440.
const test = base;

// The phone is held to the bar twice: in the browser's own font, and with every glyph 5% of an em wider. The text a font
// sets in 390 px is the font's own: a fallback font (the one `system-ui` picks differs from host to host) is wider than
// the one that happens to fit, and a 40-character slug or tag that just fits in one font scrolls the page sideways in
// another. Letter spacing widens every font the same way, so the second pass fails wherever the page only fits by the margin of
// the font it was built in, whichever font the machine has.
const WIDE_GLYPHS = '*, *::before, *::after { letter-spacing: 0.05em !important; }';
// R9 j: one viewport per distinct layout. The CSS breakpoints are 768 (the wide layout) and 1024 (the Now view's dock), so 390 is
// below both, 768 sits on the first, 1440 is above both, and 1024 sits on the second, which only the Now view has (nowOnly: only
// the states whose hash is #/now run there). ci/suitecheck pins this list against the min-width/max-width px values in web/src.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 }, css: '', nowOnly: false },
  { name: 'phone-wide-glyphs', size: { width: 390, height: 844 }, css: WIDE_GLYPHS, nowOnly: false },
  { name: 'tablet', size: { width: 768, height: 1024 }, css: '', nowOnly: false },
  { name: 'small-desktop', size: { width: 1024, height: 768 }, css: '', nowOnly: true },
  { name: 'desktop', size: { width: 1440, height: 900 }, css: '', nowOnly: false },
] as const;

const wide = (n: number, ch = 'W') => ch.repeat(n);
const tag = (i: number) => `t${String(i).padStart(2, '0')}${'w'.repeat(37)}`;
const tags = Array.from({ length: 20 }, (_, i) => tag(i));

const initiatives = [
  { slug: 'ovf-wide-title', title: wide(200), tags: ['ovf'] },
  { slug: 'ovf-narrow-title', title: wide(200, 'i'), tags: ['ovf'] },
  { slug: `ovf${"w".repeat(37)}`, title: 'Slug of the longest length', tags: ['ovf'] },
  { slug: 'ovf-max-tags', title: 'Twenty tags of forty characters', tags },
  { slug: 'ovf-url-title', title: `https://example.invalid/${'a'.repeat(176)}`, tags: ['ovf'] },
  { slug: 'ovf-mixed', title: `${wide(100)} ${wide(98, 'M')}`, tags },
];
const keyNames = [`ovf${wide(61)}`, `ovf${wide(61, 'i')}`, `ovf.${'_-.'.repeat(19)}x`];

async function overflow(page: Page) {
  return page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
    bodyScrollWidth: document.body.scrollWidth,
  }));
}

async function expectNoSidewaysScroll(page: Page) {
  const m = await overflow(page);
  expect(m.scrollWidth).toBeLessThanOrEqual(m.clientWidth);
  expect(m.bodyScrollWidth).toBeLessThanOrEqual(m.clientWidth);
}

// Nothing visible reaches past the right edge of the viewport.
async function expectNothingPastRight(page: Page, selector: string, width: number) {
  const rights = await page.locator(selector).evaluateAll((els) =>
    els.filter((el) => (el as HTMLElement).checkVisibility()).map((el) => el.getBoundingClientRect().right),
  );
  expect(rights.length).toBeGreaterThan(0);
  for (const right of rights) expect(right).toBeLessThanOrEqual(width + 0.5);
}

// ---- Phase 1 (R12) -------------------------------------------------------------------------------------------------

const P1_SLUG = `ovp${'w'.repeat(37)}`; // a 40-character slug
const P1_RESOURCE = `repo:${'r'.repeat(95)}`; // a 100-character resource
const P1_ACTOR = `ovfactor${'W'.repeat(32)}`;
const longUrl = `https://example.invalid/${'a'.repeat(170)}`;
const MAX_DESCRIPTION = 20000; // internal/domain MaxDescription, the longest accepted initiative or task description
// A description of exactly the longest accepted length: one unbroken run of wide letters, then a long URL.
const longDescription = (ch: string, url: string) => `${wide(MAX_DESCRIPTION - url.length - 1, ch)} ${url}`;
const INITIATIVE_DESCRIPTION = longDescription('W', `https://example.invalid/${'b'.repeat(400)}`);
const TASK_DESCRIPTION = longDescription('M', `https://example.invalid/${'c'.repeat(400)}`);

type Seed = { watch: string; blocker: string; dependent: string; lease: string };
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

// One initiative with the longest slug, title, description (20000 characters) and tags and four tasks: a watch task (the subject) with a
// 200-character title, a description of the longest accepted length, two long links, a blocker and a dependent (both with 200-character titles), a
// milestone and a check of 200 characters, an expired lease and a live lease with a 200-character intent and 20
// resources of 100 characters; a second actor holds the same resource on the dependent, so the page shows a conflict.
async function seedPhase1(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed> {
  const api = await apiContext(playwright, baseURL);
  const other = await apiContext(playwright, baseURL, P1_ACTOR);
  const created = await api.post('/api/v1/initiatives', {
    data: { slug: P1_SLUG, title: wide(200), description: INITIATIVE_DESCRIPTION, tags },
  });
  expect([201, 409], `seed ${P1_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string, extra: object = {}) =>
    (await send(api, '/api/v1/tasks', { initiative: P1_SLUG, title, ...extra }, 201)).ref as string;
  const watch = await task(wide(200), {
    kind: 'watch',
    cadence_seconds: 900,
    description: TASK_DESCRIPTION,
    external_refs: [
      { label: wide(80), url: longUrl },
      { label: wide(80, 'i'), url: `https://example.invalid/${'i'.repeat(170)}` },
    ],
  });
  const blocker = await task(wide(200, 'i'));
  const dependent = await task(`${wide(100, 'M')} ${wide(98, 'W')}`);
  await send(api, `/api/v1/tasks/${watch}/deps/${blocker}`, {}, 200);
  await send(api, `/api/v1/tasks/${dependent}/deps/${watch}`, {}, 200);
  await send(api, `/api/v1/tasks/${watch}/milestones`, { summary: wide(200) }, 200);
  await send(api, `/api/v1/tasks/${watch}/checks`, { summary: wide(200, 'i') }, 200);
  const resources = [P1_RESOURCE, ...Array.from({ length: 19 }, (_, i) => `res${String(i).padStart(2, '0')}:${'x'.repeat(93)}`)];
  const old = await send(api, '/api/v1/leases', { task: watch, intent: wide(200, 'M'), resources: [P1_RESOURCE] }, 201);
  await expireLease(api, old.lease.ref);
  const live = await send(api, '/api/v1/leases', { task: watch, intent: wide(200), resources }, 201);
  await send(other, '/api/v1/leases', { task: dependent, intent: wide(200, 'i'), resources: [P1_RESOURCE] }, 201);
  await api.dispose();
  await other.dispose();
  return { watch, blocker, dependent, lease: live.lease.ref as string };
}

// A visible button of the page: on the phone the action bar sits above the tab bar, on the desktop in the header.
const button = (page: Page, name: string | RegExp, exact = false) => page.getByRole('button', { name, exact }).locator('visible=true').first();

const openDialog = async (page: Page, name: string) => {
  const dialog = page.getByRole('dialog', { name });
  await expect(dialog).toBeVisible();
  return dialog;
};

type Phase1State = { name: string; open: (page: Page, seed: Seed) => Promise<void>; within: string };

// The finite list of Phase 1 states this spec holds to the no-sideways-scroll bar: 3 views and 9 dialogs.
const PHASE1_STATES: Phase1State[] = [
  {
    name: 'view #/tasks',
    within: '[data-testid=task-row], [data-testid=task-row] *, [data-testid=now-conflict], [data-testid=now-conflict] *, [data-testid=now-lease], [data-testid=now-lease] *',
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P1_SLUG}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
      const toggle = page.getByTestId('now-toggle');
      if (await toggle.isVisible()) await toggle.click();
      await expect(page.locator(`[data-testid=now-conflict][data-resource="${P1_RESOURCE}"]`)).toBeVisible();
    },
  },
  {
    name: 'view #/i/<slug>',
    within: '[data-testid=initiative-page] *',
    open: async (page, seed) => {
      await page.goto(`/#/i/${P1_SLUG}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
      await expect(page.getByTestId('initiative-description')).toContainText(INITIATIVE_DESCRIPTION);
    },
  },
  {
    name: 'view #/t/<ref>',
    within: '[data-testid=task-page] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await expect(page.getByTestId('lease')).toHaveCount(1);
      await expect(page.getByTestId('conflict')).toHaveCount(1);
      await expect(page.getByTestId('expired-lease')).toHaveCount(1);
      await expect(page.getByTestId('task-links').getByRole('link')).toHaveCount(2);
      await expect(page.getByTestId('event')).toHaveCount(7);
      await expect(page.getByTestId('task-description')).toContainText(TASK_DESCRIPTION);
    },
  },
  {
    name: 'dialog edit initiative',
    within: '[role=dialog] *',
    open: async (page) => {
      await page.goto(`/#/i/${P1_SLUG}`);
      await button(page, 'Edit', true).click();
      const dialog = await openDialog(page, 'Edit initiative');
      await dialog.getByRole('textbox', { name: 'Title' }).fill(wide(200));
      await dialog.getByLabel('Description').fill(wide(MAX_DESCRIPTION));
      await expect(dialog.getByLabel('Description')).toHaveValue(wide(MAX_DESCRIPTION));
      await dialog.getByLabel('Tags').fill(tags.join(', '));
    },
  },
  {
    name: 'dialog close initiative',
    within: '[role=dialog] *',
    open: async (page) => {
      await page.goto(`/#/i/${P1_SLUG}`);
      await button(page, 'Close', true).click();
      const dialog = await openDialog(page, 'Close initiative');
      await expect(dialog.getByRole('button', { name: /^Close and drop \d+ tasks$/ })).toBeVisible();
    },
  },
  {
    name: 'dialog new task',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P1_SLUG}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.watch}]`)).toBeVisible();
      await button(page, 'New task').click();
      const dialog = await openDialog(page, 'New task');
      await dialog.getByLabel('Initiative').selectOption(P1_SLUG);
      await dialog.getByLabel('Kind').selectOption('watch');
      await dialog.getByRole('textbox', { name: 'Title' }).fill(wide(200));
      await dialog.getByLabel('Description').fill(TASK_DESCRIPTION);
      await expect(dialog.getByLabel('Description')).toHaveValue(TASK_DESCRIPTION);
      await dialog.getByLabel('Cadence').fill('1d');
      await dialog.getByLabel('Find an open task').fill(seed.blocker);
      await dialog.getByTestId('blocker-options').getByRole('button').first().click();
      await dialog.getByLabel('Link label').fill(wide(80));
      await dialog.getByLabel('Link URL').fill(longUrl);
      await dialog.getByRole('button', { name: 'Add link' }).click();
      await expect(dialog.getByTestId('links')).toBeVisible();
      await dialog.getByLabel('Find an open task').fill(seed.watch);
    },
  },
  {
    name: 'dialog edit task',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await button(page, 'Edit', true).click();
      const dialog = await openDialog(page, 'Edit task');
      await dialog.getByRole('textbox', { name: 'Title' }).fill(wide(200));
      await expect(dialog.getByLabel('Description')).toHaveValue(TASK_DESCRIPTION);
      await dialog.getByLabel('Description').fill(wide(MAX_DESCRIPTION, 'M'));
      await expect(dialog.getByLabel('Description')).toHaveValue(wide(MAX_DESCRIPTION, 'M'));
      await dialog.getByLabel('Cadence').fill('365d');
    },
  },
  {
    name: 'dialog close task',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await button(page, 'Close', true).click();
      const dialog = await openDialog(page, 'Close task');
      await expect(dialog.getByTestId('dialog-body')).toContainText(seed.watch);
    },
  },
  {
    name: 'dialog milestone',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await button(page, 'Milestone', true).click();
      const dialog = await openDialog(page, 'Record milestone');
      await dialog.getByLabel('Summary').fill(wide(200));
    },
  },
  {
    name: 'dialog record check',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await button(page, 'Record check', true).click();
      const dialog = await openDialog(page, 'Record check');
      await dialog.getByLabel('Summary').fill(wide(200, 'i'));
    },
  },
  {
    name: 'dialog add dependency',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await button(page, 'Add dependency', true).click();
      const dialog = await openDialog(page, 'Add dependency');
      await dialog.getByLabel('Find an open task').fill(P1_SLUG);
      await expect(dialog.getByTestId('dep-options')).toBeVisible();
    },
  },
  {
    name: 'dialog end lease',
    within: '[role=dialog] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.watch}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await button(page, `End lease ${seed.lease}`, true).click();
      const dialog = await openDialog(page, 'End lease');
      await dialog.getByLabel('Outcome (optional)').fill(wide(200));
    },
  },
];

// ---- Phase 2 (R13) -------------------------------------------------------------------------------------------------

const P2_SLUG = `ovq${'w'.repeat(37)}`; // a 40-character slug
const P2_ACTOR = `ovqactor${'W'.repeat(32)}`; // the actor who asks, finishes, holds and posts
const P2_HOLDER = `ovqholder${'i'.repeat(31)}`; // the second actor of the conflict
const P2_WAY_BACK = `ralphctl status ${'W'.repeat(184)}`; // 200 characters, nowhere to break
const P2_HOLDER_WAY_BACK = `ralphctl status ${'i'.repeat(184)}`;
const P2_RESOURCE = `repo:${'q'.repeat(95)}`; // a 100-character resource
const P2_RESOURCES = [P2_RESOURCE, ...Array.from({ length: 19 }, (_, i) => `res${String(i).padStart(2, '0')}:${'y'.repeat(93)}`)];
const P2_OPTIONS = Array.from({ length: 10 }, (_, i) => `${i}${'W'.repeat(79)}`); // 10 options of 80 characters
const P2_MENTIONS = Array.from({ length: 20 }, (_, i) => `m${String(i).padStart(2, '0')}${'W'.repeat(97)}`); // 20 handles of 100 characters
const ASK_DETAILS = longDescription('W', `https://example.invalid/${'d'.repeat(400)}`);
const NOTE_BODY = longDescription('M', `https://example.invalid/${'n'.repeat(400)}`);

type Seed2 = { ask: string; askTask: string; doneTask: string; lease: string; watch: string; note: string };
let seeded2: Promise<Seed2> | undefined;

async function actorContext(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, actor: string, wayBack: string) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor, 'Board-Actor-Way-Back': wayBack },
  });
}

// One initiative with the longest slug, title and tags and one item in each of the five Attention sections: an open blocking ask with a
// 200-character question, details of the longest accepted length and 10 options of 80 characters; a done task with a 200-character title;
// an expired lease with a 200-character intent and 20 resources of 100 characters; an overdue watch with a 200-character title; and a
// conflict on a 100-character resource between two actors with 40-character handles and 200-character ways back. Notes of the longest
// accepted summary, body, resource and mentions, on a task, on the initiative and as a broadcast.
async function seedPhase2(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed2> {
  const a = await actorContext(playwright, baseURL, P2_ACTOR, P2_WAY_BACK);
  const b = await actorContext(playwright, baseURL, P2_HOLDER, P2_HOLDER_WAY_BACK);
  // A 409 is a second seeding by a worker the runner restarted after a failure: the initiative stays, its items are made again.
  const created = await a.post('/api/v1/initiatives', { data: { slug: P2_SLUG, title: wide(200), tags } });
  expect([201, 409], `seed ${P2_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string, extra: object = {}) => (await send(a, '/api/v1/tasks', { initiative: P2_SLUG, title, ...extra }, 201)).ref as string;

  const askTask = await task(wide(200));
  const ask = (await send(a, '/api/v1/asks', { question: wide(200), details: ASK_DETAILS, options: P2_OPTIONS, task: askTask, mode: 'poll', blocking: true }, 201)).ref as string;

  const doneTask = await task(wide(200, 'i'));
  await send(a, `/api/v1/tasks/${doneTask}/done`, {}, 200);

  const leaseTask = await task(`${wide(100, 'M')} ${wide(98, 'W')}`);
  const old = await send(a, '/api/v1/leases', { task: leaseTask, intent: wide(200, 'M'), resources: P2_RESOURCES }, 201);
  await expireLease(a, old.lease.ref);

  const watch = await task(wide(200, 'M'), { kind: 'watch', cadence_seconds: 900, next_check_at: '2020-01-01T00:00:00Z' });

  const first = await task(wide(200));
  const second = await task(wide(200, 'i'));
  await send(a, '/api/v1/leases', { task: first, intent: wide(200), resources: [P2_RESOURCE] }, 201);
  await send(b, '/api/v1/leases', { task: second, intent: wide(200, 'i'), resources: [P2_RESOURCE] }, 201);

  const note = async (extra: object) =>
    (await send(a, '/api/v1/notes', { summary: wide(200), body: NOTE_BODY, resource: P2_RESOURCE, mentions: P2_MENTIONS, ...extra }, 201)).ref as string;
  const onTask = await note({ task: askTask });
  await note({ initiative: P2_SLUG, summary: wide(200, 'i') });
  await send(a, '/api/v1/notes', { summary: wide(200, 'M'), body: NOTE_BODY, resource: P2_RESOURCE, mentions: P2_MENTIONS }, 201);
  await a.dispose();
  await b.dispose();
  return { ask, askTask, doneTask, lease: old.lease.ref as string, watch, note: onTask };
}

const attentionItem = (page: Page, text: string) => page.getByTestId('attention-item').filter({ hasText: text });

const openAttention = async (page: Page, seed: Seed2) => {
  await showAllBands(page);
  await page.goto(`/#/now?initiative=${P2_SLUG}`);
  await expect(page.getByTestId('band-count')).toHaveCount(4);
  await expect(attentionItem(page, seed.ask)).toBeVisible();
};

const ATTENTION_WITHIN = '[data-testid=now-view], [data-testid=now-view] *';
const DIALOG_WITHIN = '[role=dialog] *';

// hash: the route the state opens at, so the 1024 px pass can pick the Now states.
type Phase2State = { name: string; hash: string; open: (page: Page, seed: Seed2) => Promise<void>; within: string };

// The finite list of Phase 2 states this spec holds to the no-sideways-scroll bar: 2 views, 6 dialogs and the live indicator.
const PHASE2_STATES: Phase2State[] = [
  {
    name: 'view #/now',
    hash: '#/now',
    within: ATTENTION_WITHIN,
    open: async (page, seed) => {
      await openAttention(page, seed);
      for (const key of ['open_asks', 'done_not_closed', 'expired_leases', 'overdue_watches', 'conflicts'] as const) {
        await expect(items(page, key).first()).toBeVisible();
      }
      await expect(page.getByTestId('band-empty')).toHaveCount(0);
      await expect(page.getByTestId('actor-handle').filter({ hasText: P2_ACTOR }).first()).toBeVisible();
      await expect(page.getByTestId('actor-way-back').filter({ hasText: P2_WAY_BACK }).first()).toBeVisible();
      await expect(page.getByTestId('actor-handle').filter({ hasText: P2_HOLDER }).first()).toBeVisible();
    },
  },
  {
    name: 'view #/notes',
    hash: '#/notes',
    within: '[data-testid=notes-view], [data-testid=notes-view] *',
    open: async (page, seed) => {
      await page.goto(`/#/notes?initiative=${P2_SLUG}`);
      await expect(page.getByTestId('notes-list').getByTestId('note').first()).toBeVisible();
      await page.getByRole('button', { name: `Show body of ${seed.note}` }).click();
      await expect(page.getByTestId('note-body').first()).toContainText(NOTE_BODY);
      const filters = page.getByTestId('notes-filters');
      for (const label of ['Task', 'Initiative', 'Resource', 'Author', 'Mention']) await filters.getByLabel(label, { exact: true }).fill(wide(100));
    },
  },
  {
    name: 'view #/notes broadcasts',
    hash: '#/notes',
    within: '[data-testid=notes-view], [data-testid=notes-view] *',
    open: async (page) => {
      await page.goto(`/#/notes?broadcast=only&resource=${P2_RESOURCE}`);
      await expect(page.getByTestId('notes-list').getByTestId('note-scope').first()).toHaveText('broadcast');
      await page.getByRole('button', { name: /^Show body of / }).first().click();
      await expect(page.getByTestId('note-body').first()).toContainText(NOTE_BODY);
    },
  },
  {
    name: 'dialog answer ask',
    hash: '#/now',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.ask).getByRole('button', { name: `Answer ${seed.ask}` }).click();
      const dialog = await openDialog(page, `Answer ${seed.ask}`);
      await expect(page.getByTestId('answer-option')).toHaveCount(10);
      await expect(page.getByTestId('answer-details')).toContainText(ASK_DETAILS);
      await dialog.getByRole('textbox', { name: 'Answer' }).fill(wide(MAX_DESCRIPTION));
    },
  },
  {
    name: 'dialog withdraw ask',
    hash: '#/now',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.ask).getByRole('button', { name: `Withdraw ${seed.ask}` }).click();
      const dialog = await openDialog(page, `Withdraw ${seed.ask}`);
      await dialog.getByRole('textbox', { name: 'Reason (optional)' }).fill(wide(200));
    },
  },
  {
    name: 'dialog post note',
    hash: '#/notes',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await page.goto(`/#/notes?initiative=${P2_SLUG}`);
      await expect(page.getByTestId('notes-list').getByTestId('note').first()).toBeVisible();
      await button(page, 'Post note', true).click();
      const dialog = await openDialog(page, 'Post note');
      await dialog.getByLabel('Task (optional)').fill(seed.askTask);
      await dialog.getByLabel('Initiative (optional)').fill(P2_SLUG);
      await dialog.getByLabel('Summary').fill(wide(200));
      await dialog.getByLabel('Body (optional, Markdown)').fill(NOTE_BODY);
      await dialog.getByLabel('Resource (optional)').fill(P2_RESOURCE);
      await dialog.getByLabel('Mentions (optional)').fill(P2_MENTIONS.join(' '));
    },
  },
  {
    name: 'dialog close from attention',
    hash: '#/now',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.doneTask).getByRole('button', { name: `Close ${seed.doneTask}` }).click();
      const dialog = await openDialog(page, 'Close task');
      await dialog.getByLabel('Resolution').selectOption('duplicate');
    },
  },
  {
    name: 'dialog record check from attention',
    hash: '#/now',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await openAttention(page, seed);
      await attentionItem(page, seed.watch).getByRole('button', { name: `Record check ${seed.watch}` }).click();
      const dialog = await openDialog(page, 'Record check');
      await dialog.getByRole('textbox', { name: 'Summary' }).fill(wide(200, 'i'));
    },
  },
];

// ---- Phase 3 (R12) -------------------------------------------------------------------------------------------------

const P3_SLUG = `ovr${'w'.repeat(37)}`; // a 40-character slug
const P3_CLOSED_SLUG = `ovs${'w'.repeat(37)}`; // a closed initiative of the Archive
const P3_ACTOR = `ovractor${'W'.repeat(32)}`; // holds, asks and closes
const P3_HOLDER = `ovrholder${'i'.repeat(31)}`; // the other actor, whose live lease makes Done a force-done
const P3_WAY_BACK = `ralphctl status ${'W'.repeat(184)}`; // 200 characters, nowhere to break
const P3_HOLDER_WAY_BACK = `ralphctl status ${'i'.repeat(184)}`;
const P3_RESOURCES = Array.from({ length: 20 }, (_, i) => `res${String(i).padStart(2, '0')}:${'z'.repeat(93)}`); // 20 resources of 100 characters

type Seed3 = { stranded: string; lease: string; done: string; held: string; forced: string; closed: string };
let seeded3: Promise<Seed3> | undefined;

// One initiative with the longest slug, title and tags and a task of 200 characters in each state the Phase 3 views show: a stranded
// task (an expired lease of 20 resources, never acknowledged), a done task, a task with an open ask under a live lease of a 40-character
// handle, a task under another actor's live lease (so Done asks first), two plain tasks; a task closed as superseded by a 40-character
// handle, and a second initiative closed, for the Archive. Both actors leave a 200-character way back.
async function seedPhase3(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<Seed3> {
  const a = await actorContext(playwright, baseURL, P3_ACTOR, P3_WAY_BACK);
  const b = await actorContext(playwright, baseURL, P3_HOLDER, P3_HOLDER_WAY_BACK);
  for (const slug of [P3_SLUG, P3_CLOSED_SLUG]) {
    const created = await a.post('/api/v1/initiatives', { data: { slug, title: wide(200, slug === P3_SLUG ? 'W' : 'M'), tags } });
    expect([201, 409], `seed ${slug}: ${created.status()}`).toContain(created.status());
  }
  const task = async (title: string) => (await send(a, '/api/v1/tasks', { initiative: P3_SLUG, title }, 201)).ref as string;

  const stranded = await task(wide(200));
  const old = await send(a, '/api/v1/leases', { task: stranded, intent: wide(200, 'M'), resources: P3_RESOURCES }, 201);
  await expireLease(a, old.lease.ref);

  const done = await task(wide(200, 'i'));
  await send(a, `/api/v1/tasks/${done}/done`, {}, 200);

  const held = await task(wide(200, 'M'));
  await send(a, '/api/v1/leases', { task: held, intent: wide(200), resources: P3_RESOURCES }, 201);
  await send(a, '/api/v1/asks', { question: wide(200), task: held }, 201);

  const forced = await task(`${wide(100, 'M')} ${wide(98, 'W')}`);
  await send(b, '/api/v1/leases', { task: forced, intent: wide(200, 'i'), resources: P3_RESOURCES }, 201);

  await task(wide(200, 'W'));
  await task(wide(200, 'i'));

  const closed = await task(wide(200, 'M'));
  await send(a, `/api/v1/tasks/${closed}/close`, { resolution: 'superseded' }, 200);
  // A 409 is the initiative already closed by a worker the runner restarted after a failure.
  const closedInitiative = await a.post(`/api/v1/initiatives/${P3_CLOSED_SLUG}/close`, { data: {} });
  expect([200, 409], `close ${P3_CLOSED_SLUG}: ${closedInitiative.status()}`).toContain(closedInitiative.status());
  await a.dispose();
  await b.dispose();
  return { stranded, lease: old.lease.ref as string, done, held, forced, closed };
}

const NEEDS_WITHIN = '[data-testid=needs-attention], [data-testid=needs-attention] *';
const sortedHeader = (page: Page) => expect(page.locator('table th[aria-sort=descending]')).toHaveCount(1);

type Phase3State = { name: string; open: (page: Page, seed: Seed3) => Promise<void>; within: string };

// The finite list of Phase 3 states this spec holds to the no-sideways-scroll bar: 3 views, the Needs attention sections, the
// Stranded panel, the attention chips and badges, 3 dialogs and 6 sorted tables.
const PHASE3_STATES: Phase3State[] = [
  {
    name: 'view #/timeline',
    within: '[data-testid=timeline-filters], [data-testid=timeline-filters] *, [data-testid=timeline-chart]',
    open: async (page) => {
      await page.goto(`/#/timeline?initiative=${P3_SLUG}&actor=${P3_ACTOR}&tag=${tag(0)}`);
      await expect(page.getByTestId('timeline-chart')).toBeVisible();
      await expect(page.locator('.vis-labelset .vis-label').first()).toBeVisible();
      await expectInitiatives(page, [P3_SLUG]);
      await expect(page.getByTestId('actor-filter')).toHaveValue(P3_ACTOR);
    },
  },
  {
    name: 'view #/actors',
    within: '[data-testid=actors-view], [data-testid=actors-view] *',
    open: async (page) => {
      await page.goto('/#/actors');
      await expect(page.locator(`[data-testid=actor-row][data-handle="${P3_ACTOR}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=actor-row][data-handle="${P3_HOLDER}"]`).getByTestId('actor-way-back')).toHaveText(P3_HOLDER_WAY_BACK);
      await expect(page.locator(`[data-testid=actor-row][data-handle="${P3_ACTOR}"]`).getByTestId('actor-lease').first()).toBeVisible();
    },
  },
  {
    name: 'view #/archive',
    within: '[data-testid=archive-view], [data-testid=archive-view] *',
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      await expect(page.locator(`[data-testid=archive-row][data-kind=task][data-ref="${seed.closed}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=archive-row][data-kind=initiative][data-ref="${P3_CLOSED_SLUG}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`).getByTestId('archive-closed-by')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Needs attention on #/tasks',
    within: NEEDS_WITHIN,
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}`);
      for (const ref of [seed.stranded, seed.done, seed.held]) await expect(page.locator(`[data-testid=needs-row][data-ref="${ref}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=needs-row][data-ref="${seed.held}"]`).getByTestId('needs-holder')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Needs attention on #/i/<slug>',
    within: NEEDS_WITHIN,
    open: async (page, seed) => {
      await page.goto(`/#/i/${P3_SLUG}`);
      for (const ref of [seed.stranded, seed.done, seed.held]) await expect(page.locator(`[data-testid=needs-row][data-ref="${ref}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=needs-row][data-ref="${seed.held}"]`).getByTestId('needs-holder')).toContainText(P3_ACTOR);
    },
  },
  {
    name: 'Stranded panel on #/t/<ref>',
    within: '[data-testid=stranded-panel], [data-testid=stranded-panel] *',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      const panel = page.getByTestId('stranded-panel');
      await expect(panel).toBeVisible();
      await expect(panel.getByTestId('stranded-holder')).toHaveText(P3_ACTOR);
      await expect(panel.getByTestId('stranded-intent')).toHaveText(wide(200, 'M'));
      await expect(panel.getByTestId('stranded-way-back')).toHaveText(P3_WAY_BACK);
    },
  },
  {
    name: 'attention chip on #/initiatives',
    within: `[data-testid=initiative-row][data-slug=${P3_SLUG}], [data-testid=initiative-row][data-slug=${P3_SLUG}] *`,
    open: async (page) => {
      await page.goto('/#/initiatives');
      const row = page.locator(`[data-testid=initiative-row][data-slug=${P3_SLUG}]`);
      await expect(row.getByTestId('initiative-attention')).toBeVisible();
      await expect(row.getByTestId('initiative-attention')).toContainText(/need(s)? attention/);
    },
  },
  {
    name: 'attention badges on #/t/<ref>',
    within: '[data-testid=task-page] [data-testid^=attention-]',
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await expect(page.getByTestId('task-title')).toHaveText(wide(200));
      await expect(page.getByTestId('task-page').getByTestId('attention-stranded')).toBeVisible();
    },
  },
  {
    name: 'attention badge in the navigation',
    within: '[data-testid=attention-badge]',
    open: async (page) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}`);
      await expect(page.locator('[data-testid=attention-badge]').locator('visible=true').first()).toBeVisible();
    },
  },
  {
    name: 'dialog acknowledge on #/t/<ref>',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await page.getByTestId('stranded-panel').getByRole('button', { name: `Acknowledge lease ${seed.lease}` }).click();
      await openDialog(page, `Acknowledge lease ${seed.lease}`);
    },
  },
  {
    name: 'dialog archive Reopen',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      await page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`).getByRole('button', { name: `Reopen task ${seed.closed}` }).click();
      await openDialog(page, 'Reopen task');
    },
  },
  {
    name: 'dialog force-done confirm',
    within: DIALOG_WITHIN,
    open: async (page, seed) => {
      await page.goto(`/#/t/${seed.forced}`);
      await expect(page.getByTestId('task-title')).toHaveText(`${wide(100, 'M')} ${wide(98, 'W')}`);
      await button(page, 'Done', true).click();
      const dialog = await openDialog(page, `Mark ${seed.forced} done`);
      await expect(dialog.getByTestId('dialog-body')).toContainText(P3_HOLDER);
    },
  },
  {
    name: 'sorted Initiatives table',
    within: '[data-testid=initiative-row], [data-testid=initiative-row] *',
    open: async (page) => {
      await page.goto('/#/initiatives?sort=title&dir=desc');
      await expect(page.locator('[data-testid=initiative-row][data-slug=ovf-wide-title]')).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('title:desc'); // the shared sort control
    },
  },
  {
    name: 'sorted Tasks table',
    within: '[data-testid=task-row], [data-testid=task-row] *',
    open: async (page, seed) => {
      await page.goto(`/#/tasks?initiative=${P3_SLUG}&sort=holder&dir=desc`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.forced}]`)).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('holder:desc'); // Tasks sorts through the shared sort control
    },
  },
  {
    name: 'sorted initiative page Tasks table',
    within: '[data-testid=initiative-page] table, [data-testid=initiative-page] table *',
    open: async (page, seed) => {
      await page.goto(`/#/i/${P3_SLUG}?sort=holder&dir=desc`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${seed.forced}]`)).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('holder:desc'); // the shared sort control
    },
  },
  {
    name: 'sorted API keys table',
    within: '[data-testid=key-row], [data-testid=key-row] *',
    open: async (page) => {
      await page.goto('/#/settings/keys?sort=name&dir=desc');
      for (const name of keyNames) await expect(page.locator('[data-testid=key-row]', { hasText: name })).toHaveCount(1);
      await sortedHeader(page);
    },
  },
  {
    name: 'sorted Actors table',
    within: '[data-testid=actor-row], [data-testid=actor-row] *',
    open: async (page) => {
      await page.goto('/#/actors?sort=leases&dir=desc');
      await expect(page.locator(`[data-testid=actor-row][data-handle="${P3_ACTOR}"]`)).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('leases:desc'); // the shared sort control
    },
  },
  {
    name: 'sorted Archive table',
    within: '[data-testid=archive-row], [data-testid=archive-row] *',
    open: async (page, seed) => {
      await page.goto('/#/archive?window=7&sort=item&dir=desc');
      await expect(page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`)).toBeVisible();
      await expect(page.getByTestId('sort-select')).toHaveValue('item:desc'); // the shared sort control
    },
  },
];

// The wide viewports (SPEC 11, Wide layout) and the four views held to the same bar there: the Tasks view, the task page, the
// initiative page (Phase 1 seed) and Attention (Phase 2 seed).
const WIDE_VIEWPORTS = [
  { name: 'wide', size: { width: 2560, height: 1080 } },
  { name: 'ultrawide', size: { width: 3440, height: 1440 } },
] as const;
const WIDE_STATE_NAMES = ['view #/tasks', 'view #/i/<slug>', 'view #/t/<ref>', 'view #/now'];

const LIVE_STATES = ['live', 'offline', 'reconnecting'] as const;

// The stream never opens: the indicator stays on `reconnecting`.
async function neverOpen(page: Page) {
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

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    // Added to every document before its scripts run, so the styles are there from the first paint of every state.
    test.beforeEach(async ({ page }) => {
      if (vp.css === '') return;
      await page.addInitScript((css) => {
        const add = () => {
          const style = document.createElement('style');
          style.setAttribute('data-testid', 'wide-glyphs');
          style.textContent = css;
          document.head.appendChild(style);
        };
        if (document.head) add();
        else document.addEventListener('DOMContentLoaded', add);
      }, vp.css);
    });

    // The 1024 px pass runs the Now states only (R9 j): everything declared through general() is left out of it.
    const general = (title: string, body: Parameters<typeof test>[1]) => {
      if (!vp.nowOnly) test(title, body);
    };

    test.beforeAll(async ({ playwright, baseURL }) => {
      const seed = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      for (const body of initiatives) {
        const res = await seed.post('/api/v1/initiatives', { data: body });
        expect([201, 409], `seed ${body.slug}: ${res.status()}`).toContain(res.status());
      }
      for (const name of keyNames) {
        const res = await seed.post('/api/v1/keys', { data: { name } });
        expect([201, 409], `seed key ${name}: ${res.status()}`).toContain(res.status());
      }
      await seed.dispose();
    });

    general(`the ${vp.name} pass sets the page ${vp.css === '' ? 'in the font as it is' : 'with wider glyphs'}`, async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.locator('[data-testid=initiative-row]').first()).toBeVisible();
      const spacing = await page.evaluate(() => getComputedStyle(document.body).letterSpacing);
      if (vp.css === '') expect(spacing).toBe('normal');
      else expect(spacing).not.toBe('normal');
    });

    general('the Initiatives list holds the longest accepted titles, slugs and tags without scrolling sideways', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.locator('[data-testid=initiative-row][data-slug=ovf-wide-title]')).toBeVisible();
      for (const i of initiatives) await expect(page.locator(`[data-testid=initiative-row][data-slug=${i.slug}]`)).toHaveCount(1);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=initiative-row]', vp.size.width);
      await expectNothingPastRight(page, '[data-testid=initiative-row] [data-testid=tag]', vp.size.width);
      // The text is all still there: wrapped, never cut off.
      await expect(page.locator('[data-testid=initiative-row][data-slug=ovf-wide-title]')).toContainText(wide(200));
    });

    general('the Tasks view and the New task sheet hold the longest accepted values without scrolling sideways', async ({ page, playwright, baseURL }) => {
      const slug = `ovf${'w'.repeat(37)}`;
      const seed = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      const made = await seed.post('/api/v1/tasks', { data: { initiative: slug, title: wide(200) } });
      expect(made.status()).toBe(201);
      const { ref } = (await made.json()) as { ref: string };
      await seed.dispose();

      await page.goto(`/#/tasks?initiative=${slug}`);
      await expect(page.locator(`[data-testid=task-row][data-ref=${ref}]`)).toBeVisible();
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=task-row], [data-testid=task-row] *', vp.size.width);

      await page.getByRole('button', { name: 'New task' }).locator('visible=true').click();
      const dialog = page.getByRole('dialog', { name: 'New task' });
      await dialog.getByLabel('Initiative').selectOption(slug);
      await dialog.getByRole('textbox', { name: 'Title' }).fill(wide(200));
      await dialog.getByLabel('Find an open task').fill(ref);
      await dialog.getByTestId('blocker-options').getByRole('button').first().click();
      await dialog.getByLabel('Link label').fill(wide(80));
      await dialog.getByLabel('Link URL').fill(`https://example.invalid/${'a'.repeat(170)}`);
      await dialog.getByRole('button', { name: 'Add link' }).click();
      await expect(dialog.getByTestId('links')).toBeVisible();
      await dialog.getByLabel('Find an open task').fill(ref);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[role=dialog] *', vp.size.width);
    });

    general('the API keys list holds the longest accepted key names without scrolling sideways', async ({ page }) => {
      await page.goto('/#/settings/keys');
      for (const name of keyNames) await expect(page.locator(`[data-testid=key-row]`, { hasText: name })).toHaveCount(1);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=key-row]', vp.size.width);
      await expect(page.locator('[data-testid=key-row]', { hasText: keyNames[0] })).toContainText(keyNames[0]);
    });

    general('the revoke confirmation names a 64-character key without scrolling sideways', async ({ page }) => {
      await page.goto('/#/settings/keys');
      const row = page.locator('[data-testid=key-row]', { hasText: keyNames[0] });
      await row.getByRole('button', { name: 'Revoke' }).click();
      const dialog = page.getByRole('dialog', { name: 'Revoke API key' });
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText(keyNames[0]);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[role=dialog] *', vp.size.width);
    });

    general('creating an initiative with the longest slug shows a toast that stays inside the viewport', async ({ page }) => {
      await page.goto('/#/initiatives');
      await page.getByRole('button', { name: 'New initiative' }).locator('visible=true').click();
      const dialog = page.getByRole('dialog', { name: 'New initiative' });
      const slug = `ovf-toast-${vp.name}${'x'.repeat(40)}`.slice(0, 40);
      await dialog.getByLabel('Slug').fill(slug);
      await dialog.getByRole('textbox', { name: 'Title' }).fill(wide(200));
      await dialog.getByLabel('Tags').fill(tags.join(', '));
      await expectNoSidewaysScroll(page);
      await dialog.getByRole('button', { name: 'Create initiative' }).click();
      await expect(page.getByTestId('toast')).toContainText(slug);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=toast]', vp.size.width);
    });

    general('a freshly created 64-character key shows once without scrolling sideways', async ({ page }) => {
      await page.goto('/#/settings/keys');
      await page.getByRole('button', { name: 'New API key' }).locator('visible=true').click();
      const dialog = page.getByRole('dialog', { name: 'New API key' });
      const name = `ovfnew${vp.name}${wide(64 - 6 - vp.name.length)}`;
      await dialog.getByLabel('Name').fill(name);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      await expect(page.getByTestId('new-key')).toContainText(name);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=new-key], [data-testid=new-key] *', vp.size.width);
    });

    for (const state of PHASE1_STATES) {
      general(`${state.name} holds the longest accepted values without scrolling sideways`, async ({ page, playwright, baseURL }) => {
        seeded ??= seedPhase1(playwright, baseURL);
        const seed = await seeded;
        await state.open(page, seed);
        await expectNoSidewaysScroll(page);
        await expectNothingPastRight(page, state.within, vp.size.width);
      });
    }

    for (const state of PHASE2_STATES.filter((s) => !vp.nowOnly || s.hash === '#/now')) {
      test(`${state.name} holds the longest accepted values without scrolling sideways`, async ({ page, playwright, baseURL }) => {
        seeded2 ??= seedPhase2(playwright, baseURL);
        const seed = await seeded2;
        await state.open(page, seed);
        await expectNoSidewaysScroll(page);
        await expectNothingPastRight(page, state.within, vp.size.width);
      });
    }

    for (const state of PHASE3_STATES) {
      general(`${state.name} holds the longest accepted values without scrolling sideways`, async ({ page, playwright, baseURL }) => {
        seeded3 ??= seedPhase3(playwright, baseURL);
        const seed = await seeded3;
        await state.open(page, seed);
        await expectNoSidewaysScroll(page);
        await expectNothingPastRight(page, state.within, vp.size.width);
      });
    }

    test('dialog acknowledge: its sheet, the toast naming the lease and the Attention view hold the longest values without scrolling sideways', async ({ page, playwright, baseURL }) => {
      seeded2 ??= seedPhase2(playwright, baseURL);
      const seed = await seeded2;
      const api = await actorContext(playwright, baseURL, P2_ACTOR, P2_WAY_BACK);
      const taskRef = (await send(api, '/api/v1/tasks', { initiative: P2_SLUG, title: wide(200, 'M') }, 201)).ref as string;
      const lease = (await send(api, '/api/v1/leases', { task: taskRef, intent: wide(200), resources: P2_RESOURCES }, 201)).lease.ref as string;
      await expireLease(api, lease);
      await openAttention(page, seed);
      const item = attentionItem(page, lease);
      await expect(item).toBeVisible();
      await expectNoSidewaysScroll(page);
      await item.getByRole('button', { name: `Acknowledge ${lease}` }).click();
      const dialog = await openDialog(page, `Acknowledge ${lease}`);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, DIALOG_WITHIN, vp.size.width);
      await dialog.getByRole('button', { name: 'Acknowledge lease' }).click();
      await expect(page.getByTestId('toast')).toHaveText(`Lease ${lease} acknowledged`);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=toast]', vp.size.width);
      await expectNothingPastRight(page, ATTENTION_WITHIN, vp.size.width);
      await api.dispose();
    });

    // The Tasks view's Needs attention section (R2): a task with an open ask whose live lease is held by a 40-character
    // handle, a done task and a blocking ask, all in the 40-character-slug initiative, so each card carries the longest
    // ref and initiative link, and one the longest holder.
    general('the Needs attention section on the Tasks view holds the longest refs, slugs and holders without scrolling sideways', async ({ page, playwright, baseURL }) => {
      seeded2 ??= seedPhase2(playwright, baseURL);
      const seed = await seeded2;
      const api = await actorContext(playwright, baseURL, P2_ACTOR, P2_WAY_BACK);
      const held = (await send(api, '/api/v1/tasks', { initiative: P2_SLUG, title: wide(200, 'M') }, 201)).ref as string;
      await send(api, '/api/v1/leases', { task: held, intent: wide(200), resources: [P2_RESOURCE] }, 201);
      await send(api, '/api/v1/asks', { question: wide(200), task: held }, 201);
      await page.goto(`/#/tasks?initiative=${P2_SLUG}`);
      for (const ref of [held, seed.doneTask, seed.askTask]) await expect(page.locator(`[data-testid=needs-row][data-ref="${ref}"]`)).toBeVisible();
      await expect(page.locator(`[data-testid=needs-row][data-ref="${held}"]`).getByTestId('needs-holder')).toContainText(P2_ACTOR);
      await expectNoSidewaysScroll(page);
      await expectNothingPastRight(page, '[data-testid=needs-row], [data-testid=needs-row] *', vp.size.width);
      await api.dispose();
    });

    for (const word of LIVE_STATES) {
      test(`the live indicator reading ${word} stays inside the viewport without scrolling sideways`, async ({ page, context }) => {
        if (word === 'reconnecting') await neverOpen(page);
        await page.goto('/#/now');
        const indicator = page.getByRole('status', { name: /^Live updates:/ });
        await expect(indicator).toBeVisible();
        if (word === 'offline') await context.setOffline(true);
        await expect(indicator).toHaveAccessibleName(`Live updates: ${word}`);
        await expect(indicator).toHaveText(word);
        await expectNoSidewaysScroll(page);
        await expectNothingPastRight(page, '[data-testid=live-indicator], [data-testid=live-indicator] *', vp.size.width);
        await context.setOffline(false);
      });
    }

    general('the Phase 3 list names the views, the Needs attention sections, the Stranded panel, the chips, the three dialogs and the six sorted tables', () => {
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
        'sorted Initiatives table',
        'sorted Tasks table',
        'sorted initiative page Tasks table',
        'sorted API keys table',
        'sorted Actors table',
        'sorted Archive table',
      ]);
    });

    general('the Phase 2 list names the views, the six dialogs and the three live indicator states', () => {
      const names = PHASE2_STATES.map((s) => s.name);
      expect(names.filter((n) => n.startsWith('view '))).toEqual(['view #/now', 'view #/notes', 'view #/notes broadcasts']);
      // Acknowledge has its own test above: its sheet, then the toast it raises, over a lease with the longest values.
      expect(names.filter((n) => n.startsWith('dialog ')).map((n) => n.slice('dialog '.length))).toEqual([
        'answer ask',
        'withdraw ask',
        'post note',
        'close from attention',
        'record check from attention',
      ]);
      expect(LIVE_STATES).toEqual(['live', 'offline', 'reconnecting']);
    });

    general('the Phase 1 list names the 3 views and the 9 dialogs', () => {
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
}

for (const vp of WIDE_VIEWPORTS) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    for (const name of WIDE_STATE_NAMES) {
      test(`${name} holds the longest accepted values without scrolling sideways`, async ({ page, playwright, baseURL }) => {
        seeded ??= seedPhase1(playwright, baseURL);
        seeded2 ??= seedPhase2(playwright, baseURL);
        const state = [...PHASE1_STATES, ...PHASE2_STATES].find((s) => s.name === name)!;
        expect(state, name).toBeDefined();
        const seed = (state.within === ATTENTION_WITHIN ? await seeded2 : await seeded) as Seed & Seed2;
        await state.open(page, seed);
        await expectNoSidewaysScroll(page);
        await expectNothingPastRight(page, state.within, vp.size.width);
      });
    }
  });
}
