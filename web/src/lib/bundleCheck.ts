// Checks over a Vite build manifest (R7): the entry chunk stays small, vis-timeline and vis-data
// stay out of everything the page loads eagerly, the Timeline is a chunk of its own, and no
// stylesheet the page loads eagerly holds the vis-timeline rules.

export const ENTRY_CHUNK_MAX_BYTES = 450_000;
export const TIMELINE_SOURCE = 'src/Timeline.svelte';
export const EAGER_FORBIDDEN_PACKAGES = ['vis-timeline', 'vis-data'] as const;

export interface ManifestChunk {
  file: string;
  isEntry?: boolean;
  isDynamicEntry?: boolean;
  imports?: string[];
  dynamicImports?: string[];
  css?: string[];
}

export type Manifest = Record<string, ManifestChunk>;

/** What a check reads from the build directory: a file's size and text, and the module paths a chunk was bundled from. */
export interface BuildFiles {
  size(file: string): number;
  text(file: string): string;
  sources(file: string): string[];
}

/** The chunks the page loads before any view: the entry and its static imports, transitively. */
export function eagerChunks(manifest: Manifest): ManifestChunk[] {
  const seen = new Set<string>();
  const out: ManifestChunk[] = [];
  const visit = (key: string) => {
    const chunk = manifest[key];
    if (chunk === undefined || seen.has(key)) return;
    seen.add(key);
    out.push(chunk);
    for (const next of chunk.imports ?? []) visit(next);
  };
  for (const [key, chunk] of Object.entries(manifest)) if (chunk.isEntry) visit(key);
  return out;
}

/** The stylesheet files named by `<link rel="stylesheet" href="...">` in a built page. */
export function stylesheetLinks(html: string): string[] {
  const links: string[] = [];
  for (const tag of html.matchAll(/<link\b[^>]*>/g)) {
    if (!/rel=["']?stylesheet/.test(tag[0])) continue;
    const href = /href=["']?([^"'\s>]+)/.exec(tag[0]);
    if (href !== null) links.push(href[1].replace(/^\.\//, ''));
  }
  return links;
}

/** True when a stylesheet carries a rule for the vis-timeline widget. */
export function hasVisTimelineStyles(css: string): boolean {
  return /\.vis-timeline\b/.test(css);
}

function fromPackage(source: string, pkg: string): boolean {
  return source.includes(`node_modules/${pkg}/`);
}

function entryProblems(entries: ManifestChunk[], files: BuildFiles): string[] {
  const problems: string[] = [];
  for (const entry of entries) {
    const size = files.size(entry.file);
    if (size > ENTRY_CHUNK_MAX_BYTES) problems.push(`entry chunk ${entry.file} is ${size} bytes raw, over ${ENTRY_CHUNK_MAX_BYTES}`);
  }
  return problems;
}

function eagerPackageProblems(eager: ManifestChunk[], files: BuildFiles): string[] {
  const problems: string[] = [];
  for (const chunk of eager) {
    const sources = files.sources(chunk.file);
    for (const pkg of EAGER_FORBIDDEN_PACKAGES) {
      if (sources.some((s) => fromPackage(s, pkg))) problems.push(`eagerly loaded chunk ${chunk.file} contains ${pkg}`);
    }
  }
  return problems;
}

function timelineProblems(manifest: Manifest, eager: ManifestChunk[]): string[] {
  const timeline = manifest[TIMELINE_SOURCE];
  if (timeline === undefined || !timeline.isDynamicEntry || eager.some((c) => c.file === timeline.file)) {
    return ['the Timeline is not a separate, dynamically imported chunk'];
  }
  return [];
}

function stylesheetProblems(eager: ManifestChunk[], html: string, files: BuildFiles): string[] {
  const sheets = new Set<string>([...eager.flatMap((c) => c.css ?? []), ...stylesheetLinks(html)]);
  return [...sheets]
    .filter((sheet) => hasVisTimelineStyles(files.text(sheet)))
    .map((sheet) => `stylesheet ${sheet}, loaded eagerly, holds the vis-timeline rules`);
}

/** Every way this build breaks the R7 rules, one sentence each; empty when it holds. */
export function bundleViolations(manifest: Manifest, html: string, files: BuildFiles): string[] {
  const entries = Object.values(manifest).filter((c) => c.isEntry);
  if (entries.length === 0) return ['the manifest names no entry chunk'];
  const eager = eagerChunks(manifest);
  return [
    ...entryProblems(entries, files),
    ...eagerPackageProblems(eager, files),
    ...timelineProblems(manifest, eager),
    ...stylesheetProblems(eager, html, files),
  ];
}
