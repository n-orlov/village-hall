import { expect, type Page } from '@playwright/test';
import { test as base } from './fixtures';

// Signs in through the mock provider, so no API key rides along (see login.spec.ts).
// The Settings -> API keys view at the phone, tablet and desktop size: the table or the cards, the create
// form in the bottom sheet or the dialog, and the once-shown key box with its Copy and Done.
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'tablet', size: { width: 820, height: 1180 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

const newKey = (page: Page) => page.getByRole('button', { name: 'New API key' });

async function openForm(page: Page) {
  await newKey(page).click();
  const dialog = page.getByRole('dialog', { name: 'New API key' });
  await expect(dialog).toBeVisible();
  return dialog;
}

const overflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

// Holds the first `hold` GETs of the list (the first fetch and the reload after the test's own write) until the test
// releases each by its order (0 = the first fetch). Each held GET is answered from the server as it stood when the
// request was made, so a fetch released late carries an older list; `fail` answers it with a 503 problem document instead.
// The test's own write also reaches the page as a live event, whose refresh is a further GET 500 ms later (the live
// debounce): one the test never releases would keep the list loading for good once the test runs slower than that, as
// it does on a loaded agent. A GET past the held ones waits until every held one is released, then lands, newest.
async function holdListGets(page: Page, pattern: string, hold = 2) {
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
      const landed = page.waitForResponse((r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith(pattern.replace('**', '')));
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
    test.use({ viewport: vp.size, permissions: ['clipboard-read', 'clipboard-write'] });
    const keyName = (n: string) => `e2e-${vp.name}-${n}`;

    test('a key created on the Settings -> API keys page is shown once, copied, works, and gets 401 once revoked', async ({ page, playwright, baseURL }) => {
      const name = keyName('ui');
      await page.goto('/');
      if (vp.name === 'phone') {
        // Settings sits under the tab bar's More entry on the phone.
        await page.getByRole('button', { name: 'More' }).click();
        await page.getByRole('dialog', { name: 'More' }).getByRole('link', { name: 'Settings' }).click();
      } else {
        await page.getByRole('link', { name: 'Settings' }).click();
      }
      await expect(page.getByRole('heading', { level: 2, name: 'API keys' })).toBeVisible();
      // global-setup.ts's own key is already listed; the new one is not.
      await expect(page.getByTestId('key-row').first()).toBeVisible();
      const row = page.locator(`[data-testid=key-row][data-name=${name}]`);
      await expect(row).toHaveCount(0);

      // The form sits at the bottom edge on the phone and is centred on the desktop
      // (the sheet slides up on open: wait for it to settle).
      const dialog = await openForm(page);
      const bottom = async () => {
        const box = await dialog.boundingBox();
        return box === null ? -1 : Math.round(box.y + box.height);
      };
      if (vp.size.width < 768) await expect.poll(bottom).toBe(vp.size.height);
      else expect(await bottom()).toBeLessThan(vp.size.height);

      await dialog.getByLabel('Name').fill(name);
      await dialog.getByLabel('Expires in').fill('90d');
      await dialog.getByRole('button', { name: 'Create key' }).click();
      await expect(page.getByRole('dialog')).toHaveCount(0);

      // The once-shown key: a highlighted monospace box that wraps, so the page never scrolls sideways.
      const box = page.getByTestId('new-key');
      const shown = page.getByTestId('new-key-value');
      await expect(shown).toHaveText(/^brd_[A-Za-z0-9]{52}$/);
      const key = (await shown.textContent()) ?? '';
      const style = await shown.evaluate((el) => {
        const s = getComputedStyle(el);
        return { family: s.fontFamily, wrap: s.overflowWrap, wordBreak: s.wordBreak, white: s.whiteSpace };
      });
      expect(style.family).toMatch(/mono|Menlo|Consolas/);
      expect(`${style.wrap} ${style.wordBreak}`).toMatch(/break-word|break-all|anywhere/);
      const boxBounds = await box.boundingBox();
      expect(boxBounds).not.toBeNull();
      expect((boxBounds?.x ?? -1) + (boxBounds?.width ?? 0)).toBeLessThanOrEqual(vp.size.width);
      expect(await shown.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0);
      expect(await overflow(page)).toBeLessThanOrEqual(0);
      expect((await shown.boundingBox())?.height ?? 0).toBeGreaterThan(vp.size.width < 768 ? 40 : 20);

      // The row is listed as active, with the prefix and a relative creation time, and never carries the key.
      await expect(row).toHaveCount(1);
      await expect(row.getByTestId('key-status')).toHaveText('active');
      await expect(row).toContainText(key.slice(4, 12));
      await expect(row.locator('time').first()).toHaveText('just now');
      expect(await row.textContent()).not.toContain(key);

      // Copy puts exactly that key on the clipboard and confirms it.
      await expect(page.getByTestId('copied')).toHaveCount(0);
      await page.getByRole('button', { name: 'Copy' }).click();
      await expect(page.getByTestId('copied')).toHaveText('Copied');
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(key);

      const bare = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${key}`, 'Board-Actor': 'e2e-agent' } });
      expect((await bare.get('/api/v1/keys')).status()).toBe(200);

      // Done drops the key from the page, and a reload does not bring it back.
      await page.getByRole('button', { name: 'Done' }).click();
      await expect(box).toHaveCount(0);
      await expect(page.getByText(key)).toHaveCount(0);
      await page.reload();
      await expect(row).toHaveCount(1);
      await expect(page.getByText(key)).toHaveCount(0);
      expect(await overflow(page)).toBeLessThanOrEqual(0);

      // Revoke asks first. Cancel revokes nothing: the key is still active and still works.
      await row.getByRole('button', { name: 'Revoke' }).click();
      const confirm = page.getByRole('dialog', { name: 'Revoke API key' });
      await expect(confirm).toBeVisible();
      await expect(confirm).toContainText(name);
      if (vp.size.width < 768) await expect.poll(async () => Math.round((await confirm.boundingBox())?.y ?? -1) + Math.round((await confirm.boundingBox())?.height ?? 0)).toBe(vp.size.height);
      await confirm.getByRole('button', { name: 'Cancel' }).click();
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(row.getByTestId('key-status')).toHaveText('active');
      expect((await bare.get('/api/v1/keys')).status()).toBe(200);

      // Revoke then confirm: revoked, no Revoke button, and the key gets a problem+json 401.
      await row.getByRole('button', { name: 'Revoke' }).click();
      await page.getByRole('dialog', { name: 'Revoke API key' }).getByRole('button', { name: 'Revoke key' }).click();
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(row.getByTestId('key-status')).toHaveText('revoked');
      await expect(row.getByRole('button', { name: 'Revoke' })).toHaveCount(0);

      const refused = await bare.get('/api/v1/keys');
      expect(refused.status()).toBe(401);
      expect(refused.headers()['content-type']).toBe('application/problem+json');
      await bare.dispose();
    });

    test('the list is a table from 1024 px and cards below it', async ({ page }) => {
      await page.goto('/#/settings/keys');
      const row = page.getByTestId('key-row').first();
      await expect(row).toBeVisible();
      const display = (loc: ReturnType<Page['locator']>) => loc.evaluate((el) => getComputedStyle(el).display);
      const head = page.locator('thead');
      if (vp.size.width < 1024) {
        expect(await display(row)).toBe('flex');
        expect((await head.boundingBox())?.width ?? 0).toBeLessThanOrEqual(1);
      } else {
        expect(await display(row)).toBe('table-row');
        await expect(head).toBeVisible();
        await expect(head.getByRole('columnheader')).toHaveText(['Name', 'Owner', 'Prefix', 'Status', 'Created', 'Last used', 'Expires', 'Actions']);
      }
      // The status is a badge with a border of its tone (active -> success token).
      const badge = row.getByTestId('key-status').locator('span');
      expect(await badge.evaluate((el) => getComputedStyle(el).borderTopWidth)).toBe('1px');
      expect(await overflow(page)).toBeLessThanOrEqual(0);
    });

    test('every list fetch shows skeleton rows while pending: the first, and the refresh after a create and after a revoke', async ({ page }) => {
      // Holds each GET of the list from the n-th on until released; earlier ones pass through.
      let held = 0;
      let holdFrom = Number.POSITIVE_INFINITY;
      let release: () => void = () => undefined;
      let gate = Promise.resolve();
      const hold = () => {
        gate = new Promise<void>((resolve) => (release = resolve));
      };
      await page.route('**/api/v1/keys', async (route) => {
        if (route.request().method() !== 'GET') return route.continue();
        held += 1;
        if (held >= holdFrom) await gate;
        return route.fetch().then((response) => route.fulfill({ response }));
      });
      const skeleton = page.getByTestId('skeleton-row');
      const rows = page.getByTestId('key-row');

      // The first fetch.
      holdFrom = 1;
      hold();
      await page.goto('/#/settings/keys');
      await expect(skeleton).not.toHaveCount(0);
      await expect(rows).toHaveCount(0);
      release();
      await expect(skeleton).toHaveCount(0);
      await expect(rows.first()).toBeVisible();

      // The refresh after a create: the form closed, the once-shown key on the page, the list loading.
      const name = keyName('loading');
      holdFrom = held + 1;
      hold();
      const dialog = await openForm(page);
      await dialog.getByLabel('Name').fill(name);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      await expect(skeleton).not.toHaveCount(0);
      await expect(rows).toHaveCount(0);
      release();
      await expect(skeleton).toHaveCount(0);
      const row = page.locator(`[data-testid=key-row][data-name=${name}]`);
      await expect(row).toHaveCount(1);
      await expect(page.getByTestId('new-key-value')).toHaveText(/^brd_/);

      // The refresh after a revoke.
      holdFrom = held + 1;
      hold();
      await row.getByRole('button', { name: 'Revoke' }).click();
      await page.getByRole('dialog', { name: 'Revoke API key' }).getByRole('button', { name: 'Revoke key' }).click();
      await expect(skeleton).not.toHaveCount(0);
      release();
      await expect(skeleton).toHaveCount(0);
      await expect(row.getByTestId('key-status')).toHaveText('revoked');
    });

    test('a create submitted while the first fetch is pending keeps skeleton rows and the once-shown key until both fetches have landed', async ({ page }) => {
      const name = keyName('overlap');
      const gets = await holdListGets(page, '**/api/v1/keys');
      const skeleton = page.getByTestId('skeleton-row');
      await page.goto('/#/settings/keys');
      await expect(skeleton).not.toHaveCount(0);
      await expect.poll(gets.count).toBe(1);
      const dialog = await openForm(page);
      await dialog.getByLabel('Name').fill(name);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      // The form closes once the create's refresh has landed.
      await expect.poll(gets.count).toBe(2);
      await expect(page.getByTestId('new-key-value')).toHaveText(/^brd_/);
      // The refresh lands first: the first fetch is still pending, so the list is still loading.
      await gets.release(1);
      await expect(skeleton).not.toHaveCount(0);
      await expect(page.getByTestId('keys-skeleton')).toHaveAttribute('aria-busy', 'true');
      await expect(page.getByTestId('key-row')).toHaveCount(0);
      // The first fetch lands last with the older list: loading ends, the newer list and the key stay.
      await gets.release(0);
      await expect(skeleton).toHaveCount(0);
      await expect(page.locator(`[data-testid=key-row][data-name=${name}]`)).toHaveCount(1);
      await expect(page.getByTestId('new-key-value')).toHaveText(/^brd_/);
    });

    test('a first fetch that fails while the refresh after a create is pending keeps skeleton rows until the refresh lands', async ({ page }) => {
      const name = keyName('overlap-fail');
      const gets = await holdListGets(page, '**/api/v1/keys');
      const skeleton = page.getByTestId('skeleton-row');
      await page.goto('/#/settings/keys');
      await expect.poll(gets.count).toBe(1);
      const dialog = await openForm(page);
      await dialog.getByLabel('Name').fill(name);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      // The form closes once the create's refresh has landed.
      await expect.poll(gets.count).toBe(2);
      await gets.release(0, true);
      await expect(page.getByRole('alert')).toHaveText('The list is unavailable.');
      await expect(skeleton).not.toHaveCount(0);
      await gets.release(1);
      await expect(skeleton).toHaveCount(0);
      await expect(page.getByRole('alert')).toHaveCount(0);
      await expect(page.locator(`[data-testid=key-row][data-name=${name}]`)).toHaveCount(1);
    });

    test('a name that is already taken is refused on the page and no key is shown', async ({ page }) => {
      const name = keyName('dup');
      await page.goto('/#/settings/keys');
      let dialog = await openForm(page);
      await dialog.getByLabel('Name').fill(name);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      await expect(page.getByTestId('new-key')).toBeVisible();
      await page.getByRole('button', { name: 'Done' }).click();
      await expect(page.getByTestId('new-key')).toHaveCount(0);

      dialog = await openForm(page);
      await dialog.getByLabel('Name').fill(name);
      await dialog.getByRole('button', { name: 'Create key' }).click();
      // The refusal shows inside the form, which stays open with what was typed.
      await expect(dialog.getByRole('alert')).toContainText(name);
      await expect(dialog.getByLabel('Name')).toHaveValue(name);
      await expect(page.getByTestId('new-key')).toHaveCount(0);
      await expect(page.getByTestId('new-key-value')).toHaveCount(0);
    });
  });
}
