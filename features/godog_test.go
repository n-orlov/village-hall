package features

import (
	"context"
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
// each optionally suffixed ":<line>") narrows the run to those files, and
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
				Paths:       godogPaths(),
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

func godogPaths() []string {
	var paths []string
	for _, p := range strings.Split(os.Getenv("BOARD_GODOG_PATHS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return []string{"."}
	}
	return paths
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
