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

// The specs whose cost follows the size of the store they run on: see `projects` below.
const listSpecs = ['a11y.spec.ts', 'dialogs.spec.ts', 'shell.spec.ts', 'timeline*.spec.ts', 'wide.spec.ts'];

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
  //
  // Three projects, none ordered by another except that worker-isolation goes first. Playwright never shares a worker
  // between projects, so each project's workers start servers (and stores) of their own: that is what groups the specs
  // below. Every spec adds initiatives and tasks to its worker's store and nothing deletes them, and the specs in
  // `lists` load an unfiltered Timeline or every view in turn, or Tab through a list the store's volume lengthens
  // (dialogs.spec.ts reaches a button below the Initiatives list, 150 presses at most), so their cost follows the store's
  // size. Run among the other specs in one worker's store, three of them failed on the Jenkins agent (the Tab walk never
  // reached its button, shell.spec.ts took 35 s, timeline-leases found no bar in time). In their own project they see
  // only what they add themselves, whatever the order or the worker count. This is store isolation, not an order.
  projects: [
    // worker-isolation.spec.ts needs two workers alive together (it asks the other worker's server for what this one
    // seeded), which only a phase of its own gives: every other project depends on this one, so it runs first, alone.
    { name: 'worker-isolation', testMatch: 'worker-isolation.spec.ts', use: { browserName: 'chromium' } },
    { name: 'lists', testMatch: listSpecs, dependencies: ['worker-isolation'], use: { browserName: 'chromium' } },
    { name: 'chromium', testMatch: '*.spec.ts', testIgnore: listSpecs.concat('worker-isolation.spec.ts'), dependencies: ['worker-isolation'], use: { browserName: 'chromium' } },
  ],
});
