import { expect } from '@playwright/test';
import { test as base } from './fixtures';

// These specs sign in through the browser, so they send no API key: the session
// cookie the mock provider's login ends in is the only credential. (fixtures.ts's
// keyedTest would add a Bearer key to every request, which takes the keyed path instead.)
const test = base;

test('a visit to / with no session is redirected to the login page, which starts the OIDC flow', async ({ playwright, baseURL }) => {
  const bare = await playwright.request.newContext({ baseURL });
  const root = await bare.get('/', { headers: { Accept: 'text/html' }, maxRedirects: 0 });
  expect(root.status()).toBe(302);
  expect(root.headers().location).toBe('/auth/login');
  const login = await bare.get('/auth/login', { headers: { Accept: 'text/html' }, maxRedirects: 0 });
  expect(login.status()).toBe(302);
  const authorize = new URL(login.headers().location);
  expect(authorize.pathname).toBe('/authorize');
  expect(authorize.searchParams.get('code_challenge_method')).toBe('S256');
  await bare.dispose();
});

test('logging in through the mock provider lands on the Now view', async ({ page }) => {
  const visited: string[] = [];
  // Every hop of the redirect chain is its own navigation request.
  page.on('request', (request) => {
    if (request.isNavigationRequest()) visited.push(new URL(request.url()).pathname);
  });
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
  await expect(page.getByTestId('email')).toHaveText('operator@example.test');
  expect(new URL(page.url()).pathname).toBe('/');
  expect(visited).toContain('/auth/login');
  expect(visited).toContain('/authorize');
  expect(visited).toContain('/auth/oidc/callback');
  const cookies = await page.context().cookies();
  expect(cookies.map((c) => c.name)).toContain('board_session');
});

test('a page whose session call answers 401 is sent through the login page', async ({ page }) => {
  let refused = false;
  await page.route('**/auth/session', async (route) => {
    if (refused) return route.continue();
    refused = true;
    await route.fulfill({ status: 401, contentType: 'application/problem+json', body: '{"status":401}' });
  });
  const visited: string[] = [];
  // Every hop of the redirect chain is its own navigation request.
  page.on('request', (request) => {
    if (request.isNavigationRequest()) visited.push(new URL(request.url()).pathname);
  });
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 2, name: 'Now' })).toBeVisible();
  expect(refused).toBe(true);
  expect(visited).toContain('/auth/login');
});
