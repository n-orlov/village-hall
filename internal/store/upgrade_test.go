package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// phase0Rows are one row of each Phase 0 table that holds data, written at
// schema version 2 (the head before tasks existed).
var phase0Rows = []struct {
	table string
	cols  []string
	vals  []any
}{
	{"actors", []string{"id", "handle", "kind", "display_name", "host", "way_back", "meta", "first_seen_at", "last_seen_at", "protocol_seen"},
		[]any{"ACTOR1", "op@host", "operator", "Op", "host", "ssh op", `{"os":"linux"}`, 1000, 2000, 1}},
	{"initiatives", []string{"id", "slug", "title", "description", "status", "target_start", "target_end", "created_by", "created_at", "closed_at", "closed_by"},
		[]any{"INIT1", "phone-onboard", "Phone", "Markdown *text*", "closed", "2026-03-01", "2026-04-01", "ACTOR1", 3000, 4000, "ACTOR1"}},
	{"initiative_tags", []string{"initiative_id", "tag"}, []any{"INIT1", "home"}},
	{"api_keys", []string{"id", "name", "key_hash", "display_prefix", "created_at", "created_by", "last_used_at", "expires_at", "revoked_at"},
		[]any{"KEY1", "laptop", "hash1", "brd_ab", 5000, "admin-cli", 6000, 7000, 8000}},
	{"sessions", []string{"id_hash", "email", "csrf_token", "created_at", "last_seen_at", "expires_at"},
		[]any{"sess1", "op@example.com", "csrf1", 9000, 9100, 9200}},
	{"events", []string{"id", "at", "actor_id", "principal", "on_behalf_of", "initiative_id", "task_id", "type", "ref_id", "summary", "data"},
		[]any{"EV1", 9300, "ACTOR1", "oidc:op@example.com", "ACTOR1", "INIT1", "NOTATASK", "initiative_created", "INIT1", "initiative phone-onboard created", `{"slug":"phone-onboard"}`}},
}

// snapshot reads every row of table's cols as text, ordered by the first one.
func snapshot(t *testing.T, s *Store, table string, cols []string) [][]string {
	t.Helper()
	rows, err := s.Reader().Query("SELECT " + strings.Join(cols, ", ") + " FROM " + table + " ORDER BY " + cols[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = fmt.Sprintf("%T:%v", v, v)
		}
		out = append(out, row)
	}
	return out
}

// TestMigratingPhase0RowsToHeadKeepsThemUnchanged writes one row into each
// Phase 0 table at version 2, migrates to the head and reads the rows back.
func TestMigratingPhase0RowsToHeadKeepsThemUnchanged(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v != 2 {
		t.Fatalf("version before = %d, %v; want 2", v, err)
	}
	for _, r := range phase0Rows {
		q := s.Dialect().Rebind(s.Dialect().Insert(r.table, r.cols))
		if _, err := s.Writer().ExecContext(ctx, q, r.vals...); err != nil {
			t.Fatalf("insert into %s: %v", r.table, err)
		}
	}
	before := map[string][][]string{}
	for _, r := range phase0Rows {
		before[r.table] = snapshot(t, s, r.table, r.cols)
		if len(before[r.table]) != 1 {
			t.Fatalf("%s holds %d rows before, want 1", r.table, len(before[r.table]))
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if latest := s.newest; latest < 4 {
		t.Fatalf("head = %d, want at least 4", latest)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range phase0Rows {
		if got := snapshot(t, s, r.table, r.cols); !reflect.DeepEqual(got, before[r.table]) {
			t.Errorf("%s after the upgrade = %v, want %v", r.table, got, before[r.table])
		}
	}
	var next int
	if err := s.Reader().QueryRow("SELECT next_task_seq FROM initiatives WHERE id = 'INIT1'").Scan(&next); err != nil || next != 1 {
		t.Errorf("next_task_seq of the upgraded initiative = %d, %v; want 1", next, err)
	}
}

// indexColumns lists the columns of the named index in order.
func indexColumns(t *testing.T, s *Store, index string) []string {
	t.Helper()
	rows, err := s.Reader().Query("SELECT name FROM pragma_index_info('" + index + "') ORDER BY seqno")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

// TestTasksHaveTheSpecIndexesAndAUniqueSeqPerInitiative checks the SPEC 4.10
// indexes on tasks, and that two tasks of one initiative cannot share a seq.
func TestTasksHaveTheSpecIndexesAndAUniqueSeqPerInitiative(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"tasks_status_initiative":  {"status", "initiative_id"},
		"tasks_kind_next_check_at": {"kind", "next_check_at"},
		"tasks_closed_at":          {"closed_at"},
	} {
		if got := indexColumns(t, s, name); !reflect.DeepEqual(got, want) {
			t.Errorf("index %s = %v, want %v", name, got, want)
		}
	}
	d := s.Dialect()
	exec := func(table string, cols []string, vals ...any) error {
		_, err := s.Writer().ExecContext(ctx, d.Rebind(d.Insert(table, cols)), vals...)
		return err
	}
	for _, r := range phase0Rows[:2] {
		if err := exec(r.table, r.cols, r.vals...); err != nil {
			t.Fatal(err)
		}
	}
	taskCols := []string{"id", "initiative_id", "seq", "title", "created_by", "created_at", "updated_at"}
	if err := exec("tasks", taskCols, "T1", "INIT1", 1, "t", "ACTOR1", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := exec("tasks", taskCols, "T2", "INIT1", 1, "t", "ACTOR1", 1, 1); err == nil || !d.IsUniqueViolation(err) {
		t.Errorf("a second task with seq 1 = %v, want a unique violation", err)
	}
	if err := exec("tasks", taskCols, "T3", "INIT1", 2, "t", "ACTOR1", 1, 1); err != nil {
		t.Errorf("seq 2 = %v", err)
	}
	depCols := []string{"task_id", "blocked_by_task_id"}
	if err := exec("task_deps", depCols, "T1", "T1"); err == nil {
		t.Error("a task depending on itself was accepted")
	}
	if err := exec("task_deps", depCols, "T1", "T3"); err != nil {
		t.Errorf("a dependency = %v", err)
	}
}

// TestLeasesHaveTheSpecIndexesAndTheirResourcesKeyOnBothColumns checks the SPEC
// 4.4 and 4.10 indexes on leases and lease_resources, and that a lease holds a
// resource once, takes only the three states and keeps its ttl within bounds.
func TestLeasesHaveTheSpecIndexesAndTheirResourcesKeyOnBothColumns(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"leases_state_expires_at":            {"state", "expires_at"},
		"lease_resources_resource":           {"resource"},
		"sqlite_autoindex_lease_resources_1": {"lease_id", "resource"},
	} {
		if got := indexColumns(t, s, name); !reflect.DeepEqual(got, want) {
			t.Errorf("index %s = %v, want %v", name, got, want)
		}
	}
	d := s.Dialect()
	exec := func(table string, cols []string, vals ...any) error {
		_, err := s.Writer().ExecContext(ctx, d.Rebind(d.Insert(table, cols)), vals...)
		return err
	}
	for _, r := range phase0Rows[:2] {
		if err := exec(r.table, r.cols, r.vals...); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec("tasks", []string{"id", "initiative_id", "seq", "title", "created_by", "created_at", "updated_at"}, "T1", "INIT1", 1, "t", "ACTOR1", 1, 1); err != nil {
		t.Fatal(err)
	}
	cols := []string{"id", "task_id", "actor_id", "intent", "ttl_seconds", "started_at", "renewed_at", "expires_at", "state"}
	if err := exec("leases", cols, "L1", "T1", "ACTOR1", "i", 60, 1, 1, 2, "live"); err != nil {
		t.Fatalf("a lease = %v", err)
	}
	for name, vals := range map[string][]any{
		"ttl 59":        {"L2", "T1", "ACTOR1", "i", 59, 1, 1, 2, "live"},
		"ttl 43201":     {"L3", "T1", "ACTOR1", "i", 43201, 1, 1, 2, "live"},
		"state revoked": {"L4", "T1", "ACTOR1", "i", 60, 1, 1, 2, "revoked"},
		"unknown task":  {"L5", "NOPE", "ACTOR1", "i", 60, 1, 1, 2, "live"},
	} {
		if err := exec("leases", cols, vals...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := exec("lease_resources", []string{"lease_id", "resource"}, "L1", "repo:a"); err != nil {
		t.Fatal(err)
	}
	if err := exec("lease_resources", []string{"lease_id", "resource"}, "L1", "repo:a"); err == nil || !d.IsUniqueViolation(err) {
		t.Errorf("a duplicate resource = %v, want a unique violation", err)
	}
}

// TestAsksHaveTheSpecIndexesAndTheirChecks checks the SPEC 4.5 and 4.10 indexes
// on asks and that an ask takes only its three states, two modes and three
// channels, leaves poll_seen_at null and refers to a task that exists.
func TestAsksHaveTheSpecIndexesAndTheirChecks(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"asks_state_created_at": {"state", "created_at"},
		"asks_task_id":          {"task_id"},
		"asks_actor_id":         {"actor_id"},
	} {
		if got := indexColumns(t, s, name); !reflect.DeepEqual(got, want) {
			t.Errorf("index %s = %v, want %v", name, got, want)
		}
	}
	d := s.Dialect()
	exec := func(table string, cols []string, vals ...any) error {
		_, err := s.Writer().ExecContext(ctx, d.Rebind(d.Insert(table, cols)), vals...)
		return err
	}
	for _, r := range phase0Rows[:2] {
		if err := exec(r.table, r.cols, r.vals...); err != nil {
			t.Fatal(err)
		}
	}
	cols := []string{"id", "task_id", "actor_id", "question", "mode", "state", "created_at"}
	if err := exec("asks", cols, "A1", nil, "ACTOR1", "q", "stop", "open", 1); err != nil {
		t.Fatalf("a global ask = %v", err)
	}
	var seen sql.NullInt64
	if err := s.Writer().QueryRowContext(ctx, d.Rebind(`SELECT poll_seen_at FROM asks WHERE id = ?`), "A1").Scan(&seen); err != nil || seen.Valid {
		t.Errorf("poll_seen_at = %v, %v; want null", seen, err)
	}
	for name, vals := range map[string][]any{
		"mode ask":      {"A2", nil, "ACTOR1", "q", "ask", "open", 1},
		"state waiting": {"A3", nil, "ACTOR1", "q", "stop", "waiting", 1},
		"unknown task":  {"A4", "NOPE", "ACTOR1", "q", "stop", "open", 1},
		"unknown actor": {"A5", nil, "NOPE", "q", "stop", "open", 1},
	} {
		if err := exec("asks", cols, vals...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := exec("asks", append(cols, "channel"), "A6", nil, "ACTOR1", "q", "stop", "answered", 1, "carrier"); err == nil {
		t.Error("channel carrier was accepted")
	}
}

// TestMigrateToStopsAtTheVersionAsked builds the schema of an earlier release:
// the database ends at the version named, is not ready, and Migrate takes it on.
func TestMigrateToStopsAtTheVersionAsked(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.MigrateTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v != 4 {
		t.Fatalf("version after MigrateTo(4) = %d, %v; want 4", v, err)
	}
	if err := s.Ready(ctx); !errors.Is(err, ErrNotMigrated) {
		t.Errorf("Ready at version 4 = %v, want ErrNotMigrated", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(ctx); err != nil {
		t.Errorf("Ready after Migrate = %v", err)
	}
	if _, err := s.Writer().ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(ctx, 4); !errors.Is(err, ErrSchemaNewer) {
		t.Errorf("MigrateTo on a newer schema = %v, want ErrSchemaNewer", err)
	}
}
