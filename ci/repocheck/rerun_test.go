package repocheck

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The rerun decision (ci/rerun-decision.sh) and the flaky-list gate
// (ci/flakygate.sh): a unit is rerun only after an infrastructure failure,
// and a non-empty flaky or leg-divergence list fails the build. Each check is
// a pure function of a script's verdicts, run against the real script and
// against a doctored copy that must be caught.

func shellVerdict(t *testing.T, script string, args ...string) int {
	t.Helper()
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		t.Fatalf("run %s %v: %v\n%s", script, args, err, out)
		return -1
	}
}

func repoScript(t *testing.T, rel string) string {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	return filepath.Join(root, rel)
}

// rerunProblems asks the script about exactly the four outcomes a unit's
// attempt can end in that matter, and reports every wrong answer.
func rerunProblems(t *testing.T, script string) []string {
	t.Helper()
	cases := []struct {
		outcome string
		rerun   bool
	}{
		{"fail:1", false},
		{"fail:timeout", false},
		{"infra:no-node", true},
		{"infra:agent-lost", true},
	}
	var problems []string
	for _, c := range cases {
		got := shellVerdict(t, script, c.outcome) == 0
		if got != c.rerun {
			problems = append(problems, fmt.Sprintf("%s: rerun = %v, want %v", c.outcome, got, c.rerun))
		}
	}
	return problems
}

func TestRerunDecisionReRunsOnlyInfrastructureFailures(t *testing.T) {
	script := repoScript(t, "ci/rerun-decision.sh")
	for _, p := range rerunProblems(t, script) {
		t.Error(p)
	}
	// Outcomes the script is not asked about above still never rerun a test failure.
	for _, outcome := range []string{"fail:2", "fail:137", "pass", "something-else"} {
		if shellVerdict(t, script, outcome) == 0 {
			t.Errorf("%s: rerun, want none", outcome)
		}
	}
	for _, outcome := range []string{"infra:docker", "infra:ci-image", "infra:process-lost"} {
		if shellVerdict(t, script, outcome) != 0 {
			t.Errorf("%s: no rerun, want one", outcome)
		}
	}
}

func TestRerunDecisionCheckFailsForAHelperThatRerunsAFailure(t *testing.T) {
	src, err := os.ReadFile(repoScript(t, "ci/rerun-decision.sh"))
	if err != nil {
		t.Fatal(err)
	}
	doctored := map[string]string{
		"reruns fail:*":        strings.Replace(string(src), "infra:*)", "infra:* | fail:*)", 1),
		"never reruns":         strings.Replace(string(src), "infra:*)\n    echo rerun\n    exit 0", "infra:*)\n    echo no-rerun\n    exit 1", 1),
		"reruns only fail:1":   strings.Replace(string(src), "infra:*)", "infra:* | fail:1)", 1),
		"reruns every timeout": strings.Replace(string(src), "infra:*)", "infra:* | fail:timeout)", 1),
	}
	for name, text := range doctored {
		if text == string(src) {
			t.Fatalf("%s: the doctoring changed nothing", name)
		}
		path := filepath.Join(t.TempDir(), "rerun-decision.sh")
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		if len(rerunProblems(t, path)) == 0 {
			t.Errorf("a helper that %s was not caught", name)
		}
	}
}

// flakyGateCase is one results directory: file name -> content.
type flakyGateCase struct {
	name  string
	files map[string]string
	fails bool
}

var flakyGateCases = []flakyGateCase{
	{"no list files", nil, false},
	{"empty flaky-e2e.txt", map[string]string{"flaky-e2e.txt": ""}, false},
	{"flaky-e2e.txt with one line", map[string]string{"flaky-e2e.txt": "tasks.spec.ts restores the filter\n"}, true},
	{"leg-divergence.txt with one line", map[string]string{"leg-divergence.txt": "tasks.feature:12 red on postgres, green on sqlite\n"}, true},
}

func flakyGateProblems(t *testing.T, script string) []string {
	t.Helper()
	var problems []string
	for _, c := range flakyGateCases {
		dir := t.TempDir()
		for name, content := range c.files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := shellVerdict(t, script, dir) != 0; got != c.fails {
			problems = append(problems, fmt.Sprintf("%s: fails = %v, want %v", c.name, got, c.fails))
		}
	}
	return problems
}

func TestFlakyGateFailsOnANonEmptyFlakyOrDivergenceList(t *testing.T) {
	for _, p := range flakyGateProblems(t, repoScript(t, "ci/flakygate.sh")) {
		t.Error(p)
	}
}

func TestFlakyGateCheckFailsForAGateThatIgnoresAList(t *testing.T) {
	src, err := os.ReadFile(repoScript(t, "ci/flakygate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	doctored := map[string]string{
		"ignores flaky lists":    strings.Replace(string(src), `"$dir"/flaky-*.txt `, "", 1),
		"ignores leg divergence": strings.Replace(string(src), ` "$dir"/leg-divergence.txt`, "", 1),
		"never fails":            strings.Replace(string(src), "bad=1", "bad=0", 1),
		"fails on an empty file": strings.Replace(string(src), `if grep -q '[^[:space:]]' "$list"; then`, `if true; then`, 1),
		"flaky list only if >1":  strings.Replace(string(src), `grep -q '[^[:space:]]' "$list"`, `[ "$(grep -c . "$list")" -gt 1 ]`, 1),
	}
	for name, text := range doctored {
		if text == string(src) {
			t.Fatalf("%s: the doctoring changed nothing", name)
		}
		path := filepath.Join(t.TempDir(), "flakygate.sh")
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		if len(flakyGateProblems(t, path)) == 0 {
			t.Errorf("a gate that %s was not caught", name)
		}
	}
}

// retryPolicyProblems: runUnit reruns nothing but an infrastructure failure
// (the decision is ci/rerun-decision.sh's, and nothing marks a build UNSTABLE
// for a flake), and the report fails the build through ci/flakygate.sh.
func retryPolicyProblems(jf string) []string {
	var problems []string
	run := functionBody(jf, "def runUnit(Map u) {")
	if !strings.Contains(run, `script: "ci/rerun-decision.sh '${a.outcome}'"`) {
		problems = append(problems, "runUnit does not ask ci/rerun-decision.sh")
	}
	if !strings.Contains(run, "if (!again) {") || !strings.Contains(run, "boolean again = a.rerun != null ? a.rerun : a.outcome.startsWith('infra')") {
		problems = append(problems, "runUnit does not fail a unit whose outcome is not rerunnable")
	}
	for _, banned := range []string{"firstFail", "rerun = true", "unstable(", "'flaky'"} {
		if strings.Contains(run, banned) {
			problems = append(problems, "runUnit still has a rerun-and-mark-flaky path: "+banned)
		}
	}
	if !strings.Contains(run, "infra++") || !strings.Contains(run, "if (infra >= 3 || attempts.size() >= maxAttempts) {") {
		problems = append(problems, "infrastructure failures no longer keep their retries")
	}
	if strings.Contains(jf, "results[u.name] = 'flaky'") || strings.Contains(jf, "== 'flaky'") {
		problems = append(problems, "the Jenkinsfile still records a flaky unit")
	}
	post, _ := cutFrom(jf, "  post {")
	gate := strings.Index(post, "sh 'ci/flakygate.sh")
	if gate < 0 {
		problems = append(problems, "the report never runs ci/flakygate.sh")
	} else {
		before := post[:gate]
		if i := strings.LastIndex(before, "catchError("); i < 0 || !strings.Contains(before[i:], "buildResult: 'FAILURE'") {
			problems = append(problems, "ci/flakygate.sh does not fail the build")
		}
		if !strings.Contains(before, "unstash 'res-'") {
			problems = append(problems, "ci/flakygate.sh runs before the units' results are unstashed")
		}
	}
	head, _, _ := strings.Cut(jf, "@Library(")
	if strings.Contains(head, "one rerun") || strings.Contains(head, "(\"flaky\")") {
		problems = append(problems, "the Jenkinsfile header still describes a rerun of a failed command")
	}
	if !strings.Contains(head, "ci/rerun-decision.sh") || !strings.Contains(head, "ci/flakygate.sh") {
		problems = append(problems, "the Jenkinsfile header does not name ci/rerun-decision.sh and ci/flakygate.sh")
	}
	return problems
}

func TestJenkinsfileRerunsOnlyInfrastructureAndFailsOnAFlakyList(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, retryPolicyProblems, map[string]string{
		"no helper call":         replaceOnce(t, jf, "ci/rerun-decision.sh '${a.outcome}'", "true '${a.outcome}'"),
		"no fail branch":         replaceOnce(t, jf, "if (!again) {", "if (false) {"),
		"a failure rerun":        replaceOnce(t, jf, "if (!again) {", "boolean rerun = false\n    if (!again) {\n      rerun = true\n"),
		"a flaky unit":           replaceOnce(t, jf, "    if (a.outcome == 'pass') {\n      results[u.name] = 'pass'", "    if (a.outcome == 'pass') {\n      unstable('flaky')\n      results[u.name] = 'pass'"),
		"infra without retry":    replaceOnce(t, jf, "if (infra >= 3 || attempts.size() >= maxAttempts) {", "if (true) {"),
		"no flaky gate":          replaceOnce(t, jf, "sh 'ci/flakygate.sh \"$BOARD_CI_OUT\"'", "echo skipped"),
		"a gate that only warns": replaceOnce(t, jf, "catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n                sh 'ci/flakygate.sh", "catchError(buildResult: 'UNSTABLE', stageResult: 'FAILURE') {\n                sh 'ci/flakygate.sh"),
		"the old header":         replaceOnce(t, jf, "unit fails and is not rerun", "unit's command fails: one rerun"),
	})
}

// cutFrom returns the text of s from the first sep on, and whether sep is there.
func cutFrom(s, sep string) (string, bool) {
	_, after, found := strings.Cut(s, sep)
	if !found {
		return "", false
	}
	return sep + after, true
}
