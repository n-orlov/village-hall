import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { showAllBands } from './now';
import { expect, type APIRequestContext, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test as base } from './fixtures';

// R8: the four dialogs/sheets of the R4 set (new initiative, new API key, revoke confirmation, account menu),
// opened by keyboard only, at the phone and the desktop viewport. Signs in through the mock provider, so no API
// key rides along (see login.spec.ts). Only the backdrop click uses the pointer: that is what it tests.
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 }, nav: 'nav-tabbar' },
  { name: 'desktop', size: { width: 1440, height: 900 }, nav: 'nav-sidebar' },
] as const;

/** A short fingerprint of the focused element, to tell stops apart. */
const focused = (page: Page) =>
  page.evaluate(() => {
    const el = document.activeElement;
    if (!el || el === document.body) return 'body';
    const label = el.getAttribute('name') ?? el.getAttribute('aria-label') ?? el.textContent?.trim().slice(0, 20) ?? '';
    return `${el.tagName.toLowerCase()}[${label}${el instanceof HTMLInputElement ? ':' + el.value : ''}]`;
  });

const focusIsInside = (page: Page) => page.evaluate(() => document.activeElement?.closest('[role=dialog]') != null);

/** Tab from wherever focus is until `target` has it. Keyboard only; fails when it is not reached. */
async function tabTo(page: Page, target: Locator) {
  for (let i = 0; i < 150; i++) {
    await page.keyboard.press('Tab');
    if (await target.evaluate((el) => el === document.activeElement)) return;
  }
  throw new Error('the keyboard never reached the target');
}

/** 12 presses (more than any of the four dialogs has tab stops): focus stays inside and the sequence wraps. */
async function cycle(page: Page, key: 'Tab' | 'Shift+Tab') {
  const stops: string[] = [];
  for (let i = 0; i < 12; i++) {
    await page.keyboard.press(key);
    expect(await focusIsInside(page), `${key} press ${i + 1} keeps focus inside the dialog (on ${await focused(page)})`).toBe(true);
    stops.push(await focused(page));
  }
  expect(new Set(stops).size, `${key} visits more than one stop`).toBeGreaterThan(1);
  expect(stops.slice(1), `${key} wraps around: the first stop comes round again`).toContain(stops[0]);
}

type Case = {
  name: string;
  /** Opens nothing: where to go first, and the opener to reach by Tab. */
  setup: (page: Page, nav: string) => Promise<Locator>;
  dialog: string;
};

const cases: Case[] = [
  {
    name: 'new-initiative sheet',
    dialog: 'New initiative',
    setup: async (page) => page.getByRole('button', { name: 'New initiative' }).locator('visible=true'),
  },
  {
    name: 'new-key sheet',
    dialog: 'New API key',
    setup: async (page) => {
      await page.goto('/#/settings/keys');
      await expect(page.getByTestId('key-row').first()).toBeVisible();
      return page.getByRole('button', { name: 'New API key' }).locator('visible=true');
    },
  },
  {
    name: 'revoke confirmation',
    dialog: 'Revoke API key',
    setup: async (page) => {
      await page.goto('/#/settings/keys');
      await expect(page.getByTestId('key-row').first()).toBeVisible();
      return page.getByTestId('key-row').getByRole('button', { name: 'Revoke' }).locator('visible=true').first();
    },
  },
  {
    name: 'account menu',
    dialog: 'Account',
    setup: async (page) => page.getByRole('button', { name: 'Account menu' }),
  },
];

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test.beforeEach(async ({ page, playwright, baseURL }) => {
      const seed = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      const res = await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-dialogs', title: 'Dialog keyboard', tags: ['kbd'] } });
      expect([201, 409]).toContain(res.status());
      await seed.dispose();
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
    });

    for (const c of cases) {
      test(`the ${c.name}: opens by keyboard, traps focus, closes on Escape with focus back on the opener, closes on a backdrop click`, async ({ page }) => {
        const opener = await c.setup(page, vp.nav);
        await expect(opener).toBeVisible();
        const dialog = page.getByRole('dialog', { name: c.dialog });
        await expect(dialog).toBeHidden();

        // Reach the opener with Tab only and open it with Enter.
        await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
        await tabTo(page, opener);
        await page.keyboard.press('Enter');
        await expect(dialog).toBeVisible();
        await expect(dialog).toHaveAttribute('aria-modal', 'true');

        // Focus moved inside on open.
        expect(await focusIsInside(page), `focus is inside the dialog on open (on ${await focused(page)})`).toBe(true);
        await expect(opener).not.toBeFocused();

        // Tab and Shift+Tab keep focus inside, wrapping at both ends.
        await cycle(page, 'Tab');
        await cycle(page, 'Shift+Tab');

        // Escape closes it and focus is back on the opener.
        await page.keyboard.press('Escape');
        await expect(dialog).toBeHidden();
        await expect(opener).toBeFocused();

        // Open again (Enter on the opener, which has focus) and close by clicking the backdrop.
        await page.keyboard.press('Enter');
        await expect(dialog).toBeVisible();
        expect(await focusIsInside(page)).toBe(true);
        await page.getByTestId('dialog-backdrop').click({ position: { x: 5, y: 5 } });
        await expect(dialog).toBeHidden();
        await expect(opener).toBeFocused();
      });
    }
  });
}

// R12: the nine Phase 1 dialogs, each opened by keyboard only from its own opener on the page that owns it. For each:
// focus moves inside on open, Tab and Shift+Tab never leave the dialog and come round to where they started, and Escape
// closes it with focus back on the opener. Forms here have more tab stops than the four above, so the cycle runs until
// the first stop comes round again (bounded) instead of for a fixed number of presses.
const P1_SLUG = 'e2e-dlg-p1';

type P1Seed = { watch: string; lease: string };

async function apiAs(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) {
  return playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
  });
}

async function post(api: Awaited<ReturnType<typeof apiAs>>, path: string, data: object, status: number) {
  const res = await api.post(path, { data });
  expect(res.status(), `${path} ${await res.text()}`).toBe(status);
  return (await res.json()) as Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any -- seed responses are read for one field each
}

// An open initiative with an open watch task that has a live lease, so every opener of the nine dialogs exists.
async function seedPhase1(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<P1Seed> {
  const api = await apiAs(playwright, baseURL);
  const created = await api.post('/api/v1/initiatives', { data: { slug: P1_SLUG, title: 'Phase 1 dialog keyboard', tags: ['kbd'] } });
  expect([201, 409], `seed ${P1_SLUG}: ${created.status()}`).toContain(created.status());
  const watch = (await post(api, '/api/v1/tasks', { initiative: P1_SLUG, title: 'Dialog watch', kind: 'watch', cadence_seconds: 900 }, 201)).ref as string;
  const lease = (await post(api, '/api/v1/leases', { task: watch, intent: 'keyboard', resources: ['repo:dlg-p1'] }, 201)).lease.ref as string;
  await api.dispose();
  return { watch, lease };
}
let seededP1: Promise<P1Seed> | undefined;

type P1Dialog = {
  name: string;
  /** The dialog's accessible name. */
  dialog: string;
  /** Goes to the page that owns the opener and returns the opener. */
  go: (page: Page, seed: P1Seed) => Promise<Locator>;
};

const visibleButton = (page: Page, name: string, exact = true) => page.getByRole('button', { name, exact }).locator('visible=true').first();

const onInitiative = async (page: Page) => {
  await page.goto(`/#/i/${P1_SLUG}`);
  await expect(page.getByTestId('initiative-title')).toHaveText('Phase 1 dialog keyboard');
};
const onTask = async (page: Page, seed: P1Seed) => {
  await page.goto(`/#/t/${seed.watch}`);
  await expect(page.getByTestId('task-title')).toHaveText('Dialog watch');
  await expect(page.getByTestId('lease').first()).toBeVisible();
};

// The finite list of Phase 1 dialogs this spec holds to the keyboard bar.
const PHASE1_DIALOGS: P1Dialog[] = [
  { name: 'edit initiative', dialog: 'Edit initiative', go: async (page) => (await onInitiative(page), visibleButton(page, 'Edit')) },
  { name: 'close initiative', dialog: 'Close initiative', go: async (page) => (await onInitiative(page), visibleButton(page, 'Close')) },
  {
    name: 'new task',
    dialog: 'New task',
    go: async (page) => {
      await page.goto(`/#/tasks?initiative=${P1_SLUG}`);
      await expect(page.getByTestId('task-row').first()).toBeVisible();
      return visibleButton(page, 'New task');
    },
  },
  { name: 'edit task', dialog: 'Edit task', go: async (page, seed) => (await onTask(page, seed), visibleButton(page, 'Edit')) },
  { name: 'close task', dialog: 'Close task', go: async (page, seed) => (await onTask(page, seed), visibleButton(page, 'Close')) },
  { name: 'milestone', dialog: 'Record milestone', go: async (page, seed) => (await onTask(page, seed), visibleButton(page, 'Milestone')) },
  { name: 'record check', dialog: 'Record check', go: async (page, seed) => (await onTask(page, seed), visibleButton(page, 'Record check')) },
  { name: 'add dependency', dialog: 'Add dependency', go: async (page, seed) => (await onTask(page, seed), visibleButton(page, 'Add dependency')) },
  { name: 'end lease', dialog: 'End lease', go: async (page, seed) => (await onTask(page, seed), visibleButton(page, `End lease ${seed.lease}`)) },
];

/** Press `key` until the first stop comes round again (at most 80 presses); every press must leave focus inside the dialog. */
async function wrapAround(page: Page, key: 'Tab' | 'Shift+Tab') {
  const start = await focused(page);
  const stops: string[] = [start];
  for (let i = 0; i < 80; i++) {
    await page.keyboard.press(key);
    expect(await focusIsInside(page), `${key} press ${i + 1} keeps focus inside the dialog (on ${await focused(page)})`).toBe(true);
    const now = await focused(page);
    if (now === start) {
      expect(new Set(stops).size, `${key} visits more than one stop before it wraps`).toBeGreaterThan(1);
      return;
    }
    stops.push(now);
  }
  throw new Error(`${key} never came back to ${start}: ${stops.join(' > ')}`);
}

for (const vp of viewports) {
  test.describe(`Phase 1 dialogs at ${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    for (const d of PHASE1_DIALOGS) {
      test(`the ${d.name} dialog: keyboard opens it, focus is trapped inside, Escape returns focus to the opener`, async ({ page, playwright, baseURL }) => {
        seededP1 ??= seedPhase1(playwright, baseURL);
        const seed = await seededP1;
        await page.goto('/#/initiatives');
        await expect(page.getByTestId('initiative-row').first()).toBeVisible();
        const opener = await d.go(page, seed);
        await expect(opener).toBeVisible();
        const dialog = page.getByRole('dialog', { name: d.dialog });
        await expect(dialog).toBeHidden();

        // Reach the opener with Tab only, open it with Enter.
        await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
        await tabTo(page, opener);
        await page.keyboard.press('Enter');
        await expect(dialog).toBeVisible();
        await expect(dialog).toHaveAttribute('aria-modal', 'true');
        expect(await focusIsInside(page), `focus is inside the dialog on open (on ${await focused(page)})`).toBe(true);
        await expect(opener).not.toBeFocused();

        // Tab and Shift+Tab stay inside and wrap at both ends.
        await wrapAround(page, 'Tab');
        await wrapAround(page, 'Shift+Tab');

        // Escape closes it, focus is back on the opener.
        await page.keyboard.press('Escape');
        await expect(dialog).toBeHidden();
        await expect(opener).toBeFocused();
      });
    }
  });
}

test('the Phase 1 dialog list names the 9 dialogs', () => {
  expect(PHASE1_DIALOGS.map((d) => d.name)).toEqual([
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

// R13: the six Phase 2 dialogs held to the same bar, each opened by keyboard only from its own opener on the Attention or Notes
// view: focus moves inside on open, Tab and Shift+Tab never leave and come round again, Escape closes with focus back on the
// opener, and the dialog is a bottom sheet at the phone (its lower edge on the viewport's) and centred on the desktop. Acknowledge's
// dialog is its confirmation, opened over the seed's expired lease (Escape leaves the lease unacknowledged for the next viewport).
const P2_SLUG = 'e2e-dlg-p2';
const P2_ACTOR = 'dlg-p2';

type P2Seed = { ask: string; doneTask: string; watch: string; lease: string };
let seededP2: Promise<P2Seed> | undefined;

// One initiative with an open blocking ask (with options), a done task, an overdue watch task, an expired lease and a note.
async function seedPhase2(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<P2Seed> {
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': P2_ACTOR, 'Board-Actor-Way-Back': `ralphctl status ${P2_ACTOR}` },
  });
  const created = await api.post('/api/v1/initiatives', { data: { slug: P2_SLUG, title: 'Phase 2 dialog keyboard', tags: ['kbd'] } });
  expect([201, 409], `seed ${P2_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string, extra: object = {}) => (await post(api, '/api/v1/tasks', { initiative: P2_SLUG, title, ...extra }, 201)).ref as string;
  const askTask = await task('Dlg p2 ask subject');
  const ask = (await post(api, '/api/v1/asks', { question: 'Does focus stay put?', options: ['Yes', 'No'], task: askTask, mode: 'poll', blocking: true }, 201)).ref as string;
  const doneTask = await task('Dlg p2 done');
  await post(api, `/api/v1/tasks/${doneTask}/done`, {}, 200);
  const watch = await task('Dlg p2 watch', { kind: 'watch', cadence_seconds: 900, next_check_at: '2020-01-01T00:00:00Z' });
  await post(api, '/api/v1/notes', { task: askTask, summary: 'Dlg p2 note', body: 'Body of the note.' }, 201);
  const leaseTask = await task('Dlg p2 lease');
  const lease = (await post(api, '/api/v1/leases', { task: leaseTask, intent: 'to acknowledge', resources: [`repo:dlg-p2-${Date.now()}`] }, 201)).lease.ref as string;
  await expireLease(api, lease);
  await api.dispose();
  return { ask, doneTask, watch, lease };
}

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

const attentionRow = (page: Page, text: string) => page.getByTestId('attention-item').filter({ hasText: text });

const onAttention = async (page: Page, ref: string) => {
  await showAllBands(page);
  await page.goto(`/#/now?initiative=${P2_SLUG}`);
  await expect(attentionRow(page, ref)).toBeVisible();
};

type P2Dialog = {
  name: string;
  /** The dialog's accessible name, from the seed. */
  dialog: (seed: P2Seed) => string;
  go: (page: Page, seed: P2Seed) => Promise<Locator>;
};

// The finite list of Phase 2 dialogs this spec holds to the focus-trap bar.
const PHASE2_DIALOGS: P2Dialog[] = [
  {
    name: 'answer ask',
    dialog: (seed) => `Answer ${seed.ask}`,
    go: async (page, seed) => (await onAttention(page, seed.ask), attentionRow(page, seed.ask).getByRole('button', { name: `Answer ${seed.ask}` })),
  },
  {
    name: 'withdraw ask',
    dialog: (seed) => `Withdraw ${seed.ask}`,
    go: async (page, seed) => (await onAttention(page, seed.ask), attentionRow(page, seed.ask).getByRole('button', { name: `Withdraw ${seed.ask}` })),
  },
  {
    name: 'post note',
    dialog: () => 'Post note',
    go: async (page) => {
      await page.goto(`/#/notes?initiative=${P2_SLUG}`);
      await expect(page.getByTestId('notes-list').getByTestId('note').first()).toBeVisible();
      return visibleButton(page, 'Post note');
    },
  },
  {
    name: 'close from attention',
    dialog: () => 'Close task',
    go: async (page, seed) => (await onAttention(page, seed.doneTask), attentionRow(page, seed.doneTask).getByRole('button', { name: `Close ${seed.doneTask}` })),
  },
  {
    name: 'acknowledge',
    dialog: (seed) => `Acknowledge ${seed.lease}`,
    go: async (page, seed) => (await onAttention(page, seed.lease), attentionRow(page, seed.lease).getByRole('button', { name: `Acknowledge ${seed.lease}` })),
  },
  {
    name: 'record check from attention',
    dialog: () => 'Record check',
    go: async (page, seed) => (await onAttention(page, seed.watch), attentionRow(page, seed.watch).getByRole('button', { name: `Record check ${seed.watch}` })),
  },
];

for (const vp of viewports) {
  test.describe(`Phase 2 dialogs at ${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    for (const d of PHASE2_DIALOGS) {
      test(`the ${d.name} dialog: keyboard opens it as ${vp.size.width < 768 ? 'a bottom sheet' : 'a centred dialog'}, focus is trapped inside, Escape returns focus to the opener`, async ({ page, playwright, baseURL }) => {
        seededP2 ??= seedPhase2(playwright, baseURL);
        const seed = await seededP2;
        const opener = await d.go(page, seed);
        await expect(opener).toBeVisible();
        const dialog = page.getByRole('dialog', { name: d.dialog(seed) });
        await expect(dialog).toBeHidden();

        // Reach the opener with Tab only, open it with Enter.
        await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
        await tabTo(page, opener);
        await page.keyboard.press('Enter');
        await expect(dialog).toBeVisible();
        await expect(dialog).toHaveAttribute('aria-modal', 'true');
        expect(await focusIsInside(page), `focus is inside the dialog on open (on ${await focused(page)})`).toBe(true);
        await expect(opener).not.toBeFocused();

        // A bottom sheet at the phone (the sheet slides up on open: wait for it to settle), centred on the desktop.
        const bottom = async () => {
          const box = await dialog.boundingBox();
          return box === null ? -1 : Math.round(box.y + box.height);
        };
        if (vp.size.width < 768) await expect.poll(bottom, 'the dialog sits on the viewport\'s lower edge').toBe(vp.size.height);
        else expect(await bottom(), 'the dialog is centred, clear of the lower edge').toBeLessThan(vp.size.height);

        // Tab and Shift+Tab stay inside and wrap at both ends.
        await wrapAround(page, 'Tab');
        await wrapAround(page, 'Shift+Tab');

        // Escape closes it, focus is back on the opener.
        await page.keyboard.press('Escape');
        await expect(dialog).toBeHidden();
        await expect(opener).toBeFocused();
      });
    }
  });
}

test('the Phase 2 dialog list names the six dialogs', () => {
  expect(PHASE2_DIALOGS.map((d) => d.name)).toEqual([
    'answer ask',
    'withdraw ask',
    'post note',
    'close from attention',
    'acknowledge',
    'record check from attention',
  ]);
});

// R12: the three dialogs Phase 3 adds (acknowledge on the task page's Stranded panel, the archive's Reopen confirmation and the
// force-done confirm) held to the same bar as the nine and six above, at the phone and the desktop: each is the shared Dialog (the one
// backdrop button and the role=dialog panel under one wrapper), a bottom sheet at the phone and centred on the desktop, focus moves inside
// on open and Tab and Shift+Tab never leave and come round again, and Escape returns focus to the opener. Escape leaves every item as it
// was, so both viewports find the same openers.
const P3_SLUG = 'e2e-dlg-p3';
const P3_ACTOR = 'dlg-p3';
const P3_HOLDER = 'dlg-p3-holder';

type P3Seed = { stranded: string; lease: string; forced: string; closed: string };
let seededP3: Promise<P3Seed> | undefined;

// One initiative with a stranded task (an expired lease nobody acknowledged), a task under another actor's live lease (so Done asks
// first) and a task closed as superseded (so the Archive lists it with a Reopen button).
async function seedPhase3(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined): Promise<P3Seed> {
  const asActor = (actor: string) =>
    playwright.request.newContext({
      baseURL,
      extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor, 'Board-Actor-Way-Back': `ralphctl status ${actor}` },
    });
  const a = await asActor(P3_ACTOR);
  const b = await asActor(P3_HOLDER);
  const created = await a.post('/api/v1/initiatives', { data: { slug: P3_SLUG, title: 'Phase 3 dialog keyboard', tags: ['kbd'] } });
  expect([201, 409], `seed ${P3_SLUG}: ${created.status()}`).toContain(created.status());
  const task = async (title: string) => (await post(a, '/api/v1/tasks', { initiative: P3_SLUG, title }, 201)).ref as string;
  const stranded = await task('Dlg p3 stranded');
  const old = await post(a, '/api/v1/leases', { task: stranded, intent: 'will strand', resources: [`repo:dlg-p3-${Date.now()}`] }, 201);
  await expireLease(a, old.lease.ref);
  const forced = await task('Dlg p3 forced');
  await post(b, '/api/v1/leases', { task: forced, intent: 'also holding', resources: [`repo:dlg-p3-b-${Date.now()}`] }, 201);
  const closed = await task('Dlg p3 closed');
  await post(a, `/api/v1/tasks/${closed}/close`, { resolution: 'superseded' }, 200);
  await a.dispose();
  await b.dispose();
  return { stranded, lease: old.lease.ref as string, forced, closed };
}

type P3Dialog = {
  name: string;
  /** The dialog's accessible name, from the seed. */
  dialog: (seed: P3Seed) => string;
  go: (page: Page, seed: P3Seed) => Promise<Locator>;
};

// The finite list of Phase 3 dialogs this spec holds to the focus-trap bar.
const PHASE3_DIALOGS: P3Dialog[] = [
  {
    name: 'acknowledge on the task page',
    dialog: (seed) => `Acknowledge lease ${seed.lease}`,
    go: async (page, seed) => {
      await page.goto(`/#/t/${seed.stranded}`);
      await expect(page.getByTestId('task-title')).toHaveText('Dlg p3 stranded');
      return page.getByTestId('stranded-panel').getByRole('button', { name: `Acknowledge lease ${seed.lease}` });
    },
  },
  {
    name: 'archive Reopen',
    dialog: () => 'Reopen task',
    go: async (page, seed) => {
      await page.goto('/#/archive?window=7');
      const row = page.locator(`[data-testid=archive-row][data-ref="${seed.closed}"]`).locator('visible=true').first();
      await expect(row).toBeVisible();
      return row.getByRole('button', { name: `Reopen task ${seed.closed}` });
    },
  },
  {
    name: 'force-done confirm',
    dialog: (seed) => `Mark ${seed.forced} done`,
    go: async (page, seed) => {
      await page.goto(`/#/t/${seed.forced}`);
      await expect(page.getByTestId('task-title')).toHaveText('Dlg p3 forced');
      return visibleButton(page, 'Done');
    },
  },
];

for (const vp of viewports) {
  test.describe(`Phase 3 dialogs at ${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    for (const d of PHASE3_DIALOGS) {
      test(`the ${d.name} dialog: keyboard opens the shared Dialog as ${vp.size.width < 768 ? 'a bottom sheet' : 'a centred dialog'}, focus is trapped inside, Escape returns focus to the opener`, async ({ page, playwright, baseURL }) => {
        seededP3 ??= seedPhase3(playwright, baseURL);
        const seed = await seededP3;
        const opener = await d.go(page, seed);
        await expect(opener).toBeVisible();
        const dialog = page.getByRole('dialog', { name: d.dialog(seed) });
        await expect(dialog).toBeHidden();

        // Reach the opener with Tab only, open it with Enter.
        await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
        await tabTo(page, opener);
        await page.keyboard.press('Enter');
        await expect(dialog).toBeVisible();
        await expect(dialog).toHaveAttribute('aria-modal', 'true');
        expect(await focusIsInside(page), `focus is inside the dialog on open (on ${await focused(page)})`).toBe(true);
        await expect(opener).not.toBeFocused();

        // The shared Dialog: one backdrop button and the panel under the same wrapper, and no second dialog on the page.
        await expect(page.getByTestId('dialog-backdrop')).toHaveCount(1);
        await expect(page.getByTestId('dialog-backdrop').locator('..').getByRole('dialog', { name: d.dialog(seed) })).toHaveCount(1);
        await expect(page.getByRole('dialog')).toHaveCount(1);

        // A bottom sheet at the phone (the sheet slides up on open: wait for it to settle), centred on the desktop.
        const bottom = async () => {
          const box = await dialog.boundingBox();
          return box === null ? -1 : Math.round(box.y + box.height);
        };
        if (vp.size.width < 768) await expect.poll(bottom, 'the dialog sits on the viewport\'s lower edge').toBe(vp.size.height);
        else expect(await bottom(), 'the dialog is centred, clear of the lower edge').toBeLessThan(vp.size.height);

        // Tab and Shift+Tab stay inside and wrap at both ends.
        await wrapAround(page, 'Tab');
        await wrapAround(page, 'Shift+Tab');

        // Escape closes it, focus is back on the opener.
        await page.keyboard.press('Escape');
        await expect(dialog).toBeHidden();
        await expect(opener).toBeFocused();
      });
    }
  });
}

test('the Phase 3 dialog list names the three dialogs', () => {
  expect(PHASE3_DIALOGS.map((d) => d.name)).toEqual(['acknowledge on the task page', 'archive Reopen', 'force-done confirm']);
});

// Every interactive element has a visible :focus-visible ring: read from computed style after keyboard focus.
async function expectRing(target: Locator) {
  const ring = () =>
    target.evaluate((el) => {
      const s = getComputedStyle(el);
      return { style: s.outlineStyle, width: parseFloat(s.outlineWidth), color: s.outlineColor, offset: parseFloat(s.outlineOffset), focused: el === document.activeElement };
    });
  const after = await ring();
  expect(after.focused, 'the element has keyboard focus').toBe(true);
  expect(after.style).toBe('solid');
  expect(after.width).toBeGreaterThanOrEqual(2);
  expect(after.color, 'the ring is not transparent').not.toMatch(/rgba\(\s*\d+,\s*\d+,\s*\d+,\s*0\s*\)|transparent/);
  expect(after.offset).toBeGreaterThanOrEqual(0);
}

for (const vp of viewports) {
  test.describe(`focus ring at ${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test.beforeEach(async ({ page, playwright, baseURL }) => {
      const seed = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      const res = await seed.post('/api/v1/initiatives', { data: { slug: 'e2e-dialogs', title: 'Dialog keyboard', tags: ['kbd'] } });
      expect([201, 409]).toContain(res.status());
      await seed.dispose();
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
    });

    test('a nav entry shows a visible focus ring after keyboard focus', async ({ page }) => {
      const entry = page.getByTestId(vp.nav).getByRole('link', { name: 'Notes' });
      expect(await entry.evaluate((el) => getComputedStyle(el).outlineStyle), 'no ring before focus').toBe('none');
      await tabTo(page, entry);
      await expectRing(entry);
    });

    test('the New initiative primary button shows a visible focus ring after keyboard focus', async ({ page }) => {
      const button = page.getByRole('button', { name: 'New initiative' }).locator('visible=true');
      expect(await button.evaluate((el) => getComputedStyle(el).outlineStyle), 'no ring before focus').toBe('none');
      await tabTo(page, button);
      await expectRing(button);
    });

    test('the new-initiative title input shows a visible focus ring after keyboard focus', async ({ page }) => {
      const button = page.getByRole('button', { name: 'New initiative' }).locator('visible=true');
      await tabTo(page, button);
      await page.keyboard.press('Enter');
      const title = page.getByRole('dialog', { name: 'New initiative' }).locator('input[name=title]');
      expect(await title.evaluate((el) => getComputedStyle(el).outlineStyle), 'no ring before focus').toBe('none');
      await tabTo(page, title);
      await expectRing(title);
    });
  });
}

// The page behind an open dialog does not scroll: hidden overflow on both roots (Safari scrolls the html one) and a
// touch drag outside the panel is cancelled (iOS scrolls the page under a fixed overlay otherwise); a drag inside the
// panel is left alone, and everything is undone when the dialog closes.
for (const vp of viewports) {
  test.describe(`scroll lock ${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size, hasTouch: true });

    test('an open dialog locks html and body from scrolling, cancels a touch drag outside the panel, and unlocks on close', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('initiative-row').first()).toBeVisible();
      const overflow = () => page.evaluate(() => ({ html: getComputedStyle(document.documentElement).overflow, body: getComputedStyle(document.body).overflow }));
      const before = await overflow();
      expect(before.html, 'the html root scrolls while no dialog is open').not.toBe('hidden');
      expect(before.body, 'the body scrolls while no dialog is open').not.toBe('hidden');

      await page.getByRole('button', { name: 'New initiative' }).locator('visible=true').click();
      const dialog = page.getByRole('dialog', { name: 'New initiative' });
      await expect(dialog).toBeVisible();
      expect(await overflow(), 'html and body are locked while the dialog is open').toEqual({ html: 'hidden', body: 'hidden' });

      // A touchmove is cancelable only when a non-passive listener is there to cancel it; report whether it was.
      const dragCancelled = (selector: string) =>
        page.evaluate((sel) => {
          const target = document.querySelector(sel)!;
          const event = new TouchEvent('touchmove', { bubbles: true, cancelable: true });
          target.dispatchEvent(event);
          return event.defaultPrevented;
        }, selector);
      expect(await dragCancelled('[data-testid=dialog-backdrop]'), 'a drag on the backdrop is cancelled').toBe(true);
      expect(await dragCancelled('[role=dialog] input[name=title]'), 'a drag inside the panel is left to the panel').toBe(false);

      await page.keyboard.press('Escape');
      await expect(dialog).toBeHidden();
      expect(await overflow(), 'html and body scroll again once the dialog is closed').toEqual(before);
    });
  });
}
