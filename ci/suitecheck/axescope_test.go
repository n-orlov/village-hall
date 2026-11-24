// axescope_test.go keeps the feature specs' axe scans on the component under test (task 031, R9 k): a full-page
// `new AxeBuilder({ page }).analyze()` walks every node of the shell around the component again in each light and
// dark test, and only e2e/a11y.spec.ts is there to scan whole pages. Every other spec's AxeBuilder statement must
// carry `.include(` before its `.analyze()`. The check reads every *.ts file under e2e/ except a11y.spec.ts (not
// node_modules, not the run's own out/), comments included.
//
// SUITECHECK_E2E_DIR points the check at another e2e directory (a checkout of an older commit, say); it defaults to
// the repository's own e2e/.
package suitecheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	axeBuilderCall = "new AxeBuilder("
	axeInclude     = ".include("
	axeAnalyze     = ".analyze("
	wholePageSpec  = "a11y.spec.ts"
)

// unscopedAxeScans counts the AxeBuilder statements in src that reach `.analyze(` without an `.include(` between.
func unscopedAxeScans(src string) int {
	unscoped := 0
	for rest := src; ; {
		at := strings.Index(rest, axeBuilderCall)
		if at < 0 {
			return unscoped
		}
		rest = rest[at+len(axeBuilderCall):]
		statement := rest
		if end := strings.Index(statement, axeAnalyze); end >= 0 {
			statement = statement[:end]
		}
		if next := strings.Index(statement, axeBuilderCall); next >= 0 {
			statement = statement[:next]
		}
		if !strings.Contains(statement, axeInclude) {
			unscoped++
		}
	}
}

// specsWithWholePageAxeScans names every *.ts file under dir other than a11y.spec.ts that builds an AxeBuilder
// without `.include(`, with the number of such scans, as a path relative to dir.
func specsWithWholePageAxeScans(t *testing.T, dir string) []string {
	t.Helper()
	var offenders []string
	seen := 0
	files := os.DirFS(dir)
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "out" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".ts") {
			return nil
		}
		seen++
		if path == wholePageSpec {
			return nil
		}
		raw, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		if n := unscopedAxeScans(string(raw)); n > 0 {
			offenders = append(offenders, filepath.FromSlash(path)+" ("+strings.Repeat("+", n)+")")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatalf("no *.ts file under %s", dir)
	}
	sort.Strings(offenders)
	return offenders
}

func TestFeatureSpecAxeScansAreScopedToTheComponent(t *testing.T) {
	dir := os.Getenv("SUITECHECK_E2E_DIR")
	if dir == "" {
		root, err := repositoryRoot()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(root, "e2e")
	}
	if offenders := specsWithWholePageAxeScans(t, dir); len(offenders) > 0 {
		t.Errorf("%d file(s) under %s build an AxeBuilder without `.include(` (one + per full-page scan); scope the scan to the component under test, only %s scans whole pages: %s",
			len(offenders), dir, wholePageSpec, strings.Join(offenders, ", "))
	}
}

// TestAxeScopeCheckNamesEveryUnscopedScan writes files to a temporary directory: a full-page scan in any .ts file
// but a11y.spec.ts is named, once per scan, whatever other builder calls it chains; scoped scans, a11y.spec.ts,
// node_modules, out/ and other file types are left alone.
func TestAxeScopeCheckNamesEveryUnscopedScan(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a11y.spec.ts", "await new AxeBuilder({ page }).analyze();\n")
	write("whole.spec.ts", "const r = await new AxeBuilder({ page }).analyze();\n")
	write("rules.spec.ts", "await new AxeBuilder({ page }).withRules(['color-contrast']).analyze();\n")
	write("sub/two.spec.ts", "await new AxeBuilder({ page }).analyze();\nawait new AxeBuilder({ page }).include('#a').analyze();\nawait new AxeBuilder({ page })\n  .analyze();\n")
	write("scoped.spec.ts", "await new AxeBuilder({ page }).include('[data-testid=x]').analyze();\nawait new AxeBuilder({ page })\n  .include(scope)\n  .withTags(['wcag2a'])\n  .analyze();\n")
	write("node_modules/dep/index.ts", "new AxeBuilder({ page }).analyze();\n")
	write("out/report.ts", "new AxeBuilder({ page }).analyze();\n")
	write("notes.md", "new AxeBuilder({ page }).analyze();\n")
	got := strings.Join(specsWithWholePageAxeScans(t, dir), " ")
	want := "rules.spec.ts (+) " + filepath.Join("sub", "two.spec.ts") + " (++) whole.spec.ts (+)"
	if got != want {
		t.Errorf("offenders = %q, want %q", got, want)
	}
}
