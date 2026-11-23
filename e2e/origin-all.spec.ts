import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { expect, type APIRequestContext, type Page } from '@playwright/test';
import { test } from './fixtures';
import { logicViewports } from './viewports';

// Origin and owner in the UI over the whole set (PRD R13, SPEC 11). origin.spec.ts follows one seeded example through the
// three displays; this spec checks every member of each set the displays quantify over, against the API's own lists:
// every actor on the Actors page shows the origin GET /api/v1/actors holds for it (an agent's operator, "no origin" for an
// agent with none, nothing for an operator), every key in Settings -> API keys shows the owner GET /api/v1/keys holds for
// it ("no owner" for a key without one), and every event in a task's timeline says "via <origin>" exactly when the
// event's origin is set and differs from its actor. The seed gives each set all of its cases: an agent acting through a
// key the signed-in operator owns, an agent acting through a key made with no owner, and the operator's own write.
const FIVE_SECONDS = { timeout: 5000 };

type ActorView = { handle: string; kind: 'operator' | 'agent'; origin?: string };
type Key = { name: string; owner?: string };
type BoardEvent = { actor: string; origin?: string };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

async function list<T>(page: Page, path: string): Promise<T[]> {
  const response = await page.request.get(path);
  expect(response.status(), `${path} ${await response.text()}`).toBe(200);
  return ((await response.json()) as { items: T[] }).items;
}

// A key with no owner, the state a key made before any operator existed is left in (SPEC 3.3). `vh admin keys create` no
// longer makes one on a board that has an operator (a default owner that is no operator is refused), so the key is made
// for `operator` and its owner column is then cleared in the run's own store, as the migration leaves a pre-operator key.
function mintOwnerless(name: string, operator: string): string {
  const work = process.env.E2E_WORK;
  if (!work) throw new Error('E2E_WORK is not set (the worker fixture in fixtures.ts sets it)');
  const env: NodeJS.ProcessEnv = {};
  for (const [variable, value] of Object.entries(process.env)) {
    if (!variable.startsWith('BOARD_') && !variable.startsWith('VH_')) env[variable] = value;
  }
  const dbPath = join(work, 'board.db');
  env.BOARD_DB_URL = `sqlite://${dbPath}`;
  const key = execFileSync(join(work, 'vh'), ['admin', 'keys', 'create', name, '--owner', operator], { env, encoding: 'utf8' }).trim().split(/\s+/).pop() ?? '';
  expect(key).toMatch(/^brd_[0-9a-z]{52}$/);
  const db = new DatabaseSync(dbPath);
  try {
    db.exec('PRAGMA busy_timeout = 10000');
    expect(db.prepare('UPDATE api_keys SET owner = NULL WHERE name = ?').run(name).changes).toBe(1);
  } finally {
    db.close();
  }
  return key;
}

const squash = (text: string | null) => (text ?? '').replace(/\s+/g, ' ').trim();

for (const vp of logicViewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('every actor shows its origin, every key its owner, and every event says via its origin only when it differs from the actor', async ({ page, playwright, baseURL }) => {
      const keyName = `e2e-originall-${vp.name}-key`;
      const owned = `e2e-originall-${vp.name}-owned`;
      const plain = `e2e-originall-${vp.name}-plain`;
      const slug = `e2e-originall-${vp.name}`;
      await page.goto('/');
      const operator = (await (await page.request.get('/auth/session')).json()).actor as string;
      expect(operator).not.toBe('');

      // A key the signed-in operator owns, made on the Settings page.
      await page.goto('/#/settings/keys');
      await page.getByRole('button', { name: 'New API key' }).click();
      const dialog = page.getByRole('dialog', { name: 'New API key' });
      await dialog.getByLabel('Name').fill(keyName);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      const ownedKey = (await page.getByTestId('new-key-value').textContent()) ?? '';
      expect(ownedKey).toMatch(/^brd_/);

      // One agent writes through the owned key, another through a key with no owner.
      const plainKey = mintOwnerless(`e2e-originall-${vp.name}-plain-key`, operator);
      const viaOwned = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${ownedKey}`, 'Board-Actor': owned } });
      const viaPlain = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${plainKey}`, 'Board-Actor': plain } });
      await post(viaOwned, '/api/v1/initiatives', { slug, title: `Origin over all ${vp.name}` }, 201);
      const ref = (await post<{ ref: string }>(viaOwned, '/api/v1/tasks', { initiative: slug, title: 'Every origin' }, 201)).ref;
      await post(viaPlain, `/api/v1/tasks/${ref}/milestones`, { summary: 'a milestone through a key with no owner' }, 200);

      // The operator's own write, from the task page.
      await page.goto(`/#/t/${ref}`);
      await page.getByRole('button', { name: 'Milestone' }).click();
      const mile = page.getByRole('dialog', { name: 'Record milestone' });
      await mile.getByLabel('Summary').fill(`milestone by ${operator}`);
      await mile.getByRole('button', { name: 'Record milestone' }).click();
      await expect(mile).toHaveCount(0, FIVE_SECONDS);

      // The seed covers every case of each set.
      const actors = await list<ActorView>(page, '/api/v1/actors');
      const keys = await list<Key>(page, '/api/v1/keys');
      const events = await list<BoardEvent>(page, `/api/v1/tasks/${ref}/events`);
      expect(actors.find((a) => a.handle === owned)?.origin).toBe(operator);
      expect(actors.find((a) => a.handle === plain)?.origin).toBeUndefined();
      expect(actors.some((a) => a.kind === 'operator')).toBe(true);
      expect(keys.find((k) => k.name === keyName)?.owner).toBe(operator);
      expect(keys.some((k) => k.owner === undefined)).toBe(true);
      expect(events.map((e) => e.actor)).toEqual(expect.arrayContaining([owned, plain, operator]));

      // Actors: every actor the API lists is a row, and each row says what the API holds for it.
      await page.goto('/#/actors');
      await expect(page.locator(`[data-testid=actor-row][data-handle="${plain}"]`)).toBeVisible(FIVE_SECONDS);
      const actorRows = await page.getByTestId('actor-row').evaluateAll((rows) =>
        rows.map((row) => [row.getAttribute('data-handle') ?? '', row.querySelector('[data-testid=actor-origin]')?.textContent ?? null] as const),
      );
      const wantActors = Object.fromEntries(actors.map((a) => [a.handle, a.kind === 'agent' ? (a.origin !== undefined ? `origin ${a.origin}` : 'no origin') : null]));
      expect(Object.fromEntries(actorRows.map(([handle, text]) => [handle, text === null ? null : squash(text)]))).toEqual(wantActors);
      expect(actorRows).toHaveLength(actors.length);

      // Settings -> API keys: every key the API lists is a row, and each row names its owner (the cell's text past its label).
      await page.goto('/#/settings/keys');
      await expect(page.locator(`[data-testid=key-row][data-name="${keyName}"]`)).toBeVisible(FIVE_SECONDS);
      const keyRows = await page.getByTestId('key-row').evaluateAll((rows) =>
        rows.map((row) => [row.getAttribute('data-name') ?? '', [...(row.querySelector('[data-testid=key-owner]')?.childNodes ?? [])].filter((n) => n.nodeType === Node.TEXT_NODE).map((n) => n.textContent).join('')] as const),
      );
      const wantKeys = Object.fromEntries(keys.map((k) => [k.name, k.owner ?? 'no owner']));
      expect(Object.fromEntries(keyRows.map(([name, text]) => [name, squash(text)]))).toEqual(wantKeys);
      expect(keyRows).toHaveLength(keys.length);

      // The task's timeline, newest first: every event, and "via" only where the origin is set and is not the actor.
      await page.goto(`/#/t/${ref}`);
      await expect(page.getByTestId('event')).toHaveCount(events.length, FIVE_SECONDS);
      const eventRows = await page.getByTestId('event').evaluateAll((rows) =>
        rows.map((row) => [row.querySelector('[data-testid=event-actor]')?.textContent ?? '', row.querySelector('[data-testid=event-via]')?.textContent ?? null] as const),
      );
      const wantEvents = [...events].reverse().map((e) => [e.actor, e.origin !== undefined && e.origin !== e.actor ? `via ${e.origin}` : null]);
      expect(eventRows.map(([actor, via]) => [squash(actor), via === null ? null : squash(via)])).toEqual(wantEvents);
      const withVia = wantEvents.filter(([, via]) => via !== null);
      expect(withVia.length).toBeGreaterThan(0);
      expect(withVia.every(([actor, via]) => actor === owned && via === `via ${operator}`)).toBe(true);

      await viaOwned.dispose();
      await viaPlain.dispose();
    });
  });
}
