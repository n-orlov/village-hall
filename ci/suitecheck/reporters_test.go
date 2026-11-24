// reporters_test.go pins what the CI toolchain image and the Playwright config carry for R9 m: the config keeps only the
// reporters something in CI reads (list for the log, allure-playwright for Allure, junit for junit-playwright.xml, the
// partial JUnit reporter for a cut run, the run-directory cleanup), so no html and no json reporter is written at the
// end of every run for nothing, and the image installs Chromium only, since every Playwright project and every browser
// test uses it.
package suitecheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var reporterEntry = regexp.MustCompile(`\[\s*'([^']+)'`)

// reportersProblem returns "" when the reporter list of an e2e/playwright.config.ts names exactly the reporters CI reads,
// otherwise what is wrong. Comments are not read.
func reportersProblem(cfg string) string {
	code := stripTSComments(cfg)
	start := strings.Index(code, "reporter: [")
	if start < 0 {
		return "no reporter list"
	}
	rest := code[start+len("reporter: ["):]
	end := strings.Index(rest, "\n  ],")
	if end < 0 {
		return "the reporter list is not closed by `  ],`"
	}
	got := map[string]bool{}
	for _, m := range reporterEntry.FindAllStringSubmatch(rest[:end], -1) {
		got[m[1]] = true
	}
	for _, banned := range []string{"html", "json"} {
		if got[banned] {
			return "the " + banned + " reporter is back: nothing in CI reads its output"
		}
	}
	for _, want := range []string{"list", "allure-playwright", "junit", "./partial-junit-reporter.ts", "./run-dir-reporter.ts"} {
		if !got[want] {
			return "the reporter " + want + " is missing"
		}
	}
	if len(got) != 5 {
		return "the reporter list names a reporter CI does not read"
	}
	return ""
}

var browserInstall = regexp.MustCompile(`playwright install([^\n&]*)`)

// imageBrowserProblem returns "" when a ci/Dockerfile installs Chromium and no other Playwright browser, otherwise what
// is wrong.
func imageBrowserProblem(dockerfile string) string {
	code := stripCommentLines(dockerfile)
	found := browserInstall.FindAllStringSubmatch(code, -1)
	if len(found) == 0 {
		return "no `playwright install` line"
	}
	installed := map[string]bool{}
	for _, m := range found {
		for _, word := range strings.Fields(strings.ReplaceAll(m[1], `\`, " ")) {
			if !strings.HasPrefix(word, "-") {
				installed[word] = true
			}
		}
	}
	for _, banned := range []string{"firefox", "webkit"} {
		if installed[banned] {
			return "the image installs " + banned + ": the specs use chromium only"
		}
	}
	if !installed["chromium"] || len(installed) != 1 {
		return "the image must install chromium and nothing else"
	}
	return ""
}

func TestPlaywrightConfigKeepsOnlyTheReportersCIReads(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "e2e", "playwright.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	if problem := reportersProblem(cfg); problem != "" {
		t.Errorf("e2e/playwright.config.ts: %s", problem)
	}
	for name, doctored := range map[string]string{
		"html re-added": strings.Replace(cfg, "reporter: [\n", "reporter: [\n    ['html', { outputFolder: `${out}/playwright-report`, open: 'never' }],\n", 1),
		"json re-added": strings.Replace(cfg, "reporter: [\n", "reporter: [\n    ['json', { outputFile: `${out}/results.json` }],\n", 1),
		"junit gone":    strings.Replace(cfg, "['junit', ", "['xunit', ", 1),
		"allure gone":   strings.Replace(cfg, "['allure-playwright'", "['allure'", 1),
		"list gone":     strings.Replace(cfg, "['list'],", "", 1),
		"list reporter": strings.Replace(cfg, "['list'],", "['list'], ['dot'],", 1),
	} {
		if doctored == cfg {
			t.Fatalf("%s: the doctoring changed nothing", name)
		}
		if reportersProblem(doctored) == "" {
			t.Errorf("%s: not flagged", name)
		}
	}
}

func TestReportersProblemReadsCodeNotComments(t *testing.T) {
	cfg := "  // no ['html'] and no ['json'] reporter\n  reporter: [\n    ['list'],\n    ['allure-playwright', {}],\n    ['junit', {}],\n    ['./partial-junit-reporter.ts', {}],\n    ['./run-dir-reporter.ts'],\n  ],\n"
	if problem := reportersProblem(cfg); problem != "" {
		t.Errorf("a comment naming html and json was flagged: %s", problem)
	}
}

func TestCIImageInstallsChromiumOnly(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "ci", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	if problem := imageBrowserProblem(dockerfile); problem != "" {
		t.Errorf("ci/Dockerfile: %s", problem)
	}
	install := "playwright install --with-deps chromium"
	for name, doctored := range map[string]string{
		"firefox added": strings.Replace(dockerfile, install, install+" firefox", 1),
		"webkit added":  strings.Replace(dockerfile, install, install+" webkit", 1),
		"all three":     strings.Replace(dockerfile, install, install+" firefox webkit", 1),
		"no chromium":   strings.Replace(dockerfile, install, "playwright install --with-deps firefox", 1),
		"no install":    strings.Replace(dockerfile, install, "true", 1),
		"separate step": strings.Replace(dockerfile, install, install+" \\\n    && npx playwright install webkit", 1),
	} {
		if doctored == dockerfile {
			t.Fatalf("%s: the doctoring changed nothing", name)
		}
		if imageBrowserProblem(doctored) == "" {
			t.Errorf("%s: not flagged", name)
		}
	}
}
