import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { ageTaskEvents } from './store';

// R8 on Timeline (SPEC 11, "Timeline"): the sort "Active first" and the filter "Only active" use the server's one definition of
// active (a live lease, any attention reason, or an event in the window), both live in the fragment and the saved view state,
// and the touched-in window control (8h, 24h, 3d; it shares Now's window key) shows only while Only active is ticked or the
// sort is Active first. Each test seeds an initiative of its own on the shared board: an idle task (its events aged out of
// every window) created BEFORE an active one (a live lease), so Active first has to move the second above the first.
const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;
const FIVE_SECONDS = { timeout: 5000 };
const VIEW_KEY = 'vh.view.v1.timeline';
const WINDOW_KEY = 'vh.now.window.v1';

async function post<T = { ref: string; id: string }>(api: APIRequestContext, path: string, data: object, status: number): Promise<T> {
  const response = await api.post(path, { data });
  expect(response.status(), `${path} ${await response.text()}`).toBe(status);
  return (await response.json()) as T;
}

const apiAs = (playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined) =>
  playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' } });

const row = (page: Page, ref: string) => page.locator('.vis-labelset .vis-label').filter({ hasText: `${ref} ` });

/** The task refs of the chart's rows, top to bottom. */
const order = (page: Page) =>
  page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('.vis-labelset .vis-label.tl-task')]
      .map((label) => ({ top: label.getBoundingClientRect().top, text: label.textContent ?? '' }))
      .sort((a, b) => a.top - b.top)
      .map((l) => l.text.split(' ')[0]!),
  );

/** Every row label of the chart, top to bottom: an initiative row by its title, a task row by its ref. */
const labels = (page: Page) =>
  page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>('.vis-labelset .vis-label')]
      .filter((label) => label.classList.contains('tl-task') || label.classList.contains('tl-initiative'))
      .filter((label) => label.getBoundingClientRect().height > 0)
      .map((label) => ({ top: label.getBoundingClientRect().top, task: label.classList.contains('tl-task'), text: (label.textContent ?? '').trim() }))
      .sort((x, y) => x.top - y.top)
      .map((l) => (l.task ? l.text.split(' ')[0]! : l.text)),
  );

interface Seed {
  slug: string;
  idle: string;
  active: string;
}

async function seed(api: APIRequestContext, name: string): Promise<Seed> {
  const slug = `e2e-tla-${name}`;
  await post(api, '/api/v1/initiatives', { slug, title: `Timeline active ${name}` }, 201);
  const idle = await post(api, '/api/v1/tasks', { initiative: slug, title: 'Idle one' }, 201);
  const active = await post(api, '/api/v1/tasks', { initiative: slug, title: 'Active one' }, 201);
  await post(api, '/api/v1/leases', { task: active.ref, intent: 'being worked' }, 201);
  ageTaskEvents(idle.id, 400); // older than the longest window (3d)
  return { slug, idle: idle.ref, active: active.ref };
}

const open = async (page: Page, slug: string, extra = '') => {
  await page.goto(`/#/timeline?initiative=${slug}${extra}`);
  await expect(page.getByTestId('timeline-chart')).toBeVisible(FIVE_SECONDS);
};

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('Active first lists the active task above the idle one that preceded it, and the choice is kept', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `sort-${vp.name}`);
      await open(page, s.slug);
      await expect(row(page, s.idle)).toHaveCount(1, FIVE_SECONDS);
      expect(await order(page)).toEqual([s.idle, s.active]); // the current order: by number
      await expect(page.getByTestId('timeline-window-control')).toHaveCount(0);

      await page.getByTestId('timeline-sort').selectOption('active:desc');
      await expect.poll(() => order(page), FIVE_SECONDS).toEqual([s.active, s.idle]);
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?initiative=${s.slug}&sort=active&dir=desc$`));
      expect(JSON.parse((await page.evaluate((k) => localStorage.getItem(k), VIEW_KEY)) as string).query).toBe(`initiative=${s.slug}&sort=active&dir=desc`);
      await expect(page.getByTestId('timeline-sort')).toHaveValue('active:desc');
      await expect(row(page, s.idle)).toHaveCount(1); // Active first hides nothing

      await page.reload();
      await expect.poll(() => order(page), FIVE_SECONDS).toEqual([s.active, s.idle]);
      await expect(page.getByTestId('timeline-sort')).toHaveValue('active:desc');

      await page.getByTestId('timeline-sort').selectOption('');
      await expect.poll(() => order(page), FIVE_SECONDS).toEqual([s.idle, s.active]);
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?initiative=${s.slug}$`));
      await api.dispose();
    });

    test('Active first draws every active row above every idle one across initiatives, and the current order comes back', async ({ page, playwright, baseURL }) => {
      // Two initiatives, each with an idle and an active task: A-idle, A-active, B-active, B-idle in the current order.
      const api = await apiAs(playwright, baseURL);
      const a = `e2e-tla-mixa-${vp.name}`;
      const b = `e2e-tla-mixb-${vp.name}`;
      const ref: Record<string, string> = {};
      for (const [slug, tasks] of [
        [a, ['a-idle', 'a-active']],
        [b, ['b-active', 'b-idle']],
      ] as const) {
        await post(api, '/api/v1/initiatives', { slug, title: slug === a ? 'Mixed A' : 'Mixed B' }, 201);
        for (const name of tasks) {
          const task = await post(api, '/api/v1/tasks', { initiative: slug, title: name }, 201);
          ref[name] = task.ref;
          if (name.endsWith('active')) await post(api, '/api/v1/leases', { task: task.ref, intent: 'being worked' }, 201);
          else ageTaskEvents(task.id, 400);
        }
      }
      const [A, B] = ['Mixed A', 'Mixed B'];
      const current = [A, ref['a-idle'], ref['a-active'], B, ref['b-active'], ref['b-idle']];
      await open(page, a, `&initiative=${b}`);
      await expect.poll(() => labels(page), FIVE_SECONDS).toEqual(current);

      await page.getByTestId('timeline-sort').selectOption('active:desc');
      // Read top to bottom: A's active task, B's active task, then A's idle task and B's idle task under a second row each.
      await expect.poll(() => labels(page), FIVE_SECONDS).toEqual([A, ref['a-active'], B, ref['b-active'], A, ref['a-idle'], B, ref['b-idle']]);
      expect(await order(page)).toEqual([ref['a-active'], ref['b-active'], ref['a-idle'], ref['b-idle']]);

      await page.reload();
      await expect.poll(() => labels(page), FIVE_SECONDS).toEqual([A, ref['a-active'], B, ref['b-active'], A, ref['a-idle'], B, ref['b-idle']]);

      await page.getByTestId('timeline-sort').selectOption('');
      await expect.poll(() => labels(page), FIVE_SECONDS).toEqual(current);
      await api.dispose();
    });

    test('Only active hides the idle task, asks the server for the window and keeps the choice', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `only-${vp.name}`);
      const sent: string[] = [];
      page.on('request', (request) => {
        const url = new URL(request.url());
        if (url.pathname === '/api/v1/timeline' && url.searchParams.get('active') === 'true') sent.push(url.searchParams.get('window') ?? '');
      });
      await open(page, s.slug);
      await expect(row(page, s.idle)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.active)).toHaveCount(1);

      await page.getByTestId('only-active').check();
      await expect(row(page, s.idle)).toHaveCount(0, FIVE_SECONDS);
      await expect(row(page, s.active)).toHaveCount(1);
      await expect(page).toHaveURL(new RegExp(`#/timeline\\?initiative=${s.slug}&active=1$`));
      expect(sent.at(-1)).toBe('24h');
      expect(JSON.parse((await page.evaluate((k) => localStorage.getItem(k), VIEW_KEY)) as string).query).toBe(`initiative=${s.slug}&active=1`);

      // The window is Now's choice: picking 3d here asks for 3d and is stored under Now's key.
      await page.getByTestId('timeline-window').getByText('3d', { exact: true }).click();
      await expect.poll(() => sent.at(-1), FIVE_SECONDS).toBe('3d');
      expect(await page.evaluate((k) => localStorage.getItem(k), WINDOW_KEY)).toBe('3d');
      await expect(row(page, s.idle)).toHaveCount(0); // 400 hours old is outside even 3d

      await page.reload();
      await expect(row(page, s.active)).toHaveCount(1, FIVE_SECONDS);
      await expect(row(page, s.idle)).toHaveCount(0);
      await expect(page.getByTestId('only-active')).toBeChecked();
      await expect(page.getByTestId('timeline-window').locator('input:checked')).toHaveAttribute('value', '3d');

      await page.getByTestId('only-active').uncheck();
      await expect(row(page, s.idle)).toHaveCount(1, FIVE_SECONDS);
      await api.dispose();
    });

    test('the window control and its hint appear only while Only active is ticked or the sort is Active first, and Now shares the choice', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `rule-${vp.name}`);
      await open(page, s.slug);
      const control = page.getByTestId('timeline-window-control');
      const hint = page.getByTestId('timeline-window-hint');
      const only = page.getByTestId('only-active');
      const sort = page.getByTestId('timeline-sort');
      const shown = async (want: boolean) => {
        await expect(control).toHaveCount(want ? 1 : 0, FIVE_SECONDS);
        await expect(hint).toHaveCount(want ? 1 : 0);
      };

      await shown(false); // neither
      await only.check();
      await shown(true); // Only active alone
      await sort.selectOption('active:desc');
      await shown(true); // both
      await only.uncheck();
      await shown(true); // Active first alone
      await sort.selectOption('');
      await shown(false); // neither again

      await only.check();
      await page.getByTestId('timeline-window').getByText('8h', { exact: true }).click();
      expect(await page.evaluate((k) => localStorage.getItem(k), WINDOW_KEY)).toBe('8h');
      await page.goto('/#/now');
      await expect(page.getByTestId('now-window').locator('input:checked')).toHaveAttribute('value', '8h');
      await api.dispose();
    });

    test('the filter row shares one centre line: selects, Only active, Touched within (label inline) and Clear filters', async ({ page, playwright, baseURL }) => {
      const api = await apiAs(playwright, baseURL);
      const s = await seed(api, `align-${vp.name}`);
      await open(page, s.slug, '&active=1');
      await expect(page.getByTestId('timeline-window-control')).toBeVisible(FIVE_SECONDS);
      const box = async (target: ReturnType<Page['getByTestId']>) => (await target.boundingBox())!;
      const centre = (b: { y: number; height: number }) => b.y + b.height / 2;
      // "Touched within" is beside its buttons, not above them, on both viewports.
      const label = await box(page.getByTestId('timeline-window-label'));
      const buttons = await page.getByTestId('timeline-window').locator('label').all();
      for (const button of buttons) expect(Math.abs(centre(await box(button)) - centre(label))).toBeLessThanOrEqual(2);
      expect(label.x + label.width).toBeLessThanOrEqual((await box(buttons[0]!)).x);
      // The chip labels stay on one line at desktop width (12 px text: one line is under 20 px).
      if (vp.name === 'desktop') {
        // The tags row shows only when the board has tags; the initiatives row is always there (this test seeds one).
        const chipLabels = page.getByTestId('chip-filter').locator('[role=group] > span[title]');
        await expect(chipLabels.first()).toBeVisible();
        for (const chipLabel of await chipLabels.all()) {
          expect((await box(chipLabel)).height, await chipLabel.textContent()).toBeLessThan(20);
        }
        // Every control of the row: one height, and one centre line with every control it shares a line with (the row wraps
        // when the board's longest actor handle widens the Actor select, so the lines are compared, not the row as one).
        const controls = [
          page.getByTestId('actor-filter'),
          page.getByTestId('closed-filter'),
          page.getByTestId('timeline-sort'),
          page.getByTestId('only-active').locator('..'),
          ...buttons.map((b) => b as ReturnType<Page['getByTestId']>),
          page.getByTestId('clear-filters'),
        ];
        const boxes = await Promise.all(controls.map(box));
        for (const a of boxes) {
          expect(Math.abs(a.height - boxes[0]!.height)).toBeLessThanOrEqual(1);
          for (const b of boxes.filter((o) => o.y < a.y + a.height && a.y < o.y + o.height)) {
            expect(Math.abs(centre(a) - centre(b))).toBeLessThanOrEqual(2);
          }
        }
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await api.dispose();
    });

    for (const theme of ['light', 'dark'] as const) {
      test(`the sort, the checkbox and the window control pass axe in the ${theme} theme`, async ({ page, playwright, baseURL }) => {
        const api = await apiAs(playwright, baseURL);
        const s = await seed(api, `axe-${theme}-${vp.name}`);
        await page.addInitScript(([key, t]) => localStorage.setItem(key!, t!), ['vh.theme', theme]);
        // The 44 px targets are the phone's and Sparse's; Compact (the default) from 768 px lowers them to 28 px (e2e/density-lists.spec.ts).
        await page.addInitScript(() => localStorage.setItem('vh.density.v1', 'sparse'));
        await open(page, s.slug, '&active=1&sort=active&dir=desc');
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await expect(page.getByTestId('timeline-window-control')).toBeVisible(FIVE_SECONDS);
        await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
        const { violations } = await new AxeBuilder({ page }).include('[data-testid=timeline-filters]').analyze();
        const blocking = violations
          .filter((v) => v.impact === 'serious' || v.impact === 'critical')
          .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
        expect(blocking, 'serious or critical axe violations').toEqual([]);
        // Phone targets: 44 px (the checkbox's target is its label), and the page does not scroll sideways.
        const targets = { 'timeline-sort': page.getByTestId('timeline-sort'), 'only-active': page.getByTestId('only-active').locator('..') };
        for (const [id, target] of Object.entries(targets)) {
          const box = await target.boundingBox();
          expect(box!.height, id).toBeGreaterThanOrEqual(44);
          expect(box!.width, id).toBeGreaterThanOrEqual(44);
        }
        for (const radio of await page.getByTestId('timeline-window').locator('label').all()) {
          expect((await radio.boundingBox())!.height).toBeGreaterThanOrEqual(44);
        }
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
        await api.dispose();
      });
    }
  });
}
