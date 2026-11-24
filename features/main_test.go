package features

import (
	"os"
	"testing"
)

// TestMain removes the package's shared Postgres container (sharedPostgres)
// once every test has ended.
func TestMain(m *testing.M) {
	code := m.Run()
	closeSharedPostgres()
	os.Exit(code)
}
