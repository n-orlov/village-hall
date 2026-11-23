import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

// The directory every worker's own directory (its binary, store and key) is made under, and where the two tests of
// worker-isolation.spec.ts leave each other a file: chosen once by the first load of this file, which every later
// load (a worker is a child process and inherits it) reuses, and removed by run-dir-reporter.ts when the run ends.
// fixtures.ts starts one server per worker there: there is no server here, no global setup and no global teardown.
process.env.E2E_RUN_DIR ??= mkdtempSync(join(tmpdir(), 'board-e2e-run-'));
// ci/suite.sh points this at its own result directory; every reporter and the
// test artifacts live under it.
const out = process.env.E2E_OUT ?? 'out';

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // No retries anywhere: a flaky test must fail here, and ci/suite.sh does not
  // repeat a failed run either.
  retries: 0,
  workers: 1,
  reporter: [
    ['html', { outputFolder: `${out}/playwright-report`, open: 'never' }],
    ['list'],
    ['allure-playwright', { resultsDir: `${out}/allure-results` }],
    ['json', { outputFile: `${out}/results.json` }],
    ['junit', { outputFile: `${out}/junit.xml` }],
    // The tests finished so far, rewritten as they end: what a run cut by its bound leaves (partial-junit-reporter.ts).
    ['./partial-junit-reporter.ts', { outputFile: `${out}/junit-partial.xml` }],
    ['./run-dir-reporter.ts'],
  ],
  timeout: 30000,
  expect: { timeout: 10000 },
  outputDir: `${out}/test-results`,
  use: {
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  // No webServer: every worker starts the real binary, built from this checkout, on a free port of its own against a
  // freshly migrated SQLite store of its own (fixtures.ts, worker-server.ts, serve.sh), so what a spec seeds in one
  // worker is never in another's and the volume a worker's specs add is that worker's alone. A spec reaches its
  // worker's server through the `baseURL` fixture, so the config holds no baseURL.
  // No test, assertion, timeout or retry changes; the project name only appears in a test's title.
  projects: [
    // worker-isolation.spec.ts needs two workers alive together (it asks the other worker's server for what this one
    // seeded), which only a phase of its own gives: every other project depends on this one, so it runs first, alone.
    { name: 'worker-isolation', testMatch: 'worker-isolation.spec.ts', use: { browserName: 'chromium' } },
    { name: 'chromium', testMatch: '*.spec.ts', testIgnore: 'worker-isolation.spec.ts', dependencies: ['worker-isolation'], use: { browserName: 'chromium' } },
  ],
});
