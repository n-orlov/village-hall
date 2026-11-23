package suitecheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSuiteRunsVitestAndWritesItsJUnitFile is the R11/task 007 probe: ci/suite.sh
// builds web/ before the first Go pass (so Go tests embed the real UI), runs
// `npm run test` with its JUnit file and coverage directory pointed at the suite's
// result directory, runs once, and the web package's own config
// carries the JUnit reporter and the v8 coverage provider those paths feed.
func TestSuiteRunsVitestAndWritesItsJUnitFile(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	suite := stripCommentLines(read("ci/suite.sh"))

	for _, want := range []string{
		`vitest_junit="$outdir/junit-vitest.xml"`,
		`WEB_JUNIT_FILE="$vitest_junit"`,
		`WEB_COVERAGE_DIR="$outdir/web-coverage"`,
		`npm run test`,
		`npm run build`,
		`Vitest wrote no JUnit file`,
		`Vitest failed (see`,
	} {
		if !strings.Contains(suite, want) {
			t.Errorf("ci/suite.sh: missing %q", want)
		}
	}
	build, firstGoPass := strings.Index(suite, "npm run build"), strings.Index(suite, "go list ./...")
	if build < 0 || firstGoPass < 0 || build > firstGoPass {
		t.Errorf("ci/suite.sh: the web build must come before the first Go pass (build at %d, Go pass at %d)", build, firstGoPass)
	}

	cfg := read("web/vitest.config.ts")
	for _, want := range []string{`'junit'`, `process.env.WEB_JUNIT_FILE`, `provider: 'v8'`, `process.env.WEB_COVERAGE_DIR`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("web/vitest.config.ts: missing %q", want)
		}
	}
	if pkg := read("web/package.json"); !strings.Contains(pkg, `"test": "vitest run --coverage"`) {
		t.Errorf("web/package.json: the test script must run vitest with --coverage")
	}
}
