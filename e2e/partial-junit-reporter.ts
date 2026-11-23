// A reporter that keeps a JUnit file of the tests finished so far (task 006, R3). Playwright's own junit reporter writes
// its file once, when the run ends, so a run cut by its bound (ci/unitrun.sh) leaves none and the build could not say
// which tests had already failed. This one rewrites `outputFile` (E2E_PARTIAL_JUNIT, ci/suite.sh points it at
// <outdir>/junit-playwright-partial.xml) after every failed test and at most every 20 s otherwise, through a rename,
// so a kill never leaves half a file. ci/suite.sh removes it once the complete junit.xml exists.
import { mkdirSync, renameSync, writeFileSync } from 'node:fs';
import { dirname, relative } from 'node:path';
import type { FullConfig, Reporter, TestCase, TestResult } from '@playwright/test/reporter';

const everyMs = 20_000;

function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// eslint-disable-next-line no-control-regex
const ansi = /\u001b\[[0-9;]*m/g;

function stripAnsi(s: string): string {
  return s.replace(ansi, '');
}

export default class PartialJunitReporter implements Reporter {
  private readonly file: string | undefined;
  private rootDir = '';
  private cases: string[] = [];
  private failures = 0;
  private lastWrite = 0;

  constructor(options: { outputFile?: string } = {}) {
    this.file = process.env.E2E_PARTIAL_JUNIT ?? options.outputFile;
  }

  onBegin(config: FullConfig): void {
    this.rootDir = config.rootDir;
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    if (!this.file) return;
    // The same classname and name Playwright's junit reporter gives a case: the spec file, then the titles under it.
    const classname = esc(relative(this.rootDir, test.location.file));
    const name = esc(test.titlePath().slice(3).join(' › '));
    const seconds = (result.duration / 1000).toFixed(3);
    const failed = result.status === 'failed' || result.status === 'timedOut' || result.status === 'interrupted';
    let body = '';
    if (failed) {
      this.failures++;
      const message = esc(stripAnsi(result.error?.message ?? '').split('\n')[0] || result.status);
      body = `<failure message="${message}" type="FAILURE">${esc(result.status)}</failure>`;
    } else if (result.status === 'skipped') {
      body = '<skipped/>';
    }
    this.cases.push(`<testcase name="${name}" classname="${classname}" time="${seconds}">${body}</testcase>`);
    const now = Date.now();
    if (failed || now - this.lastWrite >= everyMs) {
      this.write();
      this.lastWrite = now;
    }
  }

  private write(): void {
    if (!this.file) return;
    const xml =
      '<?xml version="1.0" encoding="UTF-8"?>\n' +
      `<testsuites name="playwright (partial: tests finished so far)" tests="${this.cases.length}" failures="${this.failures}">\n` +
      `<testsuite name="partial" tests="${this.cases.length}" failures="${this.failures}">\n${this.cases.join('\n')}\n</testsuite>\n</testsuites>\n`;
    mkdirSync(dirname(this.file), { recursive: true });
    writeFileSync(`${this.file}.tmp`, xml);
    renameSync(`${this.file}.tmp`, this.file);
  }

  printsToStdio(): boolean {
    return false;
  }
}
