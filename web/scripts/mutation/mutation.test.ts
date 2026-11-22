import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { checkModules, scoreMutants } from './mutationCheck.ts';

const mutants = (killed: number, survived: number, extra: string[] = []) => [
  ...Array.from({ length: killed }, () => ({ status: 'Killed' })),
  ...Array.from({ length: survived }, () => ({ status: 'Survived' })),
  ...extra.map((status) => ({ status })),
];

describe('scoreMutants', () => {
  it('counts Killed and Timeout as detected, Survived and NoCoverage as undetected, and ignores the rest', () => {
    const score = scoreMutants(mutants(2, 1, ['Timeout', 'NoCoverage', 'Ignored', 'CompileError']));
    expect(score).toMatchObject({ detected: 3, undetected: 2, score: 60 });
  });

  it('is null with nothing to score', () => {
    expect(scoreMutants([])).toBeNull();
    expect(scoreMutants(mutants(0, 0, ['Ignored']))).toBeNull();
  });
});

describe('checkModules', () => {
  const report = (files: Record<string, ReturnType<typeof mutants>>) => ({
    files: Object.fromEntries(Object.entries(files).map(([file, m]) => [file, { mutants: m }])),
  });

  it('passes a module exactly on the break and fails one below it, each on its own', () => {
    const { scores, failures } = checkModules(report({ 'src/lib/a.ts': mutants(4, 1), 'src/lib/b.ts': mutants(79, 21) }), 'src/lib', 80);
    expect(scores.map((s) => s.module)).toEqual(['src/lib/a.ts', 'src/lib/b.ts']);
    expect(failures).toEqual(['src/lib/b.ts: 79.00% is below the break of 80%']);
  });

  it('does not let a strong module hide a weak one', () => {
    const { failures } = checkModules(report({ 'src/lib/a.ts': mutants(1000, 0), 'src/lib/b.ts': mutants(1, 9) }), 'src/lib', 80);
    expect(failures).toHaveLength(1);
  });

  it('ignores files outside the path and skips a module with nothing to score', () => {
    const { scores, failures } = checkModules(report({ 'src/lib/a.ts': mutants(5, 0), 'src/lib/types.ts': [], 'src/other.ts': mutants(0, 5) }), 'src/lib', 80);
    expect(scores).toHaveLength(1);
    expect(failures).toEqual([]);
  });

  it('fails a report with no scoreable mutant', () => {
    expect(checkModules(report({}), 'src/lib', 80).failures).toEqual(['no scoreable mutant under src/lib in the report']);
  });
});

// Spawns the CLI: bounded generously so a loaded CI agent cannot time it out (crap.test.ts, the same case).
describe('the mutation CLI judging an existing report', { timeout: 60_000 }, () => {
  const script = path.resolve(import.meta.dirname, 'mutation.ts');
  const dirs: string[] = [];
  afterEach(() => {
    for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
  });

  function judge(killed: number, survived: number) {
    const dir = mkdtempSync(path.join(tmpdir(), 'mutation-fixture-'));
    dirs.push(dir);
    writeFileSync(path.join(dir, 'report.json'), JSON.stringify({ files: { 'src/lib/a.ts': { mutants: mutants(killed, survived) } } }));
    const run = spawnSync(process.execPath, [script, '--check-only', '--report', path.join(dir, 'report.json')], { encoding: 'utf8' });
    return { status: run.status, out: `${run.stdout}${run.stderr}` };
  }

  it('exits 0 at the break and 1 below it', () => {
    expect(judge(8, 2).status).toBe(0);
    const below = judge(7, 3);
    expect(below.status).toBe(1);
    expect(below.out).toContain('src/lib/a.ts: 70.00% is below the break of 80%');
  });
});
