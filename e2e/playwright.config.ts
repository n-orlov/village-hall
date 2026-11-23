import { mkdtempSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

// A free port, asked of the kernel once per run: Playwright loads this file in
// the runner and again in every worker, so the first load publishes the port in
// the environment and every later load (a child inherits it) reuses it.
async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address() as { port: number };
      probe.close(() => resolve(port));
    });
  });
}

const port = Number(process.env.E2E_PORT ?? (process.env.E2E_PORT = String(await freePort())));
// The directory holding the binary and the SQLite store of this run. Like the
// port it is chosen once and inherited by every later load of this file;
// global-setup.ts mints the API key in it and global-teardown.ts removes it.
const work = process.env.E2E_WORK ?? (process.env.E2E_WORK = mkdtempSync(join(tmpdir(), 'board-e2e-')));
// The directory every worker's own directory (its binary, store and key) is made under: chosen once like the
// others, and removed by global-teardown.ts. fixtures.ts starts one server per worker there.
process.env.E2E_RUN_DIR ??= mkdtempSync(join(tmpdir(), 'board-e2e-run-'));
// ci/suite.sh points this at its own result directory; every reporter and the
// test artifacts live under it.
const out = process.env.E2E_OUT ?? 'out';

// The specs that load the unfiltered Timeline or every view in turn, or that Tab through a list the store's
// volume lengthens (dialogs.spec.ts reaches a button below the Initiatives list): see `projects` below.
const earlySpecs = ['a11y.spec.ts', 'dialogs.spec.ts', 'shell.spec.ts', 'timeline*.spec.ts', 'wide.spec.ts'];

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // No retries anywhere: a flaky test must fail here. The only retry is
  // ci/suite.sh's own repeat of a failed run, which records it as flaky.
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
  ],
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',
  timeout: 30000,
  expect: { timeout: 10000 },
  outputDir: `${out}/test-results`,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  // The real binary, built from this checkout, on the free port, against a
  // freshly migrated and seeded SQLite store (see serve.sh).
  webServer: {
    command: 'sh serve.sh',
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: false,
    timeout: 180000,
    env: { E2E_PORT: String(port), E2E_WORK: work },
  },
  // Two projects over one server and one store, in this order (Playwright runs the first project's files first with
  // the one worker). Every spec adds initiatives and tasks to the shared store and nothing deletes them, so by the
  // end of the run the store holds thousands of tasks, and an unfiltered Timeline draws all of them: vis-timeline
  // needs seconds per load there (about 4 s for 2,400 tasks on a 3-CPU container, measured), and these specs load
  // that view again and again. Run last, they took 25 of their 30 s on the workstation and over 30 s on the Jenkins
  // agent, which failed the first run and doubled the Suite past its 45-minute bound. They test the Timeline and the
  // navigation chrome, not store volume, so they run first, on a store that only the specs before them have filled.
  // dialogs.spec.ts is in the early project for the same reason from the other side: it reaches a button by Tab alone
  // (150 presses at most) and on a phone that button sits below the whole Initiatives list, so it needs the list short.
  // a11y.spec.ts is in the early project too, and so runs first of all (files run in name order): axe's cost follows
  // the size of the page it scans, and the beforeEach of every one of its scans loads the unfiltered Initiatives list.
  // Run after the early specs had filled the store, its scans took most of a second longer each on the workstation
  // (measured), and it was the Suite's largest single cost on the Jenkins agent. Its own seed is a handful of
  // initiatives, which leaves dialogs.spec.ts's Tab walk well inside its bound.
  // No test, assertion, timeout or retry changes; the project name only appears in a test's title.
  projects: [
    // worker-isolation.spec.ts needs two workers alive together (it asks the other worker's server for what this one
    // seeded), which only a phase of its own gives: every other project depends on this one, so it runs first, alone.
    { name: 'worker-isolation', testMatch: 'worker-isolation.spec.ts', use: { browserName: 'chromium' } },
    { name: 'chromium-early', testMatch: earlySpecs, dependencies: ['worker-isolation'], use: { browserName: 'chromium' } },
    { name: 'chromium', testMatch: '*.spec.ts', testIgnore: earlySpecs.concat('worker-isolation.spec.ts'), dependencies: ['worker-isolation'], use: { browserName: 'chromium' } },
  ],
});
