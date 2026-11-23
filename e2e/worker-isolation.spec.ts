import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './fixtures';
import { runDir, type WorkerServer } from './worker-server';

// Each Playwright worker has its own board server, port, work directory, SQLite store and API key (fixtures.ts), so
// what a spec seeds through one worker's API is not there in another worker's. The two tests below run in parallel:
// with two or more workers they land in different workers, each seeds one task through its own worker's API, and
// each then asks the OTHER worker's server (with that worker's own key) for its task. The tests meet through files
// in the run's directory and wait for each other before they finish, because a worker's server stops with the worker.
// With one worker both run on its one server and only the checks that need no second worker apply.
test.describe.configure({ mode: 'parallel' });

interface Marker {
  workerIndex: number;
  baseURL: string;
  apiKey: string;
  ref: string;
  title: string;
}

const WAIT_MS = 20_000;

async function waitForFile(path: string): Promise<void> {
  const until = Date.now() + WAIT_MS;
  while (!existsSync(path)) {
    if (Date.now() > until) throw new Error(`${path} did not appear in ${WAIT_MS} ms: the other test is not running in a second worker`);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

for (const [name, peerName] of [['a', 'b'], ['b', 'a']] as const) {
  test(`a task seeded through worker ${name}'s API is absent from the other worker's API`, async ({ playwright, workerServer }, testInfo) => {
    const own: WorkerServer = workerServer;
    const api = (server: Pick<WorkerServer, 'baseURL' | 'apiKey'>) =>
      playwright.request.newContext({
        baseURL: server.baseURL,
        extraHTTPHeaders: { Authorization: `Bearer ${server.apiKey}`, 'Board-Actor': `isolation-${name}` },
      });
    const mine = await api(own);
    const slug = `e2e-iso-${name}`;
    const title = `Isolation task ${name} of worker ${testInfo.workerIndex}`;
    expect((await mine.post('/api/v1/initiatives', { data: { slug, title: `Isolation ${name}` } })).status()).toBe(201);
    const created = await mine.post('/api/v1/tasks', { data: { initiative: slug, title } });
    expect(created.status(), await created.text()).toBe(201);
    const { ref } = (await created.json()) as { ref: string };

    // It is there on this worker's own server, and the listing holds it.
    expect((await mine.get(`/api/v1/tasks/${ref}`)).status()).toBe(200);
    const listed = (await (await mine.get(`/api/v1/tasks?status=all&limit=1000`)).json()) as { items: { title: string }[] };
    expect(listed.items.map((task) => task.title)).toContain(title);

    const dir = runDir();
    const markerOf = (who: string) => join(dir, `isolation-${who}.json`);
    const marker: Marker = { workerIndex: testInfo.workerIndex, baseURL: own.baseURL, apiKey: own.apiKey, ref, title };
    writeFileSync(markerOf(name), JSON.stringify(marker));

    if (testInfo.config.workers > 1) {
      await waitForFile(markerOf(peerName));
      const peer = JSON.parse(readFileSync(markerOf(peerName), 'utf8')) as Marker;
      // The other worker's task is absent from this worker's API, by ref and in the listing...
      expect((await mine.get(`/api/v1/tasks/${peer.ref}`)).status(), `${peer.ref} on worker ${testInfo.workerIndex}`).toBe(404);
      const after = (await (await mine.get(`/api/v1/tasks?status=all&limit=1000`)).json()) as { items: { title: string }[] };
      expect(after.items.map((task) => task.title)).not.toContain(peer.title);
      // ...and this worker's task is absent from the other worker's API, asked with that worker's own key.
      const theirs = await api(peer);
      expect((await theirs.get(`/api/v1/tasks/${ref}`)).status(), `${ref} on worker ${peer.workerIndex}`).toBe(404);
      expect((await theirs.get('/api/v1/tasks?status=all&limit=1000').then((r) => r.json()) as { items: { title: string }[] }).items.map((task) => task.title)).not.toContain(title);
      // They really are two workers, each with its own server and key.
      expect(peer.workerIndex, 'the two tests ran in different workers').not.toBe(testInfo.workerIndex);
      expect(peer.baseURL, 'the two workers have their own servers').not.toBe(own.baseURL);
      expect(peer.apiKey, 'the two workers have their own keys').not.toBe(own.apiKey);
      // This worker's key is no key of the other worker's server.
      const crossed = await playwright.request.newContext({ baseURL: peer.baseURL, extraHTTPHeaders: { Authorization: `Bearer ${own.apiKey}` } });
      expect((await crossed.get('/api/v1/tasks')).status()).toBe(401);
      await theirs.dispose();
      await crossed.dispose();

      // Neither worker stops its server before the other has finished asking it.
      writeFileSync(join(dir, `isolation-${name}.done`), '');
      await waitForFile(join(dir, `isolation-${peerName}.done`));
    }
    await mine.dispose();
  });
}
