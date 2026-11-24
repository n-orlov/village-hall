//go:build !vhfeatures

package cli

import "time"

// pollSecond is how long one second of --poll lasts: a real second, always.
// This is the file every build of vh compiles (`go build`, `go test`, the
// image and the release); only the scenario harness builds with the
// vhfeatures tag, which swaps in pollsecond_features.go.
func pollSecond(Env) time.Duration { return time.Second }
