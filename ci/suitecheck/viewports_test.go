// viewports_test.go pins the logic/layout viewport split of the browser specs (task 029, R9 i, SPEC 15.2). A spec with no
// viewport-dependent assertion runs at the desktop width only, through the one shared `logicViewports` of
// e2e/viewports.ts; a spec with any viewport-dependent assertion, touch interaction or layout check keeps its phone
// variant. phoneDroppedSpecs is the explicit list of the specs that dropped it. The checks:
//   - every listed spec imports logicViewports, declares no viewport list or size literal of its own, and contains none
//     of the viewport-dependent patterns in viewportDependence (comments are not code and are left out);
//   - every e2e file that uses logicViewports is on the list, so a spec cannot drop its phone variant unlisted;
//   - none of the specs that must keep their phone variant (mustKeepPhone) is on the list.
//
// SUITECHECK_E2E_DIR points the check at another e2e directory; it defaults to the repository's own e2e/.
package suitecheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// phoneDroppedSpecs are the specs that run at the desktop width only. Adding a spec here means reading it for the
// patterns of viewportDependence; the test below does the mechanical half of that reading.
var phoneDroppedSpecs = []string{
	"actors-chips.spec.ts",
	"actors.spec.ts",
	"archive-chips.spec.ts",
	"archive-sources.spec.ts",
	"archive.spec.ts",
	"asker-waiting.spec.ts",
	"chip-change-superseded.spec.ts",
	"chip-fresh-sources.spec.ts",
	"chip-list-owner.spec.ts",
	"chip-restore-superseded.spec.ts",
	"chip-source-order.spec.ts",
	"chip-source-owner.spec.ts",
	"chip-sources.spec.ts",
	"initiative_attention.spec.ts",
	"needs-me-view.spec.ts",
	"needs_attention.spec.ts",
	"notes-view.spec.ts",
	"notes.spec.ts",
	"now-board-sources.spec.ts",
	"now-catalog-order.spec.ts",
	"now-live-catalog.spec.ts",
	"now-live-sources.spec.ts",
	"origin-all.spec.ts",
	"origin-recent.spec.ts",
	"origin.spec.ts",
	"storage-migration.spec.ts",
	"tasks.spec.ts",
	"timeline-window.spec.ts",
}

// mustKeepPhone are the specs whose subject is layout, touch or a viewport-dependent behaviour: none may be on the
// list above.
var mustKeepPhone = []string{
	"shell.spec.ts", "dialogs.spec.ts", "touch.spec.ts", "density.spec.ts", "density-lists.spec.ts", "overflow.spec.ts", "wide.spec.ts",
	"timeline-phone.spec.ts", "a11y.spec.ts", "keys.spec.ts", "keys-view.spec.ts", "initiatives.spec.ts",
	"initiatives-view.spec.ts", "history_restore.spec.ts", "task_page.spec.ts", "tasks-view.spec.ts", "needs-me.spec.ts",
	"timeline.spec.ts", "timeline-active.spec.ts", "timeline-rows.spec.ts",
}

// viewportDependence names the patterns that make a spec viewport-dependent: a branch on the viewport's name, a read of
// its size, its nav or touch field, a touch interaction, a phone-only locator (the tab bar, the More entry), a width,
// scroll or position measurement, a computed style, an axe scan, or a `phone` of its own.
var viewportDependence = []struct {
	name    string
	pattern *regexp.Regexp // VAR stands for the spec's viewport loop variable
}{
	{"a branch on the viewport name (`.name ===`)", regexp.MustCompile(`\bVAR\.name\s*[!=]==?`)},
	{"a read of the viewport size", regexp.MustCompile(`\bVAR\.size\b`)},
	{"the viewport's nav or touch field", regexp.MustCompile(`\bVAR\.(nav|touch)\b`)},
	{"a touch interaction", regexp.MustCompile(`hasTouch|isMobile|\.tap\(|dispatchTouchEvent`)},
	{"a phone-only locator (tab bar, rail, sidebar, More)", regexp.MustCompile(`tabbar|nav-rail|nav-sidebar|name:\s*'More'`)},
	{"a position or size measurement (boundingBox)", regexp.MustCompile(`boundingBox\(`)},
	{"a width, height or scroll measurement", regexp.MustCompile(`scroll(Width|Height|Top|Left|X|Y|To)|clientWidth|clientHeight|innerWidth|innerHeight|offsetWidth`)},
	{"a computed style or media query", regexp.MustCompile(`getComputedStyle|matchMedia`)},
	{"an axe scan", regexp.MustCompile(`AxeBuilder`)},
	{"a viewport of its own (`phone`, `PHONE`, a size literal, setViewportSize)", regexp.MustCompile(`\bphone\b|\bPHONE\b|\bsize:\s*\{|setViewportSize|viewport:\s*\{`)},
}

var (
	tsBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	tsLineComment  = regexp.MustCompile(`(?m)(^|\s)//[^\n]*`)
	// The describe title every viewport loop writes, and the two ways the loop hands the size to the browser.
	allowedSizeUse   = regexp.MustCompile(`\$\{\w+\.size\.width\}x\$\{\w+\.size\.height\}|viewport:\s*\w+\.size\b|setViewportSize\(\w+\.size\)`)
	viewportLoop     = regexp.MustCompile(`for\s*\(const\s+(\w+)\s+of\s+logicViewports\)`)
	logicImport      = regexp.MustCompile(`import\s*\{[^}]*\blogicViewports\b[^}]*\}\s*from\s*'\./viewports'`)
	viewportListDecl = regexp.MustCompile(`\b(const|let|var)\s+\w*[vV]iewports\s*=`)
)

func stripComments(code string) string {
	return tsLineComment.ReplaceAllString(tsBlockComment.ReplaceAllString(code, ""), "$1")
}

// loopVariable is the spec's viewport loop variable (`for (const vp of logicViewports)`), or the two names the specs
// use for it when the file has no such loop (a snippet, a spec that does not import the module).
func loopVariable(code string) string {
	if m := viewportLoop.FindStringSubmatch(code); m != nil {
		return m[1]
	}
	return "vp|viewport"
}

// dependenceOf returns the patterns of viewportDependence that code (a spec's text) contains. The standard uses of
// `vp.size` (the describe title, `viewport: vp.size`, `setViewportSize(viewport.size)`) are not a read of the size;
// the loop variable's `.name` in a template string is not a branch either, because only comparisons are matched, and
// another object's `.name` or `.size` (a key's name, a Set's size) is not the viewport's.
func dependenceOf(code string) []string {
	code = allowedSizeUse.ReplaceAllString(stripComments(code), "")
	variable := "(?:" + loopVariable(code) + ")"
	var found []string
	for _, p := range viewportDependence {
		if regexp.MustCompile(strings.ReplaceAll(p.pattern.String(), "VAR", variable)).MatchString(code) {
			found = append(found, p.name)
		}
	}
	return found
}

// specProblem is what is wrong with one phone-dropped spec's text, or "".
func specProblem(code string) string {
	switch {
	case !logicImport.MatchString(code):
		return "does not import logicViewports from './viewports'"
	case viewportListDecl.MatchString(stripComments(code)):
		return "declares a viewport list of its own"
	}
	if found := dependenceOf(code); len(found) > 0 {
		return "has viewport-dependent code, so it keeps its phone variant: " + strings.Join(found, "; ")
	}
	return ""
}

func readIfPresent(dir, name string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	return string(raw), err
}

// listProblems checks the phone-dropped list against the directory: each listed spec exists and passes specProblem,
// none is a spec that must keep the phone, and no file outside the list uses logicViewports.
func listProblems(dir string, list, keep []string) []string {
	var problems []string
	listed := map[string]bool{}
	for _, name := range list {
		listed[name] = true
		code, err := readIfPresent(dir, name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
		} else if why := specProblem(code); why != "" {
			problems = append(problems, name+": "+why)
		}
	}
	for _, name := range keep {
		if listed[name] {
			problems = append(problems, name+": must keep its phone variant, but is on the phone-dropped list")
		}
	}
	return append(problems, unlistedUsers(dir, listed)...)
}

// unlistedUsers names every e2e/*.ts file (other than viewports.ts itself) that uses logicViewports without being on
// the list.
func unlistedUsers(dir string, listed map[string]bool) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
	sort.Strings(files)
	var problems []string
	for _, path := range files {
		name := filepath.Base(path)
		raw, _ := os.ReadFile(path)
		if name != "viewports.ts" && !listed[name] && strings.Contains(string(raw), "logicViewports") {
			problems = append(problems, name+": uses logicViewports but is not on the phone-dropped list")
		}
	}
	return problems
}

func e2eDirectory(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("SUITECHECK_E2E_DIR"); dir != "" {
		return dir
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "e2e")
}

// TestPhoneDroppedSpecsUseLogicViewports holds the explicit list of phone-dropped specs against the real e2e directory.
func TestPhoneDroppedSpecsUseLogicViewports(t *testing.T) {
	dir := e2eDirectory(t)
	if len(phoneDroppedSpecs) == 0 {
		t.Fatal("the phone-dropped list is empty")
	}
	for _, problem := range listProblems(dir, phoneDroppedSpecs, mustKeepPhone) {
		t.Error(problem)
	}
	code, err := readIfPresent(dir, "viewports.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export const logicViewports = [desktop]", "export const layoutViewports = [phone, desktop]"} {
		if !strings.Contains(code, want) {
			t.Errorf("viewports.ts must contain `%s`", want)
		}
	}
}

// TestViewportDependenceNamesEveryPattern feeds one snippet per pattern, and the spec shapes that are allowed.
func TestViewportDependenceNamesEveryPattern(t *testing.T) {
	flagged := map[string]string{
		"a branch on the viewport name":   "if (vp.name === 'phone') { await x(); }",
		"a negated branch":                "const wide = vp.name !== 'desktop';",
		"a read of the viewport size":     "if (vp.size.width < 768) await y();",
		"the nav field":                   "page.getByTestId(vp.nav)",
		"the touch field":                 "test.use({ viewport: vp.size, hasTouch: vp.touch });",
		"a tap":                           "await page.getByRole('button').tap();",
		"a tab bar locator":               "page.getByTestId('nav-tabbar')",
		"the More entry":                  "page.getByRole('button', { name: 'More' })",
		"a bounding box":                  "const box = await el.boundingBox();",
		"a scroll width":                  "document.documentElement.scrollWidth <= 1",
		"a scroll position":               "window.scrollTo(0, 1)",
		"a computed style":                "getComputedStyle(el)",
		"an axe scan":                     "await new AxeBuilder({ page }).analyze();",
		"a phone constant":                "const phone = vp.name;",
		"a size literal":                  "{ name: 'x', size: { width: 1, height: 2 } }",
		"a viewport set by hand":          "await page.setViewportSize({ width: 1, height: 2 });",
		"a viewport literal in a use":     "test.use({ viewport: { width: 1, height: 2 } });",
		"a name comparison with one `=`s": "vp.name == 'phone'",
	}
	for what, snippet := range flagged {
		if len(dependenceOf(snippet)) == 0 {
			t.Errorf("%s is not flagged: %s", what, snippet)
		}
	}
	allowed := []string{
		"test.describe(`${vp.name} ${vp.size.width}x${vp.size.height}`, () => {",
		"test.use({ viewport: vp.size });",
		"test.beforeEach(async ({ page }) => page.setViewportSize(viewport.size));",
		"const slug = `e2e-x-${vp.name}`;",
		"// at the phone: vp.name === 'phone', boundingBox, scrollWidth\nconst a = 1;",
		"/* if (vp.name === 'phone') */ const b = 2;",
		"await page.goto('http://localhost/'); const c = 3;",
	}
	for _, snippet := range allowed {
		if found := dependenceOf(snippet); len(found) > 0 {
			t.Errorf("an allowed shape is flagged (%s): %s", strings.Join(found, "; "), snippet)
		}
	}
}

// TestSpecProblemNamesWhatIsWrong covers the two other ways a listed spec fails.
func TestSpecProblemNamesWhatIsWrong(t *testing.T) {
	good := "import { logicViewports } from './viewports';\nfor (const vp of logicViewports) {}\n"
	cases := []struct {
		name, code, want string
	}{
		{"clean", good, ""},
		{"no import", "for (const vp of viewports) {}\n", "does not import logicViewports"},
		{"another module", "import { logicViewports } from './other';\n", "does not import logicViewports"},
		{"own list", good + "const viewports = [desktop];\n", "declares a viewport list of its own"},
		{"own list, commented", good + "// const viewports = [desktop];\n", ""},
		{"a branch", good + "if (vp.name === 'phone') {}\n", "keeps its phone variant"},
	}
	for _, c := range cases {
		got := specProblem(c.code)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: specProblem = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

// TestPhoneDroppedListFailsOnADoctoredEntry builds a small e2e directory and doctors the list: an entry whose spec has a
// `vp.name ===` branch, one that does not import logicViewports, a missing file, a spec that must keep its phone, and
// a spec that uses logicViewports without being listed all fail; the undoctored directory passes.
func TestPhoneDroppedListFailsOnADoctoredEntry(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	use := "import { logicViewports } from './viewports';\nfor (const vp of logicViewports) {}\n"
	write("viewports.ts", "export const logicViewports = [desktop] as const;\n")
	write("clean.spec.ts", use)
	write("branchy.spec.ts", "const viewports = [];\n")
	write("plain.spec.ts", "const viewports = [];\n")
	write("sneaky.spec.ts", use)
	write("shell.spec.ts", use)
	list := []string{"clean.spec.ts"}
	unlisted := strings.Join(listProblems(dir, list, []string{"shell.spec.ts"}), "\n")
	for _, name := range []string{"sneaky.spec.ts", "shell.spec.ts"} {
		if !strings.Contains(unlisted, name+": uses logicViewports but is not on the phone-dropped list") {
			t.Errorf("an unlisted user %s is not named: %q", name, unlisted)
		}
	}
	write("sneaky.spec.ts", "const viewports = [];\n")
	write("shell.spec.ts", "const viewports = [];\n")
	if got := listProblems(dir, list, []string{"shell.spec.ts"}); len(got) != 0 {
		t.Fatalf("undoctored list: problems = %q, want none", got)
	}
	write("branchy.spec.ts", use+"if (vp.name === 'phone') await x();\n")
	doctored := map[string][]string{
		"branchy.spec.ts": {"clean.spec.ts", "branchy.spec.ts"},
		"plain.spec.ts":   {"clean.spec.ts", "plain.spec.ts"},
		"missing.spec.ts": {"clean.spec.ts", "missing.spec.ts"},
		"shell.spec.ts":   {"clean.spec.ts", "shell.spec.ts"},
	}
	for name, entries := range doctored {
		got := strings.Join(listProblems(dir, entries, []string{"shell.spec.ts"}), "\n")
		if !strings.Contains(got, name) {
			t.Errorf("a list with %s: problems = %q, want one naming it", name, got)
		}
	}
}
