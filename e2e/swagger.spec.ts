import { readFileSync } from 'node:fs';
import { expect, keyedTest as test } from './fixtures';

// The operations api/openapi.yaml declares, read from the checked-in document
// (the YAML is regular enough: a path at two spaces of indent, a method at four).
function declaredOperations(): { method: string; path: string }[] {
  const text = readFileSync(new URL('../api/openapi.yaml', import.meta.url), 'utf8');
  const ops: { method: string; path: string }[] = [];
  let path = '';
  let inPaths = false;
  for (const line of text.split('\n')) {
    if (/^\S/.test(line)) inPaths = line.startsWith('paths:');
    if (!inPaths) continue;
    const p = /^ {2}(\/\S*):\s*$/.exec(line);
    if (p) path = p[1];
    const m = /^ {4}(get|put|post|delete|patch|head|options):\s*$/.exec(line);
    if (m && path) ops.push({ method: m[1], path });
  }
  return ops;
}

test('/api/docs lists every operation of api/openapi.yaml from embedded assets', async ({ page, baseURL }) => {
  const ops = declaredOperations();
  expect(ops.length).toBeGreaterThanOrEqual(6);

  const foreign: string[] = [];
  page.on('request', (request) => {
    if (new URL(request.url()).origin !== new URL(baseURL ?? '').origin) foreign.push(request.url());
  });

  const response = await page.goto('/api/docs');
  expect(response?.status()).toBe(200);
  await expect(page.locator('.swagger-ui .info .title')).toContainText('Village Hall');

  for (const { method, path } of ops) {
    const block = page.locator(`.opblock-${method}`).filter({
      has: page.locator(`.opblock-summary-path[data-path="${path}"]`),
    });
    await expect(block, `${method.toUpperCase()} ${path}`).toHaveCount(1);
  }
  await expect(page.locator('.opblock')).toHaveCount(ops.length);
  expect(foreign).toEqual([]);
});

test('/api/docs offers the bearerAuth scheme in its Authorize dialog', async ({ page }) => {
  await page.goto('/api/docs');
  await page.getByRole('button', { name: 'Authorize' }).click();
  const dialog = page.locator('.dialog-ux .modal-ux');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('bearerAuth');
  await expect(dialog.locator('input[type="text"], input[type="password"]').first()).toBeVisible();
});
