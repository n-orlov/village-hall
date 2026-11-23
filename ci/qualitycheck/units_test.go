package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The units gate (task 006, R3): Quality names a unit that failed or was cut by its
// bound, and the tests it had failed, from the partial results the unit left, instead
// of reporting a missing stash or a missing coverage profile.

// partialPlaywrightJUnit is what e2e/partial-junit-reporter.ts leaves for an E2E unit
// cut mid-run: the tests finished so far, one of them failed.
const partialPlaywrightJUnit = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="playwright (partial: tests finished so far)" tests="3" failures="1">
<testsuite name="partial" tests="3" failures="1">
<testcase name="board › loads" classname="board.spec.ts" time="1.000"></testcase>
<testcase name="Tasks › restores the &quot;done&quot; filter" classname="tasks.spec.ts" time="30.000"><failure message="Timeout" type="FAILURE">timedOut</failure></testcase>
<testcase name="board › skipped one" classname="board.spec.ts" time="0.000"><skipped/></testcase>
</testsuite>
</testsuites>
`

// cutE2EUnit is a stand-in for the E2E unit's command: it writes the partial Playwright
// JUnit file and a log, then runs far past its bound.
const cutE2EUnit = `
printf '%s' "$PARTIAL" > "$1/junit-playwright-partial.xml"
echo "playwright log so far" > "$1/playwright.log"
sleep 60
`

// TestQualityNamesACutOffE2EUnitAndItsFailingTest drives the real scripts end to end:
// ci/unitrun.sh cuts a stub E2E unit by a 2 s bound, then ci/quality.sh reads the
// results directory it left. Quality's report must name the unit, say it was cut, and
// name the failing test read from the partial junit-playwright file, and Quality must
// fail on it.
func TestQualityNamesACutOffE2EUnitAndItsFailingTest(t *testing.T) {
	root := repoRoot(t)
	outdir := t.TempDir()

	unit := exec.Command("sh", "ci/unitrun.sh", "E2E", "2", outdir, "--", "sh", "-c", cutE2EUnit, "sh", outdir)
	unit.Dir = root
	unit.Env = append(goChildEnv(), "PARTIAL="+partialPlaywrightJUnit)
	out, err := unit.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 124 {
		t.Fatalf("ci/unitrun.sh: want exit 124 (cut by its bound), got %v\n%s", err, out)
	}

	config := filepath.Join(t.TempDir(), "quality.json")
	if err := os.WriteFile(config, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	quality := exec.Command("sh", "ci/quality.sh", outdir, config)
	quality.Dir = root
	quality.Env = goChildEnv()
	report, err := quality.CombinedOutput()
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("ci/quality.sh: want exit 1 (a cut unit fails Quality), got %v\n%s", err, report)
	}
	for _, want := range []string{
		"=== units gate ===",
		"unit E2E: cut at",
		`tasks.spec.ts :: Tasks › restores the "done" filter`,
		"gate verdict: failed",
	} {
		if !strings.Contains(string(report), want) {
			t.Errorf("Quality's report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(string(report), "board.spec.ts :: board › loads") {
		t.Errorf("Quality's report names a passing test as failed:\n%s", report)
	}
}

// runUnits runs the qualitycheck core over files laid out in a results directory.
func runUnits(t *testing.T, files map[string]string) (report string, code int) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(t.TempDir(), "quality.json")
	if err := os.WriteFile(config, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := unitResultsDir
	unitResultsDir = dir
	t.Cleanup(func() { unitResultsDir = saved })
	report, code, err := run(config, "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return report, code
}

// TestUnitsGateReadsPartialJUnitOfAUnitKilledWithoutASummary: an attempt that hit the
// Jenkins limit leaves unit-cut-e2e.txt and whatever JUnit file was being written, which
// may itself be cut off mid-case. The gate names the unit and the cases before the cut.
func TestUnitsGateReadsPartialJUnitOfAUnitKilledWithoutASummary(t *testing.T) {
	before, _, found := strings.Cut(partialPlaywrightJUnit, "<testcase name=\"board › skipped")
	if !found {
		t.Fatal("the partial JUnit fixture lost its skipped case")
	}
	cutOff := before +
		`<testcase name="Tasks › second failure" classname="tasks.spec.ts" time="1.0"><fail`
	report, code := runUnits(t, map[string]string{
		"unit-cut-e2e.txt":                "unit E2E: attempt 1 cut at 120 min (the attempt's own limit)",
		"junit-playwright-partial.xml":    cutOff,
		"junit-vitest.xml":                `<testsuites><testsuite><testcase name="ok" classname="a.test.ts"/></testsuite></testsuites>`,
		"unit-failure-unrelated-note.txt": "",
	})
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, report)
	}
	for _, want := range []string{
		"=== units gate ===",
		"unit E2E: attempt 1 cut at 120 min",
		"junit-playwright-partial.xml (file cut off",
		`tasks.spec.ts :: Tasks › restores the "done" filter`,
		"gate verdict: failed",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "second failure") {
		t.Errorf("report names the case that was cut off mid-element:\n%s", report)
	}
	if strings.Contains(report, "junit-vitest.xml") {
		t.Errorf("report names a Vitest file with no failure:\n%s", report)
	}
}

// TestUnitsGateIsAbsentWhenNoUnitFailed: passing JUnit files and no summary add no section,
// so a clean run's report is unchanged.
func TestUnitsGateIsAbsentWhenNoUnitFailed(t *testing.T) {
	report, code := runUnits(t, map[string]string{
		"junit-playwright.xml": `<testsuites><testsuite><testcase name="ok" classname="a.spec.ts"/></testsuite></testsuites>`,
	})
	if code != 0 || strings.Contains(report, "units gate") {
		t.Errorf("exit %d, report:\n%s\nwant exit 0 and no units gate", code, report)
	}
}

// TestUnitsGateNamesAFailedSuiteUnitFromItsSummary: a unit that failed (not cut) has
// the summary ci/unitrun.sh wrote; the gate prints it once, and does not repeat the
// unit's JUnit failures under a second heading.
func TestUnitsGateNamesAFailedSuiteUnitFromItsSummary(t *testing.T) {
	summary := "unit Suite: failed with exit status 1 after 31 min 2 s\nfailing tests: 1\n  junit-go.xml: board/internal/store :: TestStoreFails"
	report, code := runUnits(t, map[string]string{
		"unit-failure-suite.txt": summary,
		"junit-go.xml":           `<testsuites><testsuite><testcase name="TestStoreFails" classname="board/internal/store"><failure/></testcase></testsuite></testsuites>`,
	})
	if code != 1 || !strings.Contains(report, summary) {
		t.Errorf("exit %d, want 1 with the summary in:\n%s", code, report)
	}
	if n := strings.Count(report, "TestStoreFails"); n != 1 {
		t.Errorf("TestStoreFails named %d times, want once:\n%s", n, report)
	}
}

// The units gate's helpers, each by the cases it tells apart.

func TestUnitOfSlugNamesTheUnitsThatWriteJUnitFilesAndNoOther(t *testing.T) {
	for slug, want := range map[string]string{"e2e": "E2E", "suite": "Suite", "image": "", "": ""} {
		if got := unitOfSlug(slug); got != want {
			t.Errorf("unitOfSlug(%q) = %q, want %q", slug, got, want)
		}
	}
}

func TestCasesSectionNamesTheFailingTestsOrSaysNoneWasNamed(t *testing.T) {
	if got := casesSection("suite", []string{"  junit-go.xml:", "    a :: b"}); got != "failing tests read from its JUnit files:\n  junit-go.xml:\n    a :: b\n" {
		t.Errorf("a unit with failing cases: %q", got)
	}
	if got := casesSection("suite", nil); got != "failing tests: none named (no failing case in its JUnit files)\n" {
		t.Errorf("a JUnit-writing unit with none: %q", got)
	}
	if got := casesSection("image", nil); got != "" {
		t.Errorf("a unit that writes no JUnit file adds nothing, got %q", got)
	}
}

func TestCaseLabelNamesAFailingCaseByWhatItHas(t *testing.T) {
	for _, c := range []struct{ class, name, want string }{
		{"a.spec.ts", "loads", "a.spec.ts :: loads"},
		{"", "loads", "loads"},
		{"a.spec.ts", "", "(unnamed test case)"},
		{"", "", "(unnamed test case)"},
	} {
		if got := caseLabel(c.class, c.name); got != c.want {
			t.Errorf("caseLabel(%q, %q) = %q, want %q", c.class, c.name, got, c.want)
		}
	}
}
