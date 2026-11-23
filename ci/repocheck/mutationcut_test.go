package repocheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A mutation unit cut by its bound (ci/unitrun.sh) keeps the report of every
// package gremlins had finished: ci/mutation.sh writes a package's report the
// moment the package finishes, and never leaves a half-written one for the
// package that was running when the unit was cut. The tests run the real script
// against stub go and gremlins commands (no real mutation runs).

const stubGoTwoPackages = `#!/bin/sh
# ci/mutationgate -list prints two packages; every other go command succeeds.
case "$*" in
*" -list "*) echo internal/stuba; echo internal/stubb ;;
esac
exit 0
`

// The stub gremlins finishes internal/stuba (a whole report in -o's file) and
// hangs on internal/stubb after writing the start of its report, the way a unit
// cut by its bound leaves the package it was running.
const stubGremlinsHangsOnSecond = `#!/bin/sh
for arg; do out=$arg; done
case "$2" in
*stuba)
    printf '%s' '{"go_module":"m","files":[{"file_name":"a.go","mutations":[{"type":"X","status":"KILLED","line":1,"column":1}]}]}' > "$out"
    ;;
*)
    printf '%s' '{"go_module":"m","files":[{"file_name":"b.go","muta' > "$out"
    : > "$STUB_SECOND_STARTED"
    exec sleep 60
    ;;
esac
exit 0
`

// TestACutMutationUnitKeepsTheReportOfEveryFinishedPackage: ci/mutation.sh is
// killed (its process group, as the unit's bound does) while gremlins is on the
// second package; the first package's whole report is in the outdir, and the
// second package has no report file at all.
func TestACutMutationUnitKeepsTheReportOfEveryFinishedPackage(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	bin := t.TempDir()
	for name, body := range map[string]string{"go": stubGoTwoPackages, "gremlins": stubGremlinsHangsOnSecond} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	started := filepath.Join(bin, "second.started")
	out := t.TempDir()
	cmd := exec.Command("sh", filepath.Join(root, "ci", "mutation.sh"), out, "ci/quality.json", "stub")
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"STUB_SECOND_STARTED=" + started,
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ci/mutation.sh: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// The short bound: wait for the second package to be running, then cut.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Fatal("gremlins never reached the second package")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("cut ci/mutation.sh: %v", err)
	}
	<-done

	raw, err := os.ReadFile(filepath.Join(out, "internal-stuba.json"))
	if err != nil {
		t.Fatalf("the finished package's report did not survive the cut: %v", err)
	}
	if !json.Valid(raw) {
		t.Errorf("the finished package's report is not whole: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(out, "internal-stubb.json")); err == nil {
		t.Error("the package that was cut left a report file; only finished packages have one")
	}
	entries, _ := filepath.Glob(filepath.Join(out, "*.json"))
	if len(entries) != 1 {
		t.Errorf("reports after the cut = %v, want only internal-stuba.json", entries)
	}
}

// TestJenkinsfileArchivesACutUnitsMutationReports: the cut-off branch of runUnit
// stashes ci-results (every finished report with it) and archives the
// reports of both the single (ci-results/mutation) and the full-mode units
// (ci-results/mutation-units/<dir>), so they outlive the post report's wipe.
func TestJenkinsfileArchivesACutUnitsMutationReports(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	i := strings.Index(jf, "ci-results/unit-cut-${slug(u.name)}.txt")
	if i < 0 {
		t.Fatal("the cut-off branch of runUnit is gone")
	}
	branch := jf[i:]
	if j := strings.Index(branch, "archiveArtifacts"); j < 0 {
		t.Fatal("the cut-off branch archives nothing")
	} else {
		branch = branch[j:]
	}
	line, _, _ := strings.Cut(branch, "\n")
	for _, want := range []string{"ci-results/mutation/*.json", "ci-results/mutation-units/*/*.json"} {
		if !strings.Contains(line, want) {
			t.Errorf("a cut unit's archive lacks %s: %s", want, line)
		}
	}
}
