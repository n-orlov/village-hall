package repocheck

// The image embeds the web UI the CI build's Web UI unit already built (SPEC 15.3,
// R9 l): ci/image-build.sh hands internal/webui/dist to the root Dockerfile as the
// named build context `web`, which BuildKit uses in place of the Dockerfile's `web`
// stage, so the stage that runs `npm run build` (`webbuild`) is never run for either
// architecture. Without the Web UI unit's marker (a standalone `docker build .`, a
// release) nothing replaces `web`, and the UI is built in the image as before.
//
// buildStagesRun models what BuildKit runs for a Dockerfile and the named contexts
// given, and the tests below run the real ci/image-build.sh against a stub docker
// that records which contexts each `buildx build` was given, then feed those to the
// model with the real Dockerfile. Doctored scripts and Dockerfiles must be caught.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stageName is the name after AS on a FROM line, or "".
func (s stage) name() string {
	for i, f := range s.from {
		if strings.EqualFold(f, "AS") && i+1 < len(s.from) {
			return s.from[i+1]
		}
	}
	return ""
}

// buildStagesRun is the stages BuildKit runs to build the Dockerfile's last stage
// when the named contexts in contexts are given: a stage with a context of its
// name is replaced by it (its own instructions never run), and only the stages the
// last one reaches, through FROM <stage> and COPY --from=<stage>, run at all.
func buildStagesRun(dockerfile string, contexts map[string]bool) []string {
	stages := dockerfileStages(dockerfile)
	if len(stages) == 0 {
		return nil
	}
	byName := map[string]stage{}
	for _, s := range stages {
		if s.name() != "" {
			byName[s.name()] = s
		}
	}
	var ran []string
	seen := map[string]bool{}
	var visit func(s stage, label string)
	visit = func(s stage, label string) {
		if seen[label] {
			return
		}
		seen[label] = true
		if contexts[s.name()] {
			return
		}
		ran = append(ran, label)
		deps := []string{s.image()}
		for _, in := range s.each("COPY") {
			for _, f := range in {
				if from, ok := strings.CutPrefix(f, "--from="); ok {
					deps = append(deps, from)
				}
			}
		}
		for _, d := range deps {
			if dep, ok := byName[d]; ok {
				visit(dep, d)
			}
		}
	}
	last := stages[len(stages)-1]
	visit(last, "(final)")
	return ran
}

// webBuilds is the stages of ran that run `npm run build`.
func webBuilds(dockerfile string, ran []string) []string {
	byName := map[string]stage{}
	for _, s := range dockerfileStages(dockerfile) {
		byName[s.name()] = s
	}
	var out []string
	for _, name := range ran {
		for _, run := range byName[name].each("RUN") {
			if strings.Contains(strings.Join(run, " "), "npm run build") {
				out = append(out, name)
			}
		}
	}
	return out
}

// imageWebBuild is one run of the real ci/image-build.sh against a stub docker.
type imageWebBuild struct {
	builds   []string          // each `docker buildx build` line
	contexts []map[string]bool // the named contexts each build was given
}

// runImageBuildWithDist runs script (ci/image-build.sh's text) in a scratch
// checkout whose internal/webui/dist holds files (path under dist to content).
func runImageBuildWithDist(t *testing.T, script string, files map[string]string) *imageWebBuild {
	t.Helper()
	repo := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(repo, "internal", "webui", "dist", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ci", "image-build.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "docker.log")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> \"$STUB_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(repo, "ci", "image-build.sh"), filepath.Join(repo, "out", "board-image.tar"))
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "STUB_LOG=" + log, "HOME=" + repo}
	// The stub builds nothing, so the script ends on its empty-tar check: only the
	// calls before that matter.
	_, _ = cmd.CombinedOutput()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("stub docker saw nothing: %v", err)
	}
	r := &imageWebBuild{}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "docker buildx build ") {
			continue
		}
		r.builds = append(r.builds, line)
		ctx := map[string]bool{}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "--build-context" && i+1 < len(fields) {
				name, dir, _ := strings.Cut(fields[i+1], "=")
				// A named context must be the checkout's own dist, with the build in it.
				if dir == filepath.Join(repo, "internal", "webui", "dist") {
					ctx[name] = true
				}
			}
		}
		r.contexts = append(r.contexts, ctx)
	}
	return r
}

var stashedDist = map[string]string{"index.html": "<html></html>", ".ci-stashed-build": ""}

// imageWebBuildProblems runs the Image unit's path (ci/image-build.sh once per
// architecture group: the OCI build for both platforms and the amd64 load) with the
// Web UI unit's stashed output, and reports every way the image would run a web build.
func imageWebBuildProblems(t *testing.T, dockerfile, script string) []string {
	t.Helper()
	r := runImageBuildWithDist(t, script, stashedDist)
	var problems []string
	if len(r.builds) != 2 {
		problems = append(problems, fmt.Sprintf("ci/image-build.sh ran %d buildx builds, want the two (OCI for both platforms, amd64 load)", len(r.builds)))
	}
	for i, build := range r.builds {
		if ran := webBuilds(dockerfile, buildStagesRun(dockerfile, r.contexts[i])); len(ran) > 0 {
			problems = append(problems, fmt.Sprintf("build %d runs the web build in stage %s with the stashed output present: %s", i+1, strings.Join(ran, ", "), build))
		}
	}
	return problems
}

// With the Web UI unit's output present, neither buildx build of the image runs
// the UI build; every doctored Dockerfile or script that would is caught.
func TestImageEmbedsTheStashedWebBuildAndRunsNoWebBuild(t *testing.T) {
	dockerfile, script := readRepoFile(t, "Dockerfile"), readRepoFile(t, "ci", "image-build.sh")
	for _, p := range imageWebBuildProblems(t, dockerfile, script) {
		t.Error(p)
	}
	// The image's standalone recipe still builds the UI: nothing replaces `web`.
	if ran := webBuilds(dockerfile, buildStagesRun(dockerfile, nil)); len(ran) != 1 {
		t.Errorf("a standalone docker build runs the web build in %v, want exactly one stage", ran)
	}
	// A web stage that builds as well as copies would build the UI twice standalone.
	twice := replaceOnce(t, dockerfile, "FROM --platform=$BUILDPLATFORM scratch AS web\n", "FROM --platform=$BUILDPLATFORM scratch AS web\nRUN npm run build\n")
	if ran := webBuilds(twice, buildStagesRun(twice, nil)); len(ran) != 2 {
		t.Errorf("a web stage that builds the UI itself ran the build in %v standalone, want two stages (caught as not one)", ran)
	}
	cases := map[string][2]string{
		// the Dockerfile's `build` stage reads the UI from the building stage, so the
		// named context replaces nothing it uses (the finding's own case)
		"a build stage that copies the web build stage directly": {
			replaceOnce(t, dockerfile, "COPY --from=web / internal/webui/dist/", "COPY --from=webbuild /src/internal/webui/dist/ internal/webui/dist/"), script},
		"a Dockerfile whose web stage is not the replaceable name": {
			strings.Replace(replaceOnce(t, dockerfile, "AS web\n", "AS webui\n"), "COPY --from=web /", "COPY --from=webui /", 1), script},
		"an image-build.sh that gives no context": {
			dockerfile, strings.ReplaceAll(script, "$web_opts", "")},
		"an image-build.sh that gives the context to the OCI build only": {
			dockerfile, replaceOnce(t, script, "-f Dockerfile $procs_arg $web_opts \\\n\t--platform linux/amd64 --provenance", "-f Dockerfile $procs_arg \\\n\t--platform linux/amd64 --provenance")},
		"an image-build.sh that names the wrong stage": {
			dockerfile, strings.ReplaceAll(script, "--build-context web=", "--build-context webui=")},
		"an image-build.sh that names a directory outside the checkout's dist": {
			dockerfile, strings.ReplaceAll(script, "$repo_root/internal/webui/dist", "$repo_root/web")},
	}
	for name, c := range cases {
		if c[0] == dockerfile && c[1] == script {
			t.Errorf("%s: the doctoring changed nothing", name)
			continue
		}
		if len(imageWebBuildProblems(t, c[0], c[1])) == 0 {
			t.Errorf("%s was not caught", name)
		}
	}
}

// Without the Web UI unit's marker the script gives no context, so the Dockerfile
// builds the UI: a stale dist left in a work tree, or a marker beside no
// index.html, is never embedded.
func TestImageBuildGivesNoWebContextWithoutTheStashedBuildsMarker(t *testing.T) {
	dockerfile, script := readRepoFile(t, "Dockerfile"), readRepoFile(t, "ci", "image-build.sh")
	for name, files := range map[string]map[string]string{
		"no dist at all":                   nil,
		"a stale dist with no marker":      {"index.html": "<html>old</html>"},
		"a marker beside no index.html":    {".ci-stashed-build": ""},
		"a dist holding only another file": {"assets/app.js": "x"},
	} {
		r := runImageBuildWithDist(t, script, files)
		if len(r.builds) != 2 {
			t.Fatalf("%s: %d buildx builds, want 2", name, len(r.builds))
		}
		for i := range r.builds {
			if len(r.contexts[i]) != 0 || strings.Contains(r.builds[i], "--build-context") {
				t.Errorf("%s: build %d was given a web context: %s", name, i+1, r.builds[i])
			}
			if ran := webBuilds(dockerfile, buildStagesRun(dockerfile, r.contexts[i])); len(ran) != 1 {
				t.Errorf("%s: build %d runs the web build in %v, want the one stage", name, i+1, ran)
			}
		}
	}
	// And with the marker, the same script gives both builds the context.
	r := runImageBuildWithDist(t, script, stashedDist)
	for i := range r.builds {
		if !r.contexts[i]["web"] {
			t.Errorf("build %d was not given the stashed dist as context web: %s", i+1, r.builds[i])
		}
	}
}
