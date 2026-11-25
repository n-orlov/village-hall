import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { gunzipSync } from 'node:zlib';
import { afterEach, describe, expect, it } from 'vitest';
import { filesUnder, isCompressible, precompress } from './precompress.ts';

const dirs: string[] = [];
function scratch(files: Record<string, string | Buffer>): string {
  const dir = mkdtempSync(path.join(tmpdir(), 'precompress-'));
  dirs.push(dir);
  for (const [name, body] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(dir, name)), { recursive: true });
    writeFileSync(path.join(dir, name), body);
  }
  return dir;
}
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

describe('isCompressible', () => {
  it('takes the text formats and leaves images and already-packed files', () => {
    for (const n of ['a.js', 'a.css', 'index.html', 'icon.svg', 'manifest.json']) expect(isCompressible(n)).toBe(true);
    for (const n of ['icon-192.png', 'a.js.gz', 'noext', 'a.map']) expect(isCompressible(n)).toBe(false);
  });
});

describe('filesUnder', () => {
  it('lists nested files relative to the directory, sorted', () => {
    const dir = scratch({ 'b.txt': 'b', 'assets/z.js': 'z', 'assets/a.css': 'a' });
    expect(filesUnder(dir)).toEqual([path.join('assets', 'a.css'), path.join('assets', 'z.js'), 'b.txt']);
  });
});

describe('precompress', () => {
  it('writes a .gz beside each text file whose gunzipped bytes equal the original', () => {
    const js = 'console.log("hello");\n'.repeat(50);
    const dir = scratch({ 'assets/app-1.js': js, 'index.html': '<html></html>', 'manifest.json': '{}', 'icon.svg': '<svg/>' });
    expect(precompress(dir)).toEqual([path.join('assets', 'app-1.js'), 'icon.svg', 'index.html', 'manifest.json']);
    expect(gunzipSync(readFileSync(path.join(dir, 'assets/app-1.js.gz'))).toString()).toBe(js);
    expect(readFileSync(path.join(dir, 'assets/app-1.js.gz')).length).toBeLessThan(js.length);
    expect(gunzipSync(readFileSync(path.join(dir, 'index.html.gz'))).toString()).toBe('<html></html>');
  });

  it('leaves images alone and does not compress a .gz again on a second run', () => {
    const dir = scratch({ 'icon-192.png': Buffer.from([137, 80, 78, 71]), 'a.css': 'a{}' });
    precompress(dir);
    precompress(dir);
    expect(readdirSync(dir).sort()).toEqual(['a.css', 'a.css.gz', 'icon-192.png']);
  });

  it('is byte-for-byte reproducible', () => {
    const dir = scratch({ 'a.js': 'x'.repeat(1000) });
    precompress(dir);
    const first = readFileSync(path.join(dir, 'a.js.gz'));
    precompress(dir);
    expect(readFileSync(path.join(dir, 'a.js.gz')).equals(first)).toBe(true);
  });
});
