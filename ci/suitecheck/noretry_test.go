// noretry_test.go pins the no-retry policy of ci/suite.sh (task 003, R3):
// Vitest, Playwright, the Go package matrix and features/TestFeatures run once
// each, a failure in any of them fails the script, and nothing is run a second
// time. Each point has a test that runs the repository's real ci/suite.sh
// against the leg fixture (with stub npm/npx where a browser or node would be
// needed) and a static test over the script's own text. Each fails against the
// version of the script before the retries were removed:
//
//	git show e9f8d27:ci/suite.sh > /tmp/old-suite.sh
//	SUITECHECK_SUITE_SH=/tmp/old-suite.sh go test -count=1 ./ci/suitecheck/ -run NoRetry
package suitecheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(string(raw), "\n")
}

// A Playwright run that fails once fails the script, and Playwright ran once.
func TestNoRetryPlaywrightFailureFailsTheSuiteAfterOneRun(t *testing.T) {
	for _, mode := range []string{"", "only"} {
		for _, env := range []string{"STUB_PLAYWRIGHT_FAIL_FIRST=1", "STUB_PLAYWRIGHT_EXIT=1"} {
			run := runSuiteMode(t, mode, env)
			if run.err == nil {
				t.Errorf("mode %q, %s: ci/suite.sh exited 0 although Playwright failed", mode, env)
			}
			if got := strings.Count(run.stubLog(), "npx e2e playwright test"); got != 1 {
				t.Errorf("mode %q, %s: Playwright ran %d times, want exactly once", mode, env, got)
			}
			for _, f := range []string{"junit-playwright-run1.xml", "playwright-run1.log", "flaky-playwright.txt"} {
				if run.has(f) {
					t.Errorf("mode %q, %s: %s was written, which only a repeat leaves", mode, env, f)
				}
			}
		}
	}
}

// A Vitest run that fails once fails the script, and Vitest ran once.
func TestNoRetryVitestFailureFailsTheSuiteAfterOneRun(t *testing.T) {
	run := runSuiteMode(t, "skip", "STUB_VITEST_FAIL_FIRST=1")
	if run.err == nil {
		t.Error("ci/suite.sh exited 0 although Vitest failed")
	}
	if got := strings.Count(run.stubLog(), "run test"); got != 1 {
		t.Errorf("Vitest ran %d times, want exactly once", got)
	}
	for _, f := range []string{"junit-vitest-run1.xml", "vitest-run1.log", "flaky-vitest.txt"} {
		if run.has(f) {
			t.Errorf("%s was written, which only a repeat leaves", f)
		}
	}
}

// A Go test that fails on its first run fails the script, and runs once.
func TestNoRetryGoTestFailureFailsTheSuiteAfterOneRun(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "unit-runs.txt")
	run := runSuiteMode(t, "skip", "LEGFIXTURE_UNIT_FAILS_FIRST="+runs)
	if run.err == nil {
		t.Error("ci/suite.sh exited 0 although a Go test failed")
	}
	if got := countLines(t, runs); got != 1 {
		t.Errorf("the failing Go test ran %d times, want exactly once", got)
	}
	for _, f := range []string{"rerun-report-go.txt", "flaky-go.txt"} {
		if run.has(f) {
			t.Errorf("%s was written, which only a rerun leaves", f)
		}
	}
}

// A scenario that fails on one database leg fails the script on that leg and is
// never rerun alone, on either leg.
func TestNoRetryGodogScenarioFailureFailsTheSuiteOnEachLegWithoutARerun(t *testing.T) {
	for _, failing := range []string{"sqlite", "postgres", "sqlite postgres"} {
		runs := filepath.Join(t.TempDir(), "feature-runs.txt")
		run := runSuiteMode(t, "skip", "BOARD_CI_DB_LEGS=sqlite postgres",
			"LEGFIXTURE_GODOG_FAILS_FIRST="+failing, "LEGFIXTURE_FEATURE_RUNS="+runs)
		if run.err == nil {
			t.Errorf("failing on %q: ci/suite.sh exited 0 although a scenario failed", failing)
		}
		raw, err := os.ReadFile(runs)
		if err != nil {
			t.Fatalf("failing on %q: %v", failing, err)
		}
		if got, want := string(raw), "leg=sqlite paths=\nleg=postgres paths=\n"; got != want {
			t.Errorf("failing on %q: TestFeatures ran as\n%s\nwant one run per leg and no per-scenario rerun:\n%s", failing, got, want)
		}
		for _, f := range []string{"flaky-features.txt", "flaky-features-postgres.txt", "junit-features-rerun-1.xml", "junit-features-rerun-1-gotestsum.xml"} {
			if run.has(f) {
				t.Errorf("failing on %q: %s was written, which only a rerun leaves", failing, f)
			}
		}
	}
}

// The script's own text: no repeat of Playwright or Vitest, no gotestsum
// --rerun-fails, no per-scenario Godog rerun, and no flaky list a rerun would
// have filled.
func TestNoRetryScriptTextHasNoRetryMachinery(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(suiteScriptPath(root))
	if err != nil {
		t.Fatal(err)
	}
	script := stripCommentLines(string(raw))
	for what, banned := range map[string][]string{
		"a Playwright repeat":       {"run_playwright", "playwright-run1", "Playwright failed twice"},
		"a Vitest repeat":           {"run_vitest", "vitest-run1", "Vitest failed twice"},
		"a Go retry":                {"--rerun-fails", "rerun-report", `"$fails" -lt "$runs"`},
		"a Godog scenario rerun":    {"BOARD_GODOG_PATHS=", "all_recovered", "rerunning failed scenario"},
		"a flaky list from a rerun": {"flaky-playwright", "flaky-vitest", "flaky-go", "flaky-features"},
	} {
		for _, b := range banned {
			if strings.Contains(script, b) {
				t.Errorf("ci/suite.sh carries %s: %q", what, b)
			}
		}
	}
	// The one-time features pass is still there, once per leg.
	if got := strings.Count(script, "run_test_features "); got != 2 {
		t.Errorf("ci/suite.sh calls run_test_features %d times, want 2 (the SQLite leg and the other legs)", got)
	}
}
