package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
)

// TestMigration00017LeaseNotice: version 16 has no leases.notice, migrating to
// 17 adds it with every existing lease ordinary (false) and lets a notice be
// stored, and rolling back to 16 drops it with the other columns intact. It runs
// on every database leg.
func TestMigration00017LeaseNotice(t *testing.T) {
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
		w, d := s.Writer(), s.Dialect()
		hasNotice := func() bool {
			rows, err := s.Reader().Query(`SELECT notice FROM leases LIMIT 1`)
			if err == nil {
				_ = rows.Close()
			}
			return err == nil
		}
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := w.ExecContext(ctx, d.Rebind(q), args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}

		if err := s.MigrateTo(ctx, 16); err != nil {
			t.Fatal(err)
		}
		if hasNotice() {
			t.Fatal("leases.notice exists at version 16")
		}
		exec(`INSERT INTO actors (id, handle, kind, first_seen_at, last_seen_at) VALUES (?, ?, 'agent', 1, 1)`, "A1", "a@h")
		exec(`INSERT INTO initiatives (id, slug, title, created_by, created_at) VALUES (?, 'aa', 'I', 'A1', 1)`, "I1")
		exec(`INSERT INTO tasks (id, initiative_id, seq, title, created_by, created_at, updated_at) VALUES (?, 'I1', 1, 't', 'A1', 1, 1)`, "T1")
		lease := `INSERT INTO leases (id, task_id, actor_id, intent, ttl_seconds, started_at, renewed_at, expires_at) VALUES (?, 'T1', 'A1', 'x', 60, 1, 1, 61000)`
		exec(lease, "L1ID")

		if err := s.MigrateTo(ctx, 17); err != nil {
			t.Fatal(err)
		}
		if !hasNotice() {
			t.Fatal("migrating to 17 did not add leases.notice")
		}
		var notice bool
		if err := s.Reader().QueryRow(`SELECT notice FROM leases WHERE id = 'L1ID'`).Scan(&notice); err != nil || notice {
			t.Fatalf("an existing lease has notice = %v, %v; want false", notice, err)
		}
		exec(`INSERT INTO leases (id, task_id, actor_id, intent, ttl_seconds, started_at, renewed_at, expires_at, notice) VALUES (?, 'T1', 'A1', 'x', 60, 1, 1, 61000, ?)`,
			"L2ID", d.EncodeBool(true))
		if err := s.Reader().QueryRow(`SELECT notice FROM leases WHERE id = 'L2ID'`).Scan(&notice); err != nil || !notice {
			t.Fatalf("a notice lease reads back notice = %v, %v; want true", notice, err)
		}

		if err := s.DownTo(ctx, 16); err != nil {
			t.Fatalf("rolling back to 16: %v", err)
		}
		if hasNotice() {
			t.Fatal("leases.notice still exists after the roll back")
		}
		var n int
		if err := s.Reader().QueryRow(`SELECT COUNT(*) FROM leases`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("leases after the roll back = %d, %v; want 2", n, err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if v, err := s.SchemaVersion(ctx); err != nil || v != 17 {
			t.Fatalf("latest version = %d, %v; want 17", v, err)
		}
	})
}
