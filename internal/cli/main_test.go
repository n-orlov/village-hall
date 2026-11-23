package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain fails the package when its tests opened more live boards than
// maxLiveCases, whichever test opened them (see askpoll_live_test.go).
func TestMain(m *testing.M) {
	code := m.Run()
	if n := int(liveBoards.Load()); n > maxLiveCases {
		fmt.Fprintf(os.Stderr, "FAIL: %d real-clock live boards opened, at most %d allowed\n", n, maxLiveCases)
		code = 1
	}
	os.Exit(code)
}
