import { expect, type Page } from '@playwright/test';
import { test as base } from './fixtures';

// The session states: sign in through the mock provider, Sign out, the Signed out view (which never
// starts a login redirect by itself), the old cookie refused, Sign in again, and the session error
// card with its Retry. No API key rides along (see login.spec.ts).
const test = base;

const viewports = [
  { name: 'phone', size: { width: 390, height: 844 } },
  { name: 'tablet', size: { width: 820, height: 1180 } },
  { name: 'desktop', size: { width: 1440, height: 900 } },
] as const;

/** Every navigation request the page makes from now on. */
function recordNavigations(page: Page): string[] {
  const visited: string[] = [];
  page.on('request', (request) => {
    if (request.isNavigationRequest()) visited.push(new URL(request.url()).pathname);
  });
  return visited;
}

for (const vp of viewports) {
  test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {
    test.use({ viewport: vp.size });

    test('Sign out shows the Signed out view without a login redirect, the old cookie gets 401, Sign in signs in again', async ({
      page,
      playwright,
      baseURL,
    }) => {
      await page.goto('/');
      await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();

      const cookies = await page.context().cookies();
      const session = cookies.find((c) => c.name === 'board_session');
      expect(session).toBeDefined();
      const old = await playwright.request.newContext({
        baseURL,
        extraHTTPHeaders: { Cookie: `board_session=${session!.value}` },
      });
      expect((await old.get('/api/v1/initiatives')).status()).toBe(200);

      // Where Sign out lives: the sidebar foot at desktop width, the account menu below it.
      const visited = recordNavigations(page);
      if (vp.name === 'desktop') {
        const foot = page.getByTestId('sidebar-foot');
        await expect(foot.getByTestId('email')).toHaveText('operator@example.test');
        await foot.getByRole('button', { name: 'Sign out' }).click();
      } else {
        await page.getByRole('button', { name: 'Account menu' }).click();
        const dialog = page.getByRole('dialog', { name: 'Account' });
        await expect(dialog.getByTestId('menu-email')).toHaveText('operator@example.test');
        await dialog.getByRole('button', { name: 'Sign out' }).click();
      }

      await expect(page.getByTestId('signed-out')).toBeVisible();
      await expect(page.getByRole('heading', { level: 2, name: 'Signed out' })).toBeAttached();
      await expect(page.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login');
      expect(new URL(page.url()).hash).toBe('#/signed-out');
      await page.waitForLoadState('networkidle');
      expect(visited).not.toContain('/auth/login');
      await expect(page.getByTestId('signed-out')).toBeVisible();

      expect((await old.get('/api/v1/initiatives')).status()).toBe(401);
      await old.dispose();

      await page.getByRole('link', { name: 'Sign in' }).click();
      await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
      expect(visited).toContain('/auth/login');
      await expect(page.getByTestId('signed-out')).toHaveCount(0);
    });
  });
}

// R12: the account menu names the operator handle the session acts as, the `actor` of GET /auth/session.
for (const vp of [viewports[0], viewports[2]]) {
  test(`the account menu shows the session's operator handle at ${vp.name} ${vp.size.width}x${vp.size.height}`, async ({ page }) => {
    await page.setViewportSize(vp.size);
    const sessionResponse = page.waitForResponse((r) => new URL(r.url()).pathname === '/auth/session');
    await page.goto('/');
    const session = (await (await sessionResponse).json()) as { actor: string; email: string };
    expect(session.actor).not.toBe('');
    await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();

    // Two account buttons exist (rail and sidebar foot); only the one at this width is visible.
    await page.getByRole('button', { name: 'Account menu' }).locator('visible=true').click();
    const dialog = page.getByRole('dialog', { name: 'Account' });
    await expect(dialog.getByTestId('menu-email')).toHaveText(session.email);
    await expect(dialog.getByTestId('menu-handle')).toHaveText(session.actor);
  });
}

test('the signing-in card shows the product name and a spinner while the session loads', async ({ page }) => {
  await page.route('**/auth/session', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 600));
    await route.continue();
  });
  await page.goto('/');
  await expect(page.getByTestId('spinner')).toBeVisible();
  await expect(page.getByRole('heading', { level: 1, name: 'Village Hall' })).toBeVisible();
  await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
});

test('a session that cannot be read shows the problem and a Retry that loads it again', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
  let failing = true;
  await page.route('**/auth/session', async (route) => {
    if (!failing) return route.continue();
    await route.fulfill({
      status: 503,
      contentType: 'application/problem+json',
      body: JSON.stringify({ status: 503, title: 'Service Unavailable', detail: 'The session store is temporarily unavailable.' }),
    });
  });
  const visited = recordNavigations(page);
  await page.reload();
  await expect(page.getByTestId('session-error')).toHaveText('The session store is temporarily unavailable.');
  failing = false;
  await page.getByRole('button', { name: 'Retry' }).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
  expect(visited).not.toContain('/auth/login');
});
