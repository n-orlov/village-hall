import { test as base } from '@playwright/test';
import { startServer, workerDir, type WorkerServer } from './worker-server';

// Every spec imports test from here. Each Playwright worker starts its own board server (own port, own work
// directory holding the binary and the SQLite store, own minted API key) before its first test and stops it after
// its last, so what one worker's specs seed is never seen by another worker's. A spec reaches its worker's server
// through `baseURL`, and its key and store through `process.env.E2E_API_KEY` and `process.env.E2E_WORK`: the worker
// fixture sets both in this worker's own process, which no other worker shares. e2e/store.ts writes to that store.
//
// `test` sends no credentials from the page: a spec that signs in through the browser (login.spec.ts) must reach
// the server without a key. `keyedTest` adds the key and an actor to every request the page or the API client sends.

interface WorkerFixtures {
  workerServer: WorkerServer;
}

export const test = base.extend<{}, WorkerFixtures>({
  workerServer: [
    async ({}, use, workerInfo) => {
      const server = await startServer(workerDir(workerInfo.workerIndex));
      process.env.E2E_WORK = server.work;
      process.env.E2E_API_KEY = server.apiKey;
      process.env.E2E_PORT = String(server.port);
      try {
        await use(server);
      } finally {
        await server.stop();
      }
    },
    { scope: 'worker', auto: true, timeout: 180_000 },
  ],
  baseURL: async ({ workerServer }, use) => {
    await use(workerServer.baseURL);
  },
});

export const keyedTest = test.extend({
  extraHTTPHeaders: async ({ workerServer }, use) => {
    await use({ Authorization: `Bearer ${workerServer.apiKey}`, 'Board-Actor': 'playwright' });
  },
});

export { expect } from '@playwright/test';
