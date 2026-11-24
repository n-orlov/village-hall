package repocheck

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ci/loadsample.sh samples load average, CPU busy and iowait. The tests run the
// real script against a fake proc directory at a short interval. A feeder
// advances the fake /proc/stat by a fixed mix each tick (30 busy, 60 idle, 10
// iowait), so every row's percentages are known whatever the timing.

var loadRow = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z load1=(\S+) busy=(\d+\.\d) iowait=(\d+\.\d)$`)

// fakeProc writes a proc directory with loadavg and stat and feeds stat until
// the test ends. It returns the directory.
func fakeProc(t *testing.T, load string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loadavg"), []byte(load+" 3.00 2.00 1/200 12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(n int) {
		// user nice system idle iowait irq softirq steal guest guest_nice
		stat := fmt.Sprintf("cpu  %d 0 0 %d %d 0 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\nintr 1\n", 1000+30*n, 5000+60*n, 100+10*n)
		tmp := filepath.Join(dir, "stat.tmp")
		if err := os.WriteFile(tmp, []byte(stat), 0o644); err != nil {
			return
		}
		_ = os.Rename(tmp, filepath.Join(dir, "stat"))
	}
	write(0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for n := 1; ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
				write(n)
			}
		}
	}()
	return dir
}

func readRows(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatalf("no sample file: %v", err)
	}
	defer f.Close()
	var rows []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			rows = append(rows, line)
		}
	}
	return rows
}

func checkRows(t *testing.T, rows []string, load string) {
	t.Helper()
	exact := 0
	for _, r := range rows {
		m := loadRow.FindStringSubmatch(r)
		if m == nil {
			t.Errorf("row %q is not '<UTC time> load1=<n> busy=<pct> iowait=<pct>'", r)
			continue
		}
		if m[1] != load {
			t.Errorf("row %q: load1 = %s, want %s from the fake loadavg", r, m[1], load)
		}
		// Every delta is a whole number of feeder steps (30 busy, 60 idle, 10
		// iowait each), so the mix is exact. A starved machine can see no
		// step between two reads: that row is 0.0 and 0.0, never anything else.
		switch {
		case m[2] == "30.0" && m[3] == "10.0":
			exact++
		case m[2] == "0.0" && m[3] == "0.0":
		default:
			t.Errorf("row %q: busy=%s iowait=%s, want 30.0 and 10.0 (or 0.0 and 0.0 when no step fell between two reads)", r, m[2], m[3])
		}
	}
	if exact < len(rows)/2 {
		t.Errorf("only %d of %d rows have the fed mix of busy=30.0 iowait=10.0", exact, len(rows))
	}
}

func TestLoadSampleWritesARowPerIntervalWithTheExpectedFields(t *testing.T) {
	proc := fakeProc(t, "7.25")
	file := filepath.Join(t.TempDir(), "ci-results", "load-samples.txt")
	cmd := exec.Command("sh", repoScript(t, "ci/loadsample.sh"), "-i", "0.2", "-w", "0.1", "-p", proc, file)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file); err == nil && len(readRows(t, file)) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("the sampler exited with %v after TERM, want a clean exit 0", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sampler did not stop within 5 s of TERM (the interval is 0.2 s)")
	}
	rows := readRows(t, file)
	if len(rows) < 3 {
		t.Fatalf("got %d rows, want at least 3 (two interval rows plus the final one): %q", len(rows), rows)
	}
	checkRows(t, rows, "7.25")
}

func TestLoadSampleStopsOnTermWithoutWaitingOutALongInterval(t *testing.T) {
	proc := fakeProc(t, "1.00")
	file := filepath.Join(t.TempDir(), "s.txt")
	cmd := exec.Command("sh", repoScript(t, "ci/loadsample.sh"), "-i", "300", "-w", "0.1", "-p", proc, file)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	for i := 0; i < 100 && (len(rowsOrNil(file)) < 1); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	start := time.Now()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit after TERM: %v, want 0", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("stopped after %s, want promptly (the interval is 300 s)", took)
	}
	if rows := readRows(t, file); len(rows) < 2 {
		t.Errorf("got %d rows, want the first and the final one: %q", len(rows), rows)
	}
}

func rowsOrNil(file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

func TestLoadSampleSamplesForTheWholeRunOfACommandAndKeepItsExitStatus(t *testing.T) {
	proc := fakeProc(t, "0.50")
	file := filepath.Join(t.TempDir(), "load-samples.txt")
	cmd := exec.Command("sh", repoScript(t, "ci/loadsample.sh"), "-i", "0.2", "-w", "0.1", "-p", proc, file,
		"--", "sh", "-c", "sleep 1.5; exit 3")
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("exit = %v, want the command's status 3", err)
	}
	rows := readRows(t, file)
	if len(rows) < 4 {
		t.Fatalf("got %d rows over a 1.5 s command at a 0.2 s interval, want at least 4: %q", len(rows), rows)
	}
	checkRows(t, rows, "0.50")
}

func TestLoadSampleDefaultsToOneRowPerMinute(t *testing.T) {
	src, err := os.ReadFile(repoScript(t, "ci/loadsample.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "\ninterval=60\n") {
		t.Error("the default interval is not 60 seconds")
	}
}

func TestLoadSampleRefusesAnUnreadableProcDirectory(t *testing.T) {
	cmd := exec.Command("sh", repoScript(t, "ci/loadsample.sh"), "-p", t.TempDir(), filepath.Join(t.TempDir(), "x.txt"))
	if err := cmd.Run(); err == nil {
		t.Error("sampling a directory with no stat succeeded, want a failure")
	}
}

func TestJenkinsfileRunsTheE2EUnitUnderTheLoadSampler(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	for _, want := range []string{
		"name: 'E2E', minutes: 40, signal: 'E2E', sample: true",
		`ci/loadsample.sh "$BOARD_CI_OUT/load-samples.txt" -- `,
		"BOARD_UNIT_SAMPLE=${u.sample ? 'yes' : ''}",
	} {
		if !strings.Contains(jf, want) {
			t.Errorf("Jenkinsfile lacks %q", want)
		}
	}
	// No other unit is sampled: one file, one unit.
	if n := strings.Count(jf, ", sample: true"); n != 1 {
		t.Errorf("%d units are marked sample: true, want only E2E", n)
	}
}

// writeTask writes <proc>/<pid>/task/<tid>/stat as the kernel does: "<tid> (<comm>) <state> ...".
func writeTask(t *testing.T, proc string, pid int, tid int, comm, state string) {
	t.Helper()
	dir := filepath.Join(proc, fmt.Sprint(pid), "task", fmt.Sprint(tid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) %s 1 1 1 0 -1 4194560 100 0 0 0 1 1 0 0 20 0 1 0 100 1000 100\n", tid, comm, state)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The sampler says whose load it is: each interval it counts the runnable and
// uninterruptible threads by process name and by cgroup, as the load average does.
func TestLoadSampleNamesTheThreadsThatMakeTheLoad(t *testing.T) {
	proc := fakeProc(t, "9.00")
	// Two runnable compile threads and one uninterruptible one (a name with a
	// space and a ")" in it), a node process running vitest, and threads that do not count.
	writeTask(t, proc, 100, 100, "compile", "R")
	writeTask(t, proc, 100, 101, "compile", "R")
	writeTask(t, proc, 100, 102, "we ird) name", "D")
	writeTask(t, proc, 100, 103, "idle thread", "S")
	writeTask(t, proc, 200, 200, "node", "R")
	writeTask(t, proc, 300, 300, "chrome", "Z")
	for pid, content := range map[int]string{
		100: "0::/system.slice/docker-abc123.scope\n",
		200: "0::/system.slice/jenkins-agent.service\n",
	} {
		if err := os.WriteFile(filepath.Join(proc, fmt.Sprint(pid), "cgroup"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proc, "200", "cmdline"), []byte("node\x00--max-old-space-size=4096\x00/app/node_modules/vitest/vitest.mjs\x00run\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "ci-results", "load-samples.txt")
	cmd := exec.Command("sh", repoScript(t, "ci/loadsample.sh"), "-i", "4", "-w", "0.1", "-n", "2", "-p", proc, file,
		"--", "sh", "-c", "sleep 6")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, filepath.Join(filepath.Dir(file), "load-samples-procs.txt"))
	if len(rows) < 1 {
		t.Fatalf("no row in load-samples-procs.txt after a 6 s command at a 4 s interval")
	}
	for _, row := range rows {
		for _, want := range []string{
			"runnable=4.0 ", // compile x2, the "D" thread, node
			" comm compile=2.0 ", "we_ird)_name=1.0", "node:vitest.mjs=1.0",
			" cgroup docker-abc123.scope=3.0 ", "jenkins-agent.service=1.0",
		} {
			if !strings.Contains(row, want) {
				t.Errorf("row %q lacks %q", row, want)
			}
		}
		if strings.Contains(row, "idle_thread") || strings.Contains(row, "chrome") {
			t.Errorf("row %q counts a thread that is neither runnable nor uninterruptible", row)
		}
	}
	// The interval rows themselves keep their own format.
	checkRows(t, readRows(t, file), "9.00")
}

func TestLoadSampleTakesNoSnapshotsAtAShortDefaultInterval(t *testing.T) {
	proc := fakeProc(t, "1.00")
	writeTask(t, proc, 100, 100, "compile", "R")
	file := filepath.Join(t.TempDir(), "load-samples.txt")
	if err := exec.Command("sh", repoScript(t, "ci/loadsample.sh"), "-i", "0.2", "-w", "0.1", "-p", proc, file,
		"--", "sh", "-c", "sleep 0.8").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(file), "load-samples-procs.txt")); err == nil {
		t.Error("a 0.2 s interval wrote a procs file; snapshots are for intervals of 10 s or more unless -n asks")
	}
}
