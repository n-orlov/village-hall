import { DatabaseSync } from 'node:sqlite';
import { join } from 'node:path';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';

// The Needs attention section above the other tasks on the Tasks view and on the initiative page (PRD R2, SPEC 11):
// a stranded task, a task with a blocking ask and a done task in that order with their badges; answering the ask and
// closing the done task take them out without a reload; an initiative with nothing that needs attention has no section.
// The board is shared with the other specs, so each test works on an initiative of its own and filters the view to it.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

async function post<T = Record<string, never>>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

// Runs a lease out the way needs-me.spec.ts does: its expiry moves into the past in the e2e store, then an API read expires it.
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

interface Seed {
  slug: string;
  api: APIRequestContext;
  stranded: string;
  asking: string;
  ask: string;
  done: string;
  quiet: string;
}

// One initiative with a done task, a task with a blocking ask and a stranded task (made in that order, so the order the
// section shows is not the order they changed), and one task that needs nothing.
async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const slug = `e2e-needs-${name}`;
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `needs-${name}` },
  });
  await post(api, '/api/v1/initiatives', { slug, title: `Needs ${name}` }, 201);
  const task = async (title: string) => (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title }, 201)).ref;
  const done = await task('Finished work');
  await post(api, `/api/v1/tasks/${done}/done`, {}, 200);
  const asking = await task('Needs an answer');
  const ask = (await post<{ ref: string }>(api, '/api/v1/asks', { question: `Which way for ${name}?`, task: asking, mode: 'poll', blocking: true }, 201)).ref;
  const stranded = await task('Ran out of time');
  const lease = await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: stranded, intent: 'long edit', resources: [`repo:needs-${name}`] }, 201);
  await expireLease(api, lease.lease.ref);
  const quiet = await task('Nothing to do here');
  return { slug, api, stranded, asking, ask, done, quiet };
}

// The cap case: 201 tasks that all need attention, so the section shows 200 of them and `+1 more`, and the one task past the cap is
// an ordinary table row. That row must carry the same attention badges (label, tone and accessible name) its card would have.
const CAPPED = [
  { name: 'done', reason: 'done', label: 'done, not closed', accessible: 'Done, not closed', flag: undefined },
  { name: 'asking', reason: 'asking', label: 'waiting on you', accessible: 'Waiting on you', flag: undefined },
  { name: 'blocking ask', reason: 'asking', label: 'waiting on you · blocking', accessible: 'Waiting on you, blocking', flag: 'blocked' },
  { name: 'stranded', reason: 'stranded', label: 'stranded', accessible: 'Stranded, its lease ran out and was not acknowledged', flag: undefined },
] as const;
const CAP = 200;

async function seedCapped(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, slug: string, kind: (typeof CAPPED)[number]['name']) {
  const api = await playwright.request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': `needs-cap-${kind.replace(' ', '-')}` },
  });
  await post(api, '/api/v1/initiatives', { slug, title: `Cap ${kind}` }, 201);
  const leased: number[] = [];
  for (let from = 0; from <= CAP; from += 20) {
    await Promise.all(
      Array.from({ length: Math.min(20, CAP + 1 - from) }, async (_, i) => {
        const ref = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: `Capped ${from + i}` }, 201)).ref;
        if (kind === 'done') await post(api, `/api/v1/tasks/${ref}/done`, {}, 200);
        else if (kind === 'stranded') {
          const lease = await post<{ lease: { ref: string } }>(api, '/api/v1/leases', { task: ref, intent: 'long edit', resources: [`repo:${slug}-${ref}`] }, 201);
          leased.push(Number(lease.lease.ref.slice(1)));
        } else await post(api, '/api/v1/asks', { question: `Which way for ${ref}?`, task: ref, mode: 'poll', blocking: kind === 'blocking ask' }, 201);
      }),
    );
  }
  if (leased.length > 0) {
    // Every lease runs out at once: expiry moves into the past in the e2e store, then an API read expires them.
    const db = new DatabaseSync(join(process.env.E2E_WORK!, 'board.db'));
    try {
      db.exec('PRAGMA busy_timeout = 5000');
      const update = db.prepare('UPDATE leases SET expires_at = ? WHERE n = ?');
      for (const n of leased) expect(update.run(Date.now() - 30_000, n).changes).toBe(1);
    } finally {
      db.close();
    }
    expect((await api.get('/api/v1/leases?state=expired')).status()).toBe(200);
  }
  return api;
}

const section = (page: Page) => page.getByTestId('needs-attention');
const refs = (page: Page) => section(page).getByTestId('needs-ref').allTextContents();

// The two views the section is on, each with where it lives and the table below it.
const views = [
  { name: 'Tasks view', url: (slug: string) => `/#/tasks?initiative=${slug}`, root: 'tasks-view' },
  { name: 'initiative page', url: (slug: string) => `/#/i/${slug}`, root: 'initiative-page' },
] as const;

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    for (const view of views) {
      test(`${view.name}: stranded, asking and done are in Needs attention in that order with their badges`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `order-${view.root}-${vp.name}`);
        await page.goto(view.url(s.slug));
        await expect(page.getByTestId(view.root)).toBeVisible();
        await expect(section(page)).toBeVisible(FIVE_SECONDS);
        await expect(section(page).getByRole('heading', { name: /Needs attention/ })).toContainText('(3)');
        expect(await refs(page)).toEqual([s.stranded, s.asking, s.done]);

        const row = (ref: string) => section(page).locator(`[data-testid=needs-row][data-ref=${ref}]`);
        await expect(row(s.stranded).getByTestId('attention-stranded')).toContainText('stranded');
        await expect(row(s.asking).getByTestId('attention-asking')).toContainText('waiting on you · blocking');
        await expect(row(s.done).getByTestId('attention-done')).toContainText('done, not closed');
        await expect(row(s.asking).getByTestId('flag-blocked')).toHaveText('blocked'); // its own flags show too
        await expect(row(s.asking).getByTestId('needs-title')).toHaveText('Needs an answer');
        await expect(row(s.asking).getByTestId('needs-ref')).toHaveAttribute('href', `#/t/${s.asking}`);
        await expect(section(page).getByTestId('needs-more')).toHaveCount(0);

        // The table below lists only the other tasks: nothing is shown twice.
        const others = page.locator('[data-testid=task-row]');
        await expect(others).toHaveCount(1);
        await expect(others.first()).toHaveAttribute('data-ref', s.quiet);
        await s.api.dispose();
      });

      test(`${view.name}: answering the ask and closing the done task remove them without a reload`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `leave-${view.root}-${vp.name}`);
        await page.goto(view.url(s.slug));
        await expect(section(page).getByTestId('needs-row')).toHaveCount(3, FIVE_SECONDS);
        await page.evaluate(() => ((window as unknown as { __kept: boolean }).__kept = true)); // a reload would lose this

        await post(s.api, `/api/v1/asks/${s.ask}/answer`, { answer: 'Go left' }, 200);
        await expect(section(page).getByTestId('needs-row')).toHaveCount(2, FIVE_SECONDS);
        expect(await refs(page)).toEqual([s.stranded, s.done]);
        await expect(page.locator(`[data-testid=task-row][data-ref=${s.asking}]`)).toBeVisible(FIVE_SECONDS); // the answered task is an ordinary row now

        await post(s.api, `/api/v1/tasks/${s.done}/close`, { resolution: 'completed' }, 200);
        await expect(section(page).getByTestId('needs-row')).toHaveCount(1, FIVE_SECONDS);
        expect(await refs(page)).toEqual([s.stranded]);
        await expect(section(page).getByRole('heading', { name: /Needs attention/ })).toContainText('(1)');
        expect(await page.evaluate(() => (window as unknown as { __kept?: boolean }).__kept)).toBe(true);
        await s.api.dispose();
      });

      test(`${view.name}: an initiative with nothing that needs attention has no section`, async ({ page, playwright, baseURL }) => {
        const slug = `e2e-needs-none-${view.root}-${vp.name}`;
        const api = await playwright.request.newContext({
          baseURL,
          extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'needs-none' },
        });
        await post(api, '/api/v1/initiatives', { slug, title: 'Quiet' }, 201);
        const ref = (await post<{ ref: string }>(api, '/api/v1/tasks', { initiative: slug, title: 'Just a task' }, 201)).ref;
        await page.goto(view.url(slug));
        await expect(page.locator(`[data-testid=task-row][data-ref=${ref}]`)).toBeVisible();
        await expect(section(page)).toHaveCount(0);
        await expect(page.getByRole('heading', { name: /Needs attention/ })).toHaveCount(0);
        await api.dispose();
      });
    }

    test('the Attention filter of the Tasks view keeps the tasks that need attention, and `+K more` would link to it', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `filter-${vp.name}`);
      await page.goto(`/#/tasks?initiative=${s.slug}&attention=true`);
      await expect(section(page).getByTestId('needs-row')).toHaveCount(3, FIVE_SECONDS);
      await expect(page.locator('[data-testid=task-row]')).toHaveCount(0); // all three are in the section; nothing else matches
      await page.getByTestId('attention-filter').selectOption('no');
      await expect(section(page)).toHaveCount(0);
      await expect(page.locator(`[data-testid=task-row][data-ref=${s.quiet}]`)).toBeVisible(FIVE_SECONDS);
      await expect(page.locator('[data-testid=task-row]')).toHaveCount(1);
      await s.api.dispose();
    });

    for (const view of views) {
      test(`${view.name}: a table row that needs nothing carries no attention badge, and an answered task loses its badge`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `plain-${view.root}-${vp.name}`);
        await post(s.api, `/api/v1/asks/${s.ask}/answer`, { answer: 'Go left' }, 200);
        await page.goto(view.url(s.slug));
        const asked = page.locator(`[data-testid=task-row][data-ref=${s.asking}]`);
        const quiet = page.locator(`[data-testid=task-row][data-ref=${s.quiet}]`);
        await expect(asked).toBeVisible(FIVE_SECONDS);
        await expect(quiet).toBeVisible(FIVE_SECONDS);
        await expect(section(page).getByTestId('needs-row')).toHaveCount(2); // stranded and done stay in the section
        for (const row of [asked, quiet]) {
          await expect(row.locator('[data-testid^=attention-]')).toHaveCount(0);
          await expect(row.getByTestId('flag-blocked')).toHaveCount(0);
        }
        await s.api.dispose();
      });
    }

    for (const capped of CAPPED) {
      test(`a task past the section cap keeps its ${capped.name} attention badge on the Tasks view and the initiative page`, async ({ page, playwright, baseURL }) => {
        test.setTimeout(120_000);
        const slug = `e2e-needs-cap-${capped.name.replace(' ', '-')}-${vp.name}`;
        const api = await seedCapped(playwright, baseURL, slug, capped.name);
        for (const view of views) {
          await page.goto(view.url(slug));
          await expect(page.getByTestId(view.root)).toBeVisible();
          await expect(section(page).getByTestId('needs-row')).toHaveCount(CAP, { timeout: 15_000 });
          await expect(section(page).getByTestId('needs-more')).toHaveText('+1 more');
          const rows = page.locator('[data-testid=task-row]');
          await expect(rows).toHaveCount(1); // the section, its cap and the disjoint table are unchanged
          const sectionRefs = await refs(page);
          expect(new Set(sectionRefs).size).toBe(CAP);
          const ref = await rows.first().getAttribute('data-ref');
          expect(sectionRefs, `${view.name}: ${ref} is listed once`).not.toContain(ref);

          const badge = rows.first().getByTestId(`attention-${capped.reason}`);
          await expect(badge, `${view.name}: row ${ref}`).toHaveCount(1);
          await expect(badge.locator('[aria-hidden=true]')).toHaveText(capped.label);
          await expect(badge.locator('.sr-only')).toHaveText(capped.accessible);
          const card = section(page).getByTestId(`attention-${capped.reason}`).first();
          expect(await badge.getAttribute('class'), `${view.name}: same tone as the card's badge`).toBe(await card.getAttribute('class'));
          if (capped.flag !== undefined) await expect(rows.first().getByTestId(`flag-${capped.flag}`)).toHaveCount(1); // its flags still show
        }
        await api.dispose();
      });
    }
  });
}
