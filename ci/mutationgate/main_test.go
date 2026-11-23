package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// gremlinsReport builds a gremlins JSON report with the given mutants by status.
func gremlinsReport(killed, lived, timedOut, notCovered int) string {
	var ms []string
	add := func(n int, status string) {
		for i := 0; i < n; i++ {
			ms = append(ms, fmt.Sprintf(`{"type":"CONDITIONALS_NEGATION","status":%q,"line":%d,"column":1}`, status, i+1))
		}
	}
	add(killed, "KILLED")
	add(lived, "LIVED")
	add(timedOut, "TIMED OUT")
	add(notCovered, "NOT COVERED")
	return `{"go_module":"m","files":[{"file_name":"a.go","mutations":[` + strings.Join(ms, ",") + `]}]}`
}

const testConfig = `{"mutation_go":{"enabled":true,"efficacy":80,"packages":["internal/domain","internal/store"],"fail_on_no_mutants":true}}`

// gate runs the gate over a temp dir holding the given reports (report
// file name -> content) and returns the exit code and output.
func gate(t *testing.T, config string, reports map[string]string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "quality.json")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range reports {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	code := run([]string{"-config", cfg, "-dir", dir}, &out, &errb)
	return code, out.String() + errb.String()
}

func TestGatePassesAtAndAboveTheFloor(t *testing.T) {
	code, out := gate(t, testConfig, map[string]string{
		"internal-domain.json": gremlinsReport(4, 1, 0, 0), // 80.00 %, exactly the floor
		"internal-store.json":  gremlinsReport(9, 1, 3, 2),
	})
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

func TestGateFailsOnAReportWithZeroMutants(t *testing.T) {
	code, out := gate(t, testConfig, map[string]string{
		"internal-domain.json": gremlinsReport(0, 0, 0, 0),
		"internal-store.json":  gremlinsReport(9, 1, 0, 0),
	})
	if code != 1 || !strings.Contains(out, "internal/domain: FAILED: gremlins generated no mutant") {
		t.Fatalf("exit %d, want 1 naming the zero-mutant package:\n%s", code, out)
	}
}

func TestGateFailsOnAReportBelowEightyPercentEfficacy(t *testing.T) {
	code, out := gate(t, testConfig, map[string]string{
		"internal-domain.json": gremlinsReport(3, 1, 0, 0), // 75 %
		"internal-store.json":  gremlinsReport(9, 1, 0, 0),
	})
	if code != 1 || !strings.Contains(out, "efficacy 75.00% is below the floor 80.00%") {
		t.Fatalf("exit %d, want 1 naming 75.00%%:\n%s", code, out)
	}
}

func TestGateFailsWhenEveryMutantIsNotCovered(t *testing.T) {
	code, out := gate(t, testConfig, map[string]string{
		"internal-domain.json": gremlinsReport(0, 0, 0, 5),
		"internal-store.json":  gremlinsReport(9, 1, 0, 0),
	})
	if code != 1 || !strings.Contains(out, "no mutant was judged") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestGateFailsOnAMissingOrMalformedReport(t *testing.T) {
	code, out := gate(t, testConfig, map[string]string{"internal-domain.json": gremlinsReport(9, 1, 0, 0)})
	if code != 1 || !strings.Contains(out, "internal/store: FAILED, report") {
		t.Fatalf("missing: exit %d:\n%s", code, out)
	}
	code, out = gate(t, testConfig, map[string]string{
		"internal-domain.json": gremlinsReport(9, 1, 0, 0),
		"internal-store.json":  "not json",
	})
	if code != 1 || !strings.Contains(out, "is not a gremlins report") {
		t.Fatalf("malformed: exit %d:\n%s", code, out)
	}
}

// A unit cut by its bound leaves the report of each package it finished and
// none for the rest: the finished one is judged on its efficacy and the other
// is named as never having run, not as unreadable.
func TestGateJudgesAFinishedPackageAndNamesTheOneThatNeverRanAsMissing(t *testing.T) {
	code, out := gate(t, testConfig, map[string]string{"internal-domain.json": gremlinsReport(9, 1, 0, 0)})
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "internal/domain: 10 mutants (killed 9, lived 1, timed out 0, not covered 0, not viable 0), efficacy 90.00%: ok") {
		t.Errorf("the finished package was not judged on its efficacy:\n%s", out)
	}
	if !strings.Contains(out, "internal/store: FAILED, report") || !strings.Contains(out, "the package never ran") {
		t.Errorf("the package that never ran is not named as missing (never ran):\n%s", out)
	}
	if strings.Contains(out, "unreadable") {
		t.Errorf("a package that never ran is reported as unreadable:\n%s", out)
	}
}

func TestGateRefusesADisabledOrPackagelessConfig(t *testing.T) {
	off := `{"mutation_go":{"enabled":false,"efficacy":80,"packages":["a"],"fail_on_no_mutants":true}}`
	if code, _ := gate(t, off, nil); code != 2 {
		t.Errorf("disabled gate: exit %d, want 2", code)
	}
	none := `{"mutation_go":{"enabled":true,"efficacy":80,"packages":[],"fail_on_no_mutants":true}}`
	if code, out := gate(t, none, nil); code != 1 || !strings.Contains(out, "no package is configured") {
		t.Errorf("no packages: exit %d: %s", code, out)
	}
}

func TestGateCountsFromMutationStatusesNotSummaryFields(t *testing.T) {
	// gremlins' summary fields omit timed-out and not-covered mutants; the
	// gate must tally the mutations themselves.
	c, err := tally([]byte(gremlinsReport(2, 1, 4, 3)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Killed != 2 || c.Lived != 1 || c.TimedOut != 4 || c.NotCovered != 3 || c.total() != 10 {
		t.Fatalf("counts %+v", c)
	}
}

func TestListPrintsTheConfiguredPackagesAndUsageErrorsExitTwo(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "q.json")
	if err := os.WriteFile(cfg, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-config", cfg, "-list"}, &out, &errb); code != 0 || out.String() != "internal/domain\ninternal/store\n" {
		t.Fatalf("list: exit %d %q", code, out.String())
	}
	if code := run(nil, &out, &errb); code != 2 {
		t.Errorf("no -config: exit %d, want 2", code)
	}
	if code := run([]string{"-config", filepath.Join(dir, "absent.json")}, &out, &errb); code != 2 {
		t.Errorf("absent config: exit %d, want 2", code)
	}
	if code := run([]string{"-bogus"}, &out, &errb); code != 2 {
		t.Errorf("bad flag: exit %d, want 2", code)
	}
}

func TestReportNameFlattensThePackagePath(t *testing.T) {
	if got := reportName("internal/domain"); got != "internal-domain.json" {
		t.Fatalf("got %q", got)
	}
	if got := reportName("./internal/store/"); got != "internal-store.json" {
		t.Fatalf("got %q", got)
	}
}

// scopedConfig narrows internal/server to two files and leaves internal/store whole.
const scopedConfig = `{"mutation_go":{"enabled":true,"efficacy":80,"packages":["internal/server","internal/store"],"fail_on_no_mutants":true,
"scopes":{"internal/server":["askops.go","noteops.go"]}}}`

// moduleWith writes a module root holding the named files under pkg and a
// quality.json, and returns the root and the config path.
func moduleWith(t *testing.T, config, pkg string, files ...string) (root, cfg string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, pkg), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, pkg, f), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg = filepath.Join(root, "quality.json")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

func TestExcludesLeaveOutEveryNonTestFileOutsideTheScope(t *testing.T) {
	root, cfg := moduleWith(t, scopedConfig, "internal/server",
		"askops.go", "noteops.go", "taskops.go", "login.go", "server_test.go", "notes.txt")
	c, err := loadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := excludes(c, root, "internal/server")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`^login\.go$`, `^taskops\.go$`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("excludes %q, want %q", got, want)
	}
	// The regexps match the out-of-scope files by path and never the scoped ones.
	for _, f := range []string{"taskops.go", "login.go"} {
		if !regexp.MustCompile(got[0]).MatchString(f) && !regexp.MustCompile(got[1]).MatchString(f) {
			t.Errorf("no exclude matches %s", f)
		}
	}
	for _, f := range []string{"askops.go", "noteops.go", "mytaskops.go"} {
		for _, re := range got {
			if regexp.MustCompile(re).MatchString(f) {
				t.Errorf("%s matches %s but is in scope or a different file", re, f)
			}
		}
	}
}

func TestAPackageWithoutAScopeIsMutatedWhole(t *testing.T) {
	root, cfg := moduleWith(t, scopedConfig, "internal/store", "store.go")
	c, err := loadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := excludes(c, root, "./internal/store/")
	if err != nil || len(got) != 0 {
		t.Fatalf("excludes %q, err %v; want none", got, err)
	}
}

func TestAScopeNamingAMissingFileOrNoFileIsRefused(t *testing.T) {
	root, cfg := moduleWith(t, scopedConfig, "internal/server", "askops.go")
	c, err := loadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := excludes(c, root, "internal/server"); err == nil || !strings.Contains(err.Error(), "noteops.go") {
		t.Errorf("a scope naming an absent file: err %v", err)
	}
	c.Scopes["internal/server"] = nil
	if _, err := excludes(c, root, "internal/server"); err == nil || !strings.Contains(err.Error(), "no file") {
		t.Errorf("an empty scope: err %v", err)
	}
}

func TestExcludesFlagPrintsThemAndRefusesABadScope(t *testing.T) {
	root, cfg := moduleWith(t, scopedConfig, "internal/server", "askops.go", "noteops.go", "login.go")
	t.Chdir(root)
	var out, errb bytes.Buffer
	if code := run([]string{"-config", cfg, "-excludes", "internal/server"}, &out, &errb); code != 0 ||
		out.String() != "^login\\.go$\n" {
		t.Fatalf("exit %d out %q err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"-config", cfg, "-excludes", "internal/store"}, &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatalf("an unscoped package: exit %d out %q", code, out.String())
	}
	if err := os.Remove(filepath.Join(root, "internal/server/noteops.go")); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-config", cfg, "-excludes", "internal/server"}, &out, &errb); code != 2 {
		t.Fatalf("a scope with a missing file: exit %d, want 2", code)
	}
}

func TestTheShippedConfigScopesOnlyPackagesItMutates(t *testing.T) {
	data, err := os.ReadFile("../quality.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		MutationGo config `json:"mutation_go"`
	}
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	cfg := all.MutationGo
	if cfg.Efficacy != 80 || !cfg.FailOnNoMutants {
		t.Fatalf("the floor moved: efficacy %v, fail_on_no_mutants %v", cfg.Efficacy, cfg.FailOnNoMutants)
	}
	in := map[string]bool{}
	for _, p := range cfg.Packages {
		in[p] = true
	}
	// The handwritten application logic Phase 2 introduced or changed.
	for _, p := range []string{"internal/domain", "internal/store", "internal/asks", "internal/attention", "internal/checkin",
		"internal/cursors", "internal/events", "internal/feed", "internal/notes", "internal/audit", "internal/tasks",
		"internal/server", "internal/cli"} {
		if !in[p] {
			t.Errorf("%s is not mutated", p)
		}
	}
	for p, files := range cfg.Scopes {
		if !in[p] {
			t.Errorf("scope for %s, which is not a mutated package", p)
		}
		if _, err := excludes(cfg, "../..", p); err != nil {
			t.Errorf("%s: %v (files %v)", p, err, files)
		}
	}
	for _, f := range []string{"askwait.go", "streamops.go"} {
		if !slices.Contains(cfg.Scopes["internal/server"], f) {
			t.Errorf("internal/server scope lacks %s", f)
		}
	}
}

const lanedConfig = `{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,
"packages":["internal/domain","internal/store","internal/asks","internal/server"],
"lanes":{"base":["internal/domain","internal/store"],"logic":["internal/asks"],"server":["internal/server"]}}}`

func laneRun(t *testing.T, config string, args []string, reports map[string]string) (int, string) {
	t.Helper()
	root, cfg := moduleWith(t, config, "internal/server", "a.go")
	for name, content := range reports {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	code := run(append([]string{"-config", cfg, "-dir", root}, args...), &out, &errb)
	return code, out.String() + errb.String()
}

func TestALaneListsAndJudgesOnlyItsOwnPackages(t *testing.T) {
	code, out := laneRun(t, lanedConfig, []string{"-list", "-lane", "base"}, nil)
	if code != 0 || out != "internal/domain\ninternal/store\n" {
		t.Fatalf("base lane list: exit %d %q", code, out)
	}
	if code, out = laneRun(t, lanedConfig, []string{"-list"}, nil); code != 0 || strings.Count(out, "\n") != 4 {
		t.Fatalf("no lane lists every package: exit %d %q", code, out)
	}
	// Only the server report exists: the server lane passes on it alone, the
	// others fail for a missing report, and "all" fails too.
	reports := map[string]string{"internal-server.json": gremlinsReport(9, 1, 0, 0)}
	if code, out = laneRun(t, lanedConfig, []string{"-lane", "server"}, reports); code != 0 {
		t.Fatalf("server lane: exit %d\n%s", code, out)
	}
	if code, out = laneRun(t, lanedConfig, []string{"-lane", "logic"}, reports); code != 1 || !strings.Contains(out, "internal/asks: FAILED, report") {
		t.Fatalf("logic lane without its report: exit %d\n%s", code, out)
	}
	if code, _ = laneRun(t, lanedConfig, nil, reports); code != 1 {
		t.Fatalf("all lanes with three reports missing: exit %d, want 1", code)
	}
	// A lane still judges against the unchanged floor.
	low := map[string]string{"internal-server.json": gremlinsReport(3, 1, 0, 0)}
	if code, out = laneRun(t, lanedConfig, []string{"-lane", "server"}, low); code != 1 || !strings.Contains(out, "efficacy 75.00% is below the floor 80.00%") {
		t.Fatalf("server lane at 75%%: exit %d\n%s", code, out)
	}
}

// TestALaneListJudgesTheUnionOfItsLanes: one Jenkins unit runs several lanes
// in one ci/mutation.sh call, "-lane server,base"; the union is listed in the
// configured order, judged as one set, and a typo or a repeat is refused.
func TestALaneListJudgesTheUnionOfItsLanes(t *testing.T) {
	code, out := laneRun(t, lanedConfig, []string{"-list", "-lane", "server,base"}, nil)
	if code != 0 || out != "internal/domain\ninternal/store\ninternal/server\n" {
		t.Fatalf("server,base list: exit %d %q", code, out)
	}
	reports := map[string]string{
		"internal-server.json": gremlinsReport(9, 1, 0, 0),
		"internal-asks.json":   gremlinsReport(9, 1, 0, 0),
	}
	if code, out = laneRun(t, lanedConfig, []string{"-lane", "server,logic"}, reports); code != 0 {
		t.Fatalf("server,logic with both reports: exit %d\n%s", code, out)
	}
	if code, out = laneRun(t, lanedConfig, []string{"-lane", "server,base"}, reports); code != 1 || !strings.Contains(out, "internal/domain: FAILED, report") {
		t.Fatalf("server,base without the base reports: exit %d\n%s", code, out)
	}
	for list, want := range map[string]string{"server,nope": "unknown lane \"nope\"", "base,base": "named twice", "base,": "unknown lane \"\""} {
		if code, out = laneRun(t, lanedConfig, []string{"-list", "-lane", list}, nil); code != 2 || !strings.Contains(out, want) {
			t.Errorf("-lane %s: exit %d, want 2 naming %q:\n%s", list, code, want, out)
		}
	}
}

func TestLanesMustPartitionThePackages(t *testing.T) {
	for name, tc := range map[string]struct{ config, want string }{
		"unknown lane": {lanedConfig, "unknown lane"},
		"left out": {`{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,"packages":["a","b"],"lanes":{"x":["a"]}}}`,
			"b is in no lane"},
		"twice": {`{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,"packages":["a"],"lanes":{"x":["a"],"y":["a"]}}}`,
			"a is in lanes"},
		"not a package": {`{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,"packages":["a"],"lanes":{"x":["a","c"]}}}`,
			"c, which is not in mutation_go.packages"},
		"empty lane": {`{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,"packages":["a"],"lanes":{"x":["a"],"y":[]}}}`,
			"lists no package"},
		"reserved": {`{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,"packages":["a"],"lanes":{"all":["a"]}}}`,
			"reserved"},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"-list"}
			if name == "unknown lane" {
				args = []string{"-list", "-lane", "nope"}
			}
			if code, out := laneRun(t, tc.config, args, nil); code != 2 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want 2 naming %q:\n%s", code, tc.want, out)
			}
		})
	}
}

func TestTheShippedLanesPartitionThePackagesAndEveryLaneStaysWithinTheOldBounds(t *testing.T) {
	data, err := os.ReadFile("../quality.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		MutationGo config `json:"mutation_go"`
	}
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	cfg := all.MutationGo
	if len(cfg.Lanes) == 0 {
		t.Fatal("the shipped config defines no lanes")
	}
	if err := cfg.checkLanes(); err != nil {
		t.Fatal(err)
	}
	var union []string
	for name := range cfg.Lanes {
		pkgs, err := cfg.lanePackages(name)
		if err != nil {
			t.Fatal(err)
		}
		union = append(union, pkgs...)
	}
	sort.Strings(union)
	want := cfg.units()
	sort.Strings(want)
	if strings.Join(union, ",") != strings.Join(want, ",") {
		t.Fatalf("lanes cover %v, packages are %v", union, want)
	}
}

// partedConfig splits internal/server's scope into two parts, run in two lanes.
const partedConfig = `{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,
"packages":["internal/store","internal/server"],
"scopes":{"internal/server":["a.go","b.go","c.go"]},
"parts":{"internal/server":{"x":["a.go"],"y":["b.go","c.go"]}},
"lanes":{"base":["internal/store"],"sx":["internal/server#x"],"sy":["internal/server#y"]}}}`

func TestAPartIsItsOwnUnitWithItsOwnReportAndJudgement(t *testing.T) {
	if got := reportName("internal/server#x"); got != "internal-server-x.json" {
		t.Fatalf("reportName of a part = %q", got)
	}
	if code, out := laneRun(t, partedConfig, []string{"-list"}, nil); code != 0 ||
		out != "internal/store\ninternal/server#x\ninternal/server#y\n" {
		t.Fatalf("units of all lanes: exit %d %q", code, out)
	}
	if code, out := laneRun(t, partedConfig, []string{"-list", "-lane", "sy"}, nil); code != 0 || out != "internal/server#y\n" {
		t.Fatalf("lane sy: exit %d %q", code, out)
	}
	// A part passing says nothing about its sibling: the other part's report is
	// missing, and a part below the floor fails although the package as a whole
	// would pass.
	ok := map[string]string{"internal-server-x.json": gremlinsReport(9, 1, 0, 0)}
	if code, out := laneRun(t, partedConfig, []string{"-lane", "sx"}, ok); code != 0 {
		t.Fatalf("lane sx: exit %d\n%s", code, out)
	}
	if code, out := laneRun(t, partedConfig, []string{"-lane", "sy"}, ok); code != 1 || !strings.Contains(out, "internal/server#y: FAILED, report") {
		t.Fatalf("lane sy without its report: exit %d\n%s", code, out)
	}
	low := map[string]string{"internal-server-x.json": gremlinsReport(9, 1, 0, 0), "internal-server-y.json": gremlinsReport(3, 1, 0, 0)}
	if code, out := laneRun(t, partedConfig, []string{"-lane", "sy"}, low); code != 1 || !strings.Contains(out, "efficacy 75.00% is below the floor 80.00%") {
		t.Fatalf("part y at 75%%: exit %d\n%s", code, out)
	}
}

func TestAPartExcludesEveryFileOutsideItself(t *testing.T) {
	root, cfgPath := moduleWith(t, partedConfig, "internal/server", "a.go", "b.go", "c.go", "d.go", "a_test.go")
	cfg, _, err := loadLane(cfgPath, "all")
	if err != nil {
		t.Fatal(err)
	}
	for unit, want := range map[string][]string{
		"internal/server#x": {"^b\\.go$", "^c\\.go$", "^d\\.go$"},
		"internal/server#y": {"^a\\.go$", "^d\\.go$"},
	} {
		got, err := excludes(cfg, root, unit)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("excludes(%s) = %v, %v; want %v", unit, got, err, want)
		}
	}
	if _, err := excludes(cfg, root, "internal/server#nope"); err == nil {
		t.Error("a part that does not exist was accepted")
	}
}

func TestPartsMustSplitTheScopeExactly(t *testing.T) {
	const head = `{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,"packages":["p","q"],`
	for name, tc := range map[string]struct{ config, want string }{
		"a file in two parts": {head + `"scopes":{"p":["a.go","b.go"]},"parts":{"p":{"x":["a.go","b.go"],"y":["b.go"]}},"lanes":{"l":["p#x","p#y","q"]}}}`,
			"b.go is in parts"},
		"a scoped file in no part": {head + `"scopes":{"p":["a.go","b.go"]},"parts":{"p":{"x":["a.go"]}},"lanes":{"l":["p#x","q"]}}}`,
			"b.go is in no part"},
		"a part file outside the scope": {head + `"scopes":{"p":["a.go"]},"parts":{"p":{"x":["a.go","z.go"]}},"lanes":{"l":["p#x","q"]}}}`,
			"z.go, which is not in the package's scope"},
		"an empty part": {head + `"scopes":{"p":["a.go"]},"parts":{"p":{"x":["a.go"],"y":[]}},"lanes":{"l":["p#x","p#y","q"]}}}`,
			"lists no file"},
		"parts of an unscoped package": {head + `"parts":{"q":{"x":["a.go"]}},"lanes":{"l":["p","q#x"]}}}`,
			"not a scoped package"},
		"a part in no lane": {head + `"scopes":{"p":["a.go","b.go"]},"parts":{"p":{"x":["a.go"],"y":["b.go"]}},"lanes":{"l":["p#x","q"]}}}`,
			"p#y is in no lane"},
		"a part in two lanes": {head + `"scopes":{"p":["a.go","b.go"]},"parts":{"p":{"x":["a.go"],"y":["b.go"]}},"lanes":{"l":["p#x","p#y","q"],"m":["p#x"]}}}`,
			"p#x is in lanes"},
		"the whole package in a lane while it has parts": {head + `"scopes":{"p":["a.go"]},"parts":{"p":{"x":["a.go"]}},"lanes":{"l":["p","p#x","q"]}}}`,
			"p, which is not in mutation_go.packages"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, out := laneRun(t, tc.config, []string{"-list"}, nil); code != 2 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want 2 naming %q:\n%s", code, tc.want, out)
			}
		})
	}
}

func TestTheShippedPartsGiveEveryPartOfAPackageALaneOfItsOwn(t *testing.T) {
	data, err := os.ReadFile("../quality.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		MutationGo config `json:"mutation_go"`
	}
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	cfg := all.MutationGo
	if err := cfg.checkLanes(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Parts["internal/server"]) != 3 || len(cfg.Parts["internal/cli"]) != 2 {
		t.Fatalf("parts = %v, want internal/server in three and internal/cli in two", cfg.Parts)
	}
	// Every part is judged on its own and is run by a lane no sibling part
	// shares, so no stage holds two parts of one package's mutants.
	lanes := map[string]string{}
	for lane, units := range cfg.Lanes {
		for _, u := range units {
			lanes[u] = lane
		}
	}
	for pkg, parts := range cfg.Parts {
		used := map[string]string{}
		for name := range parts {
			unit := pkg + partSep + name
			lane := lanes[unit]
			if lane == "" {
				t.Errorf("%s is in no lane", unit)
			} else if prev, dup := used[lane]; dup {
				t.Errorf("%s and %s share lane %q", prev, unit, lane)
			}
			used[lane] = unit
		}
	}
	for _, unit := range cfg.units() {
		if _, err := excludes(cfg, "../..", unit); err != nil {
			t.Errorf("%s: %v", unit, err)
		}
	}
}

// phaseRepo makes a git repository whose first commit holds before (path ->
// content) and whose working tree then holds after (a nil content deletes the
// file), and returns its root and the first commit's sha.
func phaseRepo(t *testing.T, before, after map[string]*string) (root, base string) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(files map[string]*string) {
		for f, content := range files {
			p := filepath.Join(root, filepath.FromSlash(f))
			if content == nil {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(*content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q")
	write(before)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base = git("rev-parse", "HEAD")
	write(after)
	git("add", "-A")
	git("commit", "-q", "-m", "phase")
	return root, base
}

func src(s string) *string { return &s }

// The rule: every handwritten application file the phase added or changed is
// measured by some unit. A changed file left out of a package's scope, or in a
// package the gate does not list, is reported; a test file, a generated file,
// a deleted file, a file outside internal/ and cmd/, and a file a unit does
// measure are not.
func TestUnmeasuredListsEveryChangedApplicationFileNoUnitMeasures(t *testing.T) {
	code := src("package x\n\nfunc F() int { return 1 }\n")
	changed := src("package x\n\nfunc F() int { return 2 }\n")
	root, base := phaseRepo(t, map[string]*string{
		"internal/server/taskops.go": code, "internal/server/askops.go": code, "internal/server/old.go": code,
		"internal/audit/audit.go": code, "internal/gone/gone.go": code, "internal/server/server_test.go": code,
	}, map[string]*string{
		"internal/server/taskops.go":     changed, // scoped package, not in its scope: reported
		"internal/server/askops.go":      changed, // in the scope: measured
		"internal/audit/audit.go":        changed, // a package the gate does not list: reported
		"internal/tasks/tasks.go":        code,    // a new file in a new, unlisted package: reported
		"internal/asks/raise.go":         code,    // a new file of a package mutated whole: measured
		"internal/server/server_test.go": changed, // a test file: not application code
		"internal/server/openapi.gen.go": src("// Code generated by oapi-codegen. DO NOT EDIT.\npackage x\n"),
		"cmd/vh/main.go":                 code, // an application root other than internal/: reported
		"ci/tool/main.go":                code, // tooling, not application code
		"internal/gone/gone.go":          nil,  // deleted: nothing left to mutate
	})
	cfg := config{
		Packages:  []string{"internal/server", "internal/asks"},
		Scopes:    map[string][]string{"internal/server": {"askops.go"}},
		PhaseBase: base,
	}
	got, err := unmeasured(cfg, root)
	want := []string{"cmd/vh/main.go", "internal/audit/audit.go", "internal/server/taskops.go", "internal/tasks/tasks.go"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, %v; want %v", got, err, want)
	}
	// Measuring them clears the list: a scope that names the file, and the
	// unlisted packages listed (mutated whole).
	cfg.Scopes["internal/server"] = []string{"askops.go", "taskops.go"}
	cfg.Packages = append(cfg.Packages, "internal/audit", "internal/tasks", "cmd/vh")
	if got, err := unmeasured(cfg, root); err != nil || len(got) != 0 {
		t.Fatalf("after measuring them: %v, %v", got, err)
	}
}

func TestUnmeasuredFlagExitsOneOnAGapAndTwoWithoutABase(t *testing.T) {
	root, base := phaseRepo(t, map[string]*string{"internal/a/a.go": src("package a\n")},
		map[string]*string{"internal/a/a.go": src("package a\n\nvar V = 1\n"), "internal/b/b.go": src("package b\n")})
	t.Chdir(root)
	for name, tc := range map[string]struct {
		config string
		code   int
		want   string
	}{
		"a gap": {`{"mutation_go":{"enabled":true,"efficacy":80,"packages":["internal/a"],"phase_base":"` + base + `"}}`,
			1, "internal/b/b.go: changed since " + base + " but no mutation unit measures it\n"},
		"no gap": {`{"mutation_go":{"enabled":true,"efficacy":80,"packages":["internal/a","internal/b"],"phase_base":"` + base + `"}}`,
			0, ""},
		"no phase_base": {`{"mutation_go":{"enabled":true,"efficacy":80,"packages":["internal/a"]}}`,
			2, "phase_base is not set"},
		"a base git does not know": {`{"mutation_go":{"enabled":true,"efficacy":80,"packages":["internal/a"],"phase_base":"0123456789abcdef"}}`,
			2, "git diff 0123456789abcdef"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "quality.json")
			if err := os.WriteFile(cfg, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errb bytes.Buffer
			code := run([]string{"-config", cfg, "-unmeasured"}, &out, &errb)
			if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.want) || (tc.code == 0 && out.Len() != 0) {
				t.Fatalf("exit %d, want %d naming %q:\n%s%s", code, tc.code, tc.want, out.String(), errb.String())
			}
		})
	}
}

// The shipped config measures every handwritten application file Phase 2
// introduced or changed: none is left out of mutation_go.
func TestTheShippedConfigMeasuresEveryApplicationFileThePhaseChanged(t *testing.T) {
	data, err := os.ReadFile("../quality.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		MutationGo config `json:"mutation_go"`
	}
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	cfg := all.MutationGo
	if !strings.HasPrefix(cfg.PhaseBase, "d3056d6") {
		t.Fatalf("phase_base = %q, want the Phase 2 start d3056d6", cfg.PhaseBase)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmeasured(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Phase 2 application files no mutation unit measures: %v", got)
	}
	// The files review found missing are measured by a unit of their own lane.
	for _, f := range []string{"internal/server/taskops.go", "internal/cli/cli.go", "internal/audit/audit.go",
		"internal/tasks/tasks.go", "internal/server/server.go"} {
		if !cfg.measures(f) {
			t.Errorf("%s is not measured", f)
		}
	}
}

// In diff mode a unit that generated no mutant passes (its change had nothing
// gremlins mutates), SKIPPED mutants (outside the diff) count nowhere, the
// efficacy floor still holds, changed lines no test covers still fail, and
// mutants that only timed out pass.
func TestDiffModeJudgesOnlyTheMutantsOfTheChangedLines(t *testing.T) {
	cfg := config{Efficacy: 80, FailOnNoMutants: true}
	skipped := counts{Skipped: 40}
	for name, tc := range map[string]struct {
		c    counts
		want string
	}{
		"zero mutants":            {counts{}, ""},
		"only skipped":            {skipped, ""},
		"at the floor":            {counts{Killed: 4, Lived: 1, Skipped: 40}, ""},
		"below the floor":         {counts{Killed: 2, Lived: 1, Skipped: 40}, "efficacy 66.67% is below the floor"},
		"changed lines uncovered": {counts{NotCovered: 2, Skipped: 40}, "no mutant was judged"},
		"only timed out":          {counts{TimedOut: 1, Skipped: 40}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := judge(cfg, tc.c, true)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("judge = %q, want %q", got, tc.want)
			}
		})
	}
	// Outside diff mode nothing changes: zero mutants (SKIPPED aside) fail.
	if judge(cfg, skipped, false) == "" {
		t.Error("full mode passed a report with only skipped mutants")
	}
	if c, _ := tally([]byte(`{"files":[{"mutations":[{"status":"SKIPPED"},{"status":"KILLED"}]}]}`)); c.Skipped != 1 || c.total() != 1 {
		t.Errorf("tally = %+v, want one skipped outside the total of 1", c)
	}
}

// -diff lists and judges only the units whose measured files changed since
// the merge base with the ref: a change outside a unit's scope or part, or to
// a test file, does not select it, and an unchanged unit passes without a
// report. With nothing changed, nothing is listed and the gate passes.
func TestDiffFlagSelectsAndJudgesOnlyTheChangedUnits(t *testing.T) {
	code := src("package x\n\nfunc F() int { return 1 }\n")
	changed := src("package x\n\nfunc F() int { return 2 }\n")
	before := map[string]*string{
		"internal/a/a.go": code, "internal/a/a_test.go": code, "internal/b/b.go": code,
		"internal/s/in.go": code, "internal/s/out.go": code, "internal/s/other.go": code,
	}
	root, base := phaseRepo(t, before, map[string]*string{"notes.txt": code})
	t.Chdir(root)
	cfgPath := filepath.Join(t.TempDir(), "quality.json")
	conf := `{"mutation_go":{"enabled":true,"efficacy":80,"fail_on_no_mutants":true,
	  "packages":["internal/a","internal/b","internal/s"],
	  "scopes":{"internal/s":["in.go","other.go"]},
	  "parts":{"internal/s":{"p1":["in.go"],"p2":["other.go"]}}}}`
	if err := os.WriteFile(cfgPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	runGate := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		c := run(append([]string{"-config", cfgPath, "-diff", base}, args...), &out, &errb)
		return c, out.String() + errb.String()
	}
	reports := t.TempDir()
	if c, out := runGate("-list"); c != 0 || out != "" {
		t.Fatalf("nothing changed: -list exit %d printed %q, want nothing", c, out)
	}
	if c, out := runGate("-dir", reports); c != 0 || strings.Contains(out, "FAILED") {
		t.Fatalf("nothing changed: gate exit %d:\n%s", c, out)
	}
	// The working tree, as gremlins --diff sees it: a's code, a test of b, and
	// a file of s outside its scope and one in part p2.
	for f, content := range map[string]*string{
		"internal/a/a.go": changed, "internal/b/b_test.go": code,
		"internal/s/out.go": changed, "internal/s/other.go": changed,
	} {
		if err := os.WriteFile(filepath.FromSlash(f), []byte(*content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c, out := runGate("-list"); c != 0 || out != "internal/a\ninternal/s#p2\n" {
		t.Fatalf("-list exit %d printed %q, want internal/a and internal/s#p2", c, out)
	}
	// a generated no mutant and p2's mutants are above the floor: pass, though
	// no report exists for b or p1.
	for name, content := range map[string]string{
		"internal-a.json":    `{"files":[{"mutations":[{"status":"SKIPPED"}]}]}`,
		"internal-s-p2.json": gremlinsReport(4, 1, 0, 0),
	} {
		if err := os.WriteFile(filepath.Join(reports, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c, out := runGate("-dir", reports); c != 0 || !strings.Contains(out, "internal/b: no measured file changed") {
		t.Fatalf("gate exit %d, want 0:\n%s", c, out)
	}
	if err := os.WriteFile(filepath.Join(reports, "internal-s-p2.json"), []byte(gremlinsReport(3, 1, 0, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, out := runGate("-dir", reports); c != 1 || !strings.Contains(out, "internal/s#p2: FAILED: efficacy 75.00%") {
		t.Fatalf("gate exit %d, want 1 on p2 below the floor:\n%s", c, out)
	}
	if c, out := runGate("-list", "-diff", "no-such-ref"); c != 2 || !strings.Contains(out, "git diff --merge-base no-such-ref") {
		t.Fatalf("unknown ref: exit %d:\n%s", c, out)
	}
}

const (
	oldImport = `import "github.com/n-orlov/` + `board/internal/domain"`
	newImport = `import "github.com/n-orlov/village-hall/internal/domain"`
)

func goFile(imp, body string) *string {
	return src("package x\n\n" + imp + "\n\n" + body + "\n")
}

// unmeasuredAfterRename lists the unmeasured files of a fixture phase; no unit
// of the config measures anything, so every file the rule does not discount is
// reported.
func unmeasuredAfterRename(t *testing.T, before, after map[string]*string) []string {
	t.Helper()
	root, base := phaseRepo(t, before, after)
	got, err := unmeasured(config{Packages: []string{"internal/other"}, PhaseBase: base}, root)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// R15: a file whose only difference from phase_base is its import paths
// (board/ to village-hall/) is not a change.
func TestUnmeasuredIgnoresAnImportOnlyChange(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(oldImport, "var V = domain.X")},
		map[string]*string{"internal/a/a.go": goFile(newImport, "var V = domain.X")})
	if len(got) != 0 {
		t.Fatalf("an import-only change was flagged: %v", got)
	}
}

// R15: the same import rewrite plus one changed code line is a change.
func TestUnmeasuredFlagsAnImportChangePlusOneCodeLine(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(oldImport, "var V = domain.X")},
		map[string]*string{"internal/a/a.go": goFile(newImport, "var V = domain.Y")})
	if want := []string{"internal/a/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}

// R15: a file moved from cmd/board/ to cmd/vh/ that is identical apart from
// its imports is not a change.
func TestUnmeasuredIgnoresTheCmdMoveWhenOnlyImportsDiffer(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"cmd/board/main.go": goFile(oldImport, "func main() {}")},
		map[string]*string{"cmd/board/main.go": nil, "cmd/vh/main.go": goFile(newImport, "func main() {}")})
	if len(got) != 0 {
		t.Fatalf("the cmd/board to cmd/vh move was flagged: %v", got)
	}
}

// R15: a file the base never held is a change.
func TestUnmeasuredFlagsANewFile(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(oldImport, "var V = domain.X")},
		map[string]*string{"internal/a/a.go": goFile(newImport, "var V = domain.X"), "internal/b/b.go": goFile(newImport, "var W = 1")})
	if want := []string{"internal/b/b.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}

// R15: only the cmd/board to cmd/vh move is discounted; any other move of an
// identical file is a change.
func TestUnmeasuredFlagsAnUnrelatedMove(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(oldImport, "var V = domain.X"), "cmd/board/main.go": goFile(oldImport, "func main() {}")},
		map[string]*string{"internal/a/a.go": nil, "internal/b/a.go": goFile(oldImport, "var V = domain.X"),
			"cmd/board/main.go": nil, "cmd/other/main.go": goFile(oldImport, "func main() {}")})
	if want := []string{"cmd/other/main.go", "internal/b/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}

const (
	rawOld = "var Doc = `\n\t\"github.com/n-orlov/" + "board/internal/domain\"\n`"
	rawNew = "var Doc = `\n\t\"github.com/n-orlov/village-hall/internal/domain\"\n`"
)

// R15: an import-looking line inside a raw string is data, so changing its
// module path is a change even when the file's real imports are rewritten.
func TestUnmeasuredFlagsAChangedRawStringValue(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(oldImport, rawOld)},
		map[string]*string{"internal/a/a.go": goFile(newImport, rawNew)})
	if want := []string{"internal/a/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}

// R15: the same applies to a changed value in a comment, an interpreted string
// and a file with no real import at all.
func TestUnmeasuredFlagsImportLookingTextThatChanged(t *testing.T) {
	cases := map[string][2]string{
		"comment":     {"// \"github.com/n-orlov/" + "board/x\"\nvar V = 1", "// \"github.com/n-orlov/village-hall/x\"\nvar V = 1"},
		"interpreted": {"var V = \"github.com/n-orlov/" + "board/x\"", "var V = \"github.com/n-orlov/village-hall/x\""},
		"no import":   {"var V = `\n\"github.com/n-orlov/" + "board/x\"\n`", "var V = `\n\"github.com/n-orlov/village-hall/x\"\n`"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := unmeasuredAfterRename(t,
				map[string]*string{"internal/a/a.go": src("package x\n\n" + c[0] + "\n")},
				map[string]*string{"internal/a/a.go": src("package x\n\n" + c[1] + "\n")})
			if want := []string{"internal/a/a.go"}; !slices.Equal(got, want) {
				t.Fatalf("unmeasured = %v, want %v", got, want)
			}
		})
	}
}

// R15: a raw string that holds import-looking text unchanged, beside a real
// rewritten import, is still only the rename; so are grouped and aliased
// imports.
func TestUnmeasuredIgnoresRewrittenGroupedAndAliasedImports(t *testing.T) {
	oldGroup := "import (\n\tfoo \"github.com/n-orlov/" + "board/internal/foo\" // the foo\n\t\"fmt\"\n\t\"github.com/n-orlov/" + "board/internal/bar\"\n)"
	newGroup := "import (\n\tfoo \"github.com/n-orlov/village-hall/internal/foo\" // the foo\n\t\"fmt\"\n\t\"github.com/n-orlov/village-hall/internal/bar\"\n)"
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": src("package x\n\n" + oldGroup + "\n\n" + rawOld + "\n"), "internal/b/b.go": src("package x\n\n" + oldGroup + "\n")},
		map[string]*string{"internal/a/a.go": src("package x\n\n" + newGroup + "\n\n" + rawOld + "\n"), "internal/b/b.go": src("package x\n\n" + newGroup + "\n")})
	if len(got) != 0 {
		t.Fatalf("rename-only files were flagged: %v", got)
	}
}

// R15: an import whose path changes by more than the module prefix is a change.
func TestUnmeasuredFlagsAnImportWhoseTailChanged(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(oldImport, "var V = domain.X")},
		map[string]*string{"internal/a/a.go": goFile(`import "github.com/n-orlov/village-hall/internal/other"`, "var V = domain.X")})
	if want := []string{"internal/a/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}

// R15: blank, dot and backquoted import specs are real imports, so rewriting
// their module path is only the rename.
func TestUnmeasuredIgnoresRewrittenBlankDotAndBackquotedImports(t *testing.T) {
	oldImps := "import (\n\t_ \"github.com/n-orlov/" + "board/internal/a\"\n\t. \"github.com/n-orlov/" + "board/internal/b\"\n\t`github.com/n-orlov/" + "board/internal/c`\n)"
	newImps := "import (\n\t_ \"github.com/n-orlov/village-hall/internal/a\"\n\t. \"github.com/n-orlov/village-hall/internal/b\"\n\t`github.com/n-orlov/village-hall/internal/c`\n)"
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": src("package x\n\n" + oldImps + "\n")},
		map[string]*string{"internal/a/a.go": src("package x\n\n" + newImps + "\n")})
	if len(got) != 0 {
		t.Fatalf("rename-only imports were flagged: %v", got)
	}
}

// R15: a changed raw-string value in a file moved from cmd/board/ to cmd/vh/
// is a change, though the move itself and its imports are discounted.
func TestUnmeasuredFlagsAChangedRawStringInTheMovedCmdFile(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"cmd/board/main.go": goFile(oldImport, rawOld+"\nfunc main() {}")},
		map[string]*string{"cmd/board/main.go": nil, "cmd/vh/main.go": goFile(newImport, rawNew+"\nfunc main() {}")})
	if want := []string{"cmd/vh/main.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}

// R15: an import under a module that merely shares the village-hall prefix is
// not the renamed module, so it is a change.
func TestUnmeasuredFlagsAnImportOfALookalikeModule(t *testing.T) {
	got := unmeasuredAfterRename(t,
		map[string]*string{"internal/a/a.go": goFile(`import "github.com/n-orlov/village-hallway/x"`, "var V = x.X")},
		map[string]*string{"internal/a/a.go": goFile(`import "github.com/n-orlov/village-hall/x"`, "var V = x.X")})
	if want := []string{"internal/a/a.go"}; !slices.Equal(got, want) {
		t.Fatalf("unmeasured = %v, want %v", got, want)
	}
}
