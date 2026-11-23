// e2eimports_test.go keeps every Playwright spec on the per-worker server (task 001, R4): each Playwright worker
// starts its own board server, port, work directory, SQLite store and API key, and a spec only reaches it through
// the `test` that e2e/fixtures.ts exports. A spec that takes `test` from '@playwright/test' instead runs against
// no worker server at all, so this reads the specs and fails on every one that does.
//
// SUITECHECK_E2E_DIR points the check at another e2e directory (a checkout of an older commit, say); it defaults to
// the repository's own e2e/.
package suitecheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// playwrightImport matches an import or re-export whose braces name `test` (also as `test as base`) and
// whose source is '@playwright/test'; the `type` modifier does not matter, a type-only name is never `test`.
var playwrightImport = regexp.MustCompile(`(?s)\b(?:import|export)\s+(?:type\s+)?(\{[^}]*\}|\*\s*as\s+\w+|\w+)\s*(?:,\s*\{[^}]*\}\s*)?from\s*['"]@playwright/test['"]`)

var testName = regexp.MustCompile(`(^|[\s,{])test(\s+as\s+\w+)?\s*($|[\s,}])`)

// specsTakingPlaywrightTest names every e2e/*.spec.ts under dir that takes `test` from '@playwright/test'.
func specsTakingPlaywrightTest(t *testing.T, dir string) []string {
	t.Helper()
	files := os.DirFS(dir)
	specs, err := fs.Glob(files, "*.spec.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) == 0 {
		t.Fatalf("no *.spec.ts under %s", dir)
	}
	var offenders []string
	for _, spec := range specs {
		raw, err := fs.ReadFile(files, spec)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range playwrightImport.FindAllStringSubmatch(string(raw), -1) {
			clause := m[1]
			whole := m[0]
			if strings.HasPrefix(whole, "import") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(whole, "import")), "type") {
				continue
			}
			if !strings.HasPrefix(clause, "{") || testName.MatchString(strings.Trim(clause, "{}")) {
				offenders = append(offenders, filepath.Base(spec))
				break
			}
		}
	}
	sort.Strings(offenders)
	return offenders
}

func TestE2ESpecsTakeTestFromFixtures(t *testing.T) {
	dir := os.Getenv("SUITECHECK_E2E_DIR")
	if dir == "" {
		root, err := repositoryRoot()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(root, "e2e")
	}
	if offenders := specsTakingPlaywrightTest(t, dir); len(offenders) > 0 {
		t.Errorf("%d spec(s) under %s take `test` from '@playwright/test'; import it from './fixtures' (the per-worker server): %s",
			len(offenders), dir, strings.Join(offenders, ", "))
	}
}

// TestE2EImportCheckNamesEveryForm runs the check on specs written to a temporary directory: each way of taking
// `test` from '@playwright/test' is named, and the ways that are fine are not.
func TestE2EImportCheckNamesEveryForm(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("direct.spec.ts", "import { expect, test, type Page } from '@playwright/test';\n")
	write("aliased.spec.ts", "import { expect, test as base } from '@playwright/test';\n")
	write("alone.spec.ts", "import { test } from '@playwright/test';\n")
	write("multiline.spec.ts", "import {\n  expect,\n  test,\n} from \"@playwright/test\";\n")
	write("namespace.spec.ts", "import * as pw from '@playwright/test';\n")
	write("reexport.spec.ts", "export { test } from '@playwright/test';\n")
	write("fixtures.spec.ts", "import { expect } from '@playwright/test';\nimport { test } from './fixtures';\n")
	write("aliasfixtures.spec.ts", "import { type Page, expect } from '@playwright/test';\nimport { test as base } from './fixtures';\n")
	write("typeonly.spec.ts", "import type { Page, test as T } from '@playwright/test';\nimport { test } from './fixtures';\n")
	write("testlike.spec.ts", "import { expect, testInfo, type Page } from '@playwright/test';\nimport { test } from './fixtures';\n")
	got := strings.Join(specsTakingPlaywrightTest(t, dir), " ")
	want := "aliased.spec.ts alone.spec.ts direct.spec.ts multiline.spec.ts namespace.spec.ts reexport.spec.ts"
	if got != want {
		t.Errorf("offenders = %q, want %q", got, want)
	}
}

// projectBaseURL matches a read of the config's static baseURL (`info.project.use.baseURL`): that address is the
// config's own, and no server listens there once every worker starts its own, so a spec reaches its worker's
// server through the `baseURL` fixture instead.
var projectBaseURL = regexp.MustCompile(`\.use\.baseURL\b`)

// specsReadingProjectBaseURL names every e2e/*.spec.ts under dir that reads the config's static baseURL.
func specsReadingProjectBaseURL(t *testing.T, dir string) []string {
	t.Helper()
	files := os.DirFS(dir)
	specs, err := fs.Glob(files, "*.spec.ts")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, spec := range specs {
		raw, err := fs.ReadFile(files, spec)
		if err != nil {
			t.Fatal(err)
		}
		if projectBaseURL.Match(raw) {
			offenders = append(offenders, filepath.Base(spec))
		}
	}
	sort.Strings(offenders)
	return offenders
}

func TestE2ESpecsReachTheirWorkersServerByTheBaseURLFixture(t *testing.T) {
	dir := os.Getenv("SUITECHECK_E2E_DIR")
	if dir == "" {
		root, err := repositoryRoot()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(root, "e2e")
	}
	if offenders := specsReadingProjectBaseURL(t, dir); len(offenders) > 0 {
		t.Errorf("%d spec(s) under %s read `info.project.use.baseURL`, the address no worker serves; take `baseURL` from the test's fixtures: %s",
			len(offenders), dir, strings.Join(offenders, ", "))
	}
}

func TestE2EBaseURLCheckNamesTheStaticAddressOnly(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("static.spec.ts", "const api = await playwright.request.newContext({ baseURL: info.project.use.baseURL });\n")
	write("fixture.spec.ts", "test.beforeAll(async ({ playwright, baseURL }) => { await playwright.request.newContext({ baseURL }); });\n")
	got := strings.Join(specsReadingProjectBaseURL(t, dir), " ")
	if got != "static.spec.ts" {
		t.Errorf("offenders = %q, want %q", got, "static.spec.ts")
	}
}
