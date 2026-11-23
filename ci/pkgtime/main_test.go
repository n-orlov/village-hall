package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// main275Server is internal/server's test time in main build 275's archived
// Suite junit (ci-results/junit-go.xml), the baseline the committed budget is
// held to: at most 80% of it.
const main275Server = 35.855

func committedBudget(t *testing.T) float64 {
	t.Helper()
	b, err := os.ReadFile("budgets.json")
	if err != nil {
		t.Fatal(err)
	}
	var limits map[string]float64
	if err := json.Unmarshal(b, &limits); err != nil {
		t.Fatal(err)
	}
	v, ok := limits["internal/server"]
	if !ok {
		t.Fatal("budgets.json has no budget for internal/server")
	}
	return v
}

// fixture writes a Suite junit whose internal/server testsuite took seconds.
func fixture(t *testing.T, name string, seconds float64) string {
	t.Helper()
	doc := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="2" failures="0" errors="0" time="%f">
	<testsuite tests="1" failures="0" time="900.000000" name="github.com/n-orlov/village-hall/internal/store" timestamp="2026-11-23T05:27:52Z">
		<testcase classname="github.com/n-orlov/village-hall/internal/store" name="TestSlow" time="900.000000"></testcase>
	</testsuite>
	<testsuite tests="1" failures="0" time="%f" name="github.com/n-orlov/village-hall/internal/server" timestamp="2026-11-23T05:27:52Z">
		<testcase classname="github.com/n-orlov/village-hall/internal/server" name="TestOne" time="%f"></testcase>
	</testsuite>
</testsuites>
`, seconds+900, seconds, seconds)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func check(junit string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run([]string{"-junit", junit, "-budgets", "budgets.json"}, &out, &errb)
	return code, out.String(), errb.String()
}

func TestTheCommittedBudgetIsAtMost80PercentOfMain275(t *testing.T) {
	if got, limit := committedBudget(t), 0.8*main275Server; got > limit {
		t.Errorf("internal/server's budget is %g s, want at most %g s (80%% of %g s in main 275)", got, limit, main275Server)
	}
}

func TestAPackageAtItsBudgetPasses(t *testing.T) {
	code, out, errs := check(fixture(t, "at.xml", committedBudget(t)))
	if code != 0 || errs != "" || !strings.HasPrefix(out, "ok   internal/server:") {
		t.Fatalf("code %d, out %q, err %q; want 0 and an ok line for internal/server", code, out, errs)
	}
}

func TestAPackageOneSecondOverItsBudgetFails(t *testing.T) {
	code, out, _ := check(fixture(t, "over.xml", committedBudget(t)+1))
	if code != 1 || !strings.HasPrefix(out, "FAIL internal/server:") || !strings.Contains(out, "over its budget") {
		t.Fatalf("code %d, out %q; want 1 and a FAIL line for internal/server", code, out)
	}
}

func TestAPackageWithNoTestsuiteFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none.xml")
	if err := os.WriteFile(p, []byte(`<testsuites><testsuite name="github.com/n-orlov/village-hall/internal/store" time="1"/></testsuites>`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := check(p)
	if code != 1 || !strings.Contains(out, "no testsuite") {
		t.Fatalf("code %d, out %q; want 1 and a missing-testsuite line", code, out)
	}
}

func TestASuiteWhoseNameOnlyEndsLikeThePackageIsNotIt(t *testing.T) {
	all := []suite{{Name: "github.com/x/notinternal/server", Time: 1}, {Name: "github.com/x/internal/server", Time: 2}}
	if got, ok := lookup(all, "internal/server"); !ok || got != 2 {
		t.Errorf("lookup = %g, %v, want the time 2 of github.com/x/internal/server", got, ok)
	}
	if _, ok := lookup(all[:1], "internal/server"); ok {
		t.Error("notinternal/server matched internal/server")
	}
}

func TestUsageAndInputErrorsExitTwo(t *testing.T) {
	var out, errb bytes.Buffer
	for name, args := range map[string][]string{
		"no flags":       nil,
		"no budgets":     {"-junit", "x.xml"},
		"missing junit":  {"-junit", filepath.Join(t.TempDir(), "gone.xml"), "-budgets", "budgets.json"},
		"missing budget": {"-junit", fixture(t, "a.xml", 1), "-budgets", filepath.Join(t.TempDir(), "gone.json")},
	} {
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%s: code %d, want 2", name, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "zero.json")
	if err := os.WriteFile(bad, []byte(`{"internal/server": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-junit", fixture(t, "b.xml", 1), "-budgets", bad}, &out, &errb); code != 2 {
		t.Errorf("a budget of zero: code %d, want 2", code)
	}
}
