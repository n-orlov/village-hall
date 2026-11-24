//go:build vhfeatures

package cli

import (
	"testing"
	"time"
)

// In the scenario harness's build, BOARD_FEATURES_POLL_SECOND shortens a
// second of --poll to a Go duration under a second; anything else (unset, not
// a duration, zero, negative, a second or longer) leaves a real second.
// Run with: go test -tags vhfeatures -run TestPollSecond ./internal/cli/
func TestPollSecondTakesAShorterSecondInTheFeaturesBuild(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"":      time.Second,
		"300ms": 300 * time.Millisecond,
		"999ms": 999 * time.Millisecond,
		"1s":    time.Second,
		"2s":    time.Second,
		"0s":    time.Second,
		"-1ms":  time.Second,
		"soon":  time.Second,
	} {
		env := func(name string) string {
			if name == pollSecondVar {
				return v
			}
			return ""
		}
		if got := pollSecond(env); got != want {
			t.Errorf("pollSecond with %s=%q = %s, want %s", pollSecondVar, v, got, want)
		}
	}
}
