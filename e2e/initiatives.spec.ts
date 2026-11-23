import { expect, test as base, type Page } from '@playwright/test';

// Signs in through the mock provider, so no API key rides along (see login.spec.ts).
// The Initiatives view at the phone and the desktop size: the table or the cards, the create form
// in the bottom sheet or the dialog, the toast, the skeleton and the empty state.
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

// The list request, whatever its query (the status filter rides on it).
// A fetch of the list is its first page: the board is shared with every other spec, so a long list is read in further pages (`after`), which a held or counted fetch must not count again.
const isList = (url: URL) => url.pathname === '/api/v1/initiatives' && !url.searchParams.has('after');

const newInitiative = (page: Page) => page.getByRole('button', { name: 'New initiative' });

async function openForm(page: Page) {
  await newInitiative(page).click();
  const dialog = page.getByRole('dialog', { name: 'New initiative' });
  await expect(dialog).toBeVisible();
  return dialog;
}

// Holds the first `hold` GETs of the list (the first fetch and the reload after the test's own write) until the test
// releases each by its order (0 = the first fetch). Each held GET is answered from the server as it stood when the
// request was made, so a fetch released late carries an older list; `fail` answers it with a 503 problem document instead.
// The test's own write also reaches the page as a live event, whose refresh is a further GET 500 ms later (the live
// debounce): one the test never releases would keep the list loading for good once the test runs slower than that, as
// it does on a loaded agent. A GET past the held ones waits until every held one is released, then lands, newest.
async function holdListGets(page: Page, pattern: (url: URL) => boolean, hold = 2) {
  const held: { release: (fail?: boolean) => void }[] = [];
  let released = 0;
  let allReleased: () => void = () => undefined;
  const afterHeld = new Promise<void>((resolve) => (allReleased = resolve));
  await page.route(pattern, async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    if (held.length >= hold) {
      await afterHeld;
      return route.fulfill({ response });
    }
    const fail = await new Promise<boolean>((resolve) => held.push({ release: (f = false) => resolve(f) }));
    if (fail) return route.fulfill({ status: 503, contentType: 'application/problem+json', json: { title: 'Unavailable', status: 503, detail: 'The list is unavailable.' } });
    return route.fulfill({ response });
  });
  return {
    count: () => held.length,
    /** Releases GET number `n` and waits until the page has taken its answer in. */
    release: async (n: number, fail = false) => {
      const landed = page.waitForResponse((r) => r.request().method() === 'GET' && pattern(new URL(r.url())));
      held[n]!.release(fail);
      released += 1;
      if (released === hold) allReleased();
      await landed;
      // Lets the page parse the answer and render it before the test looks.
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    },
  };
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });
    const slug = (name: string) => `e2e-${vp.name}-${name}`;

    test('an initiative created through the form is listed with its status and tags, announced, and survives a reload', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByRole('heading', { level: 2, name: 'Initiatives' })).toBeVisible();
      const row = page.locator(`[data-testid=initiative-row][data-slug=${slug('created')}]`);
      await expect(row).toHaveCount(0);

      const dialog = await openForm(page);
      // The form sits at the bottom edge on the phone and is centred on the desktop
      // (the sheet slides up on open: wait for it to settle).
      const bottom = async () => {
        const box = await dialog.boundingBox();
        return box === null ? -1 : Math.round(box.y + box.height);
      };
      if (vp.size.width < 768) await expect.poll(bottom).toBe(vp.size.height);
      else expect(await bottom()).toBeLessThan(vp.size.height);

      await dialog.getByLabel('Slug').fill(slug('created'));
      await dialog.getByLabel('Title').fill('Created from the page');
      await dialog.getByLabel('Tags').fill('web, e2e');
      await dialog.getByRole('button', { name: 'Create initiative' }).click();

      // Success closes the form, lists the row and announces it in the polite live region.
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(row).toHaveCount(1);
      await expect(row).toContainText('Created from the page');
      await expect(row.getByTestId('initiative-status')).toHaveText('open');
      await expect(row.getByTestId('initiative-tags').getByTestId('tag')).toHaveText(['e2e', 'web']);
      await expect(page.locator('[aria-live=polite]').getByTestId('toast')).toContainText(slug('created'));
      await expect(page.getByRole('alert')).toHaveCount(0);

      // Created is a <time> with the instant and the absolute local time.
      const time = row.locator('time');
      await expect(time).toHaveAttribute('datetime', /^\d{4}-\d\d-\d\dT/);
      await expect(time).toHaveAttribute('title', /^\d{4}-\d\d-\d\d \d\d:\d\d$/);
      await expect(time).toHaveText('just now');

      await page.reload();
      await expect(row).toHaveCount(1);
      await expect(row.getByTestId('initiative-tags').getByTestId('tag')).toHaveText(['e2e', 'web']);
    });

    test('a slug the server refuses is shown on the form, nothing is listed, and what was typed stays', async ({ page }) => {
      await page.goto('/#/initiatives');
      await expect(page.getByRole('heading', { level: 2, name: 'Initiatives' })).toBeVisible();
      const dialog = await openForm(page);
      await dialog.getByLabel('Slug').fill('Bad Slug!');
      await dialog.getByLabel('Title').fill('Refused');
      await dialog.getByRole('button', { name: 'Create initiative' }).click();

      await expect(dialog.getByRole('alert')).toContainText('slug');
      await expect(page.locator('[data-testid=initiative-row][data-slug="Bad Slug!"]')).toHaveCount(0);
      await expect(dialog.getByLabel('Slug')).toHaveValue('Bad Slug!');
      await expect(dialog.getByLabel('Title')).toHaveValue('Refused');

      await dialog.getByLabel('Slug').fill(slug('fixed'));
      await dialog.getByRole('button', { name: 'Create initiative' }).click();
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${slug('fixed')}]`)).toHaveCount(1);
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(page.getByRole('alert')).toHaveCount(0);
    });

    test('a taken slug shows the server detail on the form and keeps the input', async ({ page, playwright, baseURL }) => {
      const seed = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      const first = await seed.post('/api/v1/initiatives', { data: { slug: slug('taken'), title: 'Already here' } });
      expect([201, 409]).toContain(first.status());
      const again = await seed.post('/api/v1/initiatives', { data: { slug: slug('taken'), title: 'Again' } });
      expect(again.status()).toBe(409);
      const detail = ((await again.json()) as { detail: string }).detail;
      expect(detail).not.toBe('');
      await seed.dispose();

      await page.goto('/#/initiatives');
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${slug('taken')}]`)).toHaveCount(1);
      const dialog = await openForm(page);
      await dialog.getByLabel('Slug').fill(slug('taken'));
      await dialog.getByLabel('Title').fill('Second try');
      await dialog.getByLabel('Tags').fill('keep-me');
      await dialog.getByRole('button', { name: 'Create initiative' }).click();

      await expect(dialog.getByRole('alert')).toContainText(detail);
      await expect(dialog.getByLabel('Slug')).toHaveValue(slug('taken'));
      await expect(dialog.getByLabel('Title')).toHaveValue('Second try');
      await expect(dialog.getByLabel('Tags')).toHaveValue('keep-me');
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${slug('taken')}]`)).toHaveCount(1);

      // Escape closes the form and a reopened one starts clean.
      await page.keyboard.press('Escape');
      await expect(page.getByRole('dialog')).toHaveCount(0);
      const reopened = await openForm(page);
      await expect(reopened.getByRole('alert')).toHaveCount(0);
      await expect(reopened.getByLabel('Slug')).toHaveValue('');
    });

    test('the empty state offers New initiative, which opens the form', async ({ page }) => {
      await page.route(isList, (route) =>
        route.request().method() === 'GET' ? route.fulfill({ json: { items: [] } }) : route.continue(),
      );
      await page.goto('/#/initiatives');
      const empty = page.getByTestId('empty-state');
      await expect(empty).toBeVisible();
      await expect(page.getByTestId('initiative-row')).toHaveCount(0);
      await expect(newInitiative(page)).toHaveCount(1);
      await empty.getByRole('button', { name: 'New initiative' }).click();
      await expect(page.getByRole('dialog', { name: 'New initiative' })).toBeVisible();
    });

    test('loading shows skeleton rows until the list arrives', async ({ page }) => {
      let release: () => void = () => undefined;
      const gate = new Promise<void>((resolve) => (release = resolve));
      await page.route(isList, async (route) => {
        if (route.request().method() !== 'GET') return route.continue();
        await gate;
        return route.fulfill({ json: { items: [] } });
      });
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('skeleton-row')).not.toHaveCount(0);
      await expect(page.getByTestId('initiative-row')).toHaveCount(0);
      release();
      await expect(page.getByTestId('skeleton-row')).toHaveCount(0);
      await expect(page.getByTestId('empty-state')).toBeVisible();
    });

    test('the refresh after a create shows skeleton rows while it is pending, then the list', async ({ page }) => {
      let held = 0;
      let holdFrom = Number.POSITIVE_INFINITY;
      let release: () => void = () => undefined;
      const gate = new Promise<void>((resolve) => (release = resolve));
      await page.route(isList, async (route) => {
        if (route.request().method() !== 'GET') return route.continue();
        held += 1;
        if (held >= holdFrom) await gate;
        return route.fetch().then((response) => route.fulfill({ response }));
      });
      await page.goto('/#/initiatives');
      await expect(page.getByTestId('skeleton-row')).toHaveCount(0);
      holdFrom = held + 1;
      const dialog = await openForm(page);
      await dialog.getByLabel('Slug').fill(slug('refresh'));
      await dialog.getByLabel('Title').fill('Refreshed');
      await dialog.getByRole('button', { name: 'Create initiative' }).click();
      await expect(page.getByTestId('skeleton-row')).not.toHaveCount(0);
      await expect(page.getByTestId('initiative-row')).toHaveCount(0);
      release();
      await expect(page.getByTestId('skeleton-row')).toHaveCount(0);
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${slug('refresh')}]`)).toHaveCount(1);
    });

    test('a create submitted while the first fetch is pending keeps skeleton rows until both fetches have landed, then shows the newer list', async ({ page }) => {
      const gets = await holdListGets(page, isList);
      const skeleton = page.getByTestId('skeleton-row');
      await page.goto('/#/initiatives');
      await expect(skeleton).not.toHaveCount(0);
      await expect.poll(gets.count).toBe(1);
      const dialog = await openForm(page);
      await dialog.getByLabel('Slug').fill(slug('overlap'));
      await dialog.getByLabel('Title').fill('Overlapping');
      await dialog.getByRole('button', { name: 'Create initiative' }).click();
      // The form closes once the create's refresh has landed.
      await expect.poll(gets.count).toBe(2);
      // The refresh lands first: the first fetch is still pending, so the list is still loading.
      await gets.release(1);
      await expect(skeleton).not.toHaveCount(0);
      await expect(page.getByTestId('initiatives-skeleton')).toHaveAttribute('aria-busy', 'true');
      await expect(page.getByTestId('initiative-row')).toHaveCount(0);
      // The first fetch lands last with the older list: loading ends and the newer list stays.
      await gets.release(0);
      await expect(skeleton).toHaveCount(0);
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${slug('overlap')}]`)).toHaveCount(1);
    });

    test('a first fetch that fails while the refresh after a create is pending keeps skeleton rows until the refresh lands', async ({ page }) => {
      const gets = await holdListGets(page, isList);
      const skeleton = page.getByTestId('skeleton-row');
      await page.goto('/#/initiatives');
      await expect.poll(gets.count).toBe(1);
      const dialog = await openForm(page);
      await dialog.getByLabel('Slug').fill(slug('overlap-fail'));
      await dialog.getByLabel('Title').fill('Overlapping failure');
      await dialog.getByRole('button', { name: 'Create initiative' }).click();
      // The form closes once the create's refresh has landed.
      await expect.poll(gets.count).toBe(2);
      await gets.release(0, true);
      await expect(page.getByRole('alert')).toHaveText('The list is unavailable.');
      await expect(skeleton).not.toHaveCount(0);
      await gets.release(1);
      await expect(skeleton).toHaveCount(0);
      await expect(page.getByRole('alert')).toHaveCount(0);
      await expect(page.locator(`[data-testid=initiative-row][data-slug=${slug('overlap-fail')}]`)).toHaveCount(1);
    });

    test(vp.size.width < 768 ? 'rows are stacked cards and the primary button sits at the bottom edge' : 'rows are table rows under a header', async ({
      page,
      playwright,
      baseURL,
    }) => {
      const seed = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.E2E_API_KEY}`, 'Board-Actor': 'playwright' },
      });
      for (const n of ['a', 'b']) {
        const res = await seed.post('/api/v1/initiatives', { data: { slug: slug('layout-' + n), title: 'Layout ' + n, tags: ['x'] } });
        expect([201, 409]).toContain(res.status());
      }
      await seed.dispose();

      await page.goto('/#/initiatives');
      const rows = page.getByTestId('initiative-row');
      await expect(rows.first()).toBeVisible();
      const display = (el: ReturnType<Page['locator']>) => el.evaluate((node) => getComputedStyle(node).display);
      const first = await rows.nth(0).boundingBox();
      const second = await rows.nth(1).boundingBox();
      if (first === null || second === null) throw new Error('rows have no box');
      // The slug sits beneath the title, in monospace.
      const code = rows.first().locator('code');
      const title = rows.first().getByTestId('initiative-link');
      expect((await code.boundingBox())!.y).toBeGreaterThan((await title.boundingBox())!.y);
      expect(await code.evaluate((node) => getComputedStyle(node).fontFamily)).toMatch(/mono|Consolas|Menlo/i);

      if (vp.size.width < 768) {
        expect(await display(page.locator('table'))).toBe('block');
        expect(second.y).toBeGreaterThanOrEqual(first.y + first.height);
        expect(second.x).toBeCloseTo(first.x, 0);
        const button = await newInitiative(page).boundingBox();
        const bar = await page.getByTestId('tabbar').boundingBox();
        if (button === null || bar === null) throw new Error('no button or tab bar box');
        expect(button.y + button.height).toBeLessThanOrEqual(bar.y);
        expect(bar.y - (button.y + button.height)).toBeLessThan(40);
      } else {
        expect(await display(page.locator('table'))).toBe('table');
        expect(await display(rows.first())).toBe('table-row');
        await expect(page.getByRole('columnheader')).toHaveText(['Title', 'Status', 'Tags', 'Tasks', 'Created']);
        expect(second.y).toBeGreaterThanOrEqual(first.y + first.height);
      }
      const { scrollWidth, clientWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));
      expect(scrollWidth).toBeLessThanOrEqual(clientWidth);
    });
  });
}
