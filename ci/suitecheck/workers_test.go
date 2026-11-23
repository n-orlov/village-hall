// workers_test.go pins where the Playwright worker count lives (task 010, R6):
// defined once, as E2E_WORKERS in the Jenkinsfile, at most 3; exported as
// BOARD_E2E_WORKERS, documented in and checked by ci/suite.sh, which hands it
// to Playwright as E2E_WORKERS; and read, never spelled out, by
// e2e/playwright.config.ts.
package suitecheck

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const maxE2EWorkers = 3

var (
	workersField   = regexp.MustCompile(`(?m)^@Field\s+(?:final\s+)?(?:int|Integer)\s+E2E_WORKERS\s*=\s*(\S+?)\s*$`)
	workersExport  = regexp.MustCompile(`(?m)^\s*env\.BOARD_E2E_WORKERS\s*=\s*(.*)$`)
	workersSetting = regexp.MustCompile(`(?m)^\s*workers\s*:\s*([^,\n]*)`)
)

// e2eWorkersProblem returns "" when the worker count is defined once, within
// the limit, and only read elsewhere, otherwise what is wrong. jenkinsfile,
// suite and config are the texts of the Jenkinsfile, ci/suite.sh and
// e2e/playwright.config.ts.
func e2eWorkersProblem(jenkinsfile, suite, config string) string {
	fields := workersField.FindAllStringSubmatch(jenkinsfile, -1)
	if len(fields) != 1 {
		return "the Jenkinsfile must define E2E_WORKERS exactly once, as an int @Field (found " + strconv.Itoa(len(fields)) + ")"
	}
	n, err := strconv.Atoi(fields[0][1])
	if err != nil {
		return "E2E_WORKERS is '" + fields[0][1] + "', not a number literal"
	}
	if n < 1 || n > maxE2EWorkers {
		return "E2E_WORKERS is " + strconv.Itoa(n) + ": the Playwright worker count is 1 to " + strconv.Itoa(maxE2EWorkers) + " on int21h-ws"
	}
	exports := workersExport.FindAllStringSubmatch(jenkinsfile, -1)
	if len(exports) != 1 || !strings.Contains(exports[0][1], "E2E_WORKERS") {
		return "the Jenkinsfile must export BOARD_E2E_WORKERS once, from E2E_WORKERS"
	}
	var jfCode []string
	for _, l := range strings.Split(jenkinsfile, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			jfCode = append(jfCode, l)
		}
	}
	code := strings.Join(jfCode, "\n")
	if strings.Count(code, "BOARD_E2E_WORKERS") != strings.Count(code, "env.BOARD_E2E_WORKERS") {
		return "the Jenkinsfile sets BOARD_E2E_WORKERS somewhere other than its one export"
	}
	var header []string
	for _, l := range strings.Split(suite, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			break
		}
		header = append(header, l)
	}
	if !strings.Contains(strings.Join(header, "\n"), "BOARD_E2E_WORKERS") {
		return "the ci/suite.sh header does not document BOARD_E2E_WORKERS"
	}
	if !strings.Contains(suite, `E2E_WORKERS="$e2e_workers"`) || !strings.Contains(suite, "BOARD_E2E_WORKERS:-") {
		return "ci/suite.sh does not pass BOARD_E2E_WORKERS to Playwright as E2E_WORKERS"
	}
	if strings.Contains(suite, "--workers") || strings.Contains(jenkinsfile, "--workers") {
		return "a --workers flag sets a second worker count"
	}
	cfgCode := stripTSComments(config)
	settings := workersSetting.FindAllStringSubmatch(cfgCode, -1)
	if len(settings) != 1 {
		return "e2e/playwright.config.ts must have exactly one workers setting (found " + strconv.Itoa(len(settings)) + ")"
	}
	if v := strings.TrimSpace(settings[0][1]); v != "e2eWorkers" {
		return "e2e/playwright.config.ts hard-codes its own worker count (workers: " + v + "): it must read E2E_WORKERS"
	}
	if !strings.Contains(cfgCode, "process.env.E2E_WORKERS") {
		return "e2e/playwright.config.ts does not read E2E_WORKERS"
	}
	if !strings.Contains(cfgCode, "e2eWorkers > "+strconv.Itoa(maxE2EWorkers)) {
		return "e2e/playwright.config.ts does not refuse a count over " + strconv.Itoa(maxE2EWorkers)
	}
	return ""
}

func readWorkersInputs(t *testing.T) (jenkinsfile, suite, config string) {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	read := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(raw)
	}
	return read("Jenkinsfile"), read("ci/suite.sh"), read("e2e/playwright.config.ts")
}

// TestE2EWorkerCountHasOneSourceAndAtMostThree pins the real files.
func TestE2EWorkerCountHasOneSourceAndAtMostThree(t *testing.T) {
	jf, suite, cfg := readWorkersInputs(t)
	if problem := e2eWorkersProblem(jf, suite, cfg); problem != "" {
		t.Error(problem)
	}
}

// TestE2EWorkerCountProblemFlagsDoctoredFiles shows the pin fails on a count of
// 4 and on a config that spells out its own number.
func TestE2EWorkerCountProblemFlagsDoctoredFiles(t *testing.T) {
	jf, suite, cfg := readWorkersInputs(t)
	field := workersField.FindString(jf)
	if field == "" {
		t.Fatal("no E2E_WORKERS field in the Jenkinsfile to doctor")
	}
	swap := func(text, old, repl string) string {
		if !strings.Contains(text, old) {
			t.Fatalf("doctoring needs %q in the text", old)
		}
		return strings.Replace(text, old, repl, 1)
	}
	field4 := regexp.MustCompile(`=\s*\S+\s*$`).ReplaceAllString(field, "= 4")
	cases := map[string]struct{ jf, suite, cfg string }{
		"a count of 4":                  {swap(jf, field, field4), suite, cfg},
		"a count of 0":                  {swap(jf, field, regexp.MustCompile(`=\s*\S+\s*$`).ReplaceAllString(field, "= 0")), suite, cfg},
		"a second definition":           {jf + "\n" + field + "\n", suite, cfg},
		"no export":                     {swap(jf, "env.BOARD_E2E_WORKERS", "env.BOARD_OTHER"), suite, cfg},
		"a literal export":              {swap(jf, `env.BOARD_E2E_WORKERS = "${E2E_WORKERS}"`, `env.BOARD_E2E_WORKERS = '2'`), suite, cfg},
		"an undocumented header":        {jf, strings.ReplaceAll(suite, "# BOARD_E2E_WORKERS (task 010", "# E2E workers (task 010"), cfg},
		"a --workers flag":              {jf, swap(suite, "npx playwright test)", "npx playwright test --workers=2)"), cfg},
		"a hard-coded config of 2":      {jf, suite, swap(cfg, "workers: e2eWorkers,", "workers: 2,")},
		"a hard-coded config of 4":      {jf, suite, swap(cfg, "workers: e2eWorkers,", "workers: 4,")},
		"a conditional config":          {jf, suite, swap(cfg, "workers: e2eWorkers,", "workers: process.env.CI ? 3 : e2eWorkers,")},
		"a second workers":              {jf, suite, swap(cfg, "workers: e2eWorkers,", "workers: e2eWorkers,\n  workers: 1,")},
		"a config that ignores the env": {jf, suite, strings.ReplaceAll(cfg, "process.env.E2E_WORKERS", "process.env.OTHER")},
	}
	for name, c := range cases {
		if problem := e2eWorkersProblem(c.jf, c.suite, c.cfg); problem == "" {
			t.Errorf("%s: the pin accepts it", name)
		}
	}
}

// TestSuiteHandsTheWorkerCountToPlaywrightAndRefusesMoreThanThree runs the
// real ci/suite.sh against stub tools: BOARD_E2E_WORKERS=2 reaches npx as
// E2E_WORKERS=2, nothing is handed over when it is unset, and 4 is refused.
func TestSuiteHandsTheWorkerCountToPlaywrightAndRefusesMoreThanThree(t *testing.T) {
	for _, tc := range []struct{ set, want string }{{"", ""}, {"1", "1"}, {"2", "2"}, {"3", "3"}} {
		var extra []string
		if tc.set != "" {
			extra = []string{"BOARD_E2E_WORKERS=" + tc.set}
		}
		run := runSuiteMode(t, "only", extra...)
		if run.err != nil {
			t.Fatalf("BOARD_E2E_WORKERS=%q: ci/suite.sh failed: %v", tc.set, run.err)
		}
		log := run.stubLog()
		if !strings.Contains(log, "E2E_WORKERS=["+tc.want+"]") {
			t.Errorf("BOARD_E2E_WORKERS=%q: npx saw the wrong count, log:\n%s", tc.set, log)
		}
	}
	for _, bad := range []string{"4", "0", "two", "-1"} {
		run := runSuiteMode(t, "only", "BOARD_E2E_WORKERS="+bad)
		if run.err == nil {
			t.Errorf("BOARD_E2E_WORKERS=%s: ci/suite.sh accepted it", bad)
			continue
		}
		var ee *exec.ExitError
		if !errors.As(run.err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(run.out), "BOARD_E2E_WORKERS must be a whole number from 1 to 3") {
			t.Errorf("BOARD_E2E_WORKERS=%s: want exit 2 and the refusal, got %v:\n%s", bad, run.err, run.out)
		}
		if strings.Contains(run.stubLog(), "playwright test") {
			t.Errorf("BOARD_E2E_WORKERS=%s: Playwright ran anyway", bad)
		}
	}
}
