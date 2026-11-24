package features

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestBoardBuildArgsFollowsTheCoverSettings pins the build line the suite's
// merged coverage depends on: -cover only with a coverage directory, and the
// unit pass's mode when one is named (atomic under -race), because
// `go tool covdata` refuses to merge atomic and set counters. Every line
// carries -tags vhfeatures (the --poll second a scenario may shorten).
func TestBoardBuildArgsFollowsTheCoverSettings(t *testing.T) {
	cases := []struct {
		name                string
		coverDir, coverMode string
		want                []string
	}{
		{"no coverage: the plain build", "", "", []string{"build", "-tags", "vhfeatures", "-o", "/b", boardPackage}},
		{"cover dir only: -cover, default mode", "/cov", "", []string{"build", "-tags", "vhfeatures", "-o", "/b", "-cover", boardPackage}},
		{"cover dir and set", "/cov", "set", []string{"build", "-tags", "vhfeatures", "-o", "/b", "-cover", "-covermode=set", boardPackage}},
		{"cover dir and atomic", "/cov", "atomic", []string{"build", "-tags", "vhfeatures", "-o", "/b", "-cover", "-covermode=atomic", boardPackage}},
		{"cover dir and an unusual mode is passed through verbatim", "/cov", "count", []string{"build", "-tags", "vhfeatures", "-o", "/b", "-cover", "-covermode=count", boardPackage}},
		{"the binary path is the -o operand", "/cov", "set", []string{"build", "-tags", "vhfeatures", "-o", "/b", "-cover", "-covermode=set", boardPackage}},
		{"a mode without a cover dir is ignored", "", "atomic", []string{"build", "-tags", "vhfeatures", "-o", "/b", boardPackage}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boardBuildArgs("/b", tc.coverDir, tc.coverMode); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("boardBuildArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

// counterFiles lists the non-empty coverage counter files under dir.
func counterFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(e.Name(), "covcounters.") && info.Size() > 0 {
			files = append(files, e.Name())
		}
	}
	return files
}

// builtBoard compiles the board binary through the feature harness's own
// build path with GOCOVERDIR set to dir ("" for unset), in the unit pass's
// covermode, and returns its path.
func builtBoard(t *testing.T, dir, mode string) string {
	t.Helper()
	t.Setenv("GOCOVERDIR", dir)
	t.Setenv("BOARD_FEATURES_COVERMODE", mode)
	return buildBoard(t)
}

func runBoardWith(t *testing.T, bin, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("board %v: %v\n%s", args, err, out)
	}
}

// TestFeatureBuildEmitsCoverageWhenGOCOVERDIRIsSet runs the harness's actual
// build and a short-lived command of the result: the counters must reach the
// directory in every mode the suite can select, and a build made without
// GOCOVERDIR must leave none, which is what makes the first assertion able to
// detect absent instrumentation.
func TestFeatureBuildEmitsCoverageWhenGOCOVERDIRIsSet(t *testing.T) {
	for _, mode := range []string{"", "set", "atomic"} {
		t.Run("mode "+mode, func(t *testing.T) {
			dir := t.TempDir()
			runBoardWith(t, builtBoard(t, dir, mode), dir, "version")
			if got := counterFiles(t, dir); len(got) == 0 {
				t.Fatalf("an instrumented board binary left no counter data in %s", dir)
			}
		})
	}
	t.Run("an uninstrumented build leaves none", func(t *testing.T) {
		dir := t.TempDir()
		runBoardWith(t, builtBoard(t, "", ""), dir, "version")
		if got := counterFiles(t, dir); len(got) != 0 {
			t.Fatalf("a build without GOCOVERDIR wrote counters %v", got)
		}
	})
}

// TestStoppedServerFlushesItsCoverage drives the harness's serve start and
// stop: a server ended the way a scenario ends it must have written its
// counters, which a SIGKILL would lose.
func TestStoppedServerFlushesItsCoverage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOCOVERDIR", dir)
	h := &harness{binary: builtBoard(t, dir, "set"), leg: "sqlite"}
	if _, err := h.startServer(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.stopServer()
	if got := counterFiles(t, dir); len(got) == 0 {
		t.Fatalf("a stopped vh serve left no counter data in %s", dir)
	}
}

// startShell starts a shell script that prints "ready" once it is set up.
func startShell(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := out.Read(buf); err != nil || string(buf) != "ready" {
		t.Fatalf("shell not ready: %q %v", buf, err)
	}
	return cmd
}

// TestEndServerStopsWhatItIsGiven covers the ways a scenario's server can be
// at the end of the scenario: one that exits on SIGTERM is stopped at once
// (so it can flush counters), one that ignores SIGTERM is killed once the
// grace period is up, and one that already exited is not waited on.
func TestEndServerStopsWhatItIsGiven(t *testing.T) {
	t.Run("exits on SIGTERM without waiting out the grace", func(t *testing.T) {
		cmd := startShell(t, "trap 'exit 0' TERM; echo ready; while :; do sleep 0.05; done")
		start := time.Now()
		endServerWithin(cmd, 30*time.Second)
		if d := time.Since(start); d > 10*time.Second {
			t.Fatalf("a server that exits on SIGTERM took %v to end", d)
		}
	})
	t.Run("a server ignoring SIGTERM is killed after the grace", func(t *testing.T) {
		cmd := startShell(t, "trap '' TERM; echo ready; while :; do sleep 0.05; done")
		start := time.Now()
		endServerWithin(cmd, 300*time.Millisecond)
		if d := time.Since(start); d < 300*time.Millisecond || d > 10*time.Second {
			t.Fatalf("a SIGTERM-ignoring server ended after %v, want the grace then a kill", d)
		}
	})
	t.Run("an already exited server returns", func(t *testing.T) {
		cmd := exec.Command("true")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		endServerWithin(cmd, time.Second)
	})
}
