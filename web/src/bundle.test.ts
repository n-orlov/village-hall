import { mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { build } from 'vite';
import { bundleViolations, ENTRY_CHUNK_MAX_BYTES, eagerChunks, TIMELINE_SOURCE, type BuildFiles, type Manifest } from './lib/bundleCheck.ts';

let outDir: string;
let manifest: Manifest;
let html: string;

const files: BuildFiles = {
  size: (file) => statSync(join(outDir, file)).size,
  text: (file) => readFileSync(join(outDir, file), 'utf8'),
  sources: (file) => (JSON.parse(readFileSync(join(outDir, `${file}.map`), 'utf8')) as { sources: string[] }).sources,
};

// A real production build into a scratch directory (the shipped dist is left alone), with the
// manifest and source maps the check reads.
beforeAll(async () => {
  outDir = mkdtempSync(join(tmpdir(), 'board-web-bundle-'));
  await build({
    mode: 'production',
    logLevel: 'silent',
    build: { outDir, emptyOutDir: true, manifest: true, sourcemap: true },
  });
  manifest = JSON.parse(readFileSync(join(outDir, '.vite', 'manifest.json'), 'utf8')) as Manifest;
  html = readFileSync(join(outDir, 'index.html'), 'utf8');
}, 60_000);

afterAll(() => {
  rmSync(outDir, { recursive: true, force: true });
});

describe('built bundle (R7)', () => {
  it('breaks none of the rules: small entry chunk, no vis-timeline or vis-data eagerly, Timeline split, no vis stylesheet eagerly', () => {
    expect(bundleViolations(manifest, html, files)).toEqual([]);
  });

  it('keeps the entry chunk at or under the raw size cap', () => {
    const entry = Object.values(manifest).find((c) => c.isEntry);
    expect(entry).toBeDefined();
    expect(files.size(entry!.file)).toBeLessThanOrEqual(ENTRY_CHUNK_MAX_BYTES);
  });

  it('puts vis-timeline, vis-data and moment in the Timeline chunk, not an eager one', () => {
    const timeline = manifest[TIMELINE_SOURCE];
    expect(timeline.isDynamicEntry).toBe(true);
    const eagerFiles = new Set(eagerChunks(manifest).map((c) => c.file));
    expect(eagerFiles.has(timeline.file)).toBe(false);
    const lazySources = [timeline, ...(timeline.imports ?? []).map((k) => manifest[k])].flatMap((c) => files.sources(c.file));
    for (const pkg of ['vis-timeline', 'vis-data', 'moment']) {
      expect(lazySources.some((s) => s.includes(`node_modules/${pkg}/`))).toBe(true);
    }
  });
});
