import { expect, keyedTest as test } from './fixtures';

test('the embedded UI shell is served at /', async ({ request }) => {
  // With the key, not a session, so this is the shell itself and no login redirect:
  // login.spec.ts renders it in a browser, through the mock provider.
  const response = await request.get('/', { headers: { Accept: 'text/html' } });
  expect(response.status()).toBe(200);
  expect(response.headers()['content-type']).toContain('text/html');
  expect(await response.text()).toContain('<div id="app">');
});

test('a request without the key is refused with a problem document', async ({ playwright, baseURL }) => {
  const bare = await playwright.request.newContext({ baseURL, extraHTTPHeaders: {} });
  const response = await bare.get('/readyz');
  expect(response.status()).toBe(401);
  expect(response.headers()['content-type']).toBe('application/problem+json');
  expect((await bare.get('/healthz')).status()).toBe(200);
  await bare.dispose();
});
