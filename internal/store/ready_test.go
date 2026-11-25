package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// TestReadyReadsOnlyTheAppliedVersion pins what one readiness probe costs a
// migrated database: one reader statement (the applied version), no writer
// statement, no transaction.
func TestReadyReadsOnlyTheAppliedVersion(t *testing.T) {
	t.Parallel()
	st, rec := storetest.OpenMigrated(t)
	if err := st.Ready(context.Background()); err != nil {
		t.Fatalf("Ready on a migrated database = %v", err)
	}
	if n := rec.Statements(store.PoolReader); n != 1 {
		t.Errorf("Ready ran %d reader statements, want 1: %+v", n, rec.All())
	}
	if n := rec.Statements(store.PoolWriter); n != 0 {
		t.Errorf("Ready ran %d writer statements, want 0: %+v", n, rec.All())
	}
	if n := rec.Count("", storetest.Begin); n != 0 {
		t.Errorf("Ready began %d transactions, want 0", n)
	}
}

// TestReadyIsNotReadyOnADatabaseBehindTheBinary shows the version Open computed
// is still compared with the applied one: a database one migration behind is
// not ready, and the probe still costs one reader statement.
func TestReadyIsNotReadyOnADatabaseBehindTheBinary(t *testing.T) {
	t.Parallel()
	st, rec, err := storetest.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.MigrateTo(ctx, 15); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	if err := st.Ready(ctx); !errors.Is(err, store.ErrNotMigrated) {
		t.Fatalf("Ready on a database at version 15 = %v, want ErrNotMigrated", err)
	}
	if n := rec.Statements(""); n != 1 {
		t.Errorf("a not-ready probe ran %d statements, want 1: %+v", n, rec.All())
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Ready(ctx); err != nil {
		t.Fatalf("Ready after Migrate = %v", err)
	}
}
