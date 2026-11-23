import AxeBuilder from '@axe-core/playwright';
import { expect, type Locator, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { pickTag, tagChip } from './chips';
import { actorApi, created, openFold, showAllBands } from './now';

// R9 in the UI (SPEC 11, "Task tags as chips"): a task's tags are coloured chips that name their source on the Tasks list, Now
// (Needs me and In flight), Timeline, Archive, an initiative's page and the task page; the operator adds a tag (typing, with
// suggestions from tags in use) and removes a task's own tag on the Tasks list and the task page, and an initiative's or a derived
// actor-kind tag has no remove control. Each test seeds an initiative of its own on the shared board and filters to it.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };

const apiAs = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, actor = 'playwright') =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': actor } });

interface Seed {
  slug: string;
  /** The initiative's own tag. */
  initiativeTag: string;
  /** The tag the operator adds. */
  tag: string;
  /** An open task with no own tag, held by a `ralph:` actor (so it carries the derived tag `ralph`). */
  target: string;
  /** An open task with no tags of its own and no lease. */
  other: string;
}

async function seed(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<Seed> {
  const slug = `e2e-ttag-${name}`;
  const initiativeTag = `ttag-ini-${name}`;
  const api = await apiAs(playwright, baseURL);
  await created(api, '/api/v1/initiatives', { slug, title: `Task tags ${name}`, tags: [initiativeTag] });
  const target = (await created(api, '/api/v1/tasks', { initiative: slug, title: 'Tag me' })).ref;
  const other = (await created(api, '/api/v1/tasks', { initiative: slug, title: 'Leave me alone' })).ref;
  const holder = await actorApi(playwright, baseURL, `ralph:ttag-${name}`);
  await created(holder, '/api/v1/leases', { task: target, intent: 'work on tags' });
  await holder.dispose();
  await api.dispose();
  return { slug, initiativeTag, tag: `ttag-own-${name}`, target, other };
}

const rowOf = (page: Page, ref: string) => page.locator(`[data-testid=task-row][data-ref="${ref}"]`);
const chip = (scope: Page | Locator, tag: string) => scope.locator(`[data-testid=task-tag][data-tag="${tag}"]`);
const tableRefs = (page: Page) => page.getByTestId('task-ref').allTextContents();

/** Types a tag into the add field of `scope` (opening it when it is shut) and presses Add. */
async function addTag(scope: Page | Locator, text: string): Promise<void> {
  const open = scope.getByTestId('tag-add-open');
  if ((await open.count()) > 0) await open.click();
  await scope.getByTestId('tag-input').fill(text);
  await scope.getByTestId('tag-submit').click();
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('the operator adds a tag on the Tasks list, it appears after a reload and filters the list, then removes it', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `ops-${vp.name}`);
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      const row = rowOf(page, s.target);
      await expect(row).toBeVisible(FIVE_SECONDS);

      // An initiative's tag and a derived actor-kind tag show, name their source and have no remove control.
      const fromInitiative = chip(row, s.initiativeTag);
      await expect(fromInitiative).toHaveAttribute('data-source', 'initiative');
      await expect(fromInitiative).toContainText('initiative tag');
      await expect(fromInitiative.getByTestId('tag-remove')).toHaveCount(0);
      const derived = chip(row, 'ralph');
      await expect(derived).toHaveAttribute('data-source', 'actor');
      await expect(derived.getByTestId('tag-remove')).toHaveCount(0);
      await expect(chip(row, s.tag)).toHaveCount(0);

      // Adding: the new chip is the task's own, coloured like the chip filter's, and has a remove control.
      const puts: string[] = [];
      page.on('request', (request) => {
        if (request.method() === 'PUT' && request.url().includes('/tags/')) puts.push(new URL(request.url()).pathname);
      });
      await addTag(row, s.tag);
      const own = chip(row, s.tag);
      await expect(own).toBeVisible(FIVE_SECONDS);
      await expect(own).toHaveAttribute('data-source', 'task');
      await expect(own.getByTestId('tag-remove')).toHaveCount(1);
      expect(puts).toEqual([`/api/v1/tasks/${s.target}/tags/${s.tag}`]);
      await expect(chip(rowOf(page, s.other), s.tag)).toHaveCount(0);

      // After a reload the chip is still there, and choosing it narrows the list to the task that has it.
      await page.reload();
      await expect(chip(rowOf(page, s.target), s.tag)).toBeVisible(FIVE_SECONDS);
      await pickTag(page, s.tag);
      await expect.poll(() => tableRefs(page)).toEqual([s.target]);
      await tagChip(page, s.tag).click();
      await expect.poll(async () => (await tableRefs(page)).sort()).toEqual([s.target, s.other].sort());

      // Removing takes the own tag off, and it stays off after a reload; the initiative and derived chips stay.
      await rowOf(page, s.target).locator(`[data-testid=task-tag][data-tag="${s.tag}"] [data-testid=tag-remove]`).click();
      await expect(chip(rowOf(page, s.target), s.tag)).toHaveCount(0, FIVE_SECONDS);
      await page.reload();
      await expect(rowOf(page, s.target)).toBeVisible(FIVE_SECONDS);
      await expect(chip(rowOf(page, s.target), s.tag)).toHaveCount(0);
      await expect(chip(rowOf(page, s.target), s.initiativeTag)).toBeVisible();
      await expect(chip(rowOf(page, s.target), 'ralph')).toBeVisible();
    });

    test('typing suggests tags in use, a duplicate and an invalid tag send no request, and a tag is lower-cased', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `sug-${vp.name}`);
      const api = await apiAs(playwright, baseURL);
      await created(api, '/api/v1/tasks', { initiative: s.slug, title: 'Carries one', tags: [`ttag-sug-${vp.name}-used`] });
      await api.dispose();
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      const row = rowOf(page, s.target);
      await expect(row).toBeVisible(FIVE_SECONDS);
      const puts: string[] = [];
      page.on('request', (request) => {
        if (request.method() === 'PUT' && request.url().includes('/tags/')) puts.push(request.url());
      });

      await row.getByTestId('tag-add-open').click();
      await row.getByTestId('tag-input').fill(`TTAG-SUG-${vp.name}`);
      const suggestions = row.getByTestId('tag-suggestion');
      await expect(suggestions).toHaveText([`ttag-sug-${vp.name}-used`]);
      await row.getByTestId('tag-input').fill('zzz-nothing-like-this');
      await expect(suggestions).toHaveCount(0);

      // Invalid: refused in the page, no request.
      await row.getByTestId('tag-input').fill('not a tag!');
      await row.getByTestId('tag-submit').click();
      await expect(row.getByTestId('tag-error')).toContainText('lowercase letters');
      expect(puts).toEqual([]);

      // Picking a suggestion adds it; adding it again (any letter case) is a duplicate and sends nothing more.
      await row.getByTestId('tag-input').fill(`ttag-sug-${vp.name}`);
      await row.getByTestId('tag-suggestion').click();
      await expect(chip(row, `ttag-sug-${vp.name}-used`)).toHaveAttribute('data-source', 'task', FIVE_SECONDS);
      expect(puts).toHaveLength(1);
      await row.getByTestId('tag-input').fill(`TTAG-SUG-${vp.name}-USED`);
      await row.getByTestId('tag-submit').click();
      await expect(row.getByTestId('tag-error')).toHaveCount(0);
      expect(puts).toHaveLength(1);

      // A typed tag is trimmed and lower-cased before it is sent.
      await row.getByTestId('tag-input').fill(`  TTAG-Case-${vp.name}  `);
      await row.getByTestId('tag-submit').click();
      await expect(chip(row, `ttag-case-${vp.name}`)).toBeVisible(FIVE_SECONDS);
      expect(puts).toHaveLength(2);
    });

    test('the operator adds and removes a tag on the task page, and the initiative and derived chips have no remove', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `page-${vp.name}`);
      await page.goto(`/#/t/${s.target}`);
      const block = page.getByTestId('task-tags-block');
      await expect(chip(block, s.initiativeTag)).toHaveAttribute('data-source', 'initiative', FIVE_SECONDS);
      await expect(chip(block, 'ralph')).toHaveAttribute('data-source', 'actor');
      await expect(block.getByTestId('tag-remove')).toHaveCount(0);

      await addTag(block, s.tag);
      await expect(chip(block, s.tag)).toHaveAttribute('data-source', 'task', FIVE_SECONDS);
      await page.reload();
      await expect(chip(page.getByTestId('task-tags-block'), s.tag)).toBeVisible(FIVE_SECONDS);

      await chip(page.getByTestId('task-tags-block'), s.tag).getByTestId('tag-remove').click();
      await expect(chip(page.getByTestId('task-tags-block'), s.tag)).toHaveCount(0, FIVE_SECONDS);
      await expect(chip(page.getByTestId('task-tags-block'), s.initiativeTag)).toBeVisible();
    });

    test("the seeded task's own tag renders as a chip on Now (Needs me and In flight), Timeline, Archive, the initiative page and the task page", async ({ page, playwright, baseURL }) => {
      const name = `views-${vp.name}`;
      const s = await seed(playwright, baseURL, name);
      const api = await apiAs(playwright, baseURL);
      // The open task carries the tag, has an open ask (Needs me) and a live lease (In flight); a closed one carries it for Archive.
      expect((await api.put(`/api/v1/tasks/${s.target}/tags/${s.tag}`)).status()).toBe(200);
      await created(api, '/api/v1/asks', { question: `Which way for ${name}?`, task: s.target, mode: 'poll' });
      const closed = (await created(api, '/api/v1/tasks', { initiative: s.slug, title: 'Done and dusted', tags: [s.tag] })).ref;
      expect((await api.post(`/api/v1/tasks/${closed}/close`, { data: { resolution: 'dropped' } })).status()).toBe(200);
      await api.dispose();

      const own = (scope: Page | Locator) => scope.locator(`[data-testid=task-tag][data-tag="${s.tag}"][data-source=task]`);

      // The open ask puts the task in the Needs attention section of Tasks, above the table.
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      await expect(own(page.locator(`[data-testid=needs-row][data-ref="${s.target}"]`))).toBeVisible(FIVE_SECONDS);

      await page.goto(`/#/now?initiative=${s.slug}`);
      await showAllBands(page);
      const needs = page.locator('[data-testid=attention-item][data-section=open_asks]');
      await expect(needs).toHaveCount(1, FIVE_SECONDS);
      await expect(own(needs)).toBeVisible(FIVE_SECONDS);
      await openFold(page, 'flight'); // a phone starts In flight closed
      const flightRow = page.locator(`[data-testid=flight-row][data-task="${s.target}"]`);
      await expect(flightRow).toHaveCount(1, FIVE_SECONDS);
      await expect(own(flightRow)).toBeVisible(FIVE_SECONDS);

      await page.goto(`/#/timeline?initiative=${s.slug}`);
      const label = page.locator('.vis-labelset .vis-label').filter({ hasText: `${s.target} ` });
      await expect(own(label).first()).toBeAttached(FIVE_SECONDS);
      await expect(label.locator('[data-testid=task-tag]', { hasText: s.tag })).toBeVisible();

      await page.goto(`/#/archive?initiative=${s.slug}`);
      await expect(own(page.locator(`[data-testid=archive-row][data-ref="${closed}"]`))).toBeVisible(FIVE_SECONDS);

      await page.goto(`/#/i/${s.slug}`);
      const onPage = page.locator(`[data-testid=needs-row][data-ref="${s.target}"], [data-testid=task-row][data-ref="${s.target}"]`);
      await expect(own(onPage)).toBeVisible(FIVE_SECONDS);
      await expect(chip(onPage, s.initiativeTag)).toHaveAttribute('data-source', 'initiative');

      await page.goto(`/#/t/${s.target}`);
      await expect(own(page.getByTestId('task-tags-block'))).toBeVisible(FIVE_SECONDS);
    });

    test('Now and Timeline draw every chip (own, initiative, actor kind) from their own read, with no lookup of the tasks by ref', async ({ page, playwright, baseURL }) => {
      const name = `reads-${vp.name}`;
      const s = await seed(playwright, baseURL, name);
      const api = await apiAs(playwright, baseURL);
      expect((await api.put(`/api/v1/tasks/${s.target}/tags/${s.tag}`)).status()).toBe(200);
      // A second kind of Needs me item: an ask on the task puts it under Open asks as well as In flight.
      await created(api, '/api/v1/asks', { question: `Which way for ${name}?`, task: s.target, mode: 'poll' });
      // A recently touched task with no live lease: a lease started and ended by a claude: actor (derived tag `claude`).
      const ended = (await created(api, '/api/v1/tasks', { initiative: s.slug, title: 'Touched and let go', tags: [`${s.tag}-b`] })).ref;
      const brief = await actorApi(playwright, baseURL, `claude:ttag-${name}`);
      const lease = await created(brief, '/api/v1/leases', { task: ended, intent: 'brief' });
      expect((await brief.post(`/api/v1/leases/${lease.lease.ref}/end`, { data: {} })).status()).toBe(200);
      await brief.dispose();
      await api.dispose();

      const lookups: string[] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        // The navigation's live-lease badge reads `GET /tasks?active=true`; what must not happen is a read of tasks by ref.
        if (request.method() === 'GET' && url.pathname === '/api/v1/tasks' && url.searchParams.has('ref')) lookups.push(url.pathname + url.search);
      });
      const marked = (scope: Page | Locator, tag: string, source: string) => scope.locator(`[data-testid=task-tag][data-tag="${tag}"][data-source~="${source}"]`);

      await page.goto(`/#/now?initiative=${s.slug}`);
      await showAllBands(page);
      const needs = page.locator('[data-testid=attention-item][data-section=open_asks]');
      await expect(marked(needs, s.tag, 'task')).toBeVisible(FIVE_SECONDS);
      await expect(marked(needs, s.initiativeTag, 'initiative')).toBeVisible();
      await expect(marked(needs, 'ralph', 'actor')).toBeVisible();
      await openFold(page, 'flight'); // a phone starts In flight closed
      const flightRow = page.locator(`[data-testid=flight-row][data-task="${s.target}"]`);
      await expect(marked(flightRow, s.tag, 'task')).toBeVisible(FIVE_SECONDS);
      await expect(marked(flightRow, s.initiativeTag, 'initiative')).toBeVisible();
      await expect(marked(flightRow, 'ralph', 'actor')).toBeVisible();
      await openFold(page, 'recent');
      const recentRow = page.locator(`[data-testid=recent-row][data-task="${ended}"]`);
      await expect(marked(recentRow, `${s.tag}-b`, 'task')).toBeVisible(FIVE_SECONDS);
      await expect(marked(recentRow, s.initiativeTag, 'initiative')).toBeVisible();
      await expect(marked(recentRow, 'claude', 'actor')).toBeVisible();

      await page.goto(`/#/timeline?initiative=${s.slug}`);
      const label = page.locator('.vis-labelset .vis-label').filter({ hasText: `${s.target} ` });
      await expect(marked(label, s.tag, 'task').first()).toBeAttached(FIVE_SECONDS);
      await expect(marked(label, s.initiativeTag, 'initiative').first()).toBeAttached();
      await expect(marked(label, 'ralph', 'actor').first()).toBeAttached();
      expect(lookups).toEqual([]);
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`the tag chips and the add field pass axe in the ${theme} theme`, async ({ page, playwright, baseURL }) => {
        const s = await seed(playwright, baseURL, `axe-${theme}-${vp.name}`);
        const api = await apiAs(playwright, baseURL);
        expect((await api.put(`/api/v1/tasks/${s.target}/tags/${s.tag}`)).status()).toBe(200);
        await api.dispose();
        await page.addInitScript(([key, t]) => localStorage.setItem(key!, t!), ['vh.theme', theme]);
        await page.goto(`/#/tasks?initiative=${s.slug}`);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        const row = rowOf(page, s.target);
        await expect(chip(row, s.tag)).toBeVisible(FIVE_SECONDS);
        await row.getByTestId('tag-add-open').click();
        await row.getByTestId('tag-input').fill('ttag');
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
        const { violations } = await new AxeBuilder({ page }).analyze();
        const blocking = violations
          .filter((v) => v.impact === 'serious' || v.impact === 'critical')
          .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
        expect(blocking, 'serious or critical axe violations').toEqual([]);
      });
    }

    test('the chips and the add field leave the Tasks list with no horizontal scroll and 44 px targets on a phone', async ({ page, playwright, baseURL }) => {
      const s = await seed(playwright, baseURL, `fit-${vp.name}`);
      const api = await apiAs(playwright, baseURL);
      expect((await api.put(`/api/v1/tasks/${s.target}/tags/ttag-fit-${vp.name}-a-long-tag-name`)).status()).toBe(200);
      await api.dispose();
      await page.goto(`/#/tasks?initiative=${s.slug}`);
      const row = rowOf(page, s.target);
      await row.getByTestId('tag-add-open').click();
      await row.getByTestId('tag-input').fill('ttag');
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
      if (vp.name === 'phone') {
        for (const target of [row.getByTestId('tag-input'), row.getByTestId('tag-submit'), row.getByTestId('tag-cancel'), row.getByTestId('tag-remove').first()]) {
          const box = (await target.boundingBox())!;
          expect(box.height, 'target height').toBeGreaterThanOrEqual(44);
          expect(box.width, 'target width').toBeGreaterThanOrEqual(44);
        }
        expect(await row.getByTestId('tag-input').evaluate((el) => parseFloat(getComputedStyle(el).fontSize))).toBeGreaterThanOrEqual(16);
      }
    });
  });
}

