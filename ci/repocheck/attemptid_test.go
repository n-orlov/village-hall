package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The cleanup id a unit attempt labels its containers with and removes them
// by (ci/attempt-id.sh, called by the Jenkinsfile). Each check runs the real
// script, and a doctored copy that must be caught.

func attemptID(t *testing.T, script string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// attemptIDProblems: the same inputs give the same id, the id changes with
// each of the four inputs (the job above all: two jobs with one build number,
// unit and attempt must not share a cleanup id), and the id has the shape the
// Jenkinsfile's label filter expects.
func attemptIDProblems(t *testing.T, script string) []string {
	t.Helper()
	var problems []string
	base := []string{"board/ci/PR-1", "3", "E2E", "1"}
	want := "jenkins-board-ci-pr-1-3-e2e-1"
	got, err := attemptID(t, script, base...)
	if err != nil || got != want {
		problems = append(problems, "board/ci/PR-1 #3 E2E attempt 1 gave "+got+", want "+want)
	}
	if again, _ := attemptID(t, script, base...); again != got {
		problems = append(problems, "identical inputs gave "+again+" and "+got)
	}
	for name, args := range map[string][]string{
		"another job":     {"board/ci/PR-2", "3", "E2E", "1"},
		"another branch":  {"board/ci/main", "3", "E2E", "1"},
		"another folder":  {"other/ci/PR-1", "3", "E2E", "1"},
		"another build":   {"board/ci/PR-1", "4", "E2E", "1"},
		"another unit":    {"board/ci/PR-1", "3", "Suite", "1"},
		"another attempt": {"board/ci/PR-1", "3", "E2E", "2"},
	} {
		other, _ := attemptID(t, script, args...)
		if other == got {
			problems = append(problems, name+" gave the same id "+got)
		}
	}
	// Two jobs that differ only in a character the slug flattens still differ.
	a, _ := attemptID(t, script, "board/ci/PR-1", "3", "E2E", "1")
	b, _ := attemptID(t, script, "board/ci/PR-2", "3", "E2E", "1")
	if a == b {
		problems = append(problems, "PR-1 and PR-2 with one build, unit and attempt share id "+a)
	}
	return problems
}

func TestAttemptIDIsUniquePerJobAndStablePerInput(t *testing.T) {
	for _, p := range attemptIDProblems(t, repoScript(t, "ci/attempt-id.sh")) {
		t.Error(p)
	}
}

func TestAttemptIDRefusesAMissingPart(t *testing.T) {
	script := repoScript(t, "ci/attempt-id.sh")
	for name, args := range map[string][]string{
		"no job":       {"", "3", "E2E", "1"},
		"no build":     {"board/ci/main", "", "E2E", "1"},
		"no unit":      {"board/ci/main", "3", "", "1"},
		"no attempt":   {"board/ci/main", "3", "E2E", ""},
		"three inputs": {"board/ci/main", "3", "E2E"},
	} {
		if out, err := attemptID(t, script, args...); err == nil {
			t.Errorf("%s: gave id %q, want a refusal", name, out)
		}
	}
}

func TestAttemptIDCheckFailsForAHelperThatDropsAPart(t *testing.T) {
	src, err := os.ReadFile(repoScript(t, "ci/attempt-id.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const line = `printf 'jenkins-%s-%s-%s-%s\n' "$(slug "$1")" "$2" "$(slug "$3")" "$4"`
	replacements := map[string]string{
		"drops the job name":  `printf 'jenkins-%s-%s-%s\n' "$2" "$(slug "$3")" "$4"`,
		"drops the build":     `printf 'jenkins-%s-%s-%s\n' "$(slug "$1")" "$(slug "$3")" "$4"`,
		"drops the unit":      `printf 'jenkins-%s-%s-%s\n' "$(slug "$1")" "$2" "$4"`,
		"drops the attempt":   `printf 'jenkins-%s-%s-%s\n' "$(slug "$1")" "$2" "$(slug "$3")"`,
		"adds a clock":        `printf 'jenkins-%s-%s-%s-%s-%s\n' "$(slug "$1")" "$2" "$(slug "$3")" "$4" "$(date +%s%N)"`,
		"keeps only the leaf": `printf 'jenkins-%s-%s-%s-%s\n' "$(slug "${1##*/}")" "$2" "$(slug "$3")" "$4"`,
	}
	doctored := map[string]string{}
	for name, repl := range replacements {
		doctored[name] = strings.Replace(string(src), line, repl, 1)
	}
	for name, text := range doctored {
		if text == string(src) {
			t.Fatalf("%s: the doctoring changed nothing", name)
		}
		path := filepath.Join(t.TempDir(), "attempt-id.sh")
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		if len(attemptIDProblems(t, path)) == 0 {
			t.Errorf("a helper that %s was not caught", name)
		}
	}
}

// The Jenkinsfile gets its id from the helper, not from a string it builds.
func TestJenkinsfileTakesTheAttemptIDFromTheHelper(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	if !strings.Contains(jf, "ci/attempt-id.sh") {
		t.Error("the Jenkinsfile never calls ci/attempt-id.sh")
	}
	if strings.Contains(jf, `String runId = "jenkins-`) {
		t.Error("the Jenkinsfile still builds the cleanup id itself")
	}
}
