package store_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
)

// hotReadIndexes is what migration 00016 adds: each index with its table and the
// columns it leads with, in order.
var hotReadIndexes = []struct {
	name, table string
	columns     []string
}{
	{"events_task_n", "events", []string{"task_id", "n"}},
	{"events_task_type_n", "events", []string{"task_id", "type", "n"}},
	{"events_at_n", "events", []string{"at", "n"}},
	{"events_actor_type_n", "events", []string{"actor_id", "type", "n"}},
	{"tasks_status_id", "tasks", []string{"status", "id"}},
}

// indexColumns is the columns of the named index in order, nil when it does not exist.
func indexColumns(t *testing.T, s *store.Store, table, name string) []string {
	t.Helper()
	var query string
	switch s.Dialect().Name() {
	case "sqlite":
		query = `SELECT name FROM pragma_index_info('` + name + `') ORDER BY seqno`
	default:
		query = `SELECT a.attname FROM pg_index x JOIN pg_class c ON c.oid = x.indexrelid
			JOIN pg_class tc ON tc.oid = x.indrelid
			JOIN LATERAL unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
			JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum = k.attnum
			WHERE c.relname = '` + name + `' AND tc.relname = '` + table + `' ORDER BY k.ord`
	}
	rows, err := s.Reader().Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

// TestMigration00016HotReadIndexes: version 15 has none of the R1 indexes,
// migrating to 16 creates every one with its columns, rolling back to 15 drops
// every one, and migrating again recreates them. It runs on every database leg.
func TestMigration00016HotReadIndexes(t *testing.T) {
	ctx := context.Background()
	eachLeg(t, func(t *testing.T, leg string) {
		url := "sqlite://" + filepath.Join(t.TempDir(), "board.db")
		if leg == "postgres" {
			url = testPostgres.Load().create(t, "")
		}
		s, err := store.Open(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })

		expect := func(stage string, present bool) {
			t.Helper()
			for _, ix := range hotReadIndexes {
				got := indexColumns(t, s, ix.table, ix.name)
				switch {
				case present && !slices.Equal(got, ix.columns):
					t.Errorf("%s: index %s on %s has columns %v, want %v", stage, ix.name, ix.table, got, ix.columns)
				case !present && len(got) != 0:
					t.Errorf("%s: index %s still exists with columns %v", stage, ix.name, got)
				}
			}
		}
		if err := s.MigrateTo(ctx, 15); err != nil {
			t.Fatal(err)
		}
		expect("at version 15", false)
		if err := s.MigrateTo(ctx, 16); err != nil {
			t.Fatal(err)
		}
		expect("after Up to 16", true)
		if err := s.DownTo(ctx, 15); err != nil {
			t.Fatalf("rolling back to 15: %v", err)
		}
		expect("after Down to 15", false)
		if v, err := s.SchemaVersion(ctx); err != nil || v != 15 {
			t.Fatalf("version after Down = %d, %v; want 15", v, err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		expect("after Up again", true)
		if v, err := s.SchemaVersion(ctx); err != nil || v != 17 {
			t.Fatalf("latest version = %d, %v; want 17", v, err)
		}
		if err := s.Ready(ctx); err != nil {
			t.Errorf("not ready at the latest version: %v", err)
		}
	})
}
