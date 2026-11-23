// Removes the run's directory of per-worker servers when the run ends (playwright.config.ts creates it as
// E2E_RUN_DIR). Each worker removes its own directory when it stops; this catches what a killed worker left, and the
// marker files worker-isolation.spec.ts writes. It is a reporter because the config has no global teardown.
import { rmSync } from 'node:fs';
import type { Reporter } from '@playwright/test/reporter';

export default class RunDirReporter implements Reporter {
  onEnd(): void {
    const dir = process.env.E2E_RUN_DIR;
    if (dir) rmSync(dir, { recursive: true, force: true });
  }
}
