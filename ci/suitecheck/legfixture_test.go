// legfixture_test.go proves the database matrix of ci/suite.sh (task 013, R4):
// the feature scenarios run on a SQLite leg and a Postgres leg, each leg writes
// its own JUnit, and a scenario green on one leg and red on the other fails the
// suite and is named. It runs the repository's real ci/suite.sh, ci/junitflaky
// and ci/legcompare against testdata/legfixture/, whose TestFeatures reads
// BOARD_DB_LEG the way the real one does but needs no database.
package suitecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type legFixtureRun struct {
	outdir string
	legLog string
	out    []byte
	err    error
}

func runLegFixture(t *testing.T, redOn string, extraEnv ...string) legFixtureRun {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("go toolchain not on PATH: %v", err)
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	fixture := filepath.Join(root, "ci", "suitecheck", "testdata", "legfixture")
	module := t.TempDir()
	copyFile(t, filepath.Join(fixture, "go.mod.fixture"), filepath.Join(module, "go.mod"), 0o644)
	for _, f := range []string{"unit.go", "unit_test.go"} {
		copyFile(t, filepath.Join(fixture, "unit", f), filepath.Join(module, "unit", f), 0o644)
	}
	copyFile(t, filepath.Join(fixture, "features", "fixture_test.go"), filepath.Join(module, "features", "fixture_test.go"), 0o644)
	copyFile(t, suiteScriptPath(root), filepath.Join(module, "ci", "suite.sh"), 0o755)
	for _, tool := range []string{"junitflaky", "junit2allure", "legcompare"} {
		copyFile(t, filepath.Join(root, "ci", tool, "main.go"), filepath.Join(module, "ci", tool, "main.go"), 0o644)
	}

	run := legFixtureRun{outdir: filepath.Join(module, "out"), legLog: filepath.Join(module, "legs.txt")}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BOARD_") || strings.HasPrefix(kv, "GOWORK=") || strings.HasPrefix(kv, "GOCOVERDIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GOWORK=off", "BOARD_CI_OUT="+run.outdir, "BOARD_CI_GO_PACKAGES=./unit/",
		"LEGFIXTURE_RED_ON="+redOn, "LEGFIXTURE_LEG_LOG="+run.legLog)
	env = append(env, extraEnv...)

	cmd := exec.Command("sh", filepath.Join(module, "ci", "suite.sh"))
	cmd.Dir = module
	cmd.Env = env
	run.out, run.err = cmd.CombinedOutput()
	t.Logf("ci/suite.sh output:\n%s", run.out)
	return run
}

func (r legFixtureRun) read(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.outdir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s was not written: %v", rel, err)
	}
	return string(raw)
}

func TestSuiteRunsEveryFeatureLegAndWritesPerLegJUnit(t *testing.T) {
	run := runLegFixture(t, "")
	if run.err != nil {
		t.Fatalf("ci/suite.sh exited non-zero for scenarios that agree on both legs: %v", run.err)
	}
	if got := run.read(t, "../legs.txt"); got != "sqlite\npostgres\n" {
		t.Errorf("TestFeatures ran on legs %q, want sqlite then postgres (BOARD_DB_LEG per pass)", got)
	}
	for _, leg := range []string{"sqlite", "postgres"} {
		junit := run.read(t, "junit-merged/junit-features-"+leg+".xml")
		if !strings.Contains(junit, "agrees") || !strings.Contains(junit, "diverges") {
			t.Errorf("the %s leg's JUnit does not hold the fixture's scenarios:\n%s", leg, junit)
		}
	}
	if got := run.read(t, "leg-divergence.txt"); got != "" {
		t.Errorf("leg-divergence.txt = %q for legs that agree", got)
	}
}

func TestSuiteFailsAndNamesAScenarioGreenOnOneLegAndRedOnTheOther(t *testing.T) {
	for _, red := range []string{"postgres", "sqlite"} {
		run := runLegFixture(t, red)
		if run.err == nil {
			t.Fatalf("red on %s: ci/suite.sh exited 0 although a scenario is green on one leg and red on the other", red)
		}
		green := map[string]string{"postgres": "sqlite", "sqlite": "postgres"}[red]
		want := "diverges: sqlite=" + map[bool]string{true: "red", false: "green"}[red == "sqlite"] +
			" postgres=" + map[bool]string{true: "red", false: "green"}[red == "postgres"]
		report := run.read(t, "leg-divergence.txt")
		if !strings.Contains(report, "DIVERGENT") || !strings.Contains(report, want) {
			t.Errorf("red on %s (green on %s): leg-divergence.txt %q does not name %q", red, green, report, want)
		}
		if strings.Contains(report, "agrees") {
			t.Errorf("leg-divergence.txt names the scenario that agrees: %q", report)
		}
		if !strings.Contains(string(run.out), "the database legs disagree") {
			t.Errorf("red on %s: the suite's own output does not say the legs disagree", red)
		}
	}
}

func TestSuiteFailsWithoutADivergenceWhenAScenarioIsRedOnBothLegs(t *testing.T) {
	run := runLegFixture(t, "both")
	if run.err == nil {
		t.Fatal("ci/suite.sh exited 0 for a scenario red on both legs")
	}
	if got := run.read(t, "leg-divergence.txt"); got != "" {
		t.Errorf("a scenario red on both legs is a failure, not a divergence: %q", got)
	}
	if strings.Contains(string(run.out), "the database legs disagree") {
		t.Error("the suite reports a disagreement between legs that agree")
	}
}

func TestSuiteRunsOnlySQLiteWhenOnlySQLiteIsAsked(t *testing.T) {
	run := runLegFixture(t, "postgres", "BOARD_CI_DB_LEGS=sqlite")
	if run.err != nil {
		t.Fatalf("a sqlite-only run must not run, or be failed by, the Postgres leg: %v", run.err)
	}
	if got := run.read(t, "../legs.txt"); got != "sqlite\n" {
		t.Errorf("legs run: %q", got)
	}
}
