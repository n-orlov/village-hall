// Vitest config used by Stryker (stryker.config.mjs): the unit-test config for src/ only, without the JUnit reporter and coverage
// (Stryker brings its own per-test coverage). Same tests, same assertions. Not mergeConfig: that would concatenate `include`
// and pull the scripts/ tests (which spawn node) into every mutant run.
// src/bundle.test.ts is left out: it runs a real production build, and in a Stryker sandbox that build holds the instrumented
// src/lib (thousands of mutant switches), which is over the entry-chunk cap by construction. It judges the shipped build, not a
// mutant, and the Web UI unit still runs it; src/lib/bundleCheck.ts is mutated against its own bundleCheck.test.ts.
import { configDefaults } from 'vitest/config';
import base from './vitest.config.ts';

const test = base.test ?? {};
export default {
  ...base,
  test: { ...test, include: ['src/**/*.test.ts'], exclude: [...configDefaults.exclude, 'src/bundle.test.ts'], reporters: ['default'], coverage: { enabled: false } },
};
