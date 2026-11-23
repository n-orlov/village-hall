// lintgate_test.go, carried from deck: golangci-lint is a gate, not an
// advisory. It fails the moment the two entry points (ci/lint.sh and
// ci/quality.sh) stop sharing one invocation, the quality gate is switched
// off, or gofmt/go vet/go mod tidy turn up in a second place.
package repocheck

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shellLines returns the executable lines of a shell script: comments and
// blank lines dropped.
func shellLines(script string) []string {
	var out []string
	for _, l := range strings.Split(script, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// jenkinsfileLines returns a Jenkinsfile's lines with // comment lines and
// blank lines dropped.
func jenkinsfileLines(jf string) []string {
	var out []string
	for _, l := range strings.Split(jf, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "//") {
			out = append(out, l)
		}
	}
	return out
}

func anyLineContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// lintUnitProblems: Lint is the first unit of the Core phase and runs
// ci/lint.sh, and a red unit fails the build (unitBranch's catchError), so
// neither Stability nor Mutation, which run only after a build that has not
// failed, starts after a Lint failure. (The units run in parallel, so Lint
// no longer precedes Suite; it gates every later phase instead.)
func lintUnitProblems(jf string) []string {
	var problems []string
	core, _ := jenkinsStage(jf, "Core")
	units := coreUnits(core)
	if len(units) == 0 || units[0] != "Lint" {
		problems = append(problems, "Lint is not the first Core unit: "+strings.Join(units, ","))
	}
	if !strings.Contains(core, "units << [name: 'Lint', minutes: 30, body: { sh(returnStatus: true, script: 'ci/run.sh ci/lint.sh') }]") {
		problems = append(problems, "the Lint unit does not run ci/run.sh ci/lint.sh")
	}
	if !strings.Contains(jf, "catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n        try {") || !strings.Contains(functionBody(jf, "def unitBranch(Map u) {"), "runUnit(u)") {
		problems = append(problems, "a red unit does not fail the build")
	}
	return problems
}

// TestLintUnitRunsGolangciLintBeforeLaterPhases: the Jenkinsfile's Lint unit
// runs ci/lint.sh before the later phases, ci/lint.sh runs the shared
// golangci-lint invocation, so the linter gates the build.
func TestLintUnitRunsGolangciLintBeforeLaterPhases(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, lintUnitProblems, map[string]string{
		"Lint declared after Suite":      replaceOnce(t, replaceOnce(t, jf, "units << [name: 'Lint'", "units << [name: 'Tidy'"), "units << [name: 'Suite'", "units << [name: 'Lint', minutes: 30, body: { sh(returnStatus: true, script: 'ci/run.sh ci/lint.sh') }]\n          units << [name: 'Suite'"),
		"a Lint unit that runs nothing":  replaceOnce(t, jf, "script: 'ci/run.sh ci/lint.sh'", "script: 'true'"),
		"a Lint unit with another limit": replaceOnce(t, jf, "units << [name: 'Lint', minutes: 30,", "units << [name: 'Lint', minutes: 60,"),
		"a red unit that cannot fail":    replaceOnce(t, jf, "catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') {\n        try {", "catchError(buildResult: 'SUCCESS', stageResult: 'FAILURE') {\n        try {"),
	})

	lintSh := shellLines(readRepoFile(t, "ci", "lint.sh"))
	if !anyLineContains(lintSh, "ci/golangci.sh") {
		t.Error("ci/lint.sh does not run ci/golangci.sh, so golangci-lint never gates the Lint stage")
	}
	if !strings.Contains(strings.Join(shellLines(readRepoFile(t, "ci", "golangci.sh")), "\n"), "golangci-lint run ./...") {
		t.Error("ci/golangci.sh does not run `golangci-lint run ./...` over the whole tree")
	}
}

// TestQualityGateRunsTheSameGolangciInvocation: ci/quality.json switches the
// golangci gate on, and ci/qualitycheck runs the very script ci/lint.sh
// runs, so the two entry points cannot drift apart.
func TestQualityGateRunsTheSameGolangciInvocation(t *testing.T) {
	var cfg struct {
		Golangci struct {
			Enabled bool `json:"enabled"`
		} `json:"golangci"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "ci", "quality.json")), &cfg); err != nil {
		t.Fatalf("parse ci/quality.json: %v", err)
	}
	if !cfg.Golangci.Enabled {
		t.Error("ci/quality.json has no enabled golangci gate")
	}
	if !strings.Contains(readRepoFile(t, "ci", "qualitycheck", "golangci.go"), `"ci", "golangci.sh"`) {
		t.Error("ci/qualitycheck's golangci gate does not run ci/golangci.sh")
	}
	if !anyLineContains(shellLines(readRepoFile(t, "ci", "lint.sh")), "ci/golangci.sh") {
		t.Error("ci/lint.sh does not run ci/golangci.sh")
	}
	if !strings.Contains(strings.Join(shellLines(readRepoFile(t, "ci", "golangci.sh")), "\n"), "golangci-lint run ./...") {
		t.Error("ci/golangci.sh does not run `golangci-lint run ./...` over the whole tree")
	}
}

// TestFormattingVetAndTidyChecksLiveOnlyInLintSh: gofmt, go vet and go mod
// tidy run in one place, ci/lint.sh, and in no other CI script or pipeline
// file. Every pipeline file in the tree is checked: the Jenkinsfile counts
// from the commit that adds it.
func TestFormattingVetAndTidyChecksLiveOnlyInLintSh(t *testing.T) {
	lint := shellLines(readRepoFile(t, "ci", "lint.sh"))
	for _, want := range []string{"gofmt -l", "go vet ./...", "go mod tidy"} {
		if !anyLineContains(lint, want) {
			t.Fatalf("ci/lint.sh no longer runs %s", want)
		}
	}
	banned := []string{"gofmt", "go vet", "go mod tidy"}
	for _, rel := range [][]string{{"ci", "quality.sh"}, {"ci", "suite.sh"}, {"ci", "golangci.sh"}} {
		lines := shellLines(readRepoFile(t, rel...))
		for _, b := range banned {
			if anyLineContains(lines, b) {
				t.Errorf("%s runs %q, which belongs only in ci/lint.sh", filepath.Join(rel...), b)
			}
		}
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Jenkinsfile", "Jenkinsfile.release"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue // no such pipeline file, so no second place in it
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range jenkinsfileLines(string(raw)) {
			for _, b := range banned {
				if strings.Contains(l, b) {
					t.Errorf("%s runs %q directly (%q), a second place besides ci/lint.sh", name, b, l)
				}
			}
		}
	}
}

// TestJenkinsfileLinesDropsCommentsAndBlanks: the helper the pipeline-file
// probe reads lines through keeps every executable line, so a banned command
// cannot hide from it, and drops only // comments and blank lines.
func TestJenkinsfileLinesDropsCommentsAndBlanks(t *testing.T) {
	got := jenkinsfileLines("pipeline {\n  // gofmt here is a comment\n\n  sh 'go vet ./...'\n}\n")
	want := []string{"pipeline {", "sh 'go vet ./...'", "}"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("jenkinsfileLines = %q, want %q", got, want)
	}
	if !anyLineContains(got, "go vet") || anyLineContains(got, "gofmt") {
		t.Fatalf("a banned command on an executable line must be seen and a commented one must not: %q", got)
	}
}
