package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

type noMigrations struct{}

func (noMigrations) Open(string) (fs.File, error) { return nil, errors.New("migrations were read") }

// TestReadyAndCheckSchemaNeverReadTheEmbeddedMigrations shows the newest
// version is computed once, in Open: after it, the migration files are
// unreachable and Ready and CheckSchema still answer.
func TestReadyAndCheckSchemaNeverReadTheEmbeddedMigrations(t *testing.T) {
	t.Parallel()
	s, err := Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if s.newest < 16 {
		t.Fatalf("newest = %d, want the binary's latest (>= 16)", s.newest)
	}
	s.migrations = noMigrations{}
	if err := s.Ready(ctx); err != nil {
		t.Errorf("Ready = %v", err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Errorf("CheckSchema = %v", err)
	}
}
