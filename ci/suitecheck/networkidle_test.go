// networkidle_test.go keeps `networkidle` out of the Playwright code (task 028, R9 h): waiting for the browser's idle
// heuristic holds every test for a fixed quiet period whatever the page does, and a page with a held-open stream
// never gets there honestly. A spec waits for explicit readiness instead (a ready locator, then the request list it
// recorded reading the same on two consecutive polls, e2e/settle.ts). The check reads every *.ts file under e2e/
// (not node_modules, not the run's own out/) and fails on each that contains the word, comments included.
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

// filesWaitingForNetworkIdle names every *.ts file under dir that contains `networkidle`, as a path relative to dir.
func filesWaitingForNetworkIdle(t *testing.T, dir string) []string {
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
		raw, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToLower(string(raw)), "networkidle") {
			offenders = append(offenders, filepath.FromSlash(path))
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

func TestNoE2EFileWaitsForNetworkIdle(t *testing.T) {
	dir := os.Getenv("SUITECHECK_E2E_DIR")
	if dir == "" {
		root, err := repositoryRoot()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(root, "e2e")
	}
	if offenders := filesWaitingForNetworkIdle(t, dir); len(offenders) > 0 {
		t.Errorf("%d file(s) under %s wait for `networkidle`; wait for a ready locator and the recorded requests settling instead (e2e/settle.ts): %s",
			len(offenders), dir, strings.Join(offenders, ", "))
	}
}

// TestNetworkIdleCheckNamesEveryForm writes files to a temporary directory: a wait in any .ts file (a spec, a helper,
// the config, a subdirectory) or in a comment is named; node_modules, out/ and other file types are left alone.
func TestNetworkIdleCheckNamesEveryForm(t *testing.T) {
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
	write("wait.spec.ts", "await page.waitForLoadState('networkidle');\n")
	write("goto.spec.ts", "await page.goto('/', { waitUntil: \"networkidle\" });\n")
	write("helper.ts", "export const idle = (p) => p.waitForLoadState('NetworkIdle');\n")
	write("sub/nested.spec.ts", "// then wait for networkidle\n")
	write("clean.spec.ts", "await expect(page.getByTestId('x')).toBeVisible();\nawait page.waitForLoadState('load');\n")
	write("node_modules/dep/index.ts", "networkidle\n")
	write("out/report.ts", "networkidle\n")
	write("notes.md", "networkidle\n")
	got := strings.Join(filesWaitingForNetworkIdle(t, dir), " ")
	want := "goto.spec.ts helper.ts " + filepath.Join("sub", "nested.spec.ts") + " wait.spec.ts"
	if got != want {
		t.Errorf("offenders = %q, want %q", got, want)
	}
}
