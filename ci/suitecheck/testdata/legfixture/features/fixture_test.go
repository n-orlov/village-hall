// Package features is the leg fixture for ci/suitecheck's database-matrix test.
// Its TestFeatures stands in for the Godog entry point: two scenario subtests
// that both pass on every leg, except that "diverges" fails on the leg named by
// LEGFIXTURE_RED_ON (a database leg, "both", or nothing). It reads the leg from
// BOARD_DB_LEG exactly as the real features/ package does, so it proves
// ci/suite.sh hands each pass its own leg.
package features

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestFeatures(t *testing.T) {
	leg := os.Getenv("BOARD_DB_LEG")
	red := os.Getenv("LEGFIXTURE_RED_ON")
	if runs := os.Getenv("LEGFIXTURE_FEATURE_RUNS"); runs != "" {
		f, err := os.OpenFile(runs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, _ = fmt.Fprintf(f, "leg=%s paths=%s\n", leg, os.Getenv("BOARD_GODOG_PATHS"))
	}
	// LEGFIXTURE_GODOG_FAILS_FIRST names the legs ("sqlite", "postgres", or
	// both separated by a space) on which a run that selects every scenario
	// fails the way Godog does, with its "--- Failed steps:" summary naming the
	// scenario's file:line. A run that selects that one scenario (BOARD_GODOG_PATHS,
	// what a per-scenario rerun sets) passes. A suite that reruns the failed
	// scenario is made green by it; one that does not, stays red.
	if legs := strings.Fields(os.Getenv("LEGFIXTURE_GODOG_FAILS_FIRST")); os.Getenv("BOARD_GODOG_PATHS") == "" {
		for _, l := range legs {
			if l == leg {
				fmt.Print("--- Failed steps:\n\n  Scenario: flaky scenario # fixture.feature:3\n    Then it works # fixture.feature:5\n      Error: boom\n\n1 scenarios (1 failed)\n")
				t.Errorf("scenario fails on the %s leg until it is rerun alone", leg)
			}
		}
	}
	t.Run("agrees", func(t *testing.T) {})
	t.Run("diverges", func(t *testing.T) {
		if red == "both" || (red != "" && red == leg) {
			t.Fatalf("scenario is red on the %s leg", leg)
		}
	})
	if marker := os.Getenv("LEGFIXTURE_LEG_LOG"); marker != "" {
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, _ = f.WriteString(leg + "\n")
	}
}
