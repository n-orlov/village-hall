import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import { closeSync, mkdirSync, openSync, readFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { join } from 'node:path';

/** One board server, with the port, the work directory (binary and SQLite store) and the API key it owns. */
export interface WorkerServer {
  port: number;
  baseURL: string;
  work: string;
  apiKey: string;
  stop(): Promise<void>;
}

// A free port, asked of the kernel.
function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address() as { port: number };
      probe.close(() => resolve(port));
    });
  });
}

// The environment a board process runs in: nothing from the caller's BOARD_* or VH_* leaks in.
function cleanEnv(extra: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [name, value] of Object.entries(process.env)) {
    if (!name.startsWith('BOARD_') && !name.startsWith('VH_')) env[name] = value;
  }
  return { ...env, ...extra };
}

async function healthy(baseURL: string): Promise<boolean> {
  try {
    return (await fetch(`${baseURL}/healthz`)).status === 200;
  } catch {
    return false;
  }
}

/** The directory every worker's own directory is made under: one per run, removed by run-dir-reporter.ts when the run ends. */
export function runDir(): string {
  const dir = process.env.E2E_RUN_DIR;
  if (!dir) throw new Error('E2E_RUN_DIR is not set (playwright.config.ts sets it, run-dir-reporter.ts removes it)');
  return dir;
}

/**
 * Starts one real board server under `work` with serve.sh (the script that builds and serves the real binary): the
 * binary built from this checkout, a freshly migrated SQLite store, the mock OIDC provider, a free port. Then mints
 * this server's own API key with the `vh admin keys create` an operator runs, against this server's own store.
 */
export async function startServer(work: string): Promise<WorkerServer> {
  mkdirSync(work, { recursive: true });
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const logPath = join(work, 'serve.log');
  const log = openSync(logPath, 'a');
  const child: ChildProcess = spawn('sh', [join(import.meta.dirname, 'serve.sh')], {
    env: cleanEnv({ E2E_PORT: String(port), E2E_WORK: work }),
    stdio: ['ignore', log, log],
  });
  closeSync(log);
  let exited = false;
  const exit = new Promise<void>((resolve) => {
    child.once('exit', () => {
      exited = true;
      resolve();
    });
  });
  const stop = async (): Promise<void> => {
    if (!exited) child.kill('SIGTERM');
    await exit;
    rmSync(work, { recursive: true, force: true });
  };
  const tail = () => readFileSync(logPath, 'utf8').split('\n').slice(-20).join('\n');
  try {
    // Building the binary is part of the start; a cold Go cache is the slow case.
    const until = Date.now() + 150_000;
    while (!(await healthy(baseURL))) {
      if (exited) throw new Error(`the board server for ${work} exited before it was up:\n${tail()}`);
      if (Date.now() > until) throw new Error(`the board server for ${work} was not up on ${baseURL} in time:\n${tail()}`);
      await new Promise((resolve) => setTimeout(resolve, 200));
    }
    const out = execFileSync(join(work, 'vh'), ['admin', 'keys', 'create', 'e2e'], {
      env: cleanEnv({ BOARD_DB_URL: `sqlite://${join(work, 'board.db')}` }),
      encoding: 'utf8',
    });
    const apiKey = out.trim().split(/\s+/).pop() ?? '';
    if (!/^brd_[0-9a-z]{52}$/.test(apiKey)) throw new Error('vh admin keys create did not print a key');
    return { port, baseURL, work, apiKey, stop };
  } catch (error) {
    await stop();
    throw error;
  }
}

/** A directory for worker number `index` under the run's directory. */
export function workerDir(index: number): string {
  return join(runDir(), `worker-${index}`);
}

