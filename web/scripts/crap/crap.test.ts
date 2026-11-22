// The TS CRAP gate, end to end: the CLI runs on a planted fixture tree (source + a coverage-final.json) and its exit status is the verdict.
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

const script = path.resolve(import.meta.dirname, 'crap.ts');
const dirs: string[] = [];

afterEach(() => {
  for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

// Complexity 7 (six `if`s and the function itself): over the ceiling 8 only while uncovered.
const branchy = `export function branchy(n: number): number {
${Array.from({ length: 6 }, (_, i) => `  if (n === ${i}) return ${i};`).join('\n')}
  return -1;
}
`;
// Complexity 1.
const flat = `export function flat(n: number): number {
  return n + 1;
}
`;

/** A fixture root holding src/<file> and coverage/coverage-final.json, with every function of the file reported `hits` times. */
function plant(source: string, hits: number, ceiling = 8): string {
  const root = mkdtempSync(path.join(tmpdir(), 'crap-fixture-'));
  dirs.push(root);
  mkdirSync(path.join(root, 'src'));
  mkdirSync(path.join(root, 'coverage'));
  writeFileSync(path.join(root, 'src/fixture.ts'), source);
  const lines = source.split('\n');
  const fnEnd = lines.length - 2;
  const statementMap: Record<string, unknown> = {};
  const s: Record<string, number> = {};
  for (let line = 2; line < fnEnd; line += 1) {
    statementMap[String(line)] = { start: { line, column: 2 }, end: { line, column: 20 } };
    s[String(line)] = hits;
  }
  const bodyColumn = lines[0].indexOf('{', lines[0].indexOf('):'));
  const file = {
    fnMap: { 0: { loc: { start: { line: 1, column: bodyColumn }, end: { line: fnEnd, column: 1 } } } },
    statementMap,
    s,
    f: { 0: hits },
  };
  writeFileSync(path.join(root, 'coverage/coverage-final.json'), JSON.stringify({ [path.join(root, 'src/fixture.ts')]: file }));
  writeFileSync(path.join(root, 'quality.json'), JSON.stringify({ crap_ts: { enabled: true, ceiling } }));
  return root;
}

function gate(root: string, extra: string[] = []) {
  const run = spawnSync(process.execPath, [script, '--root', root, '--config', path.join(root, 'quality.json'), ...extra], { encoding: 'utf8' });
  return { status: run.status, out: `${run.stdout}${run.stderr}` };
}

// Each case spawns the CLI (a fresh node + TypeScript compile), about 0.5 s on an idle machine but well past
// Vitest's 5 s default on a loaded CI agent: give the subprocess its own bound rather than the default.
const spawnTimeout = 60_000;

describe('TS CRAP gate', { timeout: spawnTimeout }, () => {
  it('fails a planted function over the ceiling, naming it and its score', () => {
    const { status, out } = gate(plant(branchy, 0));
    expect(status).toBe(1);
    expect(out).toContain('src/fixture.ts:branchy');
    expect(out).toContain('CRAP 56');
  });

  it('passes the same function once its tests cover it', () => {
    const { status, out } = gate(plant(branchy, 5));
    expect(out).toContain('TS CRAP gate passed');
    expect(status).toBe(0);
  });

  it('reads the ceiling from the config: a lower ceiling fails what 8 passed', () => {
    expect(gate(plant(flat, 3, 8)).status).toBe(0);
    expect(gate(plant(flat, 3, 0.5)).status).toBe(1);
  });

  it('treats a file with no coverage entry as uncovered', () => {
    const root = plant(branchy, 5);
    writeFileSync(path.join(root, 'coverage/coverage-final.json'), '{}');
    expect(gate(root).status).toBe(1);
  });

  it('is an input error (exit 2) without coverage, or without an enabled ceiling', () => {
    const root = plant(flat, 1);
    rmSync(path.join(root, 'coverage'), { recursive: true });
    expect(gate(root).status).toBe(2);
    const other = plant(flat, 1);
    writeFileSync(path.join(other, 'quality.json'), JSON.stringify({ crap_ts: { enabled: false, ceiling: 8 } }));
    expect(gate(other).status).toBe(2);
  });

  it('is an input error when the scan finds no function', () => {
    const root = plant(flat, 1);
    writeFileSync(path.join(root, 'src/fixture.ts'), 'export const x = 1;\n');
    const { status, out } = gate(root);
    expect(status).toBe(2);
    expect(out).toContain('no function was scored');
  });
});
