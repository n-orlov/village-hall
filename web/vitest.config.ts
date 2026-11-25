import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  // Compiles .svelte files, so a unit test can render a component (server-side, through svelte/server).
  plugins: [svelte()],
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts', 'scripts/**/*.test.ts'],
    // ci/suite.sh points both at its own result directory.
    reporters: ['default', ['junit', { outputFile: process.env.WEB_JUNIT_FILE ?? 'junit/junit.xml' }]],
    coverage: {
      provider: 'v8',
      // Write coverage even when a test fails, so a red run still has evidence.
      reportOnFailure: true,
      reportsDirectory: process.env.WEB_COVERAGE_DIR ?? 'coverage',
      reporter: ['text', 'json', 'json-summary', 'lcov'],
      include: ['src/lib/**/*.ts'],
      // src/lib/api holds generated type declarations (openapi-typescript): no statements.
      exclude: ['src/**/*.test.ts', 'src/lib/api/**'],
    },
  },
});
