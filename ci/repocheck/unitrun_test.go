package repocheck

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ci/unitrun.sh runs a unit's command under a bound, keeps what it wrote when
// it fails or is cut, and names the failing tests from its junit-*.xml. The
// tests run the real script against a stub command (no CI tool runs), and run
// the same checks against a plain pass-through to show they fail for one.

// stubUnitCommand stands for a unit's command: it writes a Go-style and a
// Playwright-style junit file with one failing case each (and passing, skipped
// and self-closing ones that must not be named), a log, a finished mutation
// report and a flaky list, then runs for $STUB_SLEEP seconds and writes
// late.txt, which only a command left to finish would write.
const stubUnitCommand = `
out=$1
mkdir -p "$out/mutation-units/cli"
cat > "$out/junit-go.xml" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="board/internal/stubpkg" tests="3" failures="1">
    <testcase classname="board/internal/stubpkg" name="TestPasses" time="0.1"></testcase>
    <testcase classname="board/internal/stubpkg" name="TestStubFails" time="0.2">
      <failure message="Failed" type="">stub_test.go:9: boom</failure>
    </testcase>
    <testcase classname="board/internal/stubpkg" name="TestSkipped" time="0"><skipped message="s"/></testcase>
  </testsuite>
</testsuites>
XML
cat > "$out/junit-playwright.xml" <<'XML'
<testsuites><testsuite name="chromium" tests="2" failures="1">
<testcase name="Tasks › restores the &quot;done&quot; filter" classname="tasks.spec.ts" time="3"><failure message="x">timeout</failure></testcase>
<testcase name="board › loads" classname="board.spec.ts" time="1"/>
</testsuite></testsuites>
XML
echo "stub run log" > "$out/stub.log"
echo '{"package":"internal/cli","efficacy":91}' > "$out/mutation-units/cli/internal-cli.json"
echo "flaky one" > "$out/flaky-go.txt"
sleep "$STUB_SLEEP"
echo late > "$out/late.txt"
exit "${STUB_EXIT:-0}"
`

type unitRunResult struct {
	exit    int
	out     string // combined stdout and stderr of the script
	dir     string // the results directory
	elapsed time.Duration
}

// runUnitScript runs script as `unitrun.sh <name> <bound> <dir> -- sh -c <stub> sh <dir>`.
func runUnitScript(t *testing.T, script, name string, bound int, sleep int, stubExit int) unitRunResult {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("sh", script, name, fmt.Sprint(bound), dir, "--", "sh", "-c", stubUnitCommand, "sh", dir)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		fmt.Sprintf("STUB_SLEEP=%d", sleep),
		fmt.Sprintf("STUB_EXIT=%d", stubExit),
	}
	start := time.Now()
	b, err := cmd.CombinedOutput()
	r := unitRunResult{out: string(b), dir: dir, elapsed: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.exit = ee.ExitCode()
	default:
		t.Fatalf("run %s: %v", script, err)
	}
	return r
}

func unitFile(r unitRunResult, rel ...string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(append([]string{r.dir}, rel...)...))
	return string(b), err == nil
}

// cutUnitProblems judges one run of the stub cut by a 2 s bound while it would
// have run 60 s: the script must end it, keep every result written before the
// cut, and name the two failing cases in the console and in the summary file.
func cutUnitProblems(r unitRunResult) []string {
	var problems []string
	if r.exit != 124 {
		problems = append(problems, fmt.Sprintf("exit status %d, want 124 for a command cut by its bound", r.exit))
	}
	if r.elapsed > 40*time.Second {
		problems = append(problems, fmt.Sprintf("the command was not cut: the run took %s", r.elapsed.Round(time.Second)))
	}
	if _, ok := unitFile(r, "late.txt"); ok {
		problems = append(problems, "the command ran on past its bound (late.txt exists)")
	}
	for rel, want := range map[string]string{
		"junit-go.xml":                         "TestStubFails",
		"junit-playwright.xml":                 "restores the",
		"stub.log":                             "stub run log",
		"flaky-go.txt":                         "flaky one",
		"mutation-units/cli/internal-cli.json": "internal/cli",
	} {
		if got, ok := unitFile(r, rel); !ok || !strings.Contains(got, want) {
			problems = append(problems, "the kept results lost "+rel)
		}
	}
	kept, _ := unitFile(r, "unit-kept-e2e.txt")
	for _, rel := range []string{"junit-go.xml", "junit-playwright.xml", "stub.log", "flaky-go.txt", "internal-cli.json"} {
		if !strings.Contains(kept, rel) {
			problems = append(problems, "the kept list does not hold "+rel)
		}
	}
	summary, ok := unitFile(r, "unit-failure-e2e.txt")
	if !ok {
		problems = append(problems, "no unit-failure-e2e.txt summary was written")
	}
	for _, want := range []string{
		"unit E2E: cut at",
		"failing tests: 2",
		"junit-go.xml: board/internal/stubpkg :: TestStubFails",
		`junit-playwright.xml: tasks.spec.ts :: Tasks › restores the "done" filter`,
	} {
		if !strings.Contains(summary, want) {
			problems = append(problems, fmt.Sprintf("the summary file lacks %q", want))
		}
		if !strings.Contains(r.out, want) {
			problems = append(problems, fmt.Sprintf("the console output lacks %q", want))
		}
	}
	for _, not := range []string{"TestPasses", "TestSkipped", "board.spec.ts"} {
		if strings.Contains(summary, not) {
			problems = append(problems, "the summary names a test that did not fail: "+not)
		}
	}
	return problems
}

// plainRunScript is the script a lazy edit would leave: it runs the
// command and nothing else, no bound, no summary.
const plainRunScript = "#!/bin/sh\nshift 4\nexec \"$@\"\n"

func TestUnitRunKeepsACutUnitsResultsAndNamesItsFailingTests(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	script := filepath.Join(root, "ci", "unitrun.sh")
	for _, p := range cutUnitProblems(runUnitScript(t, script, "E2E", 2, 60, 0)) {
		t.Errorf("ci/unitrun.sh: %s", p)
	}

	// The check can fail: a plain pass-through of the same stub is reported.
	pass := filepath.Join(t.TempDir(), "unitrun.sh")
	if err := os.WriteFile(pass, []byte(plainRunScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(cutUnitProblems(runUnitScript(t, pass, "E2E", 2, 3, 0))) == 0 {
		t.Error("a plain pass-through of the command satisfies the cut-off checks")
	}
}

func TestUnitRunNamesTheFailingTestsOfACommandThatFailed(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	r := runUnitScript(t, filepath.Join(root, "ci", "unitrun.sh"), "Mutation streams+cli", 60, 0, 3)
	if r.exit != 3 {
		t.Errorf("exit status %d, want the command's own 3", r.exit)
	}
	summary, ok := unitFile(r, "unit-failure-mutation-streams-cli.txt")
	if !ok {
		t.Fatal("no summary file named after the unit's slug")
	}
	for _, want := range []string{"unit Mutation streams+cli: failed with exit status 3", "failing tests: 2", "board/internal/stubpkg :: TestStubFails"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "cut at") {
		t.Errorf("a command that failed on its own is reported as cut:\n%s", summary)
	}
	if _, ok := unitFile(r, "late.txt"); !ok {
		t.Error("the command did not run to its end")
	}
}

func TestUnitRunLeavesAPassingUnitAlone(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	r := runUnitScript(t, filepath.Join(root, "ci", "unitrun.sh"), "Suite", 60, 0, 0)
	if r.exit != 0 {
		t.Errorf("exit status %d, want 0", r.exit)
	}
	if _, ok := unitFile(r, "unit-failure-suite.txt"); ok {
		t.Error("a passing unit got a failure summary")
	}
	if strings.Contains(r.out, "failing tests") {
		t.Errorf("a passing unit printed a failure summary:\n%s", r.out)
	}
}

// arrayTakeRE: take(n) straight on split's String[] result.
var arrayTakeRE = regexp.MustCompile(`\.split\([^)]*\)\.take\(`)

// unitRunWiringProblems: the Suite and E2E units and the three full mutation
// units run through unitSh, which runs ci/unitrun.sh and prints its summary to
// the console and the build description; runUnit hands it the bound and reads
// an exit status of 124 as the unit's own timeout.
func unitRunWiringProblems(jf string) []string {
	var problems []string
	core, _ := jenkinsStage(jf, "Core")
	for name, want := range map[string]string{
		"Suite": "body: { unitSh(script: 'BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh') }",
		"E2E":   "body: { unitSh(script: 'BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh') }",
	} {
		decl, ok := coreUnitDecl(core, name)
		if !ok || !strings.Contains(decl, want) {
			problems = append(problems, name+" does not run through unitSh")
		}
	}
	full := functionBody(jf, "def runFullMutation(Map plan) {")
	if !strings.Contains(full, "body: { unitSh(script: fullTune + ") {
		problems = append(problems, "the full mutation units do not run through unitSh")
	}
	if strings.Contains(full, "body: { sh(returnStatus: true, script: fullTune") {
		problems = append(problems, "a full mutation unit still runs its command bare")
	}
	us := functionBody(jf, "int unitSh(Map m) {")
	for _, want := range []string{
		"ci/unitrun.sh \"$BOARD_UNIT_NAME\" \"$BOARD_UNIT_BOUND_SECONDS\" \"$BOARD_CI_OUT\" -- ",
		"unit-failure-",
		"echo text",
		"currentBuild.description = ",
		"if (lines.size() < 5) {",
		"' | ' + lines.join('; ')",
	} {
		if !strings.Contains(us, want) {
			problems = append(problems, "unitSh lacks "+want)
		}
	}
	// take() on the String[] that split returns is not on the sandbox
	// whitelist: Jenkins throws RejectedAccessException in place of the
	// failed unit's summary.
	if arrayTakeRE.MatchString(jf) {
		problems = append(problems, "the Jenkinsfile calls take() on a split array, which the sandbox rejects")
	}
	run := functionBody(jf, "def runUnit(Map u) {")
	for _, want := range []string{"BOARD_UNIT_NAME=${u.name}", "BOARD_UNIT_BOUND_SECONDS=", "rc == 124 ? 'fail:timeout'"} {
		if !strings.Contains(run, want) {
			problems = append(problems, "runUnit lacks "+want)
		}
	}
	// The bound stays inside the attempt's own limit.
	if !strings.Contains(run, "u.minutes * 60 - ") {
		problems = append(problems, "the command's bound is not taken from the unit's limit")
	}
	return problems
}

func TestJenkinsfileRunsTheHeavyUnitsThroughUnitRun(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, unitRunWiringProblems, map[string]string{
		"Suite bare":           replaceOnce(t, jf, "unitSh(script: 'BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh')", "sh(returnStatus: true, script: 'BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh')"),
		"E2E bare":             replaceOnce(t, jf, "unitSh(script: 'BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh')", "sh(returnStatus: true, script: 'BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh')"),
		"mutation bare":        replaceOnce(t, jf, "body: { unitSh(script: fullTune + ", "body: { sh(returnStatus: true, script: fullTune + "),
		"no console text":      replaceOnce(t, jf, "    echo text\n", ""),
		"no description":       replaceOnce(t, jf, "currentBuild.description = (currentBuild.description ?: '') + ' | '", "def ignored = ' | '"),
		"array take":           replaceOnce(t, jf, "' | ' + lines.join('; ')", "' | ' + text.split('\\n').take(5).join('; ')"),
		"array take elsewhere": replaceOnce(t, jf, "mutationPlanText.split('\\n').each { line ->", "mutationPlanText.split('\\n').take(50).each { line ->"),
		"no 124 timeout":       replaceOnce(t, jf, "rc == 124 ? 'fail:timeout' : ", ""),
		"no bound":             replaceOnce(t, jf, "BOARD_UNIT_BOUND_SECONDS=${left > 60 ? left : 60}", "BOARD_UNIT_X=1"),
	})
}
