//go:build !vhfeatures

package cli

import (
	"testing"
	"time"
)

// The shipped binary's second of --poll is a real second, whatever the
// environment holds: only the scenario harness's build reads
// BOARD_FEATURES_POLL_SECOND.
func TestPollSecondIsARealSecondInTheShippedBuild(t *testing.T) {
	for _, v := range []string{"", "300ms", "1ms", "2s", "soon"} {
		env := func(name string) string {
			if name == "BOARD_FEATURES_POLL_SECOND" {
				return v
			}
			return ""
		}
		if got := pollSecond(env); got != time.Second {
			t.Errorf("pollSecond with BOARD_FEATURES_POLL_SECOND=%q = %s, want 1s", v, got)
		}
	}
}
