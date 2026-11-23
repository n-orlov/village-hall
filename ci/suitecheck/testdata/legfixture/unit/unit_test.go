// Package unit is the leg fixture's ordinary Go package: one passing test that
// covers its one statement, one that fails on its first run when asked to, so ci/suite.sh's step-1 pass has something to run
// and the merged coverage profile is not empty.
package unit

import (
	"os"
	"testing"
)

func TestFixtureUnitPasses(t *testing.T) {
	if Answer() != 42 {
		t.Fatal("Answer")
	}
}

// TestFixtureUnitFailsOnFirstRun stands in for a flaky test: when
// LEGFIXTURE_UNIT_FAILS_FIRST names a file, the first run of the test (the file
// does not exist yet) records itself there and fails, and every later run
// passes. A suite that reruns failed tests passes it; one that does not, fails.
// The file holds one line per run, so a test can count them.
func TestFixtureUnitFailsOnFirstRun(t *testing.T) {
	path := os.Getenv("LEGFIXTURE_UNIT_FAILS_FIRST")
	if path == "" {
		return
	}
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("run\n"); err != nil {
		t.Fatal(err)
	}
	if statErr != nil {
		t.Fatal("this test fails on its first run only")
	}
}
