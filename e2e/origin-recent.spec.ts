import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { expect, type APIRequestContext } from '@playwright/test';
import { test } from './fixtures';
import { openFold } from './now';

// The origin on Now's Recently touched rows (PRD R13, SPEC 11). Each row shows the task's last event; the row says
// "via <origin>" exactly when that event's own origin is set and is not its actor, whatever the actor's origin is now.
// The seed gives the display all three cases, each as the last event of its own task: an agent acting through a key the
// signed-in operator owns (differing), an agent acting through a key with no owner (null), and the operator's own write
// (origin equal to the actor). A fourth task makes the point about "whatever the actor's origin is now": the owned
// key's agent writes once more through the ownerless key, which clears the actor's origin, and the first task's row,
// whose last event was written earlier, still says "via <operator>".
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

type NowRecent = { task: { task: string }; last_event: { actor: { handle: string }; origin?: string } };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
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

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('Recently touched says via <origin> from the last event itself: differing, equal and null', async ({ page, playwright, baseURL }) => {
      const keyName = `e2e-originrecent-${vp.name}-key`;
      const owned = `e2e-originrecent-${vp.name}-owned`;
      const plain = `e2e-originrecent-${vp.name}-plain`;
      const slug = `e2e-originrecent-${vp.name}`;
      await page.goto('/');
      const operator = (await (await page.request.get('/auth/session')).json()).actor as string;
      expect(operator).not.toBe('');

      await page.goto('/#/settings/keys');
      await page.getByRole('button', { name: 'New API key' }).click();
      const dialog = page.getByRole('dialog', { name: 'New API key' });
      await dialog.getByLabel('Name').fill(keyName);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      const ownedKey = (await page.getByTestId('new-key-value').textContent()) ?? '';
      expect(ownedKey).toMatch(/^brd_/);
      const plainKey = mintOwnerless(`e2e-originrecent-${vp.name}-plain-key`, operator);
      const viaOwned = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${ownedKey}`, 'Board-Actor': owned } });
      const viaPlain = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${plainKey}`, 'Board-Actor': plain } });

      await post(viaOwned, '/api/v1/initiatives', { slug, title: `Origin on Now ${vp.name}` }, 201);
      const differing = (await post<{ ref: string }>(viaOwned, '/api/v1/tasks', { initiative: slug, title: 'Differing origin' }, 201)).ref;
      const nullOrigin = (await post<{ ref: string }>(viaPlain, '/api/v1/tasks', { initiative: slug, title: 'Null origin' }, 201)).ref;
      const equal = (await post<{ ref: string }>(viaPlain, '/api/v1/tasks', { initiative: slug, title: 'Equal origin' }, 201)).ref;
      const later = (await post<{ ref: string }>(viaOwned, '/api/v1/tasks', { initiative: slug, title: 'Origin cleared later' }, 201)).ref;
      // The owned key's agent writes again through the key with no owner: its own origin is now none.
      const ownedViaPlain = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${plainKey}`, 'Board-Actor': owned } });
      await post(ownedViaPlain, `/api/v1/tasks/${later}/milestones`, { summary: 'through a key with no owner' }, 200);
      const actorNow = ((await (await page.request.get('/api/v1/actors')).json()) as { items: { handle: string; origin?: string }[] }).items.find((a) => a.handle === owned);
      expect(actorNow?.origin).toBeUndefined();

      // The operator's own write, as the last event of the "equal" task.
      await page.goto(`/#/t/${equal}`);
      await page.getByRole('button', { name: 'Milestone' }).click();
      const mile = page.getByRole('dialog', { name: 'Record milestone' });
      await mile.getByLabel('Summary').fill(`milestone by ${operator}`);
      await mile.getByRole('button', { name: 'Record milestone' }).click();
      await expect(mile).toHaveCount(0, FIVE_SECONDS);

      const now = (await (await page.request.get('/api/v1/attention/now?window=8h')).json()) as { recent: NowRecent[] };
      const byTask = Object.fromEntries(now.recent.map((r) => [r.task.task, r.last_event]));
      expect(byTask[differing]).toMatchObject({ actor: { handle: owned }, origin: operator });
      expect(byTask[nullOrigin]).toMatchObject({ actor: { handle: plain } });
      expect(byTask[nullOrigin].origin).toBeUndefined();
      expect(byTask[equal]).toMatchObject({ actor: { handle: operator }, origin: operator });
      expect(byTask[later]).toMatchObject({ actor: { handle: owned } });
      expect(byTask[later].origin).toBeUndefined();

      await page.goto('/#/now');
      await openFold(page, 'recent');
      const row = (ref: string) => page.locator(`[data-testid=recent-row][data-task="${ref}"]`);
      await expect(row(differing)).toBeVisible(FIVE_SECONDS);
      const lastOf = async (ref: string) => squash(await row(ref).getByTestId('recent-last').textContent());
      expect(await lastOf(differing)).toMatch(new RegExp(`^task_created by ${owned} via ${operator} `));
      expect(await lastOf(nullOrigin)).not.toContain('via');
      expect(await lastOf(equal)).not.toContain('via');
      expect(await lastOf(equal)).toContain(`by ${operator} `);
      expect(await lastOf(later)).not.toContain('via');

      // Every row of the list says via exactly as its own event's origin and actor ask, without the Actors page's origin.
      const rows = await page.getByTestId('recent-row').evaluateAll((els) => els.map((el) => [el.getAttribute('data-task') ?? '', el.querySelector('[data-testid=recent-last]')?.textContent ?? ''] as const));
      for (const [ref, text] of rows) {
        const e = byTask[ref];
        if (e === undefined) continue;
        const want = e.origin !== undefined && e.origin !== e.actor.handle ? ` via ${e.origin} ` : null;
        if (want === null) expect(squash(text), ref).not.toMatch(/ via /);
        else expect(squash(text), ref).toContain(want);
      }
      await viaOwned.dispose();
      await viaPlain.dispose();
      await ownedViaPlain.dispose();
    });
  });
}
