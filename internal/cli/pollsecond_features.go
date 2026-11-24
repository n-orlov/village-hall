//go:build vhfeatures

package cli

import "time"

// pollSecondVar names the CI-internal variable that shortens a second of
// --poll in the scenario harness's build. It is not a product variable
// (internal/envvar), and no other build reads it.
const pollSecondVar = "BOARD_FEATURES_POLL_SECOND"

// pollSecond is how long one second of --poll lasts in the binary the
// scenario harness builds (features/harness_test.go, -tags vhfeatures): a real
// second, unless a scenario sets BOARD_FEATURES_POLL_SECOND to a shorter Go
// duration, so a `--poll 1s` deadline passes in less than a second. The
// deadline is still reached through the same waits: each ?wait= is whole
// seconds and carries the deadline as until, which ends the last hold.
func pollSecond(env Env) time.Duration {
	d, err := time.ParseDuration(env(pollSecondVar))
	if err != nil {
		return time.Second
	}
	return shorterSecond(d)
}

// shorterSecond is d when it is above zero and under a second, else a real
// second: the variable can only shorten a second of --poll.
func shorterSecond(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	return min(d, time.Second)
}
