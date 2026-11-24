package repocheck

// R6, task 020: the agent has 12 hardware threads and the build's load must stay
// at or below 12 on every sample. An uncapped Go build of a cold cache runs 25-30
// threads at once, so every Core unit runs under a CPU cap (the Jenkinsfile's
// cpus: field -> BOARD_CI_CPUS -> ci/run.sh's --cpus, and the BuildKit
// container's cpu-quota for the Image unit), and the caps of the four units the
// four executors can run at once sum to at most 12.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	unitCPUsRE = regexp.MustCompile(`units << \[name: '([^']+)', minutes: [^,]+, cpus: ([0-9]+),`)
	cpuEnvRE   = regexp.MustCompile(`"BOARD_CI_CPUS=\$\{u\.cpus \?: ''\}"`)
	qualityRE  = regexp.MustCompile(`runUnit\(\[name: 'Quality', minutes: 30, cpus: ([0-9]+),`)
)

// cpuCappedUnits are the units of the Core phase that run beside one another.
var cpuCappedUnits = []string{"Lint", "Web UI", "Suite", "E2E", "Image", "Allure smoke"}

const (
	agentThreads   = 12
	agentExecutors = 4
)

// cpuCapProblems checks that every Core unit and Quality carries a cap, that the
// attempt exports it, and that the largest four caps fit the agent's threads.
func cpuCapProblems(jf string) []string {
	var problems []string
	caps := map[string]int{}
	for _, m := range unitCPUsRE.FindAllStringSubmatch(jf, -1) {
		n, _ := strconv.Atoi(m[2])
		caps[m[1]] = n
	}
	var sizes []int
	for _, name := range cpuCappedUnits {
		n, ok := caps[name]
		if !ok || n < 1 {
			problems = append(problems, "unit "+name+" has no cpus: cap of at least 1")
			continue
		}
		sizes = append(sizes, n)
	}
	if !qualityRE.MatchString(jf) {
		problems = append(problems, "the Quality unit has no cpus: cap")
	}
	if !cpuEnvRE.MatchString(jf) {
		problems = append(problems, "runUnit does not export BOARD_CI_CPUS from the unit's cpus")
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	sum := 0
	for i := 0; i < len(sizes) && i < agentExecutors; i++ {
		sum += sizes[i]
	}
	if sum > agentThreads {
		problems = append(problems, "the four largest unit caps sum to "+strconv.Itoa(sum)+", over the agent's "+strconv.Itoa(agentThreads)+" threads")
	}
	return problems
}

func TestEveryCoreUnitRunsUnderACPUCapThatFitsTheAgent(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, cpuCapProblems, map[string]string{
		"a Suite without a cap":        replaceOnce(t, jf, "minutes: nightly ? 120 : 45, cpus: 4,", "minutes: nightly ? 120 : 45,"),
		"an E2E without a cap":         replaceOnce(t, jf, "name: 'E2E', minutes: 40, cpus: 3,", "name: 'E2E', minutes: 40,"),
		"an Image without a cap":       replaceOnce(t, jf, "name: 'Image', minutes: 30, cpus: 1,", "name: 'Image', minutes: 30,"),
		"a Quality without a cap":      replaceOnce(t, jf, "name: 'Quality', minutes: 30, cpus: 4,", "name: 'Quality', minutes: 30,"),
		"caps that sum past the agent": replaceOnce(t, jf, "name: 'E2E', minutes: 40, cpus: 3,", "name: 'E2E', minutes: 40, cpus: 9,"),
		"a cap never exported":         replaceOnce(t, jf, `"BOARD_CI_CPUS=${u.cpus ?: ''}"`, `"BOARD_CI_NOTHING=1"`),
	})
}

// TestRunShCPUCap: BOARD_CI_CPUS caps the sibling's CPU time (--cpus), and
// without it, or empty, the sibling has no CPU limit.
func TestRunShCPUCap(t *testing.T) {
	argv := runShRun(t, []string{"RALPHD_HOST_WORKSPACE=/host/ws", noSocket, "BOARD_CI_CPUS=4"}, "true")
	img := indexOf(argv, "board-ci:local")
	if img < 0 {
		t.Fatalf("image board-ci:local missing from argv %q", argv)
	}
	if i := indexOf(argv, "--cpus=4"); i < 0 || i > img {
		t.Errorf("want --cpus=4 before the image, got %q", argv)
	}
	for _, env := range [][]string{{"BOARD_CI_CPUS="}, {}} {
		argv = runShRun(t, append([]string{"RALPHD_HOST_WORKSPACE=/host/ws", noSocket}, env...), "true")
		for _, a := range argv {
			if strings.HasPrefix(a, "--cpus") {
				t.Errorf("env %v, yet argv caps CPUs: %q", env, argv)
			}
		}
	}
}

// imageBuildCreateArgs runs the real ci/image-build.sh against a stub docker
// and returns the `docker buildx create` line it issued.
func imageBuildCreateArgs(t *testing.T, env ...string) string {
	t.Helper()
	for _, line := range imageBuildLog(t, env...) {
		if strings.HasPrefix(line, "docker buildx create") {
			return line
		}
	}
	t.Fatal("no docker buildx create in the stub docker's log")
	return ""
}

// imageBuildLog runs the real ci/image-build.sh against a stub docker and
// returns every line the stub saw.
func imageBuildLog(t *testing.T, env ...string) []string {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", filepath.Join(root, "ci", "image-build.sh"), filepath.Join(repo, "ci")).CombinedOutput(); err != nil {
		t.Fatalf("copy image-build.sh: %v\n%s", err, out)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "docker.log")
	stub := "#!/bin/sh\necho \"docker $*\" >> \"$STUB_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(repo, "ci", "image-build.sh"), filepath.Join(repo, "out", "board-image.tar"))
	cmd.Dir = repo
	cmd.Env = append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "STUB_LOG=" + log, "HOME=" + repo}, env...)
	// The script ends by finding its tars empty (the stub builds nothing): only
	// the calls before that matter.
	_, _ = cmd.CombinedOutput()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("stub docker saw nothing: %v", err)
	}
	return strings.Split(string(raw), "\n")
}

// TestImageBuildCapsTheBuildKitContainer: BOARD_CI_CPUS=3 gives the builder a
// quota of 3 CPUs per 100 ms period; unset, it has no quota.
func TestImageBuildCapsTheBuildKitContainer(t *testing.T) {
	line := imageBuildCreateArgs(t, "BOARD_CI_CPUS=3")
	for _, want := range []string{"--driver-opt cpu-period=100000", "--driver-opt cpu-quota=300000"} {
		if !strings.Contains(line, want) {
			t.Errorf("want %q in %q", want, line)
		}
	}
	for _, env := range [][]string{{}, {"BOARD_CI_CPUS="}} {
		if line := imageBuildCreateArgs(t, env...); strings.Contains(line, "cpu-quota") {
			t.Errorf("env %v, yet the builder is capped: %q", env, line)
		}
	}
}

// TestImageBuildBoundsTheGoCompilerThreads: a BuildKit step cgroup hides the
// builder's cpu quota from Go, so BOARD_CI_CPUS=3 is also passed to both
// `docker buildx build` calls as the Dockerfile's GOMAXPROCS build arg (the two
// cross-compiles ran 28 runnable compile threads in #310's first minutes);
// unset or empty, no build arg is passed.
func TestImageBuildBoundsTheGoCompilerThreads(t *testing.T) {
	builds := func(env ...string) []string {
		var out []string
		for _, line := range imageBuildLog(t, env...) {
			if strings.HasPrefix(line, "docker buildx build") {
				out = append(out, line)
			}
		}
		return out
	}
	capped := builds("BOARD_CI_CPUS=3")
	if len(capped) != 2 {
		t.Fatalf("want two buildx build calls, got %q", capped)
	}
	for _, line := range capped {
		if !strings.Contains(line, "--build-arg GOMAXPROCS=3") {
			t.Errorf("want --build-arg GOMAXPROCS=3 in %q", line)
		}
	}
	for _, env := range [][]string{{}, {"BOARD_CI_CPUS="}} {
		for _, line := range builds(env...) {
			if strings.Contains(line, "GOMAXPROCS") {
				t.Errorf("env %v, yet the build bounds Go: %q", env, line)
			}
		}
	}
	df := readRepoFile(t, "Dockerfile")
	if !strings.Contains(df, "\nARG GOMAXPROCS\n") {
		t.Errorf("the Dockerfile's build stage does not declare ARG GOMAXPROCS")
	}
}
