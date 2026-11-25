// Precompresses the built UI's text files: every compressible file in a build
// directory gets a `<name>.gz` beside it, so the Go server can answer a gzip client
// with those bytes as they are (no compression work per request).

import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { gzipSync, constants } from 'node:zlib';

/** File extensions worth compressing: text formats, not the PNG icons. */
export const COMPRESSIBLE = ['.js', '.css', '.html', '.svg', '.json'] as const;

export function isCompressible(name: string): boolean {
  return (COMPRESSIBLE as readonly string[]).includes(path.extname(name));
}

/** Every file under dir, as paths relative to it, in a stable order. */
export function filesUnder(dir: string, rel = ''): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(path.join(dir, rel), { withFileTypes: true })) {
    const at = path.join(rel, entry.name);
    if (entry.isDirectory()) found.push(...filesUnder(dir, at));
    else found.push(at);
  }
  return found.sort();
}

/** Writes `<file>.gz` (best compression, no timestamp) for each compressible file; returns the files compressed. */
export function precompress(dir: string): string[] {
  const done: string[] = [];
  for (const file of filesUnder(dir)) {
    if (!isCompressible(file)) continue;
    const packed = gzipSync(readFileSync(path.join(dir, file)), { level: constants.Z_BEST_COMPRESSION });
    writeFileSync(path.join(dir, `${file}.gz`), packed);
    done.push(file);
  }
  return done;
}
