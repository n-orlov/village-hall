package features

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

// godogConcurrency is how many scenarios of the default suite run at once. It
// is set here and nowhere else (ci/suitecheck pins it above 1): every scenario
// builds its own harness, its own temporary directory, its own server on its own
// free port and its own database, so scenarios share only the compiled binary,
// the leg's one Postgres container (a database each) and the output formatters,
// which take their own locks. The number is also bounded from above: the Suite
// unit shares the agent with E2E, and four scenarios at once took the agent's
// 1-minute load past R6's ceiling of 12 (each scenario starts a server and
// drives a Postgres container that no CPU cap reaches), so it is two.
const godogConcurrency = 2

// Scenarios that must not overlap another scenario run in a second suite of one
// scenario at a time: the ones tagged @dedicated-database start (and stop) a
// Postgres container of their own, and the ones tagged @serve-upgrade (every
// scenario of serve_upgrade.feature) restart `vh serve` on a database they seed
// themselves.
const (
	tagDedicated    = "@dedicated-database"
	tagServeUpgrade = "@serve-upgrade"
)

// featureSuite is one godog run: the scenarios its tag filter selects, run
// concurrency at a time.
type featureSuite struct {
	name        string
	concurrency int
	tags        string
}

// featureSuites are the runs TestFeatures makes, in order: every scenario that
// can share the machine concurrently, then the ones that cannot, serially. A
// scenario belongs to exactly one of them.
var featureSuites = []featureSuite{
	{name: "concurrent", concurrency: godogConcurrency, tags: "~" + tagDedicated + " && ~" + tagServeUpgrade},
	{name: "serial", concurrency: 1, tags: tagDedicated + "," + tagServeUpgrade},
}

// legTags narrows a suite's tag filter to the scenarios the leg runs: the
// Postgres leg runs everything the filter selects, and the SQLite leg leaves out
// the scenarios tagged @postgres.
func legTags(tags, leg string) string {
	if leg == "postgres" {
		return tags
	}
	terms := strings.Split(tags, ",")
	for i, term := range terms {
		terms[i] = term + " && ~@postgres"
	}
	return strings.Join(terms, ",")
}

// TestFeatures runs the Godog scenarios against the real binary.
//
// BOARD_GODOG_PATHS (comma separated feature files relative to this package,
// each optionally suffixed ":<line>", or a bundle of godogBundles; see
// godogPaths) narrows the run to those files, and
// BOARD_DB_LEG (sqlite, the default, or postgres) selects the database the
// server under test runs on. Unset, the whole directory runs on SQLite. A
// scenario tagged @postgres runs on the Postgres leg only.
//
// The scenarios run in the suites of featureSuites: a harness is built for each
// scenario, so nothing a scenario leaves behind reaches the next one.
func TestFeatures(t *testing.T) {
	leg, err := dbLeg()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := godogPaths()
	if err != nil {
		t.Fatal(err)
	}
	bin := buildBoard(t)
	var shared *postgresServer
	if leg == "postgres" {
		server, err := startPostgres(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { server.terminate(context.Background()) })
		shared = server
	}
	failed := false
	for i, fs := range featureSuites {
		suite := godog.TestSuite{
			ScenarioInitializer: func(sc *godog.ScenarioContext) {
				registerSteps(sc, &harness{binary: bin, leg: leg, shared: shared})
			},
			Options: &godog.Options{
				Format:      godogFormatFor(i),
				Paths:       paths,
				Strict:      true,
				Tags:        legTags(fs.tags, leg),
				Concurrency: fs.concurrency,
				TestingT:    t,
			},
		}
		if suite.Run() != 0 {
			failed = true
		}
	}
	if failed {
		t.Fatal("godog feature suite failed")
	}
}

// godogBundles are named selectors BOARD_GODOG_PATHS accepts besides feature
// files: each expands to the existing files it names, and those files run as
// if they had been listed one by one. They copy no scenario and add nothing to
// the default suite, which still runs the whole directory.
var godogBundles = map[string][]string{
	// keys.feature: every scenario about API keys, from the admin CLI, the
	// HTTP API and the web UI.
	"keys.feature": {"admin_keys.feature", "api_keys.feature", "ui_keys.feature"},
}

// godogPaths returns the paths TestFeatures hands godog: "." (the whole
// directory) when BOARD_GODOG_PATHS is unset, else the comma separated
// selectors it names. A selector may carry the repository's "features/" prefix
// (features/health.feature names health.feature), and a bundle of godogBundles
// expands to its members; a path named twice runs once. Every resulting path
// must exist inside this package's directory (after any ":<line>" suffix is
// set aside): a selector naming no file or bundle, or a bundle whose member is
// missing, is an error, never a smaller run.
func godogPaths() ([]string, error) {
	return resolveGodogPaths(os.Getenv("BOARD_GODOG_PATHS"), ".", godogBundles)
}

// resolveGodogPaths is godogPaths for the selector list spec, resolved against
// the directory dir and the bundles given.
func resolveGodogPaths(spec, dir string, bundles map[string][]string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	root := os.DirFS(dir)
	add := func(p string) error {
		file := p
		if i := strings.LastIndex(p, ".feature:"); i >= 0 {
			file = p[:i+len(".feature")]
		}
		if _, err := fs.Stat(root, filepath.ToSlash(filepath.Clean(file))); err != nil {
			return fmt.Errorf("BOARD_GODOG_PATHS: %q names no feature file in %s: %w", p, dir, err)
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
		return nil
	}
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(p), "features/")
		if p == "" {
			continue
		}
		members, isBundle := bundles[p]
		if !isBundle {
			members = []string{p}
		}
		for _, m := range members {
			if err := add(m); err != nil {
				return nil, err
			}
		}
	}
	if len(paths) == 0 {
		return []string{"."}, nil
	}
	return paths, nil
}

// godogFormat returns the godog Format string the first suite of TestFeatures
// uses: exactly "pretty" unless BOARD_GODOG_JUNIT names a path (godog's own
// JUnit formatter then writes one <testcase> per scenario there) or
// BOARD_GODOG_ALLURE names an absolute allure-results directory (the in-repo
// "allure" formatter, allure_formatter_test.go, writes Allure 2 results there).
// ci/suite.sh sets both; a local run that sets neither is unaffected.
func godogFormat() string { return godogFormatFor(0) }

// godogFormatFor is godogFormat for the suite at index i of featureSuites. Each
// run of godog's JUnit formatter writes its whole file, so every suite after
// the first writes the file of its own, named like the first with "-<suite
// name>" before the extension (junit-features.xml, junit-features-serial.xml);
// ci/suite.sh merges them.
func godogFormatFor(i int) string {
	format := "pretty"
	if path := strings.TrimSpace(os.Getenv("BOARD_GODOG_JUNIT")); path != "" {
		if i > 0 {
			ext := filepath.Ext(path)
			path = strings.TrimSuffix(path, ext) + "-" + featureSuites[i].name + ext
		}
		format += ",junit:" + path
	}
	if dir := envAbsPath(allureDirEnv); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err == nil {
			// godog needs a file for every "name:path" part: a one-line summary.
			format += ",allure:" + filepath.Join(dir, "godog-allure-summary.txt")
		}
	}
	return format
}

// envAbsPath returns the cleaned absolute path exported in the named
// environment variable, or "" when it is unset, blank or not absolute.
func envAbsPath(name string) string {
	p := strings.TrimSpace(os.Getenv(name))
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(p)
}
