package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open("sqlite://" + filepath.Join(t.TempDir(), "sub", "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestReadyOnlyAfterMigrate(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Ready(ctx); !errors.Is(err, ErrNotMigrated) {
		t.Fatalf("Ready on a fresh database = %v, want ErrNotMigrated", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("Ready after Migrate = %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate = %v, want idempotent", err)
	}
}

func TestReadyFailsWhenClosed(t *testing.T) {
	s := open(t)
	_ = s.Close()
	if err := s.Ready(context.Background()); err == nil {
		t.Fatal("Ready on a closed database = nil")
	}
}

func TestOpenRejectsOtherSchemes(t *testing.T) {
	if _, err := Open("mysql://x/y"); err == nil {
		t.Fatal("expected an unsupported scheme error")
	}
	if _, err := Open("sqlite://"); err == nil {
		t.Fatal("expected an error for an empty path")
	}
}

func TestOpenSQLiteFailsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The database's directory would have to live under a regular file.
	_, err := Open("sqlite://" + filepath.Join(file, "sub", "board.db"))
	if err == nil || !strings.Contains(err.Error(), "create database directory") {
		t.Fatalf("Open = %v, want a create-directory error", err)
	}
}

func TestOpenSQLiteFailsWhenThePathIsADirectory(t *testing.T) {
	if _, err := Open("sqlite://" + t.TempDir()); err == nil {
		t.Fatal("Open on a directory path succeeded")
	}
}

func pragma(t *testing.T, c *sql.Conn, name string) string {
	t.Helper()
	var v string
	if err := c.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

// TestSQLitePragmasOnEveryConnection reads the five required settings back
// from the writer and from every connection the read pool opens. It fails
// when any one of them is dropped from the DSN.
func TestSQLitePragmasOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	want := map[string]string{
		"journal_mode":       "wal",
		"busy_timeout":       "5000",
		"foreign_keys":       "1",
		"synchronous":        "1", // NORMAL
		"wal_autocheckpoint": "10000",
	}
	check := func(label string, c *sql.Conn) {
		for name, w := range want {
			if got := strings.ToLower(pragma(t, c, name)); got != w {
				t.Errorf("%s: PRAGMA %s = %q, want %q", label, name, got, w)
			}
		}
	}
	wc, err := s.Writer().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer wc.Close()
	check("writer", wc)

	for i := 0; i < 3; i++ { // held at once, so the pool opens distinct connections
		c, err := s.Reader().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		check(fmt.Sprintf("reader %d", i), c)
	}
}

func TestSQLiteUsesOneWriterAndAReadPool(t *testing.T) {
	s := open(t)
	if got := s.Writer().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := s.Reader().Stats().MaxOpenConnections; got < 2 {
		t.Errorf("reader MaxOpenConnections = %d, want a pool of at least 2", got)
	}
	if _, err := s.Reader().Exec("CREATE TABLE x (a INT)"); err == nil {
		t.Error("the read pool accepted a write")
	}
}

func columns(t *testing.T, s *Store, table string) map[string]bool {
	t.Helper()
	rows, err := s.Reader().Query("SELECT name FROM pragma_table_info('" + table + "')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols[n] = true
	}
	return cols
}

// TestMigrateCreatesThePhase0Tables checks the SPEC 4 columns of the tables in use.
func TestMigrateCreatesThePhase0Tables(t *testing.T) {
	s := open(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"actors": {"n", "id", "handle", "kind", "display_name", "host", "way_back", "meta",
			"first_seen_at", "last_seen_at", "protocol_seen", "origin"},
		"initiatives": {"n", "id", "slug", "title", "description", "status", "target_start", "target_end",
			"created_by", "created_at", "closed_at", "closed_by", "next_task_seq"},
		"initiative_tags": {"initiative_id", "tag"},
		"events": {"n", "id", "at", "actor_id", "principal", "on_behalf_of", "initiative_id", "task_id",
			"type", "ref_id", "summary", "data", "origin"},
		"api_keys": {"n", "id", "name", "key_hash", "display_prefix", "created_at", "created_by",
			"last_used_at", "expires_at", "revoked_at", "owner"},
		"sessions": {"id_hash", "email", "csrf_token", "created_at", "last_seen_at", "expires_at"},
		"tasks": {"n", "id", "initiative_id", "seq", "title", "description", "kind", "status", "resolution",
			"planned_start", "planned_end", "cadence_seconds", "next_check_at", "external_refs",
			"created_by", "created_at", "updated_at", "done_at", "done_by", "closed_at", "closed_by"},
		"task_deps": {"task_id", "blocked_by_task_id"},
		"task_tags": {"task_id", "tag"},
		"notes": {"n", "id", "actor_id", "summary", "body", "task_id", "initiative_id", "resource", "mentions",
			"created_at"},
		"cursors": {"actor_id", "stream", "position"},
	}
	for table, cols := range want {
		got := columns(t, s, table)
		if len(got) != len(cols) {
			t.Errorf("%s has %d columns, want %d", table, len(got), len(cols))
		}
		for _, c := range cols {
			if !got[c] {
				t.Errorf("%s lacks column %s", table, c)
			}
		}
	}
}

func TestShortRefIdentityIsNeverReused(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ins := s.Dialect().InsertReturning("actors",
		[]string{"id", "handle", "kind", "first_seen_at", "last_seen_at"}, "n")
	var first, second int64
	w := s.Writer()
	if err := w.QueryRowContext(ctx, ins, "A1", "a", "agent", 1, 1).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecContext(ctx, "DELETE FROM actors WHERE id = 'A1'"); err != nil {
		t.Fatal(err)
	}
	if err := w.QueryRowContext(ctx, ins, "A2", "b", "agent", 1, 1).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("n %d was reused or went backwards after %d", second, first)
	}
}

// insertOperator writes one operator actors row with the first and last seen times 1.
func insertOperator(s *Store, id, handle string) error {
	ins := s.Dialect().Insert("actors", []string{"id", "handle", "kind", "first_seen_at", "last_seen_at"})
	_, err := s.Writer().ExecContext(context.Background(), ins, id, handle, "operator", 1, 1)
	return err
}

// TestSeveralOperatorActorsMayExist: migration 00012 drops actors_one_operator,
// so each handle may be an operator row, while a handle stays unique.
func TestSeveralOperatorActorsMayExist(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := insertOperator(s, "A1", "op"); err != nil {
		t.Fatal(err)
	}
	if err := insertOperator(s, "A2", "op2"); err != nil {
		t.Fatalf("a second operator = %v, want it accepted", err)
	}
	if err := insertOperator(s, "A3", "op2"); err == nil || !s.Dialect().IsUniqueViolation(err) {
		t.Fatalf("a second row for one handle = %v, want a unique violation", err)
	}
}

// TestTheOneOperatorIndexIsTheOnlyThingMigration00012Changes: at version 11 a
// second operator is refused by the unique index, at 12 it is accepted, and
// rolling 12 back restores the index, first demoting every operator but the
// earliest seen.
func TestTheOneOperatorIndexIsTheOnlyThingMigration00012Changes(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.MigrateTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	if err := insertOperator(s, "A1", "op"); err != nil {
		t.Fatal(err)
	}
	if err := insertOperator(s, "A2", "op2"); err == nil || !s.Dialect().IsUniqueViolation(err) {
		t.Fatalf("a second operator at version 11 = %v, want a unique violation", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := insertOperator(s, "A2", "op2"); err != nil {
		t.Fatalf("a second operator at the head = %v, want it accepted", err)
	}
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 11); err != nil {
		t.Fatalf("rolling back to 11 with two operators = %v", err)
	}
	kinds := map[string]string{}
	rows, err := s.Reader().QueryContext(ctx, "SELECT handle, kind FROM actors")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, k string
		if err := rows.Scan(&h, &k); err != nil {
			t.Fatal(err)
		}
		kinds[h] = k
	}
	if kinds["op"] != "operator" || kinds["op2"] != "agent" || len(kinds) != 2 {
		t.Errorf("kinds after the roll back = %v, want op an operator and op2 an agent", kinds)
	}
	if err := insertOperator(s, "A3", "op3"); err == nil || !s.Dialect().IsUniqueViolation(err) {
		t.Fatalf("a second operator after the roll back = %v, want a unique violation", err)
	}
}

// TestMigration00013BacksUpAndRollsBack: at version 12 the three columns do not
// exist; migrating gives every key the operator as owner, every event the
// operator or its key's owner as origin, and an agent the origin of its latest
// keyed write; rolling back drops the columns and leaves the rows.
func TestMigration00013BackfillsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.MigrateTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, col string }{{"api_keys", "owner"}, {"events", "origin"}, {"actors", "origin"}} {
		if columns(t, s, c.table)[c.col] {
			t.Fatalf("%s has %s at version 12", c.table, c.col)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Writer().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES ('OP', 'operator', 'operator', 1, 1), ('AG', 'agent-a', 'agent', 2, 2), ('AH', 'agent-b', 'agent', 3, 3)`)
	exec(`INSERT INTO api_keys (id, name, key_hash, display_prefix, created_at, created_by) VALUES ('K1', 'laptop', 'h1', 'p1', 1, 'x')`)
	ev := `INSERT INTO events (id, at, actor_id, principal, type, summary) VALUES (?, 1, ?, ?, 't', 's')`
	exec(ev, "E1", "OP", "oidc:op@example.com")
	exec(ev, "E2", "AG", "key:laptop")
	exec(ev, "E3", "AG", "system")
	exec(ev, "E4", "AH", "system")
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	read := func(q string) map[string]string {
		t.Helper()
		got := map[string]string{}
		rows, err := s.Reader().QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var v sql.NullString
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			got[k] = v.String
		}
		return got
	}
	if got := read(`SELECT name, owner FROM api_keys`); got["laptop"] != "OP" {
		t.Errorf("key owners = %v, want laptop owned by OP", got)
	}
	got := read(`SELECT id, origin FROM events`)
	if got["E1"] != "OP" || got["E2"] != "OP" || got["E3"] != "" || got["E4"] != "" {
		t.Errorf("event origins = %v, want E1 and E2 from OP and the system events none", got)
	}
	if got := read(`SELECT handle, origin FROM actors`); got["agent-a"] != "OP" || got["agent-b"] != "" || got["operator"] != "" {
		t.Errorf("actor origins = %v, want agent-a from OP and the others none", got)
	}
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 12); err != nil {
		t.Fatalf("rolling back to 12: %v", err)
	}
	for _, c := range []struct{ table, col string }{{"api_keys", "owner"}, {"events", "origin"}, {"actors", "origin"}} {
		if columns(t, s, c.table)[c.col] {
			t.Errorf("%s still has %s after the roll back", c.table, c.col)
		}
	}
	if got := read(`SELECT id, principal FROM events`); len(got) != 4 {
		t.Errorf("events after the roll back = %v, want the 4 rows", got)
	}
}

// TestMigration00014TaskTags: at version 13 there is no task_tags table; migrating
// creates it with the primary key (task_id, tag) and an index on tag, and rolling
// back drops it.
func TestMigration00014TaskTags(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.MigrateTo(ctx, 13); err != nil {
		t.Fatal(err)
	}
	if len(columns(t, s, "task_tags")) != 0 {
		t.Fatal("task_tags exists at version 13")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		_, err := s.Writer().Exec(q, args...)
		return err
	}
	if err := exec(`INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES ('A', 'a', 'agent', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO initiatives (id, slug, title, description, status, created_by, created_at) VALUES ('I', 'ini', 't', '', 'open', 'A', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO tasks (id, initiative_id, seq, title, description, kind, status, external_refs, created_by, created_at, updated_at)
		VALUES ('T', 'I', 1, 't', '', 'work', 'open', '[]', 'A', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO task_tags (task_id, tag) VALUES ('T', 'home')`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO task_tags (task_id, tag) VALUES ('T', 'home')`); err == nil || !s.Dialect().IsUniqueViolation(err) {
		t.Fatalf("the same tag twice on one task = %v, want a unique violation", err)
	}
	var idx int
	if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM pragma_index_list('task_tags') WHERE name = 'task_tags_tag'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("index task_tags_tag count = %d, err %v, want 1", idx, err)
	}
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 13); err != nil {
		t.Fatalf("rolling back to 13: %v", err)
	}
	if len(columns(t, s, "task_tags")) != 0 {
		t.Error("task_tags still exists after the roll back")
	}
}

// TestMigration00015LeaseTaskStateIndex: at version 14 leases has no index
// leading with (task_id, state, expires_at); migrating creates it, the live-lease
// look-up of one task uses it, and rolling back drops it.
func TestMigration00015LeaseTaskStateIndex(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.MigrateTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	indexed := func() int {
		var n int
		if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM pragma_index_list('leases') WHERE name = 'leases_task_state_expires_at'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if indexed() != 0 {
		t.Fatal("the index exists at version 14")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if indexed() != 1 {
		t.Fatal("migrating did not create leases_task_state_expires_at")
	}
	var cols []string
	rows, err := s.Reader().Query(`SELECT name FROM pragma_index_info('leases_task_state_expires_at') ORDER BY seqno`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	_ = rows.Close()
	if strings.Join(cols, ",") != "task_id,state,expires_at" {
		t.Errorf("index columns = %v", cols)
	}
	plan := ""
	steps, err := s.Reader().Query(`EXPLAIN QUERY PLAN SELECT 1 FROM leases l WHERE l.task_id = 'T' AND l.state = 'live' AND l.expires_at >= 5`)
	if err != nil {
		t.Fatal(err)
	}
	for steps.Next() {
		var id, parent, unused int
		var detail string
		if err := steps.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "; "
	}
	_ = steps.Close()
	if !strings.Contains(plan, "leases_task_state_expires_at") {
		t.Errorf("one task's live-lease look-up plans %q, not the new index", plan)
	}
	p, err := s.provider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 14); err != nil {
		t.Fatalf("rolling back to 14: %v", err)
	}
	if indexed() != 0 {
		t.Error("the index still exists after the roll back")
	}
}

func setVersion(t *testing.T, s *Store, v int) {
	t.Helper()
	if _, err := s.Writer().Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)", v); err != nil {
		t.Fatal(err)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema on a current database = %v", err)
	}
	setVersion(t, s, 9999)
	for name, err := range map[string]error{
		"CheckSchema": s.CheckSchema(ctx),
		"Migrate":     s.Migrate(ctx),
		"Ready":       s.Ready(ctx),
	} {
		if !errors.Is(err, ErrSchemaNewer) {
			t.Errorf("%s = %v, want ErrSchemaNewer", name, err)
			continue
		}
		if strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), "9999") {
			t.Errorf("%s error %q is not one line naming the version", name, err)
		}
	}
}

func TestSchemaVersionOfAnEmptyDatabaseIsZero(t *testing.T) {
	s := open(t)
	if v, err := s.SchemaVersion(context.Background()); err != nil || v != 0 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	if err := s.CheckSchema(context.Background()); err != nil {
		t.Fatalf("CheckSchema on an empty database = %v", err)
	}
}

func TestEmbeddedMigrationsCoverBothDialects(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		mig, err := dialectMigrations(name)
		if err != nil {
			t.Fatal(err)
		}
		files, err := fs.Glob(mig, "*.sql")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 16 || files[15] != "00016_hot_read_indexes.sql" || files[14] != "00015_lease_task_state_index.sql" || files[13] != "00014_task_tags.sql" || files[12] != "00013_key_owner_and_origin.sql" || files[11] != "00012_many_operators.sql" || files[10] != "00011_notes_concern_indexes.sql" || files[9] != "00010_idempotency.sql" || files[8] != "00009_notifications.sql" || files[7] != "00008_ask_end_commit.sql" || files[6] != "00007_ask_ended_at.sql" || files[5] != "00006_notes.sql" || files[4] != "00005_asks.sql" || files[3] != "00004_leases.sql" || files[0] != "00001_core_tables.sql" || files[1] != "00002_join_tables_sessions_indexes.sql" || files[2] != "00003_tasks.sql" {
			t.Errorf("%s migrations = %v", name, files)
		}
		if _, err := fs.ReadFile(mig, files[1]); err != nil {
			t.Errorf("%s: reading the common migration: %v", name, err)
		}
		if _, err := mig.Open("nope.sql"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: opening a missing file = %v", name, err)
		}
	}
}

func TestOpenPostgresIsLazy(t *testing.T) {
	s, err := Open("postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Dialect().Name() != "postgres" || s.Reader() != s.Writer() {
		t.Fatalf("postgres store: dialect %s, shared pool %v", s.Dialect().Name(), s.Reader() == s.Writer())
	}
	if err := s.Ready(context.Background()); err == nil {
		t.Fatal("Ready against a closed port = nil")
	}
}
