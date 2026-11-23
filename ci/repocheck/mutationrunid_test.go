package repocheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Jenkins Mutation unit's Postgres testcontainers (features, internal/server
// and internal/feed tests, run by gremlins for every mutant) must carry the
// attempt's ralphd.run label, or the unit's exact-ID cleanup never sees the ones
// a killed mutant run leaves behind. The tests read BOARD_CI_RUN_ID, ci/run.sh
// forwards it from RALPHD_RUN_ID, and ci/mutation.sh scrubs every BOARD_*
// variable: it has to hand that one on to gremlins on purpose.

const stubGo = `#!/bin/sh
# ci/mutationgate -list prints one package; every other go command succeeds.
case "$*" in
*" -list "*) echo internal/stub ;;
esac
exit 0
`

const stubGremlins = `#!/bin/sh
env | sed -n 's/^\(BOARD_[A-Za-z0-9_]*=.*\)$/\1/p' | sort > "$STUB_GREMLINS_ENV"
exit 0
`

// gremlinsBoardEnv runs ci/mutation.sh with stub go and gremlins commands first
// on PATH (nothing real runs) and returns the BOARD_* variables gremlins saw.
func gremlinsBoardEnv(t *testing.T, env ...string) []string {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("repositoryRoot: %v", err)
	}
	bin := t.TempDir()
	for name, body := range map[string]string{"go": stubGo, "gremlins": stubGremlins} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	record := filepath.Join(bin, "gremlins.env")
	out := t.TempDir()
	// Lane "stub" is neither all nor base, so the script never reaches Stryker.
	cmd := exec.Command("sh", filepath.Join(root, "ci", "mutation.sh"), out, "ci/quality.json", "stub")
	cmd.Dir = root
	cmd.Env = append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"STUB_GREMLINS_ENV=" + record,
	}, env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ci/mutation.sh failed: %v\n%s", err, b)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the stub gremlins never ran: %v", err)
	}
	return strings.Fields(string(raw))
}

// TestMutationScriptHandsTheAttemptLabelToGremlins: the tests gremlins runs see
// the attempt's run id and BOARD_DB_LEG=sqlite (whatever leg the caller named),
// and no other BOARD_* variable.
func TestMutationScriptHandsTheAttemptLabelToGremlins(t *testing.T) {
	got := gremlinsBoardEnv(t,
		"BOARD_CI_RUN_ID=attempt-7",
		"BOARD_CI_DOCKER_HOST=host.docker.internal",
		"BOARD_DB_LEG=postgres",
		"BOARD_STRAY=1",
	)
	want := []string{"BOARD_CI_RUN_ID=attempt-7", "BOARD_DB_LEG=sqlite"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("gremlins saw BOARD_* variables %q, want exactly %q", got, want)
	}
}

// TestMutationScriptWithoutAnAttemptLabelPassesNoOtherBoardVariable: a local run
// has no run id, so none is invented and the scrub stays total: the one variable
// gremlins sees is BOARD_DB_LEG=sqlite.
func TestMutationScriptWithoutAnAttemptLabelPassesNoOtherBoardVariable(t *testing.T) {
	got := gremlinsBoardEnv(t, "BOARD_STRAY=1")
	if len(got) != 1 || got[0] != "BOARD_DB_LEG=sqlite" {
		t.Errorf("gremlins saw BOARD_* variables %q, want exactly [BOARD_DB_LEG=sqlite]", got)
	}
}

// TestJenkinsfileMutationUnitRunsUnderTheAttemptRunID: every unit, the Mutation
// unit included, runs with RALPHD_RUN_ID set to the attempt's id (ci/run.sh
// turns it into the container label and BOARD_CI_RUN_ID), and the unit's
// finally block removes by that same label.
func TestJenkinsfileMutationUnitRunsUnderTheAttemptRunID(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	for _, want := range []string{
		`withEnv(["RALPHD_RUN_ID=${runId}"`,
		"docker ps -aq --filter label=ralphd.run=${runId}",
		"docker rm -f \\$ids",
	} {
		if !strings.Contains(jf, want) {
			t.Errorf("Jenkinsfile lacks %s", want)
		}
	}
	if !strings.Contains(jf, " ci/run.sh ci/mutation.sh ci-results/mutation ci/quality.json all") {
		t.Error("the Mutation unit no longer runs ci/mutation.sh through ci/run.sh")
	}
}

const stubGoRecordingTests = `#!/bin/sh
# ci/mutationgate -list prints one package; go test records the BOARD_DB_LEG it
# runs with; every other go command succeeds.
case "$*" in
*" -list "*) echo internal/stub ;;
"test "*) echo "BOARD_DB_LEG=${BOARD_DB_LEG-unset}" >> "$STUB_GO_TEST_ENV" ;;
esac
exit 0
`

const stubGremlinsRecordingLeg = `#!/bin/sh
echo "BOARD_DB_LEG=${BOARD_DB_LEG-unset}" >> "$STUB_GREMLINS_ENV"
exit 0
`

// TestMutationScriptRunsTheSQLiteLegOnly: both test runs ci/mutation.sh starts,
// the covered run it times (go test) and the mutant runs (gremlins), get
// BOARD_DB_LEG=sqlite, even when the caller asked for the Postgres leg, so no
// mutant of internal/server, internal/store or internal/feed starts a Postgres
// container. The Suite unit's matrix runs both legs.
func TestMutationScriptRunsTheSQLiteLegOnly(t *testing.T) {
	for _, caller := range []string{"BOARD_DB_LEG=postgres", "BOARD_STRAY=1"} {
		root, err := repositoryRoot()
		if err != nil {
			t.Fatalf("repositoryRoot: %v", err)
		}
		bin := t.TempDir()
		for name, body := range map[string]string{"go": stubGoRecordingTests, "gremlins": stubGremlinsRecordingLeg} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
				t.Fatalf("write stub %s: %v", name, err)
			}
		}
		goRec := filepath.Join(bin, "go.env")
		gremRec := filepath.Join(bin, "gremlins.env")
		cmd := exec.Command("sh", filepath.Join(root, "ci", "mutation.sh"), t.TempDir(), "ci/quality.json", "stub")
		cmd.Dir = root
		cmd.Env = []string{
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"STUB_GO_TEST_ENV=" + goRec,
			"STUB_GREMLINS_ENV=" + gremRec,
			caller,
		}
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ci/mutation.sh failed: %v\n%s", err, b)
		}
		for what, path := range map[string]string{"the covered run (go test)": goRec, "the mutant run (gremlins)": gremRec} {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: the stub never ran (caller %s): %v", what, caller, err)
			}
			if got := strings.TrimSpace(string(raw)); got != "BOARD_DB_LEG=sqlite" {
				t.Errorf("%s ran with %q (caller %s), want BOARD_DB_LEG=sqlite", what, got, caller)
			}
		}
	}
}
