package repocheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The queue cap (R8): a unit that waits past 30 minutes for a node fails that
// attempt as infrastructure ("queue wait"), keeping its infrastructure retries.
// The decision is ci/queue-cap.sh's (plain sh, run here against every case);
// the Jenkinsfile asks it on the node and watches the wait before one.

// queueCapProblems asks the script about waits around the cap and reports every
// wrong answer; the exit status is the verdict (0 within the cap, 1 past it).
func queueCapProblems(t *testing.T, script string) []string {
	t.Helper()
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"0"}, 0},
		{[]string{"1799"}, 0},
		{[]string{"1800"}, 0},
		{[]string{"1801"}, 1},
		{[]string{"2700"}, 1},
		{[]string{"8100"}, 1},
		{[]string{"600", "5"}, 1},
		{[]string{"300", "5"}, 0},
		{[]string{"soon"}, 2},
		{[]string{"-1"}, 2},
	}
	var problems []string
	for _, c := range cases {
		if got := shellVerdict(t, script, c.args...); got != c.want {
			problems = append(problems, fmt.Sprintf("queue-cap.sh %v: exit %d, want %d", c.args, got, c.want))
		}
	}
	return problems
}

func TestQueueCapClassesAWaitPastTheCapAsQueueWaitAndAWaitWithinItNot(t *testing.T) {
	script := repoScript(t, "ci/queue-cap.sh")
	for _, p := range queueCapProblems(t, script) {
		t.Error(p)
	}
	// The word the build log shows is "queue-wait" past the cap, "within-cap" inside it.
	for args, want := range map[string]string{"1801": "queue-wait", "1800": "within-cap"} {
		out := shellOutput(t, script, args)
		if strings.TrimSpace(out) != want {
			t.Errorf("queue-cap.sh %s printed %q, want %q", args, out, want)
		}
	}
}

func TestQueueCapCheckFailsForAHelperWithoutTheCap(t *testing.T) {
	src, err := os.ReadFile(repoScript(t, "ci/queue-cap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	doctored := map[string]string{
		"has no cap check":       strings.Replace(string(src), `if [ "$waited" -gt $((cap * 60)) ]; then`, `if false; then`, 1),
		"counts the cap as past": strings.Replace(string(src), `-gt $((cap * 60))`, `-ge $((cap * 60))`, 1),
		"caps at 45 minutes":     strings.Replace(string(src), "cap=${2:-30}", "cap=${2:-45}", 1),
		"ignores the cap given":  strings.Replace(string(src), "cap=${2:-30}", "cap=30", 1),
		"never past it":          strings.Replace(string(src), "    echo queue-wait\n    exit 1", "    echo queue-wait\n    exit 0", 1),
	}
	for name, text := range doctored {
		if text == string(src) {
			t.Fatalf("%s: the doctoring changed nothing", name)
		}
		path := filepath.Join(t.TempDir(), "queue-cap.sh")
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
		if len(queueCapProblems(t, path)) == 0 {
			t.Errorf("a helper that %s was not caught", name)
		}
	}
}

var queueCapConstant = regexp.MustCompile(`(?m)^@Field final int QUEUE_CAP_MINUTES = (\d+)$`)

// queueCapJenkinsfileProblems: runUnit caps the wait at 30 minutes through the
// helper and the watcher, names it infra:queue-wait, and keeps the retries.
func queueCapJenkinsfileProblems(jf string) []string {
	var problems []string
	m := queueCapConstant.FindStringSubmatch(jf)
	if m == nil || m[1] != "30" {
		problems = append(problems, "QUEUE_CAP_MINUTES is not 30")
	}
	run := functionBody(jf, "def runUnit(Map u) {")
	if strings.Contains(run, "45 + u.minutes") || strings.Contains(jf, "45 + u.minutes") {
		problems = append(problems, "the queue timeout is the unit's bound plus 45 minutes again")
	}
	if !strings.Contains(run, "queueCapped(a) {") {
		problems = append(problems, "runUnit does not run an attempt under the queue cap")
	}
	if !strings.Contains(run, `ci/queue-cap.sh ${waited} ${QUEUE_CAP_MINUTES}`) {
		problems = append(problems, "runUnit does not ask ci/queue-cap.sh about the wait")
	}
	if !strings.Contains(run, "a.outcome = 'infra:queue-wait'") {
		problems = append(problems, "a wait past the cap is not an infra:queue-wait outcome")
	}
	if !strings.Contains(run, "(a.capped && !isSuperseded()) ? 'infra:queue-wait'") {
		problems = append(problems, "a cancelled wait is not classed as infra:queue-wait")
	}
	capped := functionBody(jf, "def queueCapped(Map a, Closure body) {")
	if !strings.Contains(capped, "QUEUE_CAP_MINUTES * 60000L") || !strings.Contains(capped, "a.capped = true") {
		problems = append(problems, "queueCapped does not end a wait at the cap")
	}
	if !strings.Contains(capped, "failFast: true") {
		problems = append(problems, "queueCapped does not cancel the queued node when the cap passes")
	}
	if !strings.Contains(functionBody(jf, "String attemptSummary(List attempts) {"), "queue wait over ${QUEUE_CAP_MINUTES} min") {
		problems = append(problems, "the build summary does not name a queue wait")
	}
	if !strings.Contains(run, "infra++") || !strings.Contains(run, "if (infra >= 3 || attempts.size() >= maxAttempts) {") {
		problems = append(problems, "a queue wait no longer keeps its infrastructure retries")
	}
	return problems
}

func TestJenkinsfileCapsAUnitsQueueWaitAtThirtyMinutes(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, queueCapJenkinsfileProblems, map[string]string{
		"the old 45 + bound timeout":  replaceOnce(t, jf, "      queueCapped(a) {", "      timeout(time: 45 + u.minutes, unit: 'MINUTES') {"),
		"a cap of 45":                 replaceOnce(t, jf, "QUEUE_CAP_MINUTES = 30", "QUEUE_CAP_MINUTES = 45"),
		"no helper":                   replaceOnce(t, jf, "ci/queue-cap.sh ${waited} ${QUEUE_CAP_MINUTES}", "true ${waited} ${QUEUE_CAP_MINUTES}"),
		"a wait that is not infra":    replaceOnce(t, jf, "a.outcome = 'infra:queue-wait'\n                return", "a.outcome = 'fail:queue-wait'\n                return"),
		"a cancelled wait not named":  replaceOnce(t, jf, "(a.capped && !isSuperseded()) ? 'infra:queue-wait'", "(a.capped && !isSuperseded()) ? 'fail:queue-wait'"),
		"the watcher never fires":     replaceOnce(t, jf, "a.capped = true", "a.capped = false"),
		"a queue wait without cancel": replaceOnce(t, jf, "failFast: true", "failFast: false"),
		"an unnamed summary":          replaceOnce(t, jf, "queue wait over ${QUEUE_CAP_MINUTES} min", "waited"),
	})
}

func TestQueueCapIsDocumentedWhereTheJenkinsfileSetsIt(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	header, _, _ := strings.Cut(jf, "\npipeline {")
	for name, text := range map[string]string{
		"the Jenkinsfile header": header,
		"docs/ci.md":             readRepoFile(t, "docs", "ci.md"),
		"SPEC.md":                readRepoFile(t, "SPEC.md"),
	} {
		if !strings.Contains(text, "ci/queue-cap.sh") && name != "SPEC.md" {
			t.Errorf("%s does not name ci/queue-cap.sh", name)
		}
		if !strings.Contains(text, "30-minute queue cap") && !strings.Contains(text, "30-minute cap") {
			t.Errorf("%s does not describe the 30-minute queue cap", name)
		}
		if strings.Contains(text, "capped at 45 minutes beyond") {
			t.Errorf("%s still describes the old 45-minute queue cap", name)
		}
	}
}

func shellOutput(t *testing.T, script string, args ...string) string {
	t.Helper()
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, _ := cmd.Output()
	return string(out)
}
