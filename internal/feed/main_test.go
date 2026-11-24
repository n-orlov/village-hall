package feed

import (
	"os"
	"testing"
)

// TestMain removes the package's Postgres container (startPostgres) once every
// test has ended.
func TestMain(m *testing.M) {
	code := m.Run()
	closePostgres()
	os.Exit(code)
}
