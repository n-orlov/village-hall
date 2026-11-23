// Package suitecheck's covfixture tests are task 002's (R187) runnable
// regression for ci/suite.sh's BOARD_* scrub and merged-coverage-profile
// behaviour: they run the repository's real ci/suite.sh, unmodified,
// against the deterministic fixture in testdata/covfixture/ (an all-passing
// unit/ package and a no-op features/ package, both deliberately free of
// any non-test statement, so -coverpkg=./... instruments zero blocks for
// either -- see testdata/covfixture/unit/unit_test.go's own header).
//
// Without the scrub the probe file holds the leaked BOARD_SEED_PROBE value
// instead of "", and without the merge <outdir>/coverage-merged.out is never
// written at all.
package suitecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// covFixtureRun is one ci/suite.sh run against a fresh copy of
// testdata/covfixture/.
type covFixtureRun struct {
	module string
	outdir string
	probe  string
	out    []byte
	err    error
}

// runCovFixture copies the covfixture module plus the real ci/suite.sh and
// ci/junitflaky into a temp dir and runs the script there with the caller's
// BOARD_* stripped, then extraEnv appended.
func runCovFixture(t *testing.T, extraEnv ...string) covFixtureRun {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("go toolchain not on PATH: %v", err)
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	fixture := filepath.Join(root, "ci", "suitecheck", "testdata", "covfixture")
	module := t.TempDir()
	copyFile(t, filepath.Join(fixture, "go.mod.fixture"), filepath.Join(module, "go.mod"), 0o644)
	for _, rel := range []string{"unit/unit_test.go", "unit/extraflag_test.go", "features/fixture_test.go", "features/extraflag_test.go", "probe/main.go"} {
		copyFile(t, filepath.Join(fixture, filepath.FromSlash(rel)), filepath.Join(module, filepath.FromSlash(rel)), 0o644)
	}
	copyFile(t, suiteScriptPath(root), filepath.Join(module, "ci", "suite.sh"), 0o755)
	copyFile(t, filepath.Join(root, "ci", "junitflaky", "main.go"), filepath.Join(module, "ci", "junitflaky", "main.go"), 0o644)
	copyFile(t, filepath.Join(root, "ci", "junit2allure", "main.go"), filepath.Join(module, "ci", "junit2allure", "main.go"), 0o644)
	copyFile(t, filepath.Join(root, "ci", "legcompare", "main.go"), filepath.Join(module, "ci", "legcompare", "main.go"), 0o644)

	run := covFixtureRun{
		module: module,
		outdir: filepath.Join(module, "out"),
		probe:  filepath.Join(module, "probe.txt"),
	}

	env := make([]string, 0, len(os.Environ())+6+len(extraEnv))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BOARD_") || strings.HasPrefix(kv, "VH_") || strings.HasPrefix(kv, "GOWORK=") || strings.HasPrefix(kv, "GOCOVERDIR=") || strings.HasPrefix(kv, "EXTRA_PROBE_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GOWORK=off",
		"BOARD_CI_OUT="+run.outdir,
		"BOARD_CI_GO_PACKAGES=./unit/",
		// Not a BOARD_* name, so the scrub must leave it alone -- it is how
		// the fixture's own test process reports what it saw back to this
		// test, across the subprocess boundary.
		"PROBE_OUT="+run.probe,
	)
	env = append(env, extraEnv...)

	cmd := exec.Command("sh", filepath.Join(module, "ci", "suite.sh"))
	cmd.Dir = module
	cmd.Env = env
	run.out, run.err = cmd.CombinedOutput()
	t.Logf("ci/suite.sh output:\n%s", run.out)
	return run
}

func TestSuiteScriptScrubsBoardAndVHEnvironmentAndFailsOnEmptyMergedCoverage(t *testing.T) {
	// The seeded leak this test exists to catch: a BOARD_* variable present
	// in the CALLER's own environment for a reason that has nothing to do
	// with this run (BOARD_SEED_PROBE stands in for a real one like
	// BOARD_GODOG_PATHS/BOARD_HOME).
	run := runCovFixture(t, "BOARD_SEED_PROBE=leaked-board-value", "VH_SEED_PROBE=leaked-vh-value")
	probe, outdir, runErr := run.probe, run.outdir, run.err

	seen, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("fixture never wrote probe file %s (did unit/unit_test.go even run?): %v", probe, err)
	}
	if string(seen) != "" {
		t.Errorf("BOARD_SEED_PROBE leaked into the go test process's own environment: saw %q, want \"\" (unset) -- ci/suite.sh must unset every BOARD_* and VH_* variable from its own environment before launching go test", seen)
	}

	if runErr == nil {
		t.Errorf("ci/suite.sh exited 0 for a run whose unit+features coverage data is empty (both fixture packages report \"[no statements]\"); it must exit non-zero when coverage-merged.out has no blocks")
	}

	mergedPath := filepath.Join(outdir, "coverage-merged.out")
	merged, err := os.ReadFile(mergedPath)
	if err != nil {
		t.Fatalf("%s was never written: %v", mergedPath, err)
	}
	// At most the "mode: <mode>" header line; a genuine block line would
	// mean this fixture failed to stay statement-free and the test is no
	// longer exercising the empty-profile path it claims to.
	lines := strings.Split(strings.TrimRight(string(merged), "\n"), "\n")
	blocks := 0
	for _, l := range lines {
		if l != "" && !strings.HasPrefix(l, "mode:") {
			blocks++
		}
	}
	if blocks != 0 {
		t.Fatalf("%s unexpectedly has %d coverage block line(s); fixture precondition (zero instrumented statements) no longer holds:\n%s", mergedPath, blocks, merged)
	}
}

// TestSuiteScriptPassesCapturedExtraFlagsToBothPasses is the regression for
// the scrub unsetting BOARD_CI_GO_EXTRA_FLAGS before the two `go test`
// invocations expanded it (task 002, first validation): the nightly lane's
// -race silently vanished from both passes. -tags=suiteextraflag stands in
// for -race (which needs cgo) -- it builds the fixture's extraflag_test.go
// files only when the flag actually reaches `go test`, and each pass then
// writes its own marker.
func TestSuiteScriptPassesCapturedExtraFlagsToBothPasses(t *testing.T) {
	markers := t.TempDir()
	runCovFixture(t, "BOARD_CI_GO_EXTRA_FLAGS=-tags=suiteextraflag", "EXTRA_PROBE_DIR="+markers)
	for _, pass := range []string{"unit", "features"} {
		got, err := os.ReadFile(filepath.Join(markers, pass))
		if err != nil {
			t.Errorf("%s pass never built its -tags=suiteextraflag file: BOARD_CI_GO_EXTRA_FLAGS did not reach that pass's go test (%v)", pass, err)
			continue
		}
		if string(got) != "tagged" {
			t.Errorf("%s pass marker = %q, want \"tagged\"", pass, got)
		}
	}
}

// TestSuiteScriptGivesTheFeaturesPassTheUnitPassCoverMode is the regression
// for the first nightly dispatch at 1b169a1e61: -race makes the unit pass
// write atomic counters while the features harness built its black-box board
// binary in the default set mode, `go tool covdata textfmt` refused the mix
// ("counter mode clash") and coverage-merged.out came out empty. The
// features pass must therefore be told the unit pass's covermode.
func TestSuiteScriptGivesTheFeaturesPassTheUnitPassCoverMode(t *testing.T) {
	for _, tc := range []struct{ name, flags, want string }{
		{"plain run", "-tags=suiteextraflag", "set"},
		{"nightly with -race", "-race -tags=suiteextraflag", "atomic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			markers := t.TempDir()
			runCovFixture(t, "BOARD_CI_GO_EXTRA_FLAGS="+tc.flags, "EXTRA_PROBE_DIR="+markers)
			got, err := os.ReadFile(filepath.Join(markers, "features-covermode"))
			if err != nil {
				t.Fatalf("features pass never reported its covermode: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("features pass saw BOARD_FEATURES_COVERMODE=%q, want %q", got, tc.want)
			}
		})
	}
}

// TestSuiteScriptFailsWhenScenariosLeaveNoFeatureCoverage is the regression for
// the suite accepting a unit-only merged profile (cure-01-04): a features leg
// whose log shows scenarios ran must have left counter data in GOCOVERDIR, and
// the leg that did not is named. The fixture's probe stands in for the
// instrumented board binary; the real build path is pinned by
// features/coverage_test.go.
func TestSuiteScriptFailsWhenScenariosLeaveNoFeatureCoverage(t *testing.T) {
	const missing = "ran scenarios but wrote no coverage counters"
	for _, tc := range []struct {
		name         string
		coverageLegs string
		flags        string
		wantLegs     []string // legs the failure must name
	}{
		{"neither leg instrumented", "", "", []string{"sqlite", "postgres"}},
		{"only sqlite instrumented", "sqlite", "", []string{"postgres"}},
		{"only postgres instrumented", "postgres", "", []string{"sqlite"}},
		{"both legs instrumented", "sqlite postgres", "", nil},
		{"both legs instrumented under -race", "sqlite postgres", "-race", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := []string{"FIXTURE_SCENARIOS=1", "FIXTURE_COVERAGE_LEGS=" + tc.coverageLegs, "BOARD_CI_DB_LEGS=sqlite postgres"}
			if tc.flags != "" {
				if _, err := exec.LookPath("gcc"); err != nil && tc.flags == "-race" {
					t.Skip("-race needs cgo")
				}
				env = append(env, "BOARD_CI_GO_EXTRA_FLAGS="+tc.flags)
			}
			run := runCovFixture(t, env...)
			out := string(run.out)
			for _, leg := range []string{"sqlite", "postgres"} {
				named := strings.Contains(out, "features/ on the "+leg+" leg "+missing)
				want := false
				for _, w := range tc.wantLegs {
					want = want || w == leg
				}
				if named != want {
					t.Errorf("failure naming the %s leg = %v, want %v", leg, named, want)
				}
			}
			if len(tc.wantLegs) > 0 && run.err == nil {
				t.Errorf("ci/suite.sh exited 0 although a leg ran scenarios without leaving feature coverage")
			}
			if len(tc.wantLegs) == 0 {
				if strings.Contains(out, "no usable feature coverage") {
					t.Errorf("a suite whose legs left counters was refused:\n%s", out)
				}
				merged, err := os.ReadFile(filepath.Join(run.outdir, "coverage-merged.out"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(merged), "\n") < 2 || !strings.Contains(string(merged), "boardci.fixture/cov/probe") {
					t.Errorf("merged profile lacks the feature binary's blocks:\n%s", merged)
				}
			}
		})
	}
}

// TestSuiteScriptDemandsFeatureCoverageOnlyFromLegsThatRanScenarios pins the
// edges of the same rule (cure-01-04) that the table above does not name: a
// suite configured with one database leg names only that leg, a leg whose log
// shows no scenarios ran is never accused of missing counters (there was
// nothing to instrument), and an instrumented leg is not blamed for a
// neighbour's gap.
func TestSuiteScriptDemandsFeatureCoverageOnlyFromLegsThatRanScenarios(t *testing.T) {
	const missing = "ran scenarios but wrote no coverage counters"
	for _, tc := range []struct {
		name      string
		scenarios string // FIXTURE_SCENARIOS
		legs      string // BOARD_CI_DB_LEGS
		covLegs   string // FIXTURE_COVERAGE_LEGS
		wantNamed []string
	}{
		{"single sqlite leg, uninstrumented", "1", "sqlite", "", []string{"sqlite"}},
		{"single sqlite leg, instrumented", "1", "sqlite", "sqlite", nil},
		{"no scenarios ran on any leg", "", "sqlite postgres", "", nil},
		{"postgres instrumented, sqlite not, only sqlite named", "1", "sqlite postgres", "postgres", []string{"sqlite"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := runCovFixture(t, "FIXTURE_SCENARIOS="+tc.scenarios, "FIXTURE_COVERAGE_LEGS="+tc.covLegs, "BOARD_CI_DB_LEGS="+tc.legs)
			out := string(run.out)
			for _, leg := range []string{"sqlite", "postgres"} {
				want := false
				for _, w := range tc.wantNamed {
					want = want || w == leg
				}
				if named := strings.Contains(out, "features/ on the "+leg+" leg "+missing); named != want {
					t.Errorf("failure naming the %s leg = %v, want %v", leg, named, want)
				}
			}
			if len(tc.wantNamed) > 0 && run.err == nil {
				t.Errorf("ci/suite.sh exited 0 although a leg that ran scenarios left no feature coverage")
			}
		})
	}
}
