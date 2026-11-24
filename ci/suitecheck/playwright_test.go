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

// playwrightServerProblem returns "" when the Playwright config starts no
// server of its own (no webServer, globalSetup or globalTeardown entry, no
// serve.sh) and the worker fixture that does is in place, otherwise what is
// wrong. fixtures is the text of e2e/fixtures.ts and workerServer that of
// e2e/worker-server.ts: every worker starts the real binary on a free port of
// its own, so one worker's specs never fill another's store.
func playwrightServerProblem(cfg, fixtures, workerServer string) string {
	for _, banned := range []string{"webServer", "globalSetup", "globalTeardown", "sh serve.sh"} {
		for _, line := range strings.Split(stripTSComments(cfg), "\n") {
			if strings.Contains(line, banned) {
				return "the config brings back a global server: it has " + banned
			}
		}
	}
	if !strings.Contains(fixtures, "scope: 'worker'") || !strings.Contains(fixtures, "startServer(") {
		return "e2e/fixtures.ts has no worker-scoped fixture starting a server per worker"
	}
	for _, want := range []string{"listen(0,", "serve.sh", "admin"} {
		if !strings.Contains(workerServer, want) {
			return "e2e/worker-server.ts: missing " + want
		}
	}
	return ""
}

// stripTSComments drops the // comments of a TypeScript file, so that prose
// about a removed server is not mistaken for a server.
func stripTSComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if at := strings.Index(l, "//"); at >= 0 && !strings.Contains(l[:at], "'") && !strings.Contains(l[:at], "`") {
			lines[i] = l[:at]
		}
	}
	return strings.Join(lines, "\n")
}

func TestPlaywrightServerProblemFlagsAGlobalServer(t *testing.T) {
	const cfg = "export default defineConfig({\n  retries: 0,\n  // no webServer here\n  projects: [],\n});\n"
	const fixtures = "export const test = base.extend({ workerServer: [async () => { await startServer(dir); }, { scope: 'worker', auto: true }] });"
	const workerServer = "probe.listen(0, '127.0.0.1'); spawn('sh', [join(dir, 'serve.sh')]); execFileSync(vh, ['admin', 'keys', 'create']);"
	if got := playwrightServerProblem(cfg, fixtures, workerServer); got != "" {
		t.Fatalf("a correct config was flagged: %s", got)
	}
	for name, tc := range map[string]struct{ cfg, fixtures, workerServer string }{
		"web server back":    {strings.Replace(cfg, "projects", "webServer: { command: 'x' },\n  projects", 1), fixtures, workerServer},
		"serve.sh back":      {strings.Replace(cfg, "projects", "x: 'sh serve.sh',\n  projects", 1), fixtures, workerServer},
		"global setup back":  {strings.Replace(cfg, "projects", "globalSetup: './g.ts',\n  projects", 1), fixtures, workerServer},
		"global teardown":    {strings.Replace(cfg, "projects", "globalTeardown: './g.ts',\n  projects", 1), fixtures, workerServer},
		"worker scope gone":  {cfg, strings.Replace(fixtures, "scope: 'worker'", "scope: 'test'", 1), workerServer},
		"worker server gone": {cfg, strings.Replace(fixtures, "startServer(", "start(", 1), workerServer},
		"no free port":       {cfg, fixtures, strings.Replace(workerServer, "listen(0,", "listen(8080,", 1)},
		"no key minted":      {cfg, fixtures, strings.Replace(workerServer, "'admin'", "'x'", 1)},
	} {
		if playwrightServerProblem(tc.cfg, tc.fixtures, tc.workerServer) == "" {
			t.Errorf("%s: not flagged", name)
		}
	}
}

// TestPlaywrightConfigNeverRetries is the R11/task 010 probe: the Playwright
// config in e2e/ sets retries: 0 and carries the reporters CI reads (reporters_test.go pins the list); the real
// binary (not a mock) is started per worker on a free port by the worker
// fixture, and the config starts no server of its own (task 002, R4).
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
	for _, want := range []string{`['list']`, `['allure-playwright'`, `['junit'`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("e2e/playwright.config.ts: missing %q", want)
		}
	}
	fixtures, err := os.ReadFile(filepath.Join(root, "e2e", "fixtures.ts"))
	if err != nil {
		t.Fatalf("read e2e/fixtures.ts: %v", err)
	}
	workerServer, err := os.ReadFile(filepath.Join(root, "e2e", "worker-server.ts"))
	if err != nil {
		t.Fatalf("read e2e/worker-server.ts: %v", err)
	}
	if problem := playwrightServerProblem(cfg, string(fixtures), string(workerServer)); problem != "" {
		t.Errorf("e2e: %s", problem)
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
