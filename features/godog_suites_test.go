package features

import (
	"path/filepath"
	"strings"
	"testing"
)

// The SQLite leg leaves out @postgres scenarios in every suite, whichever
// alternative of the suite's own filter selects a scenario; the Postgres leg
// runs the filter as written.
func TestLegTagsNarrowEverySuiteToTheLegsScenarios(t *testing.T) {
	cases := []struct{ tags, leg, want string }{
		{"~@a && ~@b", "sqlite", "~@a && ~@b && ~@postgres"},
		{"~@a && ~@b", "postgres", "~@a && ~@b"},
		{"@a,@b", "sqlite", "@a && ~@postgres,@b && ~@postgres"},
		{"@a,@b", "postgres", "@a,@b"},
	}
	for _, c := range cases {
		if got := legTags(c.tags, c.leg); got != c.want {
			t.Errorf("legTags(%q, %q) = %q, want %q", c.tags, c.leg, got, c.want)
		}
	}
}

// Every suite after the first writes Godog's JUnit to a file of its own, named
// for the suite beside the first's, because each run of the formatter writes
// its whole file.
func TestEverySuiteWritesItsOwnJUnitFile(t *testing.T) {
	dir := t.TempDir()
	junit := filepath.Join(dir, "junit-features.xml")
	t.Setenv("BOARD_GODOG_JUNIT", junit)
	t.Setenv(allureDirEnv, "")
	if got, want := godogFormatFor(0), "pretty,junit:"+junit; got != want {
		t.Errorf("suite 0 format = %q, want %q", got, want)
	}
	seen := map[string]bool{}
	for i, s := range featureSuites {
		format := godogFormatFor(i)
		if seen[format] {
			t.Errorf("suite %q (%d) shares its format %q with an earlier suite", s.name, i, format)
		}
		seen[format] = true
		if i > 0 && !strings.Contains(format, "junit-features-"+s.name+".xml") {
			t.Errorf("suite %q format = %q, want junit-features-%s.xml in it", s.name, format, s.name)
		}
	}
	if len(featureSuites) < 2 {
		t.Errorf("%d suite(s), want the concurrent one and the serial one", len(featureSuites))
	}
}
