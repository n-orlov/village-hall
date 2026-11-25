import { describe, expect, it } from 'vitest';
import {
  bundleViolations,
  eagerChunks,
  ENTRY_CHUNK_MAX_BYTES,
  hasVisTimelineStyles,
  stylesheetLinks,
  TIMELINE_SOURCE,
  type BuildFiles,
  type Manifest,
} from './bundleCheck.ts';

const HTML = '<link rel="stylesheet" crossorigin href="./assets/index-a.css"><script type="module" src="./assets/index-a.js"></script>';

function good(): Manifest {
  return {
    'index.html': { file: 'assets/index-a.js', isEntry: true, dynamicImports: [TIMELINE_SOURCE], css: ['assets/index-a.css'] },
    [TIMELINE_SOURCE]: { file: 'assets/Timeline-b.js', isDynamicEntry: true, css: ['assets/Timeline-b.css'] },
  };
}

function goodFiles(over: { size?: number; sources?: Record<string, string[]>; css?: Record<string, string> } = {}): BuildFiles {
  const sources: Record<string, string[]> = { 'assets/index-a.js': ['../src/App.svelte'], ...over.sources };
  const css: Record<string, string> = { 'assets/index-a.css': '.card{color:red}', 'assets/Timeline-b.css': '.vis-timeline{x:1}', ...over.css };
  return {
    size: (f) => (f === 'assets/index-a.js' ? (over.size ?? 300_000) : 10),
    text: (f) => css[f],
    sources: (f) => sources[f] ?? [],
  };
}

describe('bundleViolations', () => {
  it('is empty for a split build', () => {
    expect(bundleViolations(good(), HTML, goodFiles())).toEqual([]);
  });

  it('fails when the entry chunk is over the raw cap, and not at the cap', () => {
    expect(bundleViolations(good(), HTML, goodFiles({ size: ENTRY_CHUNK_MAX_BYTES }))).toEqual([]);
    const out = bundleViolations(good(), HTML, goodFiles({ size: ENTRY_CHUNK_MAX_BYTES + 1 }));
    expect(out).toHaveLength(1);
    expect(out[0]).toContain('entry chunk');
  });

  it.each(['vis-timeline', 'vis-data'])('fails when the entry chunk contains %s', (pkg) => {
    const out = bundleViolations(good(), HTML, goodFiles({ sources: { 'assets/index-a.js': [`../node_modules/${pkg}/esm/x.js`] } }));
    expect(out).toEqual([`eagerly loaded chunk assets/index-a.js contains ${pkg}`]);
  });

  it('fails when a statically imported chunk contains vis-data', () => {
    const m = good();
    m['index.html'].imports = ['_vendor'];
    m['_vendor'] = { file: 'assets/vendor-c.js' };
    const out = bundleViolations(m, HTML, goodFiles({ sources: { 'assets/vendor-c.js': ['../node_modules/vis-data/x.js'] } }));
    expect(out).toEqual(['eagerly loaded chunk assets/vendor-c.js contains vis-data']);
  });

  it('fails when the Timeline is not a separate chunk', () => {
    const m = good();
    delete m[TIMELINE_SOURCE];
    expect(bundleViolations(m, HTML, goodFiles())).toEqual(['the Timeline is not a separate, dynamically imported chunk']);
  });

  it('fails when the Timeline is only a static import of the entry', () => {
    const m = good();
    m[TIMELINE_SOURCE] = { file: 'assets/Timeline-b.js' };
    m['index.html'].imports = [TIMELINE_SOURCE];
    expect(bundleViolations(m, HTML, goodFiles())).toHaveLength(1);
  });

  it('fails when the entry css list holds the vis-timeline stylesheet', () => {
    const out = bundleViolations(good(), HTML, goodFiles({ css: { 'assets/index-a.css': '.a{}.vis-timeline{border:1px}' } }));
    expect(out).toEqual(['stylesheet assets/index-a.css, loaded eagerly, holds the vis-timeline rules']);
  });

  it('fails when a stylesheet linked from index.html holds it, though the manifest css list does not', () => {
    const m = good();
    m['index.html'].css = [];
    const html = '<link rel="stylesheet" href="./assets/other.css">';
    const out = bundleViolations(m, html, goodFiles({ css: { 'assets/other.css': '.vis-timeline .vis-item{}' } }));
    expect(out).toEqual(['stylesheet assets/other.css, loaded eagerly, holds the vis-timeline rules']);
  });

  it('reports a manifest with no entry', () => {
    expect(bundleViolations({}, HTML, goodFiles())).toEqual(['the manifest names no entry chunk']);
  });
});

describe('helpers', () => {
  it('eagerChunks follows static imports once, never dynamic ones', () => {
    const m: Manifest = {
      a: { file: 'a.js', isEntry: true, imports: ['b', 'c'], dynamicImports: ['d'] },
      b: { file: 'b.js', imports: ['c'] },
      c: { file: 'c.js', imports: ['b'] },
      d: { file: 'd.js', isDynamicEntry: true },
    };
    expect(eagerChunks(m).map((c) => c.file)).toEqual(['a.js', 'b.js', 'c.js']);
  });

  it('stylesheetLinks picks stylesheet links only, with the ./ dropped', () => {
    expect(stylesheetLinks('<link rel="icon" href="./f.svg"><link rel=stylesheet href=./a.css><link href="b.css" rel="stylesheet">')).toEqual([
      'a.css',
      'b.css',
    ]);
    expect(stylesheetLinks('<link rel="stylesheet">')).toEqual([]);
  });

  it('hasVisTimelineStyles matches the .vis-timeline selector only', () => {
    expect(hasVisTimelineStyles('.vis-timeline{}')).toBe(true);
    expect(hasVisTimelineStyles('.vis-timeline-x{}')).toBe(true);
    expect(hasVisTimelineStyles('.vis-item{}')).toBe(false);
  });
});
