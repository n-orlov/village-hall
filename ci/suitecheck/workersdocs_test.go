// workersdocs_test.go pins that the prose describing the Playwright worker
// count (docs/ci.md, SPEC.md, the Jenkinsfile and ci/suite.sh headers) agrees
// with E2E_WORKERS, the one place the count is set. A sentence that still says
// the suite runs on one worker while E2E_WORKERS is 2 is a spec that contradicts
// the live code (R7).
package suitecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// workersStated matches "E2E_WORKERS ... (N; at most 3" / "(N, at most 3" / "E2E_WORKERS below, N, at most 3".
	workersStated = regexp.MustCompile(`E2E_WORKERS[^\n]{0,80}?[(,]?\s*(\d+)[;,]\s+at\s+most\s+3`)
	// workersOneClaim matches a present-tense claim that Playwright runs on one worker.
	workersOneClaim = regexp.MustCompile(`(?i)\b(?:Playwright\s+changed:\s*|Playwright\s+(?:runs?\s+)?(?:on|with|in)\s+|E2E(?:\s+unit)?\s+(?:runs?\s+)?(?:on|with)\s+)(?:one|a\s+single|1)\s+(?:Playwright\s+)?worker\b`)
)

// workersDocsProblem returns "" when every stated count equals want and no
// text claims one worker, otherwise what is wrong. texts maps a name to a text.
func workersDocsProblem(want int, texts map[string]string) string {
	for name, text := range texts {
		flat := strings.Join(strings.Fields(text), " ")
		flat = strings.ReplaceAll(flat, "// ", "")
		flat = strings.ReplaceAll(flat, "# ", "")
		for _, m := range workersStated.FindAllStringSubmatch(flat, -1) {
			if m[1] != strconv.Itoa(want) {
				return name + " states " + m[1] + " Playwright workers, E2E_WORKERS is " + strconv.Itoa(want)
			}
		}
		if want != 1 && workersOneClaim.MatchString(flat) {
			return name + " claims Playwright runs on one worker, E2E_WORKERS is " + strconv.Itoa(want)
		}
	}
	return ""
}

func TestWorkerCountProseAgreesWithE2EWorkers(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	texts := map[string]string{}
	for _, rel := range []string{"docs/ci.md", "SPEC.md", "Jenkinsfile", "ci/suite.sh"} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		texts[rel] = string(raw)
	}
	m := workersField.FindStringSubmatch(texts["Jenkinsfile"])
	if m == nil {
		t.Fatal("Jenkinsfile defines no E2E_WORKERS")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("E2E_WORKERS: %v", err)
	}
	if p := workersDocsProblem(n, texts); p != "" {
		t.Error(p)
	}
	// The statements are really found (the check is not vacuous).
	for _, rel := range []string{"docs/ci.md", "SPEC.md", "Jenkinsfile", "ci/suite.sh"} {
		flat := strings.Join(strings.Fields(texts[rel]), " ")
		if !workersStated.MatchString(strings.ReplaceAll(strings.ReplaceAll(flat, "// ", ""), "# ", "")) {
			t.Errorf("%s states no E2E_WORKERS count the check can read", rel)
		}
	}
}

func TestWorkerCountProseProblemFlagsDoctoredText(t *testing.T) {
	cases := map[string]struct {
		text string
		bad  bool
	}{
		"the finding's own sentence":   {"Nothing else about Playwright changed: one\n  worker, no `fullyParallel`", true},
		"a wrong count in parentheses": {"`E2E_WORKERS` in the `Jenkinsfile` (3; at most 3 on int21h-ws)", true},
		"a wrong count in a comment":   {"// Playwright workers (R6): E2E_WORKERS below, 1, at most 3 (ci/suitecheck", true},
		"runs on a single worker":      {"The E2E unit runs on a single worker.", true},
		"Playwright with one worker":   {"It runs Playwright with one worker per project.", true},
		"the right count":              {"`E2E_WORKERS` in the `Jenkinsfile` (2; at most 3 on int21h-ws)", false},
		"a store's worker":             {"what one worker's specs seed is never seen by another's", false},
		"a run with none":              {"a run with none (a local one) uses one worker", false},
		"history":                      {"at the same one worker, the load", false},
	}
	for name, c := range cases {
		got := workersDocsProblem(2, map[string]string{"doc": c.text})
		if c.bad && got == "" {
			t.Errorf("%s: not flagged", name)
		}
		if !c.bad && got != "" {
			t.Errorf("%s: flagged: %s", name, got)
		}
	}
}
