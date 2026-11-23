package suitecheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The published summaries name every infrastructure attempt and each queue wait
// past the 30-minute cap (R8, task 016): ci/attempts-summary.sh reads the
// Jenkinsfile's timing table, and ci/jenkins-report.sh puts its "Unit attempts"
// section in ci-summary/summary.md and index.html and its count line in
// ci-summary/attempts.txt (the build page's "Distributed" line).

const timingHeader = "| unit | result | attempt | node | queue (min) | run (min) | outcome |\n" +
	"|------|--------|---------|------|-------------|-----------|---------|\n"

// threeQueueWaits: E2E's first attempt was cancelled still queued, its second
// got a node past the cap, and Suite's first waited past it too; then both ran.
const threeQueueWaits = timingHeader +
	"| E2E | pass | 1 | - | 30.2 | - | infra:queue-wait |\n" +
	"| E2E | pass | 2 | int21h-ws | 30.4 | 0.3 | infra:queue-wait |\n" +
	"| E2E | pass | 3 | int21h-ws | 0.5 | 21.0 | pass |\n" +
	"| Suite | pass | 1 | int21h-ws | 31.0 | 0.2 | infra:queue-wait |\n" +
	"| Suite | pass | 2 | int21h-ws | 0.1 | 12.0 | pass |\n"

// report is what one ci/jenkins-report.sh run published.
type report struct {
	markdown, html, line string
}

// doctoring is one edit of ci/jenkins-report.sh: its first old replaced by new.
type doctoring struct{ old, new string }

// publish runs ci/jenkins-report.sh (its text with doctor applied, when doctor
// names an edit) over a ci-results holding timing.md (none when timing is
// empty), with no suite results. The script runs as `sh -c <text> <its path>`,
// so $0, and the ci/attempts-summary.sh it calls beside it, are the real ones.
func publish(t *testing.T, timing string, doctor doctoring) report {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	out := filepath.Join(work, "ci-results")
	if err := os.MkdirAll(filepath.Join(out, "distributed"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "ci", "jenkins-report.sh")
	src, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if doctor.old != "" {
		if !strings.Contains(text, doctor.old) {
			t.Fatalf("ci/jenkins-report.sh no longer contains %q, so this doctoring checks nothing", doctor.old)
		}
		text = strings.Replace(text, doctor.old, doctor.new, 1)
	}
	if timing != "" {
		if err := os.WriteFile(filepath.Join(out, "distributed", "timing.md"), []byte(timing), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", text, script, out)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "BUILD_URL=", "ALLURE_CACHE_DIR="+filepath.Join(work, "cache"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ci/jenkins-report.sh: %v\n%s", err, combined)
	}
	markdown, err := os.ReadFile(filepath.Join(work, "ci-summary", "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(work, "ci-summary", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	line, err := os.ReadFile(filepath.Join(work, "ci-summary", "attempts.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return report{markdown: string(markdown), html: string(html), line: strings.TrimSpace(string(line))}
}

// queueWaitProblems: the three queue waits of threeQueueWaits are named, one row
// each, in every published summary.
func queueWaitProblems(r report) []string {
	var problems []string
	const line = "3 infrastructure attempts (3 queue waits over 30 min)"
	if r.line != line {
		problems = append(problems, "attempts.txt is "+r.line+", want "+line)
	}
	rows := []string{
		"| E2E | 1 | - | 30.2 | infra:queue-wait | queue wait over 30 min |",
		"| E2E | 2 | int21h-ws | 30.4 | infra:queue-wait | queue wait over 30 min |",
		"| Suite | 1 | int21h-ws | 31.0 | infra:queue-wait | queue wait over 30 min |",
	}
	if !strings.Contains(r.markdown, "### Unit attempts") || !strings.Contains(r.markdown, line) {
		problems = append(problems, "summary.md has no Unit attempts section naming the queue waits")
	}
	for _, row := range rows {
		if !strings.Contains(r.markdown, row) {
			problems = append(problems, "summary.md lacks the row "+row)
		}
	}
	if strings.Contains(r.markdown, "| pass |") {
		problems = append(problems, "summary.md lists a passing attempt as infrastructure")
	}
	if !strings.Contains(r.html, "<h3>Unit attempts</h3>") || !strings.Contains(r.html, line) {
		problems = append(problems, "index.html has no Unit attempts section naming the queue waits")
	}
	if n := strings.Count(r.html, "<td>queue wait over 30 min</td>"); n != 3 {
		problems = append(problems, fmt.Sprintf("index.html names %d queue waits, want 3", n))
	}
	return problems
}

func TestPublishedSummariesNameEveryQueueWait(t *testing.T) {
	for _, p := range queueWaitProblems(publish(t, threeQueueWaits, doctoring{})) {
		t.Error(p)
	}
}

func TestQueueWaitCheckFailsForAReportThatDropsTheAttempts(t *testing.T) {
	for name, doctor := range map[string]doctoring{
		"no Unit attempts section": {`sh "$here/attempts-summary.sh" "$timing" >> "$summary"`, `true`},
		"no attempts.txt line":     {`sh "$here/attempts-summary.sh" --line "$timing" > ci-summary/attempts.txt`, `: > ci-summary/attempts.txt`},
		"another timing file":      {`timing="$outdir/distributed/timing.md"`, `timing="$outdir/timing.md"`},
	} {
		if len(queueWaitProblems(publish(t, threeQueueWaits, doctor))) == 0 {
			t.Errorf("a report with %s was not caught", name)
		}
	}
}

func TestPublishedSummariesNameOtherInfrastructureAttemptsWithoutAQueueWait(t *testing.T) {
	r := publish(t, timingHeader+
		"| Image | pass | 1 | int21h-ws | 0.1 | 0.5 | infra:agent-lost |\n"+
		"| Image | pass | 2 | int21h-ws | 0.1 | 4.0 | pass |\n"+
		"| Suite | fail | 1 | int21h-ws | 29.9 | 12.0 | fail:1 |\n", doctoring{})
	if r.line != "1 infrastructure attempt" {
		t.Errorf("attempts.txt is %q, want %q", r.line, "1 infrastructure attempt")
	}
	if !strings.Contains(r.markdown, "| Image | 1 | int21h-ws | 0.1 | infra:agent-lost | infrastructure |") {
		t.Errorf("summary.md does not list the lost agent:\n%s", r.markdown)
	}
	// A wait within the cap (29.9 min) and a test failure are no queue wait.
	if strings.Contains(r.markdown+r.html+r.line, "queue wait") || strings.Contains(r.markdown, "| Suite |") {
		t.Errorf("a wait within the cap or a failed command is named as infrastructure:\n%s", r.markdown)
	}
}

func TestPublishedSummariesSayNoInfrastructureAttemptForACleanBuild(t *testing.T) {
	r := publish(t, timingHeader+"| E2E | pass | 1 | int21h-ws | 0.2 | 20.0 | pass |\n", doctoring{})
	if r.line != "no infrastructure attempt" || !strings.Contains(r.markdown, "### Unit attempts\n\nno infrastructure attempt") {
		t.Errorf("a clean build: attempts.txt %q, summary.md:\n%s", r.line, r.markdown)
	}
}

func TestPublishedSummariesWithoutATimingTableAddNothing(t *testing.T) {
	r := publish(t, "", doctoring{})
	if r.line != "" || strings.Contains(r.markdown, "Unit attempts") {
		t.Errorf("no timing table: attempts.txt %q, summary.md:\n%s", r.line, r.markdown)
	}
}
