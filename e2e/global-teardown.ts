import { rmSync } from 'node:fs';

// Removes the directory serve.sh and global-setup.ts used (binary, store, key) and the run's directory of per-worker
// servers (each worker removes its own on exit; this catches a worker that was killed).
export default function globalTeardown(): void {
  const work = process.env.E2E_WORK;
  if (work) rmSync(work, { recursive: true, force: true });
  const run = process.env.E2E_RUN_DIR;
  if (run) rmSync(run, { recursive: true, force: true });
}
