package suitecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var retriesSetting = regexp.MustCompile(`(?m)^\s*retries\s*:\s*([^,\n]*)`)

// playwrightRetriesProblem returns "" when every `retries` setting in a
// Playwright config is the literal 0 (and there is at least one), otherwise
// what is wrong. Playwright never retries: a flaky spec must fail, and
// ci/suite.sh does not repeat a failed run either.
func playwrightRetriesProblem(cfg string) string {
	found := retriesSetting.FindAllStringSubmatch(cfg, -1)
	if len(found) == 0 {
		return "no retries setting: it must be spelled out as retries: 0"
	}
	for _, m := range found {
		if v := strings.TrimSpace(m[1]); v != "0" {
			return "retries is " + v + ", not 0"
		}
	}
	return ""
}

// TestPlaywrightConfigNeverRetries is the R11/task 010 probe: the Playwright
// config in e2e/ sets retries: 0, carries the five reporters, and starts the
// real binary (not a mock) on a free port.
func TestPlaywrightConfigNeverRetries(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "e2e", "playwright.config.ts"))
	if err != nil {
		t.Fatalf("read e2e/playwright.config.ts: %v", err)
	}
	cfg := string(raw)
	if problem := playwrightRetriesProblem(cfg); problem != "" {
		t.Errorf("e2e/playwright.config.ts: %s", problem)
	}
	for _, want := range []string{`['html'`, `['list']`, `['allure-playwright'`, `['json'`, `['junit'`, `webServer`, `sh serve.sh`, `listen(0,`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("e2e/playwright.config.ts: missing %q", want)
		}
	}
}

var earlySpecsList = regexp.MustCompile(`(?s)const earlySpecs = \[([^\]]*)\]`)

// playwrightEarlyProblem returns "" when the config runs the volume-sensitive
// specs (the navigation chrome, every Timeline spec, the wide layout and the
// dialogs spec, which Tabs past a list that only grows) in a
// project listed before the project that runs every other spec, and keeps
// those specs out of the second one, otherwise what is wrong. The shared e2e
// store only grows, an unfiltered Timeline takes seconds to draw at the end
// of the run, and these specs load it many times: run last they failed the
// first run on the Jenkins agent and the repeat took the Suite past its
// 45-minute bound.
func playwrightEarlyProblem(cfg string) string {
	list := earlySpecsList.FindStringSubmatch(cfg)
	if list == nil {
		return "no `const earlySpecs = [...]` list"
	}
	for _, want := range []string{"dialogs.spec.ts", "shell.spec.ts", "timeline*.spec.ts", "wide.spec.ts"} {
		if !strings.Contains(list[1], "'"+want+"'") {
			return "earlySpecs does not name " + want
		}
	}
	early := strings.Index(cfg, "name: 'chromium-early'")
	main := strings.Index(cfg, "name: 'chromium',")
	if early < 0 || main < 0 || early > main {
		return "the chromium-early project must be listed before the chromium project"
	}
	earlyLine := cfg[early:main]
	if !strings.Contains(earlyLine, "testMatch: earlySpecs") {
		return "the chromium-early project must match earlySpecs"
	}
	if !strings.Contains(cfg[main:], "testIgnore: earlySpecs") {
		return "the chromium project must ignore earlySpecs"
	}
	return ""
}

func TestPlaywrightRunsTheVolumeSensitiveSpecsFirst(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "e2e", "playwright.config.ts"))
	if err != nil {
		t.Fatalf("read e2e/playwright.config.ts: %v", err)
	}
	if problem := playwrightEarlyProblem(string(raw)); problem != "" {
		t.Errorf("e2e/playwright.config.ts: %s", problem)
	}
}

func TestPlaywrightEarlyProblemFlagsALostOrdering(t *testing.T) {
	const good = "const earlySpecs = ['dialogs.spec.ts', 'shell.spec.ts', 'timeline*.spec.ts', 'wide.spec.ts'];\n" +
		"projects: [\n{ name: 'chromium-early', testMatch: earlySpecs },\n{ name: 'chromium', testMatch: '*.spec.ts', testIgnore: earlySpecs },\n]"
	if got := playwrightEarlyProblem(good); got != "" {
		t.Fatalf("a correct config was flagged: %s", got)
	}
	for name, cfg := range map[string]string{
		"no list":           strings.Replace(good, "const earlySpecs", "const other", 1),
		"timeline dropped":  strings.Replace(good, " 'timeline*.spec.ts',", "", 1),
		"shell dropped":     strings.Replace(good, "'shell.spec.ts', ", "", 1),
		"dialogs dropped":   strings.Replace(good, "'dialogs.spec.ts', ", "", 1),
		"order swapped":     "const earlySpecs = ['dialogs.spec.ts', 'shell.spec.ts', 'timeline*.spec.ts', 'wide.spec.ts'];\n{ name: 'chromium', testIgnore: earlySpecs },\n{ name: 'chromium-early', testMatch: earlySpecs },",
		"early not matched": strings.Replace(good, "testMatch: earlySpecs", "testMatch: '*.spec.ts'", 1),
		"main not ignoring": strings.Replace(good, "testIgnore: earlySpecs", "testMatch: '*.spec.ts'", 1),
	} {
		if playwrightEarlyProblem(cfg) == "" {
			t.Errorf("%s: not flagged", name)
		}
	}
}

func TestPlaywrightRetriesProblemFlagsAnyRetry(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg    string
		wantOK bool
	}{
		"zero":             {"  retries: 0, // none\n", true},
		"one":              {"  retries: 1,\n", false},
		"ci conditional":   {"  retries: process.env.CI ? 2 : 0,\n", false},
		"absent":           {"  workers: 1,\n", false},
		"second one wrong": {"  retries: 0,\n  retries: 2,\n", false},
	} {
		if got := playwrightRetriesProblem(tc.cfg) == ""; got != tc.wantOK {
			t.Errorf("%s: accepted = %v, want %v", name, got, tc.wantOK)
		}
	}
}

// TestSuiteRunsPlaywrightOnce pins ci/suite.sh's Playwright step: one run, its
// JUnit file, and no repeat (noretry_test.go runs it).
func TestSuiteRunsPlaywrightOnce(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(suiteScriptPath(root))
	if err != nil {
		t.Fatalf("read ci/suite.sh: %v", err)
	}
	suite := stripCommentLines(string(raw))
	for _, want := range []string{
		`playwright_junit="$outdir/junit-playwright.xml"`,
		`E2E_OUT="$outdir/playwright" npx playwright test`,
		`Playwright failed (see`,
		`Playwright wrote no JUnit file`,
	} {
		if !strings.Contains(suite, want) {
			t.Errorf("ci/suite.sh: missing %q", want)
		}
	}
	build, pw := strings.Index(suite, "npm run build"), strings.Index(suite, "npx playwright test")
	if build < 0 || pw < build {
		t.Errorf("ci/suite.sh: Playwright must run after the web build (build at %d, Playwright at %d)", build, pw)
	}
}

// specClaimsScenarioPrecision reports whether a document line describes the
// Playwright retry as selecting only failed cases. ci/suite.sh repeats the
// whole `npx playwright test` run, so such a claim is false (R16).
func specClaimsScenarioPrecision(line string) bool {
	l := strings.ToLower(line)
	if !strings.Contains(l, "playwright") {
		return false
	}
	return strings.Contains(l, "scenario-precise") || strings.Contains(l, "only the failed") && !strings.Contains(l, "not only the failed")
}

func TestSpecClaimsScenarioPrecisionFlagsPreciseWording(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		want bool
	}{
		"deck wording":    {"Playwright (retry-once, scenario-precise like deck's Godog rerun)", true},
		"only failed":     {"Playwright reruns only the failed specs", true},
		"whole run":       {"Playwright: a failed run is repeated whole", false},
		"not only failed": {"Playwright repeats once (every spec runs again, not only the failed ones)", false},
		"godog elsewhere": {"Godog reruns are scenario-precise", false},
	} {
		if got := specClaimsScenarioPrecision(tc.line); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

// TestPlaywrightRetryIsDescribedAsAWholeRun: SPEC.md and docs/ci.md must not
// describe a scenario-precise Playwright retry, and the suite script must not
// pass a failed-test selection to a rerun (it has no rerun).
func TestPlaywrightRetryIsDescribedAsAWholeRun(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	for _, doc := range []string{"SPEC.md", filepath.Join("docs", "ci.md")} {
		raw, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if specClaimsScenarioPrecision(line) {
				t.Errorf("%s:%d claims a scenario-precise Playwright retry the suite does not do", doc, i+1)
			}
		}
	}
	raw, err := os.ReadFile(suiteScriptPath(root))
	if err != nil {
		t.Fatalf("read ci/suite.sh: %v", err)
	}
	for _, flag := range []string{"--last-failed", "--grep"} {
		if strings.Contains(stripCommentLines(string(raw)), flag) {
			t.Errorf("ci/suite.sh passes %s: the repeat is no longer a whole run, update SPEC and docs/ci.md", flag)
		}
	}
}

// TestSuiteOverlapsPlaywrightWithTheGoPassesAndWaitsBeforeMerging pins the
// overlap that keeps the sequential suite inside the Suite stage's budget:
// Playwright starts in the background before the Go passes, and the script waits
// for it, replays its messages and lets its failure fail the suite before the
// step that merges the JUnit files.
func TestSuiteOverlapsPlaywrightWithTheGoPassesAndWaitsBeforeMerging(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(suiteScriptPath(root))
	if err != nil {
		t.Fatalf("read ci/suite.sh: %v", err)
	}
	suite := stripCommentLines(string(raw))
	at := func(s string) int { return strings.Index(suite, s) }
	start := at(`playwright_step 2> "$playwright_messages" &`)
	unit := at(`go run "$gotestsum_pkg"`)
	wait := at(`wait "$playwright_pid"`)
	merge := at(`merged_out`)
	if start < 0 || unit < 0 || wait < 0 || merge < 0 {
		t.Fatalf("ci/suite.sh: Playwright start %d, Go pass %d, wait %d, merge %d: each must be present", start, unit, wait, merge)
	}
	if start >= unit || unit >= wait {
		t.Errorf("ci/suite.sh: Playwright must start (%d) before the Go passes (%d) and be waited for after them (%d)", start, unit, wait)
	}
	if lastFeatures := strings.LastIndex(suite, "run_test_features"); wait < lastFeatures {
		t.Errorf("ci/suite.sh: the wait (%d) must follow every features/ pass (%d)", wait, lastFeatures)
	}
	if !strings.Contains(suite, `cat "$playwright_messages" >&2`) {
		t.Errorf("ci/suite.sh: the Playwright messages must be replayed to stderr")
	}
	after := suite[wait:]
	failure, mergeAfter := strings.Index(after, "overall_status=1"), strings.Index(after, "merged_out")
	if failure < 0 || (mergeAfter >= 0 && failure > mergeAfter) {
		t.Errorf("ci/suite.sh: a failed Playwright step must set overall_status at the wait, before the merge")
	}
}
