package server

import (
	goflag "flag"
	"os"
	"runtime"
	"strconv"
	"testing"
)

// The package's slots for parallel tests follow its CPUs, twice over, unless
// the caller named -parallel: ci/stability.sh's run at -parallel=1 stays one
// test at a time.
func TestParallelIsRaisedOnlyWhenNoCallerNamedIt(t *testing.T) {
	f := goflag.Lookup("test.parallel")
	if f == nil {
		t.Fatal("no test.parallel flag registered")
	}
	args, was := os.Args, f.Value.String()
	t.Cleanup(func() { os.Args = args; _ = f.Value.Set(was) })

	_ = f.Value.Set("1")
	os.Args = []string{"server.test", "-test.count=1", "-test.parallel=1"}
	raiseParallelWhenUnset()
	if got := f.Value.String(); got != "1" {
		t.Errorf("-parallel named as 1, package set %s", got)
	}

	os.Args = []string{"server.test", "-test.count=1"}
	raiseParallelWhenUnset()
	if want := strconv.Itoa(sleepBoundParallelFactor * runtime.GOMAXPROCS(0)); f.Value.String() != want {
		t.Errorf("-parallel unnamed: slots = %s, want %s", f.Value.String(), want)
	}
}
