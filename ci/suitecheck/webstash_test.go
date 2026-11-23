// webstash_test.go proves ci/suite.sh builds the web UI only when no stashed build
// is there (R9 l): the Jenkinsfile's Web UI unit builds internal/webui/dist once
// (ci/web-build.sh, which leaves a marker beside the build) and Suite and E2E
// unstash it, so with the marker present the script invokes no `npm run build`,
// and with it absent the script builds as it always did. It runs the real
// ci/suite.sh with the stub npm of playwrightmode_test.go.
package suitecheck

import (
	"strings"
	"testing"
)

var stashedDist = map[string]string{"index.html": "<html></html>", ".ci-stashed-build": ""}

func buildsInvoked(run modeRun) int {
	return strings.Count(run.stubLog(), "npm web run build")
}

// With the stashed build present, no mode builds the web UI.
func TestSuiteRunsNoWebBuildWhenTheStashedDistIsPresent(t *testing.T) {
	for _, mode := range []string{"skip", "only", "with"} {
		run := runSuiteWithDist(t, mode, stashedDist)
		if run.err != nil {
			t.Fatalf("mode %q: ci/suite.sh failed: %v", mode, run.err)
		}
		if n := buildsInvoked(run); n != 0 {
			t.Errorf("mode %q: %d web build(s) invoked with the stashed dist present: %q", mode, n, run.stubLog())
		}
	}
}

// The stash replaces the build, not Vitest's install: Suite still runs npm ci in
// web/ and Vitest; E2E runs no web npm at all and still runs Playwright.
func TestSuiteWithTheStashedDistStillInstallsForVitestAndRunsItsSteps(t *testing.T) {
	skip := runSuiteWithDist(t, "skip", stashedDist)
	if skip.err != nil {
		t.Fatalf("skip: ci/suite.sh failed: %v", skip.err)
	}
	log := skip.stubLog()
	if !strings.Contains(log, "npm web ci") || !strings.Contains(log, "run test") {
		t.Errorf("skip: web npm ci and Vitest must still run: %q", log)
	}
	only := runSuiteWithDist(t, "only", stashedDist)
	if only.err != nil {
		t.Fatalf("only: ci/suite.sh failed: %v", only.err)
	}
	log = only.stubLog()
	if strings.Contains(log, "npm web") {
		t.Errorf("only: a web npm command ran with the stashed dist present: %q", log)
	}
	if !strings.Contains(log, "npx e2e playwright test") || !only.has("junit-playwright.xml") {
		t.Errorf("only: Playwright did not run on the stashed build: %q", log)
	}
}

// With no stashed build the script builds, once: no dist at all, a dist with no
// marker (built some other way, or left by an earlier local run), and a marker
// with no index.html (a half-restored stash).
func TestSuiteBuildsTheWebUIWhenNoStashedDistIsPresent(t *testing.T) {
	cases := map[string]map[string]string{
		"no dist":                     nil,
		"a dist with no marker":       {"index.html": "<html></html>"},
		"a marker with no index.html": {".ci-stashed-build": ""},
	}
	for name, dist := range cases {
		for _, mode := range []string{"skip", "only"} {
			run := runSuiteWithDist(t, mode, dist)
			if run.err != nil {
				t.Fatalf("%s, mode %q: ci/suite.sh failed: %v", name, mode, run.err)
			}
			if n := buildsInvoked(run); n != 1 {
				t.Errorf("%s, mode %q: %d web builds invoked, want one: %q", name, mode, n, run.stubLog())
			}
		}
	}
}
