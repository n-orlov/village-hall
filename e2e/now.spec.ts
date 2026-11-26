import AxeBuilder from '@axe-core/playwright';
import { expect, type Page, type PlaywrightTestArgs } from '@playwright/test';
import { test } from './fixtures';
import { catalogRead, initiativeChip, openChips, pickInitiative, pickTag, tagChip } from './chips';
import { BANDS_KEY, FLIGHT_KEY, LAYOUT_KEY, actorApi, band, created, openBand, openFold, seedFlight, seedNeedsMe, showAllBands } from './now';

// The Now view's Needs me bands (PRD R1, SPEC 11): the four bands in order with their counts, every band open on the desktop
// but Done, not closed, an accordion on the phone (Open asks and Expired leases and conflicts open when they hold something,
// the others closed), `#/attention` and `#/` landing on `#/now` with any filter kept, and the open state of a band remembered
// across a reload, apart for wide and narrow screens. The board is shared, so each test filters to an initiative of its own.
const FIVE_SECONDS = { timeout: 5000 };
const PHONE = { width: 390, height: 844 };
const DESKTOP = { width: 1440, height: 900 };
const KEYS = ['asks', 'expired', 'watches', 'done'] as const;

const expanded = async (page: Page) => Promise.all(KEYS.map((key) => band(page, key).getByTestId('band-toggle').getAttribute('aria-expanded')));
const storedLayout = (page: Page) => page.evaluate((key) => JSON.parse(localStorage.getItem(key) ?? 'null') as unknown, LAYOUT_KEY);
const stored = (page: Page) => page.evaluate((key) => JSON.parse(localStorage.getItem(key) ?? 'null') as unknown, BANDS_KEY);

test.describe('desktop 1440x900', () => {
  test.use({ viewport: DESKTOP });

  test('the bands come in order with their counts, every one open but Done, not closed', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'order-desktop');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 3, name: 'Open asks' })).toBeVisible();
    await expect(page.getByTestId('band-toggle')).toHaveText([/^\s*Open asks\s*1\s*$/, /^\s*Expired leases and conflicts\s*1\s*$/, /^\s*Overdue watches\s*1\s*$/, /^\s*Done, not closed\s*1\s*$/], FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['true', 'true', 'true', 'false']);

    // Top to bottom in band order, Done, not closed last; each open band shows its item, the closed one hides it.
    const tops = await Promise.all(KEYS.map(async (key) => (await band(page, key).boundingBox())!.y));
    expect([...tops].sort((x, y) => x - y)).toEqual(tops);
    await expect(band(page, 'asks').getByTestId('attention-item')).toContainText(s.ask);
    await expect(band(page, 'expired').getByTestId('attention-item')).toHaveCount(1);
    await expect(band(page, 'watches').getByTestId('attention-item')).toContainText('Slow watch');
    await expect(band(page, 'done').getByTestId('attention-item')).toHaveCount(1);
    await expect(band(page, 'done').getByTestId('attention-item')).toBeHidden();
    // Not dimmed: the last band is as opaque as the first.
    const opacity = (key: (typeof KEYS)[number]) => band(page, key).evaluate((el) => getComputedStyle(el).opacity);
    expect(await opacity('done')).toBe(await opacity('asks'));
  });

  test('an empty board filter leaves every band open but Done, not closed', async ({ page, playwright, baseURL }) => {
    const api = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'now-quiet' } });
    expect((await api.post('/api/v1/initiatives', { data: { slug: 'e2e-now-quiet-desktop', title: 'Quiet' } })).status()).toBe(201);
    await api.dispose();
    await page.goto('/#/now?initiative=e2e-now-quiet-desktop');
    await expect(page.getByTestId('band-count')).toHaveText(['0', '0', '0', '0'], FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['true', 'true', 'true', 'false']);
    await expect(page.getByTestId('band-empty')).toHaveCount(4);
  });

  test('a band toggled closed stays closed after a reload, and Done, not closed stays open once opened', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'reload-desktop');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    await band(page, 'asks').getByTestId('band-toggle').click();
    await band(page, 'done').getByTestId('band-toggle').click();
    expect(await expanded(page)).toEqual(['false', 'true', 'true', 'true']);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeHidden();
    await expect(band(page, 'done').getByTestId('attention-item')).toBeVisible();
    expect(await storedLayout(page)).toMatchObject({ open: { asks: false, done: true } });
    expect(await stored(page)).toBeNull(); // the dock keeps its own open states: the bands key is the stacked view's

    await page.reload();
    await expect(band(page, 'watches').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['false', 'true', 'true', 'true']);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeHidden();
    await expect(band(page, 'done').getByTestId('attention-item')).toBeVisible();
  });

  test('the open state is remembered apart for the docked desktop and the narrow accordion', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'apart-desktop');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    await band(page, 'asks').getByTestId('band-toggle').click(); // wide: closed
    expect(await expanded(page)).toEqual(['false', 'true', 'true', 'false']);

    await page.setViewportSize(PHONE); // narrow: its own defaults, the wide choice untouched
    await expect.poll(() => expanded(page)).toEqual(['true', 'true', 'false', 'false']);
    await band(page, 'watches').getByTestId('band-toggle').click(); // narrow: opened
    expect(await stored(page)).toEqual({ wide: {}, narrow: { watches: true } });
    expect(await storedLayout(page)).toMatchObject({ open: { asks: false } });

    await page.setViewportSize(DESKTOP);
    await expect.poll(() => expanded(page)).toEqual(['false', 'true', 'true', 'false']);
    await page.setViewportSize(PHONE);
    await expect.poll(() => expanded(page)).toEqual(['true', 'true', 'true', 'false']);
  });

  test('a bad stored value falls back to the defaults', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'bad-desktop');
    await page.addInitScript((key) => localStorage.setItem(key, '{"wide":7,"narrow":["x"]'), BANDS_KEY);
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['true', 'true', 'true', 'false']);
  });
});

test.describe('phone 390x844', () => {
  test.use({ viewport: PHONE });

  test('the bands are an accordion: Open asks and Expired open when they hold something, the others closed', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'accordion-phone');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('band-count')).toHaveText(['1', '1', '1', '1'], FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['true', 'true', 'false', 'false']);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeVisible();
    await expect(band(page, 'watches').getByTestId('attention-item')).toBeHidden();
    await expect(band(page, 'done').getByTestId('attention-item')).toBeHidden();

    // One line per band: a 44 px target that names the band and its count; opening one shows its item.
    for (const key of KEYS) {
      const toggle = band(page, key).getByTestId('band-toggle');
      expect((await toggle.boundingBox())!.height).toBeGreaterThanOrEqual(44);
    }
    await band(page, 'watches').getByTestId('band-toggle').click();
    await expect(band(page, 'watches').getByTestId('attention-item')).toBeVisible();
    await band(page, 'done').getByTestId('band-toggle').click();
    await expect(band(page, 'done').getByTestId('attention-item')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });

  test('empty bands all start closed on the phone', async ({ page, playwright, baseURL }) => {
    const api = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'now-quiet' } });
    expect((await api.post('/api/v1/initiatives', { data: { slug: 'e2e-now-quiet-phone', title: 'Quiet' } })).status()).toBe(201);
    await api.dispose();
    await page.goto('/#/now?initiative=e2e-now-quiet-phone');
    await expect(page.getByTestId('band-count')).toHaveText(['0', '0', '0', '0'], FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['false', 'false', 'false', 'false']);
  });

  test('a band opened on the phone stays open after a reload, and the keyboard toggles it', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'reload-phone');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(band(page, 'asks').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    const done = band(page, 'done').getByTestId('band-toggle');
    await done.focus();
    await page.keyboard.press('Enter');
    await expect(band(page, 'done').getByTestId('attention-item')).toBeVisible();
    await band(page, 'asks').getByTestId('band-toggle').click();
    await page.reload();
    await expect(band(page, 'done').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['false', 'true', 'false', 'true']);
  });
});

test.describe('landing', () => {
  test.use({ viewport: DESKTOP });

  for (const entry of ['/#/attention', '/', '/#/']) {
    test(`${entry} lands on #/now`, async ({ page }) => {
      await page.goto(entry);
      await expect(page).toHaveURL(/#\/now$/, FIVE_SECONDS);
      await expect(page.getByTestId('now-view')).toBeVisible();
      await expect(page.getByRole('navigation', { name: 'Main' }).locator('visible=true').getByRole('link', { name: 'Now' })).toHaveAttribute('aria-current', 'page');
    });
  }

  test('#/attention keeps the filter in the fragment', async ({ page, playwright, baseURL }) => {
    const s = await seedNeedsMe(playwright, baseURL, 'land-filter');
    await page.goto(`/#/attention?initiative=${s.slug}`);
    await expect(page).toHaveURL(new RegExp(`#/now\\?initiative=${s.slug}$`), FIVE_SECONDS);
    await expect(initiativeChip(page, s.slug)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
    await expect(band(page, 'asks').getByTestId('attention-item')).toHaveCount(1, FIVE_SECONDS);
  });

  test('the nav says Now and a hash change to #/attention lands on it too', async ({ page }) => {
    await page.goto('/#/tasks');
    await expect(page.getByTestId('tasks-view')).toBeVisible(FIVE_SECONDS);
    await page.evaluate(() => (window.location.hash = '#/attention'));
    await expect(page).toHaveURL(/#\/now$/, FIVE_SECONDS);
    await expect(page.getByTestId('now-view')).toBeVisible();
  });
});

// The right column (PRD R1, SPEC 11): In flight (a row per task with a live lease, its leases nested with holder, intent and time
// left) and Recently touched (tasks with no live lease touched within the window, each with its last event), both from the one
// `GET /attention/now` read, so the page never asks `GET /api/v1/events`. The window switch (8h, 24h, 3d; 24h by default) is
// remembered under `vh.now.window.v1`.
const flight = (page: Page) => page.getByTestId('now-flight');
const flightRow = (page: Page, ref: string) => page.locator(`[data-testid=flight-row][data-task="${ref}"]`);
const recentRow = (page: Page, ref: string) => page.locator(`[data-testid=recent-row][data-task="${ref}"]`);
const windowChoice = (page: Page, label: string) => page.getByTestId('now-window').getByRole('radio', { name: label });
const storedWindow = (page: Page) => page.evaluate((key) => localStorage.getItem(key), FLIGHT_KEY);

test.describe('the right column on the desktop', () => {
  test.use({ viewport: DESKTOP });

  test('two columns: Needs me on the left, In flight with nested leases and Recently touched on the right', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'columns');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(flightRow(page, s.live)).toBeVisible(FIVE_SECONDS);
    await openFold(page, 'recent');
    const needs = (await band(page, 'asks').boundingBox())!;
    const right = (await flight(page).boundingBox())!;
    expect(right.x).toBeGreaterThanOrEqual(needs.x + needs.width);
    expect(Math.abs(right.y - needs.y)).toBeLessThanOrEqual(2);
    await expect(page.getByRole('heading', { level: 3, name: /^In flight/ })).toBeVisible();
    await expect(page.getByRole('heading', { level: 3, name: /^Recently touched/ })).toBeVisible();

    // One row for the task, its two leases nested: holder, intent and the time left.
    await expect(page.getByTestId('flight-row')).toHaveCount(1);
    const leases = flightRow(page, s.live).getByTestId('flight-lease');
    await expect(leases).toHaveCount(2);
    await expect(leases.nth(0).getByTestId('lease-holder')).toHaveText(s.holders[0]);
    await expect(leases.nth(0).getByTestId('lease-intent')).toHaveText('edit columns');
    await expect(leases.nth(0).getByTestId('lease-ttl')).toHaveText(/^expires in (\d+h )?\d+[hm]$/);
    await expect(leases.nth(1).getByTestId('lease-holder')).toHaveText(s.holders[1]);
    await expect(leases.nth(1).getByTestId('lease-intent')).toHaveText('review columns');
    await expect(flightRow(page, s.live).getByTestId('flight-task')).toHaveAttribute('href', `#/t/${s.live}`);

    // The task whose lease ended is Recently touched with its last event, and is not In flight.
    await expect(flightRow(page, s.ended)).toHaveCount(0);
    await expect(page.getByTestId('recent-row')).toHaveCount(1);
    await expect(recentRow(page, s.ended).getByTestId('recent-last')).toContainText(/^lease_ended by flight-a-columns /);
    await expect(recentRow(page, s.ended).getByTestId('recent-task')).toHaveAttribute('href', `#/t/${s.ended}`);
    await expect(recentRow(page, s.live)).toHaveCount(0);
    await s.api.dispose();
  });

  test('a lease started through the API appears in In flight without a reload, and leaves it when it ends', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'live');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('flight-row')).toHaveCount(1, FIVE_SECONDS);
    await page.evaluate(() => ((window as unknown as { __kept: boolean }).__kept = true));
    const fresh = (await created(s.api, '/api/v1/tasks', { initiative: s.slug, title: 'Fresh work' })).ref;
    const lease = await created<{ lease: { ref: string } }>(s.api, '/api/v1/leases', { task: fresh, intent: 'started while watching' });
    await expect(page.getByTestId('flight-row')).toHaveCount(2, FIVE_SECONDS);
    await expect(flightRow(page, fresh).getByTestId('lease-intent')).toHaveText('started while watching');
    expect(await page.evaluate(() => (window as unknown as { __kept?: boolean }).__kept)).toBe(true);

    expect((await s.api.post(`/api/v1/leases/${lease.lease.ref}/end`, { data: {} })).status()).toBe(200);
    await expect(page.getByTestId('flight-row')).toHaveCount(1, FIVE_SECONDS);
    await expect(recentRow(page, fresh)).toBeVisible();
    await s.api.dispose();
  });

  test('Now builds the column from one read and never asks GET /api/v1/events', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'noevents');
    const paths: string[] = [];
    page.on('request', (request) => paths.push(new URL(request.url()).pathname));
    // The filter chips drop in below the page when the last page of the initiatives list is in and move the window switch:
    // wait for that page, so the forced click lands where the switch will stay.
    const listed = lastInitiativesPage(page);
    await page.goto(`/#/now?initiative=${s.slug}`);
    await listed;
    await expect(flightRow(page, s.live)).toBeVisible(FIVE_SECONDS);
    await expect(recentRow(page, s.ended)).toBeVisible();
    await windowChoice(page, '3d').check({ force: true });
    await expect.poll(() => paths.filter((path) => path === '/api/v1/attention/now').length).toBeGreaterThanOrEqual(2);
    expect(paths.filter((path) => path === '/api/v1/events')).toEqual([]);
    await s.api.dispose();
  });

  test('the window switch defaults to 24h, is remembered and changes what is read', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'window');
    const windows: (string | null)[] = [];
    page.on('request', (request) => {
      const url = new URL(request.url());
      if (url.pathname === '/api/v1/attention/now') windows.push(url.searchParams.get('window'));
    });
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(recentRow(page, s.ended)).toBeVisible(FIVE_SECONDS);
    await expect(page.getByTestId('now-window').getByRole('radio')).toHaveCount(3);
    await expect(windowChoice(page, '24h')).toBeChecked();
    expect(await storedWindow(page)).toBeNull();
    expect(windows[0]).toBeNull(); // the default is the server's own

    await windowChoice(page, '8h').check({ force: true });
    await expect.poll(() => windows).toContain('8h');
    expect(await storedWindow(page)).toBe('8h');
    await expect(recentRow(page, s.ended)).toBeVisible(); // just touched: inside 8h too

    await page.reload();
    await expect(windowChoice(page, '8h')).toBeChecked(FIVE_SECONDS);
    await expect(recentRow(page, s.ended)).toBeVisible();
    expect(windows[windows.length - 1]).toBe('8h');
    await s.api.dispose();
  });

  test('a bad stored window falls back to 24h', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'badwindow');
    await page.addInitScript((key) => localStorage.setItem(key, '99y'), FLIGHT_KEY);
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(recentRow(page, s.ended)).toBeVisible(FIVE_SECONDS);
    await expect(windowChoice(page, '24h')).toBeChecked();
    await s.api.dispose();
  });

  test('an initiative with nothing live says so', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'quiet');
    const quiet = 'e2e-flight-quiet-empty';
    await created(s.api, '/api/v1/initiatives', { slug: quiet, title: 'Quiet flight' });
    await page.goto(`/#/now?initiative=${quiet}`);
    await expect(page.getByTestId('flight-empty')).toBeVisible(FIVE_SECONDS);
    await expect(page.getByTestId('recent-empty')).toBeVisible();
    await s.api.dispose();
  });
});

test.describe('the right column on the phone', () => {
  test.use({ viewport: PHONE, hasTouch: true });

  test('the column stacks under Needs me with 44 px targets and no horizontal scroll', async ({ page, playwright, baseURL }) => {
    const s = await seedFlight(playwright, baseURL, 'phone');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await openFold(page, 'flight'); // a phone starts In flight closed
    await expect(flightRow(page, s.live)).toBeVisible(FIVE_SECONDS);
    await openFold(page, 'recent');
    const needs = (await page.getByTestId('now-needs').boundingBox())!;
    const right = (await flight(page).boundingBox())!;
    expect(right.y).toBeGreaterThanOrEqual(needs.y + needs.height);
    for (const target of [page.getByTestId('flight-task').first(), page.getByTestId('recent-task').first(), ...(await windowRadios(page))]) {
      const box = (await target.boundingBox())!;
      expect(box.height).toBeGreaterThanOrEqual(44 - 1 / 32);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await s.api.dispose();
  });
});

async function windowRadios(page: Page) {
  return page.getByTestId('now-window').locator('label').all();
}

for (const vp of [
  { name: 'phone', size: PHONE },
  { name: 'desktop', size: DESKTOP },
]) {
  for (const theme of ['light', 'dark'] as const) {
    for (const density of ['compact', 'sparse'] as const) {
      test.describe(`axe ${vp.name} ${theme} ${density}`, () => {
        test.use({ viewport: vp.size });

        test('Now with the right column passes axe', async ({ page, playwright, baseURL }) => {
          const s = await seedFlight(playwright, baseURL, `axe-${vp.name}-${theme}-${density}`);
          await page.addInitScript(
            ([t, d]) => {
              localStorage.setItem('vh.theme', t!);
              localStorage.setItem('vh.density.v1', d!);
            },
            [theme, density],
          );
          await page.goto(`/#/now?initiative=${s.slug}`);
          await openFold(page, 'flight');
          await expect(flightRow(page, s.live)).toBeVisible(FIVE_SECONDS);
          await openFold(page, 'recent');
          await expect(recentRow(page, s.ended)).toBeVisible();
          await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
          await expect(page.locator('html')).toHaveAttribute('data-density', density);
          await page.evaluate(() => Promise.all(document.getAnimations().map((a) => a.finished.catch(() => undefined))).then(() => undefined));
          const { violations } = await new AxeBuilder({ page }).include('[data-testid=now-view]').analyze();
          const found = violations
            .filter((v) => v.impact === 'serious' || v.impact === 'critical')
            .map((v) => ({ rule: v.id, impact: v.impact, nodes: v.nodes.map((n) => ({ target: n.target, summary: n.failureSummary })) }));
          expect(found, 'serious or critical axe violations').toEqual([]);
          await s.api.dispose();
        });
      });
    }
  }
}

// The shared chip filter on Now (PRD R7, SPEC 11): tags combine with AND and initiatives with OR, both are sent to the server
// as repeated `tag` and `initiative` on `GET /attention/now`, so Needs me, In flight and Recently touched all narrow together.
// The filter is in the fragment and in the saved view state. Each test seeds two initiatives of its own, one with a tag and
// one without, each holding an open ask and a task with a live lease.
interface ChipSeed {
  tag: string;
  tagged: { slug: string; ask: string; live: string };
  plain: { slug: string; ask: string; live: string };
}

async function seedChips(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string): Promise<ChipSeed> {
  const tag = `nowchip-${name}`;
  const api = await actorApi(playwright, baseURL, `nowchip-${name}`);
  const one = async (suffix: string, tags: string[]) => {
    const slug = `e2e-nowchip-${name}-${suffix}`;
    await created(api, '/api/v1/initiatives', { slug, title: `Chips ${name} ${suffix}`, tags });
    const asked = (await created(api, '/api/v1/tasks', { initiative: slug, title: `Asks in ${suffix}` })).ref;
    const live = (await created(api, '/api/v1/tasks', { initiative: slug, title: `Leased in ${suffix}` })).ref;
    const ask = (await created(api, '/api/v1/asks', { question: `Which way in ${suffix} ${name}?`, task: asked, mode: 'poll' })).ref;
    await created(api, '/api/v1/leases', { task: live, intent: `work in ${suffix}` });
    return { slug, ask, live };
  };
  const tagged = await one('a', [tag]);
  const plain = await one('b', []);
  await api.dispose();
  return { tag, tagged, plain };
}

const askItem = (page: Page, ref: string) => band(page, 'asks').locator('[data-testid=attention-item]', { hasText: ref });

for (const vp of [
  { name: 'phone', size: PHONE },
  { name: 'desktop', size: DESKTOP },
]) {
  test.describe(`the chip filter on Now, ${vp.name}`, () => {
    test.use({ viewport: vp.size });

    test('the shared chip filter renders on Now with its tag and initiative chips', async ({ page, playwright, baseURL }, testInfo) => {
      const s = await seedChips(playwright, baseURL, `render-${vp.name}-${testInfo.repeatEachIndex}`);
      // The chips are drawn from the initiatives list, which the view reads page by page: until its last page is in, the
      // filter holds no chip and has no height (on a wide screen its fold line is hidden too), so it is not visible. On a
      // store of hundreds of initiatives that takes longer than any fixed wait, so wait for the last page itself.
      const listed = page.waitForResponse(async (r) => {
        const url = new URL(r.url());
        return r.request().method() === 'GET' && url.pathname === '/api/v1/initiatives' && (await r.json()).next_after === undefined;
      });
      await page.goto('/#/now');
      await listed;
      await expect(page.getByTestId('now-filters').getByTestId('chip-filter')).toBeVisible(FIVE_SECONDS);
      await openChips(page);
      await expect(page.getByTestId('chip-tags')).toBeVisible();
      await expect(tagChip(page, s.tag)).toBeVisible();
      await expect(page.getByTestId('chip-initiatives')).toBeVisible();
      await expect(page.getByTestId('chip-tag').first()).toHaveAttribute('aria-pressed', 'false');
    });

    test('picking a tag removes a non-matching task from Needs me and from In flight, a matching one stays', async ({ page, playwright, baseURL }) => {
      const s = await seedChips(playwright, baseURL, `pick-${vp.name}`);
      await showAllBands(page);
      await page.goto('/#/now');
      await expect(askItem(page, s.tagged.ask)).toHaveCount(1, FIVE_SECONDS);
      await expect(askItem(page, s.plain.ask)).toHaveCount(1);
      await expect(flightRow(page, s.tagged.live)).toHaveCount(1);
      await expect(flightRow(page, s.plain.live)).toHaveCount(1);

      await pickTag(page, s.tag);
      await expect(page).toHaveURL(new RegExp(`#/now\\?tag=${s.tag}$`));
      await expect(askItem(page, s.plain.ask)).toHaveCount(0, FIVE_SECONDS);
      await expect(flightRow(page, s.plain.live)).toHaveCount(0);
      await expect(askItem(page, s.tagged.ask)).toHaveCount(1);
      await expect(flightRow(page, s.tagged.live)).toHaveCount(1);

      // Clearing the chips brings the other initiative's task back.
      await openChips(page);
      await page.getByTestId('chip-clear').click();
      await expect(askItem(page, s.plain.ask)).toHaveCount(1, FIVE_SECONDS);
      await expect(flightRow(page, s.plain.live)).toHaveCount(1);
    });

    test('initiatives combine with OR: two initiatives show both, one shows its own', async ({ page, playwright, baseURL }) => {
      const s = await seedChips(playwright, baseURL, `or-${vp.name}`);
      await showAllBands(page);
      const listed = catalogRead(page, s.plain.slug);
      await page.goto(`/#/now?initiative=${s.tagged.slug}`);
      await expect(askItem(page, s.tagged.ask)).toHaveCount(1, FIVE_SECONDS);
      await expect(askItem(page, s.plain.ask)).toHaveCount(0);
      await listed;
      await pickInitiative(page, s.plain.slug);
      await expect(askItem(page, s.plain.ask)).toHaveCount(1, FIVE_SECONDS);
      await expect(askItem(page, s.tagged.ask)).toHaveCount(1);
      await expect(flightRow(page, s.plain.live)).toHaveCount(1);
      await expect(flightRow(page, s.tagged.live)).toHaveCount(1);
    });

    test('the URL holds the filter and a reload keeps it', async ({ page, playwright, baseURL }) => {
      const s = await seedChips(playwright, baseURL, `url-${vp.name}`);
      await showAllBands(page);
      await page.goto('/#/now');
      await pickTag(page, s.tag);
      await expect(page).toHaveURL(new RegExp(`#/now\\?tag=${s.tag}$`));
      await expect(askItem(page, s.plain.ask)).toHaveCount(0, FIVE_SECONDS);

      await page.reload();
      await expect(page).toHaveURL(new RegExp(`#/now\\?tag=${s.tag}$`));
      await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true', FIVE_SECONDS);
      await expect(askItem(page, s.tagged.ask)).toHaveCount(1, FIVE_SECONDS);
      await expect(askItem(page, s.plain.ask)).toHaveCount(0);
      await expect(flightRow(page, s.plain.live)).toHaveCount(0);
      await expect(flightRow(page, s.tagged.live)).toHaveCount(1);
    });

    test('a filter carried over from a #/attention link applies, and is saved with the view state', async ({ page, playwright, baseURL }) => {
      const s = await seedChips(playwright, baseURL, `link-${vp.name}`);
      await showAllBands(page);
      await page.goto(`/#/attention?tag=${s.tag}`);
      await expect(page).toHaveURL(new RegExp(`#/now\\?tag=${s.tag}$`), FIVE_SECONDS);
      await expect(tagChip(page, s.tag)).toHaveAttribute('aria-pressed', 'true');
      await expect(askItem(page, s.tagged.ask)).toHaveCount(1, FIVE_SECONDS);
      await expect(askItem(page, s.plain.ask)).toHaveCount(0);
      await expect(flightRow(page, s.plain.live)).toHaveCount(0);
      expect(await page.evaluate(() => JSON.parse(localStorage.getItem('vh.view.v1.attention') ?? 'null') as unknown)).toEqual({ v: 1, query: `tag=${s.tag}` });
    });
  });
}

// The one sort of Now (PRD R3, SPEC 11): the shared sort control orders the rows inside every band, newest first by default,
// and the choice is remembered per browser under `vh.now.sort.v1`.
const SORT_KEY = 'vh.now.sort.v1';

// Three open asks of one initiative, raised a second apart by actors whose handles sort the other way round: the newest ask is
// by `a`, the oldest by `m`. Newest first is therefore a, z, m; oldest first m, z, a; by actor a, m, z.
async function seedSortedAsks(playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  const slug = `e2e-nowsort-${name}`;
  const owner = await actorApi(playwright, baseURL, `nowsort-owner-${name}`);
  await created(owner, '/api/v1/initiatives', { slug, title: `Sort ${name}` });
  const task = (await created(owner, '/api/v1/tasks', { initiative: slug, title: 'Needs answers' })).ref;
  const refs: Record<string, string> = {};
  for (const who of ['m', 'z', 'a']) {
    const api = await actorApi(playwright, baseURL, `nowsort-${who}-${name}`);
    refs[who] = (await created(api, '/api/v1/asks', { question: `Question from ${who} in ${name}?`, task, mode: 'poll' })).ref;
    await api.dispose();
    await new Promise((resolve) => setTimeout(resolve, 1100));
  }
  await owner.dispose();
  return { slug, refs };
}

const askOrder = async (page: Page, refs: Record<string, string>): Promise<string[]> => {
  const titles = await band(page, 'asks').getByTestId('attention-item').locator('p.font-medium').allInnerTexts();
  return titles.map((t) => Object.keys(refs).find((who) => t.startsWith(`${refs[who]}:`))!);
};

for (const vp of [
  { name: 'phone', size: PHONE },
  { name: 'desktop', size: DESKTOP },
]) {
  test.describe(`the band sort on Now, ${vp.name}`, () => {
    test.use({ viewport: vp.size });

    test('the shared control reorders a seeded band, and the choice survives a reload', async ({ page, playwright, baseURL }) => {
      const s = await seedSortedAsks(playwright, baseURL, `reorder-${vp.name}`);
      await showAllBands(page);
      await page.goto(`/#/now?initiative=${s.slug}`);
      const select = page.getByTestId('now-sort').getByTestId('sort-select');
      await expect(band(page, 'asks').getByTestId('attention-item')).toHaveCount(3, FIVE_SECONDS);
      await expect(select).toHaveValue('');
      await expect(select.locator('option')).toHaveText(['Newest first', 'Oldest first', 'By actor']);
      expect(await askOrder(page, s.refs)).toEqual(['a', 'z', 'm']);

      await select.selectOption({ label: 'Oldest first' });
      await expect.poll(() => askOrder(page, s.refs)).toEqual(['m', 'z', 'a']);
      expect(await page.evaluate((key) => localStorage.getItem(key), SORT_KEY)).toBe('oldest');

      await select.selectOption({ label: 'By actor' });
      await expect.poll(() => askOrder(page, s.refs)).toEqual(['a', 'm', 'z']);

      await page.reload();
      await expect(band(page, 'asks').getByTestId('attention-item')).toHaveCount(3, FIVE_SECONDS);
      await expect(page.getByTestId('now-sort').getByTestId('sort-select')).toHaveValue('actor:asc');
      expect(await askOrder(page, s.refs)).toEqual(['a', 'm', 'z']);

      await page.getByTestId('now-sort').getByTestId('sort-select').selectOption({ label: 'Newest first' });
      await expect.poll(() => askOrder(page, s.refs)).toEqual(['a', 'z', 'm']);
      await page.reload();
      await expect(page.getByTestId('now-sort').getByTestId('sort-select')).toHaveValue('', FIVE_SECONDS);
    });

    test('conflicts of different ages sort by when their contest began, in both time orders', async ({ page, playwright, baseURL }) => {
      const slug = `e2e-nowsort-conflicts-${vp.name}`;
      const a = await actorApi(playwright, baseURL, `nowsort-ca-${vp.name}`);
      const b = await actorApi(playwright, baseURL, `nowsort-cb-${vp.name}`);
      await created(a, '/api/v1/initiatives', { slug, title: `Conflict sort ${vp.name}` });
      const task = async (title: string) => (await created(a, '/api/v1/tasks', { initiative: slug, title })).ref;
      const resources = [`repo:sort-old-${vp.name}`, `repo:sort-new-${vp.name}`];
      for (const resource of resources) {
        await created(a, '/api/v1/leases', { task: await task(`First on ${resource}`), intent: 'first', resources: [resource] });
        await created(b, '/api/v1/leases', { task: await task(`Second on ${resource}`), intent: 'second', resources: [resource] });
        await new Promise((resolve) => setTimeout(resolve, 1100));
      }
      await a.dispose();
      await b.dispose();
      await showAllBands(page);
      await page.goto(`/#/now?initiative=${slug}`);
      await openBand(page, 'expired');
      const rows = band(page, 'expired').getByTestId('attention-item');
      await expect(rows).toHaveCount(2, FIVE_SECONDS);
      const order = async () => (await rows.locator('p.font-medium').allInnerTexts()).map((t) => resources.findIndex((r) => t.includes(r)));
      expect(await order()).toEqual([1, 0]);
      const select = page.getByTestId('now-sort').getByTestId('sort-select');
      await select.selectOption({ label: 'Oldest first' });
      await expect.poll(order).toEqual([0, 1]);
      await select.selectOption({ label: 'Newest first' });
      await expect.poll(order).toEqual([1, 0]);
    });

    test('a bad stored value falls back to newest first, and the sort leaves the URL alone', async ({ page, playwright, baseURL }) => {
      const s = await seedSortedAsks(playwright, baseURL, `bad-${vp.name}`);
      await page.addInitScript((key) => localStorage.setItem(key, 'sideways'), SORT_KEY);
      await showAllBands(page);
      await page.goto(`/#/now?initiative=${s.slug}`);
      await expect(band(page, 'asks').getByTestId('attention-item')).toHaveCount(3, FIVE_SECONDS);
      await expect(page.getByTestId('now-sort').getByTestId('sort-select')).toHaveValue('');
      expect(await askOrder(page, s.refs)).toEqual(['a', 'z', 'm']);
      await page.getByTestId('now-sort').getByTestId('sort-select').selectOption({ label: 'Oldest first' });
      await expect(page).toHaveURL(new RegExp(`#/now\\?initiative=${s.slug}$`));
    });
  });
}

// The dock (PRD R4, SPEC 11): from lg the widgets sit in 1 to 4 columns the person arranges by dragging a handle, by the
// splitters and height bars, or from the keyboard, and the arrangement is kept under `vh.now.layout.v1`. Drags use the mouse API.
interface StoredLayout {
  columns: { width: number; widgets: string[] }[];
  heights: Record<string, number>;
  open: Record<string, boolean>;
}
const layoutOf = async (page: Page) => (await storedLayout(page)) as StoredLayout | null;
const columnsOf = async (page: Page) => (await layoutOf(page))!.columns.map((c) => c.widgets.join(','));
const handle = (page: Page, key: string) => page.locator(`[data-handle="${key}"]`);
const dockColumn = (page: Page, n: number) => page.locator(`[data-dock-col="${n}"]`);
const resetLayout = (page: Page) => page.getByTestId('reset-layout');
const DEFAULT_COLUMNS = ['asks,expired,watches,done', 'flight,recent'];

async function centre(target: ReturnType<Page['locator']>) {
  const box = (await target.boundingBox())!;
  return { x: box.x + box.width / 2, y: box.y + box.height / 2, box };
}

// A real pointer drag: press on the handle, move in steps, release.
async function dragTo(page: Page, grip: ReturnType<Page['locator']>, x: number, y: number) {
  const from = await centre(grip);
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move((from.x + x) / 2, (from.y + y) / 2, { steps: 6 });
  await page.mouse.move(x, y, { steps: 6 });
  await page.mouse.up();
}

// Resolves when the last page of the initiatives list has been read (`next_after` absent). Call it before the navigation.
function lastInitiativesPage(page: Page) {
  return page.waitForResponse(async (r) => {
    const url = new URL(r.url());
    return r.request().method() === 'GET' && url.pathname === '/api/v1/initiatives' && (await r.json()).next_after === undefined;
  });
}

async function openDock(page: Page, playwright: PlaywrightTestArgs['playwright'], baseURL: string | undefined, name: string) {
  // A repeated run (--repeat-each) of the same test seeds its own initiative: the slug is unique on the store.
  const again = test.info().repeatEachIndex;
  const s = await seedFlight(playwright, baseURL, again === 0 ? `dock-${name}` : `dock-${name}-${again}`);
  // On a long run the shared board's filter chips push the dock below the fold, and a pointer drag only reaches what is on
  // screen. The chips are drawn from the initiatives list, which the view reads page by page and which holds no chip until its
  // last page is in, so the dock does not move while the pages arrive and then drops by the height of every chip at once.
  // Wait for that last page, then for the dock to stop moving, and only then scroll it into view.
  const listed = lastInitiativesPage(page);
  await page.goto(`/#/now?initiative=${s.slug}`);
  await listed;
  await expect(flightRow(page, s.live)).toBeVisible(FIVE_SECONDS);
  const dock = page.getByTestId('now-dock');
  let last = -1;
  await expect
    .poll(async () => {
      const top = await dock.evaluate((el) => Math.round(el.getBoundingClientRect().top + window.scrollY));
      const still = top === last;
      last = top;
      return still;
    }, { intervals: [300], timeout: 10000 })
    .toBe(true);
  await dock.evaluate((el) => el.scrollIntoView({ block: 'start' }));
  await s.api.dispose();
  return s;
}

test.describe('the dock on the desktop', () => {
  test.use({ viewport: DESKTOP });

  test('every widget has a handle, two columns start at half each with a splitter between, and Reset layout is disabled', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'default');
    await expect(page.getByTestId('widget-handle')).toHaveCount(6);
    await expect(page.getByTestId('dock-column')).toHaveCount(2);
    await expect(page.getByTestId('column-splitter')).toHaveCount(1);
    await expect(resetLayout(page)).toBeVisible();
    await expect(resetLayout(page)).toBeDisabled();
    const a = (await dockColumn(page, 0).boundingBox())!;
    const b = (await dockColumn(page, 1).boundingBox())!;
    expect(Math.abs(a.width - b.width)).toBeLessThanOrEqual(2);
    expect(await layoutOf(page)).toBeNull(); // nothing is written until the person arranges something
  });

  test('a widget dragged by its handle into the other column docks there, and the layout holds after a reload', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'drag');
    const target = await centre(page.getByTestId('now-flight'));
    await dragTo(page, handle(page, 'asks'), target.x, target.box.y + 6);
    await expect(dockColumn(page, 1).getByTestId('band-asks')).toBeVisible();
    await expect(dockColumn(page, 0).getByTestId('band-asks')).toHaveCount(0);
    expect(await columnsOf(page)).toEqual(['expired,watches,done', 'asks,flight,recent']);
    await expect(resetLayout(page)).toBeEnabled();

    await page.reload();
    await expect(dockColumn(page, 1).getByTestId('band-asks')).toBeVisible(FIVE_SECONDS);
    expect(await columnsOf(page)).toEqual(['expired,watches,done', 'asks,flight,recent']);
    await expect(resetLayout(page)).toBeEnabled();
    const asks = (await band(page, 'asks').boundingBox())!;
    const flightBox = (await page.getByTestId('now-flight').boundingBox())!;
    expect(asks.y).toBeLessThan(flightBox.y); // docked above In flight, where it was dropped
  });

  test('a widget dropped on the edge of the dock opens a third column, and the last widget out of a column closes it', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'edge');
    const dock = (await page.getByTestId('now-dock').boundingBox())!;
    await dragTo(page, handle(page, 'recent'), dock.x + dock.width - 12, dock.y + 60);
    await expect(page.getByTestId('dock-column')).toHaveCount(3);
    expect(await columnsOf(page)).toEqual(['asks,expired,watches,done', 'flight', 'recent']);
    await expect(dockColumn(page, 2).getByTestId('now-recent')).toBeVisible();
    const widths = (await layoutOf(page))!.columns.map((c) => c.width);
    expect(widths.reduce((x, y) => x + y, 0)).toBeCloseTo(1, 6);
    expect(Math.min(...widths)).toBeGreaterThanOrEqual(0.18 - 1e-9);

    const first = await centre(dockColumn(page, 0));
    await dragTo(page, handle(page, 'flight'), first.x, first.box.y + 6);
    await expect(page.getByTestId('dock-column')).toHaveCount(2);
    expect(await columnsOf(page)).toEqual(['flight,asks,expired,watches,done', 'recent']);
  });

  test('a splitter resizes the columns, kept at least 18% and after a reload', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'split');
    const before = (await dockColumn(page, 0).boundingBox())!;
    const grip = await centre(page.getByTestId('column-splitter'));
    await dragTo(page, page.getByTestId('column-splitter'), grip.x + 160, grip.y);
    await expect.poll(async () => (await dockColumn(page, 0).boundingBox())!.width).toBeGreaterThan(before.width + 120);
    const stored = (await layoutOf(page))!;
    expect(stored.columns[0]!.width).toBeGreaterThan(0.55);
    expect(stored.columns[0]!.width + stored.columns[1]!.width).toBeCloseTo(1, 6);
    const after = (await dockColumn(page, 0).boundingBox())!;

    await page.reload();
    await expect(dockColumn(page, 0)).toBeVisible(FIVE_SECONDS);
    expect(Math.abs((await dockColumn(page, 0).boundingBox())!.width - after.width)).toBeLessThanOrEqual(2);
    await expect(resetLayout(page)).toBeEnabled();

    // Far past the other column's minimum: it stops at 18%.
    const again = await centre(page.getByTestId('column-splitter'));
    await dragTo(page, page.getByTestId('column-splitter'), again.x + 1000, again.y);
    await expect.poll(async () => (await layoutOf(page))!.columns[1]!.width).toBeCloseTo(0.18, 6);
  });

  test('the bar under a widget resizes its height, kept after a reload', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'height');
    const panel = band(page, 'asks').getByTestId('band-panel');
    const before = (await panel.boundingBox())!.height;
    await dragTo(page, band(page, 'asks').getByTestId('height-handle'), (await centre(band(page, 'asks').getByTestId('height-handle'))).x, (await centre(band(page, 'asks').getByTestId('height-handle'))).y + 140);
    await expect.poll(async () => (await panel.boundingBox())!.height).toBeCloseTo(before + 140, -1);
    const stored = (await layoutOf(page))!.heights.asks!;
    expect(stored).toBeGreaterThan(before + 100);

    await page.reload();
    await expect(band(page, 'asks')).toBeVisible(FIVE_SECONDS);
    expect(Math.abs((await panel.boundingBox())!.height - stored)).toBeLessThanOrEqual(1);
    await expect(resetLayout(page)).toBeEnabled();
  });

  test('Reset layout restores the default columns, widths, heights and open states, then disables itself', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'reset');
    const defaultWidth = (await dockColumn(page, 0).boundingBox())!.width;
    await dragTo(page, handle(page, 'done'), (await centre(dockColumn(page, 1))).x, (await dockColumn(page, 1).boundingBox())!.y + 6);
    const grip = await centre(page.getByTestId('column-splitter'));
    await dragTo(page, page.getByTestId('column-splitter'), grip.x - 150, grip.y);
    const heightBar = band(page, 'asks').getByTestId('height-handle');
    await dragTo(page, heightBar, (await centre(heightBar)).x, (await centre(heightBar)).y + 90);
    await band(page, 'watches').getByTestId('band-toggle').click();
    await expect(resetLayout(page)).toBeEnabled();
    // The layout is written when the page has handled each pointer-up, which on a large store can trail the drag: read it
    // only once the stored layout shows the resized height (and the closed band), never after a fixed wait.
    await expect
      .poll(async () => {
        const stored = await layoutOf(page);
        return stored !== null && (stored.heights.asks ?? 0) > 0 && stored.open.watches === false;
      }, FIVE_SECONDS)
      .toBe(true);
    const arranged = (await layoutOf(page))!;
    expect(arranged.columns.map((c) => c.widgets.join(','))).not.toEqual(DEFAULT_COLUMNS);
    expect(Object.keys(arranged.heights)).toEqual(['asks']);
    expect(arranged.open).toMatchObject({ watches: false });

    await resetLayout(page).click();
    await expect(resetLayout(page)).toBeDisabled();
    expect(await columnsOf(page)).toEqual(DEFAULT_COLUMNS);
    const reset = (await layoutOf(page))!;
    expect(reset.columns.map((c) => c.width)).toEqual([0.5, 0.5]);
    expect(reset.heights).toEqual({});
    expect(reset.open).toEqual({});
    await expect(band(page, 'watches').getByTestId('band-toggle')).toHaveAttribute('aria-expanded', 'true');
    await expect(band(page, 'done').getByTestId('band-toggle')).toHaveAttribute('aria-expanded', 'false');
    await expect(dockColumn(page, 0).getByTestId('band-done')).toBeVisible();
    await expect.poll(async () => Math.abs((await dockColumn(page, 0).boundingBox())!.width - defaultWidth)).toBeLessThanOrEqual(2);
    await page.reload();
    await expect(resetLayout(page)).toBeDisabled();
  });

  test('a toggled band put back to its default open state counts as the default layout', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'toggle');
    const toggle = band(page, 'asks').getByTestId('band-toggle');
    await toggle.click();
    await expect(resetLayout(page)).toBeEnabled();
    await toggle.click();
    await expect(resetLayout(page)).toBeDisabled();
  });

  test('a focused handle moves with the arrow keys, announces its place and keeps focus; a move across the edge docks a new column', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'keys');
    const status = page.getByTestId('dock-status');
    await handle(page, 'asks').focus();
    await page.keyboard.press('ArrowDown');
    await expect(status).toHaveText('Open asks, column 1 of 2, position 2 of 4');
    await expect(handle(page, 'asks')).toBeFocused();
    expect(await columnsOf(page)).toEqual(['expired,asks,watches,done', 'flight,recent']);
    await page.keyboard.press('ArrowUp');
    await expect(status).toHaveText('Open asks, column 1 of 2, position 1 of 4');

    await handle(page, 'done').focus();
    await page.keyboard.press('ArrowRight');
    await expect(status).toHaveText('Done, not closed, column 2 of 2, position 3 of 3');
    await expect(handle(page, 'done')).toBeFocused();
    await expect(dockColumn(page, 1).getByTestId('band-done')).toBeVisible();
    await page.keyboard.press('ArrowRight');
    await expect(status).toHaveText('Done, not closed, column 3 of 3, position 1 of 1');
    await expect(page.getByTestId('dock-column')).toHaveCount(3);
    await expect(dockColumn(page, 2).getByTestId('band-done')).toBeVisible();
    expect(await columnsOf(page)).toEqual(['asks,expired,watches', 'flight,recent', 'done']);
    await page.keyboard.press('ArrowRight');
    await expect(status).toHaveText('Done, not closed, column 3 of 3, position 1 of 1, cannot move right');
    await page.keyboard.press('ArrowLeft'); // back across: the lone widget's column closes
    await expect(page.getByTestId('dock-column')).toHaveCount(2);
    await expect(status).toHaveText('Done, not closed, column 2 of 2, position 1 of 3');
    await expect(resetLayout(page)).toBeEnabled();

    await page.reload();
    await expect(dockColumn(page, 1).getByTestId('band-done')).toBeVisible(FIVE_SECONDS);
    expect(await columnsOf(page)).toEqual(['asks,expired,watches', 'done,flight,recent']);
  });

  test('a splitter and a height bar take the arrow keys and say their value', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'keysize');
    const splitter = page.getByTestId('column-splitter');
    await expect(splitter).toHaveAttribute('aria-valuenow', '50');
    await splitter.focus();
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await expect(splitter).toHaveAttribute('aria-valuenow', '56');
    await expect(page.getByTestId('dock-status')).toHaveText('Column 1 is 56% wide, column 2 is 44%');
    expect((await layoutOf(page))!.columns[0]!.width).toBeCloseTo(0.56, 6);
    await page.keyboard.press('ArrowLeft');
    await expect(splitter).toHaveAttribute('aria-valuenow', '54');

    const bar = band(page, 'asks').getByTestId('height-handle');
    const before = (await band(page, 'asks').getByTestId('band-panel').boundingBox())!.height;
    await bar.focus();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await expect.poll(async () => (await layoutOf(page))!.heights.asks).toBeCloseTo(Math.round(before) + 48, -1);
    await expect(page.getByTestId('dock-status')).toHaveText(/^Open asks is \d+ pixels high$/);
    await page.keyboard.press('Home');
    await expect(page.getByTestId('dock-status')).toHaveText('Open asks takes the height of its content');
    expect((await layoutOf(page))!.heights).toEqual({});
  });

  test('a stored layout that breaks the model (a column under 18%) is the default', async ({ page, playwright, baseURL }) => {
    const bad = JSON.stringify({ columns: [{ width: 0.05, widgets: ['asks', 'expired', 'watches', 'done'] }, { width: 0.95, widgets: ['flight', 'recent'] }], heights: {}, open: {} });
    await page.addInitScript(([key, value]) => localStorage.setItem(key!, value!), [LAYOUT_KEY, bad]);
    await openDock(page, playwright, baseURL, 'bad');
    await expect(page.getByTestId('dock-column')).toHaveCount(2);
    await expect(resetLayout(page)).toBeDisabled();
    const a = (await dockColumn(page, 0).boundingBox())!;
    const b = (await dockColumn(page, 1).boundingBox())!;
    expect(Math.abs(a.width - b.width)).toBeLessThanOrEqual(2);
  });

  test('the dock is drawn from 1024 px and the widgets stack below it', async ({ page, playwright, baseURL }) => {
    await openDock(page, playwright, baseURL, 'lg');
    await page.setViewportSize({ width: 1024, height: 800 });
    await expect(page.getByTestId('now-dock')).toBeVisible();
    await page.setViewportSize({ width: 1023, height: 800 });
    await expect(page.getByTestId('now-dock')).toHaveCount(0);
    await expect(resetLayout(page)).toHaveCount(0);
    await expect(page.getByTestId('widget-handle')).toHaveCount(0);
    await expect(page.getByTestId('now-needs')).toBeVisible();
    await page.setViewportSize({ width: 1024, height: 800 });
    await expect(page.getByTestId('now-dock')).toBeVisible();
  });
});

test.describe('the dock and the phone', () => {
  const custom = JSON.stringify({
    columns: [
      { width: 0.3, widgets: ['asks', 'expired'] },
      { width: 0.3, widgets: ['flight', 'done'] },
      { width: 0.4, widgets: ['watches', 'recent'] },
    ],
    heights: { asks: 300 },
    open: { done: true },
  });

  test('at 390x844 the accordion shows with no handles or Reset, and the stored desktop layout is kept untouched', async ({ page, playwright, baseURL }) => {
    await page.setViewportSize(PHONE);
    await page.addInitScript(([key, value]) => localStorage.getItem(key!) === null && localStorage.setItem(key!, value!), [LAYOUT_KEY, custom]);
    const s = await seedNeedsMe(playwright, baseURL, 'phone-dock');
    await page.goto(`/#/now?initiative=${s.slug}`);
    await expect(page.getByTestId('band-count')).toHaveText(['1', '1', '1', '1'], FIVE_SECONDS);
    expect(await expanded(page)).toEqual(['true', 'true', 'false', 'false']);
    await expect(page.getByTestId('now-dock')).toHaveCount(0);
    await expect(page.getByTestId('widget-handle')).toHaveCount(0);
    await expect(resetLayout(page)).toHaveCount(0);
    await band(page, 'watches').getByTestId('band-toggle').click();
    await expect(band(page, 'watches').getByTestId('attention-item')).toBeVisible();
    await page.reload();
    await expect(band(page, 'watches').getByTestId('attention-item')).toBeVisible(FIVE_SECONDS);
    expect(await page.evaluate((key) => localStorage.getItem(key), LAYOUT_KEY)).toBe(custom);

    // The same browser on the desktop shows the arrangement it kept.
    await page.setViewportSize(DESKTOP);
    await expect(page.getByTestId('dock-column')).toHaveCount(3);
    expect(await columnsOf(page)).toEqual(['asks,expired', 'flight,done', 'watches,recent']);
    await expect(resetLayout(page)).toBeEnabled();
    await expect(dockColumn(page, 1).getByTestId('band-done').getByTestId('band-toggle')).toHaveAttribute('aria-expanded', 'true');
    expect(Math.round((await band(page, 'asks').getByTestId('band-panel').boundingBox())!.height)).toBe(300);
  });
});
