// ci/mutationgate: the Go mutation gate (SPEC 15.4). It reads the
// "mutation_go" section of ci/quality.json and one gremlins JSON report per
// configured package, and fails when
//
//   - a package's report is missing (the package never ran: its unit was cut
//     or failed before the package finished; a finished package's report is
//     written the moment it finishes, so only unfinished packages are missing)
//     or is not a gremlins report,
//   - a package generated no mutant (fail_on_no_mutants: a gate that
//     measured nothing has not passed), or
//   - a package's efficacy is below the configured floor.
//
// Efficacy is gremlins' own definition: KILLED / (KILLED + LIVED). Timed-out,
// not-viable and not-covered mutants are reported but sit outside it. A
// package whose mutants are all not covered has no efficacy and fails.
//
// The counts are taken from the per-mutation statuses in the report, not
// from its summary fields, which omit timed-out and not-covered mutants.
//
//	mutationgate -config ci/quality.json -dir ci-results/mutation
//	mutationgate -config ci/quality.json -list     # the packages, one per line
//	mutationgate -config ci/quality.json -list -lane logic
//	                                               # one lane's packages
//	mutationgate -config ci/quality.json -dir d -lane logic
//	                                               # judge one lane's reports
//	mutationgate -config ci/quality.json -list -lane server-core,logic
//	                                               # several lanes at once
//	mutationgate -config ci/quality.json -unmeasured
//	                                               # application files changed since phase_base
//	                                               # that no unit measures (exit 1 if any)
//	                                               # (import-path-only rewrites board/ to village-hall/
//	                                               # and the cmd/board/ to cmd/vh/ move are not changes)
//	mutationgate -config ci/quality.json -excludes internal/server
//	                                               # one gremlins -E regexp per out-of-scope file
//	                                               # (a part: internal/server#ops)
//	mutationgate -config ci/quality.json -diff <ref> -list -lane logic
//	                                               # only the units with a measured file changed
//	                                               # since the merge base with ref
//	mutationgate -config ci/quality.json -diff <ref> -dir d -lane logic
//	                                               # judge a diff-mode run (see below)
//	mutationgate -config ci/quality.json -stale <ref>
//	                                               # the units a full run at ref no longer
//	                                               # stands for (incremental mode, stale.go)
//	mutationgate -config ci/quality.json -list -lane cli,internal/tasks
//	                                               # a lane list may name single units too
//
// Diff mode (-diff, the Jenkinsfile's branch builds; ci/mutation.sh passes the
// same ref to gremlins --diff, which mutates only lines changed since the
// merge base with it): a unit none of whose measured files changed is not run
// and passes without a report, and a changed unit that generated no mutant
// (a change with nothing gremlins mutates) passes too, whatever
// fail_on_no_mutants says. A unit that did generate mutants is held to the
// same efficacy floor; one whose changed lines no test covers at all fails,
// and one whose judged mutants all timed out passes (gremlins could not judge
// them either way). gremlins marks the mutants outside the diff SKIPPED; they
// sit outside every count.
//
// Exit codes: 0 pass, 1 gate failed, 2 usage or config error, 3 (-stale
// only) a shared input changed, so every unit must run.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// config is the "mutation_go" section of ci/quality.json.
type config struct {
	Enabled         bool     `json:"enabled"`
	Efficacy        float64  `json:"efficacy"`
	Packages        []string `json:"packages"`
	FailOnNoMutants bool     `json:"fail_on_no_mutants"`
	// Scopes narrows a package to the handwritten files listed for it (base
	// names in the package directory): every other non-test .go file of the
	// package is excluded from the gremlins run. A package with no entry is
	// mutated whole. It is how a large package that holds both older code and
	// code a phase introduced or changed (internal/server, internal/cli) is
	// measured over the phase's own files without a per-test bound raised.
	Scopes map[string][]string `json:"scopes"`
	// Parts splits one scoped package's files into named parts, each of which
	// is its own unit: a lane lists it as "internal/server#ops", gremlins runs
	// the package once per part with every file outside the part excluded, and
	// the part has its own report and its own efficacy judgement against the
	// same floor. It exists because gremlins re-runs a package's whole suite
	// for every mutant, so a large package's mutants are split into parts that
	// each run on their own (no bound is raised). The parts of
	// a package are disjoint and together are exactly its scope, so splitting
	// never leaves a scoped file unmeasured.
	Parts map[string]map[string][]string `json:"parts"`
	// Lanes splits Packages into groups that ci/mutation.sh runs, and the
	// Jenkinsfile runs as separate stages, each under its own timeout: a
	// gremlins run of a package re-runs the package's whole suite for every
	// mutant, and one stage cannot hold them all in 30 minutes. Every package
	// is in exactly one lane (a package with Parts: each of its parts, written
	// "package#part"). No lanes means one lane, "all".
	Lanes map[string][]string `json:"lanes"`
	// PhaseBase is the commit the current phase started from. Every
	// handwritten application file (a non-test, non-generated .go file under
	// internal/ or cmd/) added or changed since it must be measured by some
	// unit: its package is listed and, if scoped, its file is in the scope.
	// -unmeasured lists the ones that are not, and a test of the shipped
	// config requires none, so the scope cannot silently fall behind the
	// phase's code.
	PhaseBase string `json:"phase_base"`
}

// allLane is the lane name that selects every package.
const allLane = "all"

// partSep separates a package from the name of one of its parts in a unit.
const partSep = "#"

// splitUnit splits a unit into its package directory and part ("" for a unit
// that is a whole package).
func splitUnit(unit string) (pkg, part string) {
	pkg, part, _ = strings.Cut(unit, partSep)
	return pkg, part
}

// units returns what gremlins runs and the gate judges, in the configured
// order: a package, or for a package with Parts each of its parts by name.
func (c config) units() []string {
	var out []string
	for _, p := range c.Packages {
		parts, ok := c.Parts[p]
		if !ok {
			out = append(out, p)
			continue
		}
		names := make([]string, 0, len(parts))
		for n := range parts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, p+partSep+n)
		}
	}
	return out
}

// laneUnion returns the set of units in a comma-separated list of lanes, or
// an error for a lane named twice or not defined.
func (c config) laneUnion(lanes string) (map[string]bool, error) {
	in := map[string]bool{}
	named := map[string]bool{}
	for _, name := range strings.Split(lanes, ",") {
		if named[name] {
			return nil, fmt.Errorf("lane %q is named twice in %q", name, lanes)
		}
		named[name] = true
		members, err := c.laneMembers(name)
		if err != nil {
			return nil, err
		}
		for _, u := range members {
			in[u] = true
		}
	}
	return in, nil
}

// laneMembers returns the units of one lane, or the unit itself when name is
// a unit ("internal/tasks", "internal/server#ops"): incremental mutation runs
// a lane list narrowed to the units that changed by naming them.
func (c config) laneMembers(name string) ([]string, error) {
	if members, ok := c.Lanes[name]; ok {
		return members, nil
	}
	if strings.Contains(name, "/") && slices.Contains(c.units(), name) {
		return []string{name}, nil
	}
	return nil, fmt.Errorf("unknown lane %q (lanes: %s)", name, strings.Join(c.laneNames(), ", "))
}

// lanePackages returns the units of a lane in the configured order, or an
// error for a lane the config does not define. A comma-separated list of
// lanes ("server-core,logic", one Jenkins unit running several lanes) selects
// the union of those lanes, still in the configured order; a lane named twice
// is an error, so a typo never hides behind a duplicate.
func (c config) lanePackages(lane string) ([]string, error) {
	if lane == "" || lane == allLane {
		return c.units(), nil
	}
	in, err := c.laneUnion(lane)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range c.units() {
		if in[u] {
			out = append(out, u)
		}
	}
	return out, nil
}

func (c config) laneNames() []string {
	names := make([]string, 0, len(c.Lanes))
	for n := range c.Lanes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// checkLanes fails a config whose lanes do not partition its packages: a
// package left out of every lane would never be measured by any stage, one in
// two lanes would be measured twice, and a lane naming a package the gate does
// not list measures something the gate never judges.
func (c config) checkLanes() error {
	if err := c.checkParts(); err != nil {
		return err
	}
	if len(c.Lanes) == 0 {
		return nil
	}
	seen := map[string]string{}
	for _, name := range c.laneNames() {
		if err := c.checkLane(name, seen); err != nil {
			return err
		}
	}
	for _, u := range c.units() {
		if _, ok := seen[u]; !ok {
			return fmt.Errorf("%s is in no lane, so no stage would measure it", u)
		}
	}
	return nil
}

// checkParts fails a config whose parts would measure less than the scope
// they split, or measure a file twice: a part belongs to a scoped package, and
// the parts of a package are non-empty, disjoint, and together exactly its
// scope.
func (c config) checkParts() error {
	for _, pkg := range sortedKeys(c.Parts) {
		scope, ok := c.Scopes[pkg]
		if !slices.Contains(c.Packages, pkg) || !ok {
			return fmt.Errorf("parts for %s, which is not a scoped package of mutation_go.packages", pkg)
		}
		if err := checkPartsOf(pkg, scope, c.Parts[pkg]); err != nil {
			return err
		}
	}
	return nil
}

// checkPartsOf checks that parts are non-empty, disjoint and exactly scope.
func checkPartsOf(pkg string, scope []string, parts map[string][]string) error {
	owner := map[string]string{}
	for _, name := range sortedKeys(parts) {
		if len(parts[name]) == 0 {
			return fmt.Errorf("%s: part %q lists no file", pkg, name)
		}
		for _, f := range parts[name] {
			if prev, dup := owner[f]; dup {
				return fmt.Errorf("%s: %s is in parts %q and %q, so it would be mutated twice", pkg, f, prev, name)
			}
			if !slices.Contains(scope, f) {
				return fmt.Errorf("%s: part %q lists %s, which is not in the package's scope", pkg, name, f)
			}
			owner[f] = name
		}
	}
	for _, f := range scope {
		if _, ok := owner[f]; !ok {
			return fmt.Errorf("%s: scoped file %s is in no part, so no stage would measure it", pkg, f)
		}
	}
	return nil
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkLane checks one lane and records its packages in seen (package -> lane).
func (c config) checkLane(name string, seen map[string]string) error {
	if name == allLane {
		return fmt.Errorf("lane %q is reserved for every package", allLane)
	}
	if len(c.Lanes[name]) == 0 {
		return fmt.Errorf("lane %q lists no package", name)
	}
	units := c.units()
	for _, p := range c.Lanes[name] {
		if !slices.Contains(units, p) {
			return fmt.Errorf("lane %q lists %s, which is not in mutation_go.packages", name, p)
		}
		if prev, dup := seen[p]; dup {
			return fmt.Errorf("%s is in lanes %q and %q", p, prev, name)
		}
		seen[p] = name
	}
	return nil
}

// report is the part of a gremlins JSON report the gate reads.
type report struct {
	Files []struct {
		Mutations []struct {
			Status string `json:"status"`
		} `json:"mutations"`
	} `json:"files"`
}

// counts is a package's mutants by outcome.
type counts struct {
	Killed, Lived, TimedOut, NotViable, NotCovered, Skipped, Other int
}

// total is every mutant gremlins generated that compiles: everything but
// the not-viable ones and, in diff mode, the ones outside the diff.
func (c counts) total() int {
	return c.Killed + c.Lived + c.TimedOut + c.NotCovered + c.Other
}

// efficacy is KILLED over KILLED + LIVED, as a percentage; ok is false when
// no mutant was judged.
func (c counts) efficacy() (pct float64, ok bool) {
	judged := c.Killed + c.Lived
	if judged == 0 {
		return 0, false
	}
	return 100 * float64(c.Killed) / float64(judged), true
}

// add counts one mutation status.
func (c *counts) add(status string) {
	switch status {
	case "KILLED":
		c.Killed++
	case "LIVED":
		c.Lived++
	case "TIMED OUT":
		c.TimedOut++
	case "NOT VIABLE":
		c.NotViable++
	case "NOT COVERED":
		c.NotCovered++
	case "SKIPPED":
		c.Skipped++
	default:
		c.Other++
	}
}

// tally counts the mutation statuses of a gremlins report.
func tally(data []byte) (counts, error) {
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		return counts{}, err
	}
	var c counts
	for _, f := range r.Files {
		for _, m := range f.Mutations {
			c.add(m.Status)
		}
	}
	return c, nil
}

// reportName is the file a unit's report lives in: internal/domain ->
// internal-domain.json, and a part internal/server#ops ->
// internal-server-ops.json. ci/mutation.sh writes the same names.
func reportName(pkg string) string {
	return strings.NewReplacer("/", "-", partSep, "-").Replace(strings.Trim(pkg, "./")) + ".json"
}

// judge is one package's verdict: a failure message, or "" when it passes.
// diff is a diff-mode run (see the package comment).
func judge(cfg config, c counts, diff bool) string {
	if c.total() == 0 {
		return judgeEmpty(cfg, diff)
	}
	pct, ok := c.efficacy()
	if !ok {
		return judgeUnmeasured(c, diff)
	}
	if pct < cfg.Efficacy {
		return fmt.Sprintf("FAILED: efficacy %.2f%% is below the floor %.2f%%", pct, cfg.Efficacy)
	}
	return ""
}

// judgeEmpty is the verdict on a report with no mutant: a failure outside
// diff mode when the config asks for one; in diff mode the normal case.
func judgeEmpty(cfg config, diff bool) string {
	if cfg.FailOnNoMutants && !diff {
		return "FAILED: gremlins generated no mutant, so nothing was measured"
	}
	return ""
}

// judgeUnmeasured is the verdict when no mutant was judged: in diff mode
// only timed-out mutants pass (nothing uncovered), otherwise it fails.
func judgeUnmeasured(c counts, diff bool) string {
	if diff && c.NotCovered == 0 {
		return ""
	}
	return "FAILED: no mutant was judged (every mutant is not covered or timed out), so there is no efficacy"
}

// evaluatePackages gates the given packages (a lane, or all of them) and
// returns the report text and whether all of them passed. changed is nil
// outside diff mode; in diff mode it holds the units that changed, and a unit
// not in it passes without a report.
func evaluatePackages(cfg config, dir string, pkgs []string, changed map[string]bool) (string, bool) {
	if len(pkgs) == 0 {
		return "mutation-go gate: FAILED, no package is configured\n", false
	}
	diff := changed != nil
	var b strings.Builder
	allOK := true
	for _, pkg := range pkgs {
		if diff && !changed[pkg] {
			b.WriteString(pkg + ": no measured file changed, nothing to mutate: ok\n")
			continue
		}
		line, ok := evaluatePackage(cfg, dir, pkg, diff)
		b.WriteString(line)
		allOK = allOK && ok
	}
	if diff {
		fmt.Fprintf(&b, "mutation-go gate (diff mode): efficacy floor %.2f%% over the changed lines' mutants, zero mutants pass\n", cfg.Efficacy)
	} else {
		fmt.Fprintf(&b, "mutation-go gate: efficacy floor %.2f%%, zero mutants fail=%v\n", cfg.Efficacy, cfg.FailOnNoMutants)
	}
	return b.String(), allOK
}

func evaluatePackage(cfg config, dir, pkg string, diff bool) (string, bool) {
	path := filepath.Join(dir, reportName(pkg))
	data, err := os.ReadFile(path) //nolint:gosec // G304: the report path is the gate's own directory argument plus a name derived from the configured package
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("%s: FAILED, report %s missing: the package never ran (its unit was cut or failed before gremlins finished it)\nwhat to do: read the unit's log; ci/mutation.sh writes each package's report as soon as that package finishes\n", pkg, path), false
	}
	if err != nil {
		return fmt.Sprintf("%s: FAILED, report %s unreadable: %v\nwhat to do: ci/mutation.sh writes it\n", pkg, path, err), false
	}
	c, err := tally(data)
	if err != nil {
		return fmt.Sprintf("%s: FAILED, report %s is not a gremlins report: %v\n", pkg, path, err), false
	}
	pct, _ := c.efficacy()
	summary := fmt.Sprintf("%s: %d mutants (killed %d, lived %d, timed out %d, not covered %d, not viable %d), efficacy %.2f%%",
		pkg, c.total(), c.Killed, c.Lived, c.TimedOut, c.NotCovered, c.NotViable, pct)
	if diff {
		summary += fmt.Sprintf(" (%d outside the diff skipped)", c.Skipped)
	}
	if msg := judge(cfg, c, diff); msg != "" {
		return summary + "\n" + pkg + ": " + msg + "\n", false
	}
	return summary + ": ok\n", true
}

// excludes returns one anchored regexp per non-test .go file of the package
// that its scope leaves out, for gremlins' --exclude-files (matched against
// the file's path relative to the package directory, which is what gremlins
// matches). root is the module root; a package with no scope has no
// exclusion. A scoped file that does not exist is an error: a scope that
// names nothing real would silently measure less than it claims.
func excludes(cfg config, root, unit string) ([]string, error) {
	pkg, part := splitUnit(strings.Trim(unit, "./"))
	scope, ok := cfg.Scopes[pkg]
	if !ok {
		return nil, nil
	}
	if part != "" {
		scope, ok = cfg.Parts[pkg][part]
		if !ok {
			return nil, fmt.Errorf("%s: no part %q", pkg, part)
		}
	}
	present, err := nonTestGoFiles(filepath.Join(root, pkg))
	if err != nil {
		return nil, err
	}
	return outOfScope(pkg, scope, present)
}

// outOfScope returns one anchored regexp per file of present that scope does
// not list. A scoped file that is not in present, or a scope with no file, is
// an error.
func outOfScope(pkg string, scope []string, present map[string]bool) ([]string, error) {
	if len(scope) == 0 {
		return nil, fmt.Errorf("%s: its scope lists no file", pkg)
	}
	for _, f := range scope {
		if !present[f] {
			return nil, fmt.Errorf("%s: scoped file %s is not a non-test .go file of the package", pkg, f)
		}
		delete(present, f)
	}
	out := make([]string, 0, len(present))
	for n := range present {
		out = append(out, "^"+regexp.QuoteMeta(n)+"$")
	}
	sort.Strings(out)
	return out, nil
}

// unitFiles returns the slash-separated paths, relative to the module root,
// of the files a unit mutates: its part, its package's scope, or every
// non-test .go file of an unscoped package.
func unitFiles(cfg config, root, unit string) ([]string, error) {
	pkg, part := splitUnit(strings.Trim(unit, "./"))
	names, scoped := cfg.Scopes[pkg]
	if part != "" {
		names, scoped = cfg.Parts[pkg][part]
		if !scoped {
			return nil, fmt.Errorf("%s: no part %q", pkg, part)
		}
	}
	if !scoped {
		present, err := nonTestGoFiles(filepath.Join(root, pkg))
		if err != nil {
			return nil, err
		}
		names = sortedKeys(present)
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, path.Join(pkg, n))
	}
	return out, nil
}

// changedUnits returns the units among units with a measured file that
// differs between the merge base of ref and HEAD and the working tree: the
// same comparison gremlins --diff makes (git diff --merge-base ref).
func changedUnits(cfg config, root, ref string, units []string) (map[string]bool, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--no-renames", "--diff-filter=d", "--merge-base", ref) //nolint:gosec // G204: git diff against the ref the CI passes
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --merge-base %s: %w", ref, err)
	}
	diffed := map[string]bool{}
	for _, f := range strings.Fields(string(out)) {
		diffed[f] = true
	}
	changed := map[string]bool{}
	for _, u := range units {
		files, err := unitFiles(cfg, root, u)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(files, func(f string) bool { return diffed[f] }) {
			changed[u] = true
		}
	}
	return changed, nil
}

// nonTestGoFiles is the set of a directory's non-test .go file names.
func nonTestGoFiles(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			present[n] = true
		}
	}
	return present, nil
}

func loadConfig(path string) (config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the config path is the tool's own command-line argument
	if err != nil {
		return config{}, err
	}
	var all struct {
		MutationGo config `json:"mutation_go"`
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if !all.MutationGo.Enabled {
		return config{}, errors.New("mutation_go is not enabled in " + path + ": the gate is never skipped")
	}
	return all.MutationGo, nil
}

// run is main's testable core and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mutationgate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to ci/quality.json (required)")
	dir := fs.String("dir", "ci-results/mutation", "directory holding one gremlins JSON report per package")
	list := fs.Bool("list", false, "print the configured packages and exit")
	lane := fs.String("lane", allLane, "judge or list only this lane of mutation_go.lanes (or a comma-separated list of lanes)")
	excl := fs.String("excludes", "", "print, one per line, the gremlins --exclude-files regexps that keep `package` to its configured scope, and exit")
	unmeas := fs.Bool("unmeasured", false, "print each application file changed since phase_base that no unit measures; exit 1 if there is one")
	diffRef := fs.String("diff", "", "diff mode: only the units with a measured file changed since the merge base with `ref` (git diff --merge-base, as gremlins --diff)")
	staleRef := fs.String("stale", "", "print the units whose report made at `ref` may differ from one made now (incremental mutation); exit 3 when a shared input changed and every unit must run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, pkgs, changed, err := loadSelection(*cfgPath, *lane, *diffRef)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "mutationgate:", err)
		return 2
	}
	switch {
	case *staleRef != "":
		return printStale(cfg, *cfgPath, *staleRef, pkgs, stdout, stderr)
	case *list:
		printLines(stdout, onlyChanged(pkgs, changed))
	case *unmeas:
		return reportUnmeasured(cfg, stdout, stderr)
	case *excl != "":
		return printExcludes(cfg, *excl, stdout, stderr)
	default:
		return judgeReports(cfg, *dir, pkgs, changed, stdout)
	}
	return 0
}

// printExcludes prints one unit's gremlins --exclude-files regexps.
func printExcludes(cfg config, unit string, stdout, stderr io.Writer) int {
	lines, err := excludesFromCwd(cfg, unit)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "mutationgate:", err)
		return 2
	}
	printLines(stdout, lines)
	return 0
}

// judgeReports judges the packages' reports and prints the verdict; 1 on a failure.
func judgeReports(cfg config, dir string, pkgs []string, changed map[string]bool, stdout io.Writer) int {
	text, ok := evaluatePackages(cfg, dir, pkgs, changed)
	_, _ = fmt.Fprint(stdout, text)
	if !ok {
		return 1
	}
	return 0
}

// loadLane reads the config, checks its lanes and returns the packages of one
// lane.
func loadLane(cfgPath, lane string) (config, []string, error) {
	if cfgPath == "" {
		return config{}, nil, errors.New("-config is required")
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return config{}, nil, err
	}
	if err := cfg.checkLanes(); err != nil {
		return config{}, nil, err
	}
	pkgs, err := cfg.lanePackages(lane)
	return cfg, pkgs, err
}

// loadSelection is loadLane, then in diff mode (diffRef set) the lane's
// changed units; changed is nil outside diff mode.
func loadSelection(cfgPath, lane, diffRef string) (cfg config, pkgs []string, changed map[string]bool, err error) {
	cfg, pkgs, err = loadLane(cfgPath, lane)
	if err == nil && diffRef != "" {
		changed, err = changedFromCwd(cfg, diffRef, pkgs)
	}
	return cfg, pkgs, changed, err
}

// changedFromCwd is changedUnits with the working directory as the module root.
func changedFromCwd(cfg config, ref string, units []string) (map[string]bool, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return changedUnits(cfg, root, ref, units)
}

// onlyChanged keeps the units in changed, or all of them outside diff mode
// (changed nil).
func onlyChanged(units []string, changed map[string]bool) []string {
	if changed == nil {
		return units
	}
	var out []string
	for _, u := range units {
		if changed[u] {
			out = append(out, u)
		}
	}
	return out
}

// excludesFromCwd is excludes with the working directory as the module root.
func excludesFromCwd(cfg config, pkg string) ([]string, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return excludes(cfg, root, pkg)
}

// appRoots are the module directories that hold the product's application
// code; everything else (ci/, features/, e2e/) is tooling or tests.
var appRoots = []string{"internal", "cmd"}

// generatedRE is Go's marker for a generated file (go help generate).
var generatedRE = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// measures reports whether some unit of the config mutates file, a
// slash-separated path relative to the module root.
func (c config) measures(file string) bool {
	dir, name := path.Split(file)
	dir = strings.TrimSuffix(dir, "/")
	if !slices.Contains(c.Packages, dir) {
		return false
	}
	scope, ok := c.Scopes[dir]
	return !ok || slices.Contains(scope, name)
}

// changedSince lists the files under appRoots that differ between base and
// the working tree and still exist (git diff, deletions left out).
func changedSince(root, base string) ([]string, error) {
	if base == "" {
		return nil, errors.New("mutation_go.phase_base is not set, so the phase's changed files cannot be listed")
	}
	args := append([]string{"diff", "--name-only", "--no-renames", "--diff-filter=d", base, "--"}, appRoots...)
	cmd := exec.Command("git", args...) //nolint:gosec // G204: git diff against the base commit the repository's own config names
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", base, err)
	}
	return strings.Fields(string(out)), nil
}

// unmeasured returns, sorted, each handwritten application file changed
// since the config's phase_base (a non-test .go file that is not generated)
// that no unit measures.
func unmeasured(cfg config, root string) ([]string, error) {
	changed, err := changedSince(root, cfg.PhaseBase)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range changed {
		needs, err := needsUnit(cfg, root, f)
		if err != nil {
			return nil, err
		}
		if needs {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

// needsUnit reports whether a changed file is handwritten application code
// (a non-test, non-generated .go file) that no unit of cfg measures and that
// differs from phase_base by more than the rename (import paths, and the
// cmd/board/ to cmd/vh/ move).
func needsUnit(cfg config, root, f string) (bool, error) {
	if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") || cfg.measures(f) {
		return false, nil
	}
	if same, err := unchangedModuloRename(root, cfg.PhaseBase, f); err != nil || same {
		return false, err
	}
	generated, err := isGenerated(filepath.Join(root, filepath.FromSlash(f)))
	return !generated, err
}

// renamedModule is the module path every import is normalised to: the one the
// phase started with. It is written in two parts so that the module path of the
// repository, which no source file may name any more, does not appear in it.
const renamedModule = "github.com/n-orlov/" + "board/"

// oldCmdDir and newCmdDir are the one directory move the rename makes.
const (
	oldCmdDir = "cmd/board/"
	newCmdDir = "cmd/vh/"
)

// newModule is the module path the rename moved the repository to, written in
// two parts for the same reason as renamedModule.
const newModule = "github.com/n-orlov/" + "village-hall/"

// normaliseImports rewrites, in src, the path of each real Go import
// declaration that names a package of the renamed module back to the path the
// phase started with, so a file that differs only by the rename reads the same
// as its base. Only the parsed import specs are touched: a line inside a
// string literal or a comment that merely looks like an import stays as it is,
// and so does every other byte. Source that does not parse is returned as it
// is.
func normaliseImports(src string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return src
	}
	out := src
	for i := len(f.Imports) - 1; i >= 0; i-- {
		lit := f.Imports[i].Path
		start := fset.Position(lit.Pos()).Offset
		end := fset.Position(lit.End()).Offset
		val := src[start:end]
		if len(val) < 2 {
			continue
		}
		if rest, ok := strings.CutPrefix(val[1:len(val)-1], newModule); ok {
			out = out[:start] + val[:1] + renamedModule + rest + val[len(val)-1:] + out[end:]
		}
	}
	return out
}

// baseCounterpart is the path f had at the phase base: the same path, except
// that a file under cmd/vh/ was under cmd/board/.
func baseCounterpart(f string) string {
	if rest, ok := strings.CutPrefix(f, newCmdDir); ok {
		return oldCmdDir + rest
	}
	return f
}

// unchangedModuloRename reports whether the working-tree file f is, at the
// phase base, the same file apart from its import paths (board/ to
// village-hall/) and the cmd/board/ to cmd/vh/ move. A file the base does not
// hold at that counterpart path is a change.
func unchangedModuloRename(root, base, f string) (bool, error) {
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f))) //nolint:gosec // G304: a path git diff listed inside the repository
	if err != nil {
		return false, err
	}
	cmd := exec.Command("git", "show", base+":"+baseCounterpart(f)) //nolint:gosec // G204: the base commit the repository's own config names and a path git diff listed
	cmd.Dir = root
	old, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) { // git show exits non-zero when the base has no such file: the file is new
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return normaliseImports(string(cur)) == string(old), nil
}

// isGenerated reports whether the Go file at path carries the generated-code
// marker.
func isGenerated(path string) (bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a path git diff listed inside the repository
	if err != nil {
		return false, err
	}
	return generatedRE.Match(data), nil
}

// reportUnmeasured prints the unmeasured files of the working directory's
// module and returns the exit code: 0 none, 1 some, 2 an error.
func reportUnmeasured(cfg config, stdout, stderr io.Writer) int {
	root, err := os.Getwd()
	if err == nil {
		var files []string
		if files, err = unmeasured(cfg, root); err == nil {
			for _, f := range files {
				_, _ = fmt.Fprintf(stdout, "%s: changed since %s but no mutation unit measures it\n", f, cfg.PhaseBase)
			}
			if len(files) > 0 {
				return 1
			}
			return 0
		}
	}
	_, _ = fmt.Fprintln(stderr, "mutationgate:", err)
	return 2
}

func printLines(w io.Writer, lines []string) {
	for _, l := range lines {
		_, _ = fmt.Fprintln(w, l)
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
