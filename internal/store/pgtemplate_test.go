package store_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
)

// TestPostgresTestDatabasesAreMigratedCopiesOfOneTemplate is the contract of
// newPostgresDatabase: each database it hands out comes from the container's
// template, which is migrated once, so a store opened on it (and never
// migrated by the test) already reports the current goose schema version, and
// the two databases are separate: a row written to one is absent from the other.
// It runs on the Postgres leg only, as the whole-suite Go pass (BOARD_DB_LEG
// unset) does; on BOARD_DB_LEG=sqlite there is no container to copy.
func TestPostgresTestDatabasesAreMigratedCopiesOfOneTemplate(t *testing.T) {
	if !slices.Contains(legsOf(t), "postgres") {
		return
	}
	ctx := context.Background()

	reference, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reference.Close() })
	if err := reference.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	head, err := reference.SchemaVersion(ctx)
	if err != nil || head == 0 {
		t.Fatalf("schema version of a migrated database = %d, %v; want the head", head, err)
	}

	withPostgres(t, func() {
		open := func(url string) *store.Store {
			st, err := store.Open(url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}
		a, b := open(newPostgresDatabase(t)), open(newPostgresDatabase(t))
		for name, st := range map[string]*store.Store{"first": a, "second": b} {
			if v, err := st.SchemaVersion(ctx); err != nil || v != head {
				t.Errorf("%s database: goose version = %d, %v; want %d without a Migrate of the test's own", name, v, err, head)
			}
			if err := st.Ready(ctx); err != nil {
				t.Errorf("%s database is not ready: %v", name, err)
			}
		}

		d := a.Dialect()
		if _, err := a.Writer().ExecContext(ctx, d.Rebind("INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?, ?)"),
			"ACTOR1", "claude@laptop", "agent", 1, 2); err != nil {
			t.Fatal(err)
		}
		count := func(st *store.Store) int {
			var n int
			if err := st.Reader().QueryRowContext(ctx, "SELECT count(*) FROM actors").Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if got := count(a); got != 1 {
			t.Errorf("first database holds %d actors after the insert, want 1", got)
		}
		if got := count(b); got != 0 {
			t.Errorf("second database holds %d actors, want the row written to the first to be absent", got)
		}
	})
}
