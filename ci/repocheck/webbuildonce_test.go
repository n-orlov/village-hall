package repocheck

// The web UI is built once per CI build (SPEC 15.3, R9 l): the Jenkinsfile's Web UI
// unit runs ci/web-build.sh and stashes internal/webui/dist as web-dist; Suite, E2E
// and Quality unstash it, and nothing else in the Jenkinsfile builds web/. These
// tests pin the Jenkinsfile's wiring (a doctored Jenkinsfile in which Suite, E2E or
// Quality lacks the stash dependency is caught) and run the real ci/web-build.sh
// against a stub npm. The Image unit's image embeds the same build rather than
// building the UI in the Dockerfile: webbuildimage_test.go pins that path, and
// TestWebUIIsBuiltOnceAndStashedForSuiteE2EAndQuality below runs it too.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// webBuildOnceProblems: one Web UI unit that runs ci/web-build.sh and stashes
// web-dist, Suite and E2E that wait for it and unstash it, Quality that unstashes
// it, and no npm build anywhere else in the Jenkinsfile's code.
func webBuildOnceProblems(jf string) []string {
	var problems []string
	core, _ := jenkinsStage(jf, "Core")
	code := strings.Join(jenkinsfileLines(jf), "\n")
	if n := strings.Count(code, "ci/web-build.sh"); n != 1 {
		problems = append(problems, fmt.Sprintf("the Jenkinsfile runs ci/web-build.sh %d times, want once", n))
	}
	for _, build := range []string{"npm run build", "vite build"} {
		if strings.Contains(code, build) {
			problems = append(problems, "the Jenkinsfile builds the web UI itself ("+build+") instead of through the Web UI unit")
		}
	}
	web, ok := coreUnitDecl(core, "Web UI")
	switch {
	case !ok:
		problems = append(problems, "Core has no Web UI unit")
	default:
		if !strings.Contains(web, "ci/run.sh ci/web-build.sh") {
			problems = append(problems, "the Web UI unit does not run ci/web-build.sh")
		}
		if !strings.Contains(web, "stash name: 'web-dist', includes: 'internal/webui/dist/**'") {
			problems = append(problems, "the Web UI unit does not stash internal/webui/dist as web-dist")
		}
		if !strings.Contains(web, "signal: 'Web UI'") {
			problems = append(problems, "the Web UI unit raises no signal for the units that wait for it")
		}
	}
	for _, name := range []string{"Suite", "E2E", "Image"} {
		decl, ok := coreUnitDecl(core, name)
		if !ok {
			problems = append(problems, "Core has no "+name+" unit")
			continue
		}
		if !strings.Contains(decl, "after: 'Web UI'") {
			problems = append(problems, name+" does not wait for the Web UI unit")
		}
		if !strings.Contains(decl, "needs: ['web-dist']") {
			problems = append(problems, name+" does not unstash web-dist")
		}
	}
	quality := functionBodyFrom(core, "runUnit([name: 'Quality'")
	if !strings.Contains(quality, "'web-dist'") {
		problems = append(problems, "Quality does not unstash web-dist")
	}
	// internal/server's UI tests fail without the real build, so the extended
	// lane's units (Stability's three clones, every Mutation unit) get it too.
	stability, _ := jenkinsStage(jf, "Stability")
	if !strings.Contains(stability, "def u = [name: 'Stability runs', minutes: 40,\n                       needs: ['web-dist'],") {
		problems = append(problems, "Stability runs does not unstash web-dist")
	}
	if !strings.Contains(stability, `cp -a internal/webui/dist "stability-$i/internal/webui/dist"`) {
		problems = append(problems, "Stability's clones do not each get the web build")
	}
	mutation, _ := jenkinsStage(jf, "Mutation")
	if !strings.Contains(functionBody(jf, "def runFullMutation(Map plan) {"), "maxAttempts: 1,\n             needs: ['web-dist'],") {
		problems = append(problems, "the full mutation units do not unstash web-dist")
	}
	if !strings.Contains(mutation, "name: 'Mutation diff', minutes: 45,\n                         needs: ['web-dist'],") {
		problems = append(problems, "Mutation diff does not unstash web-dist")
	}
	branch := functionBody(jf, "def unitBranch(Map u) {")
	if !strings.Contains(branch, "waitUntil(initialRecurrencePeriod: 5000) { signals[u.after] == true }") ||
		!strings.Contains(branch, "if (results[u.after] != 'pass') {") {
		problems = append(problems, "unitBranch does not hold a unit back until the unit it runs after passed")
	}
	return problems
}

// functionBodyFrom returns the first line of text at sig, to the end of that line.
func functionBodyFrom(text, sig string) string {
	i := strings.Index(text, sig)
	if i < 0 {
		return ""
	}
	line, _, _ := strings.Cut(text[i:], "\n")
	return line
}

func TestWebUIIsBuiltOnceAndStashedForSuiteE2EAndQuality(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	// The image path: with the Web UI unit's output present the Image unit's
	// ci/image-build.sh gives both builds the output, and neither runs a UI build.
	for _, p := range imageWebBuildProblems(t, readRepoFile(t, "Dockerfile"), readRepoFile(t, "ci", "image-build.sh")) {
		t.Error(p)
	}
	expectProblems(t, jf, webBuildOnceProblems, map[string]string{
		"a Suite that does not unstash the web build": replaceOnce(t, jf,
			"signal: 'Suite', after: 'Web UI', needs: ['web-dist'],", "signal: 'Suite', after: 'Web UI',"),
		"a Suite that does not wait for the web build": replaceOnce(t, jf,
			"signal: 'Suite', after: 'Web UI', needs: ['web-dist'],", "signal: 'Suite', needs: ['web-dist'],"),
		"an E2E that does not unstash the web build": replaceOnce(t, jf,
			"sample: true, after: 'Web UI', needs: ['web-dist'],", "sample: true, after: 'Web UI',"),
		"an E2E that does not wait for the web build": replaceOnce(t, jf,
			"sample: true, after: 'Web UI', needs: ['web-dist'],", "sample: true, needs: ['web-dist'],"),
		"an Image unit that does not unstash the web build": replaceOnce(t, jf,
			"signal: 'Image', after: 'Web UI', needs: ['web-dist'],", "signal: 'Image', after: 'Web UI',"),
		"an Image unit that does not wait for the web build": replaceOnce(t, jf,
			"signal: 'Image', after: 'Web UI', needs: ['web-dist'],", "signal: 'Image', needs: ['web-dist'],"),
		"a Quality that does not unstash the web build": replaceOnce(t, jf,
			"'res-image', 'image', 'web-dist'],", "'res-image', 'image'],"),
		"a Stability that does not unstash the web build": replaceOnce(t, jf,
			"minutes: 40,\n                       needs: ['web-dist'],", "minutes: 40,"),
		"a Stability clone without the web build": replaceOnce(t, jf,
			`cp -a internal/webui/dist "stability-$i/internal/webui/dist"`, "true"),
		"a full mutation unit that does not unstash the web build": replaceOnce(t, jf,
			"maxAttempts: 1,\n             needs: ['web-dist'],", "maxAttempts: 1,"),
		"a Mutation diff that does not unstash the web build": replaceOnce(t, jf,
			"minutes: 45,\n                         needs: ['web-dist'],", "minutes: 45,"),
		"a Web UI unit that stashes nothing": replaceOnce(t, jf,
			"stash name: 'web-dist', includes: 'internal/webui/dist/**'", "stash name: 'web-dist', includes: 'nothing/**'"),
		"a second build in Suite": replaceOnce(t, jf,
			"body: { unitSh(script: 'BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh') }",
			"body: { unitSh(script: 'cd web && npm run build && BOARD_CI_PLAYWRIGHT=skip ci/run.sh ci/suite.sh') }"),
		"a second ci/web-build.sh in E2E": replaceOnce(t, jf,
			"BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh", "ci/web-build.sh && BOARD_CI_PLAYWRIGHT=only ci/run.sh ci/suite.sh"),
	})
}

const webBuildStubNpm = `#!/bin/sh
echo "npm $(basename "$PWD") $*" >> "$STUB_LOG"
case "$*" in
*"run build"*)
	[ -n "${STUB_BUILD_FAIL:-}" ] && exit 1
	[ -n "${STUB_BUILD_NOTHING:-}" ] && exit 0
	mkdir -p ../internal/webui/dist
	echo '<html></html>' > ../internal/webui/dist/index.html
	;;
esac
`

func runWebBuild(t *testing.T, env ...string) (dir, calls string, err error) {
	t.Helper()
	dir = t.TempDir()
	for rel, body := range map[string]string{
		"bin/npm":                      webBuildStubNpm,
		"ci/web-build.sh":              readRepoFile(t, "ci", "web-build.sh"),
		"web/package.json":             "{}\n",
		"internal/webui/dist/.gitkeep": "",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(rel, "bin/") || strings.HasPrefix(rel, "ci/") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "calls.log")
	cmd := exec.Command("sh", filepath.Join(dir, "ci", "web-build.sh"))
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"), "HOME=" + dir, "STUB_LOG=" + log}, env...)
	out, err := cmd.CombinedOutput()
	t.Logf("ci/web-build.sh output:\n%s", out)
	raw, _ := os.ReadFile(log)
	return dir, string(raw), err
}

// ci/web-build.sh builds once and leaves the marker beside the build.
func TestWebBuildScriptBuildsOnceAndMarksTheBuild(t *testing.T) {
	dir, calls, err := runWebBuild(t)
	if err != nil {
		t.Fatalf("ci/web-build.sh failed: %v", err)
	}
	if n := strings.Count(calls, "npm web run build"); n != 1 {
		t.Errorf("npm run build ran %d times, want once: %q", n, calls)
	}
	if !strings.Contains(calls, "npm web ci") {
		t.Errorf("npm ci did not run before the build: %q", calls)
	}
	for _, f := range []string{"index.html", ".ci-stashed-build"} {
		if _, err := os.Stat(filepath.Join(dir, "internal", "webui", "dist", f)); err != nil {
			t.Errorf("dist/%s was not left: %v", f, err)
		}
	}
}

// A failed build, or one that wrote no index.html, leaves no marker, so nothing
// downstream takes it for a build to reuse.
func TestWebBuildScriptLeavesNoMarkerOnAFailedBuild(t *testing.T) {
	for name, env := range map[string]string{"a failing build": "STUB_BUILD_FAIL=1", "a build that wrote nothing": "STUB_BUILD_NOTHING=1"} {
		dir, _, err := runWebBuild(t, env)
		if err == nil {
			t.Errorf("%s: ci/web-build.sh exited 0", name)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "internal", "webui", "dist", ".ci-stashed-build")); statErr == nil {
			t.Errorf("%s: the marker was written", name)
		}
	}
}

// The release dry run runs in the Image unit, which unstashes the web build: with
// the build's marker beside it ci/release-build.sh runs no npm build, and a real
// release (no marker) builds the UI once.
func TestReleaseBuildRunsNoWebBuildWhenTheStashedBuildIsPresent(t *testing.T) {
	build, check := readRepoFile(t, "ci", "release-build.sh"), readRepoFile(t, "ci", "release-check.sh")
	builds := func(r *imageOnceRun) int {
		n := 0
		for _, c := range r.calls("npm ") {
			if strings.Contains(c, "run build") {
				n++
			}
		}
		return n
	}
	with := runImageOnceWeb(t, true, build, check, true, "release-check.sh", "ci-results")
	if with.err != nil {
		t.Fatalf("the dry run failed: %v\n%s", with.err, with.failure())
	}
	if n := builds(with); n != 0 {
		t.Errorf("the dry run ran %d web build(s) with the stashed build present", n)
	}
	without := runImageOnceWeb(t, false, build, check, false, "release-build.sh", dryRunVersion, "out")
	if without.err != nil {
		t.Fatalf("the release build failed: %v\n%s", without.err, without.failure())
	}
	if n := builds(without); n != 1 {
		t.Errorf("a release with no stashed build ran %d web builds, want one", n)
	}
	always := replaceOnce(t, build, "if [ -f internal/webui/dist/.ci-stashed-build ]", "if false")
	if r := runImageOnceWeb(t, true, always, check, true, "release-check.sh", "ci-results"); builds(r) == 0 {
		t.Error("a release-build.sh that always builds the UI was not caught")
	}
}
