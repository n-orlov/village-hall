package storetest_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

var ctx = context.Background()

func TestALeasesReadIsReaderStatementsOnly(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	if n := len(rec.All()); n != 0 {
		t.Fatalf("%d statements recorded after OpenMigrated, want none: the migration is not measured", n)
	}
	s := leases.Store{Store: st, Now: func() time.Time { return time.Unix(1000, 0) }}
	got, err := s.LiveFor(ctx, []string{"no-such-task"})
	if err != nil || len(got) != 0 {
		t.Fatalf("LiveFor = %v, %v", got, err)
	}
	// The lease select, then the lease_resources select (it runs even when the first found no lease).
	if n := rec.Count(store.PoolReader, storetest.Query); n != 2 {
		t.Errorf("reader queries = %d, want 2: %+v", n, rec.All())
	}
	if n := rec.Statements(store.PoolWriter) + rec.Count(store.PoolWriter, storetest.Begin); n != 0 {
		t.Errorf("writer records = %d, want 0: %+v", n, rec.All())
	}
	all := rec.All()
	if len(all) != 2 {
		t.Fatalf("%d records, want exactly the 2 reader queries: %+v", len(all), all)
	}
	if q := all[0]; !strings.Contains(q.SQL, "FROM leases") || q.InTx {
		t.Errorf("the first recorded statement is %+v, want the leases select outside a transaction", q)
	}
	if q := all[1]; !strings.Contains(q.SQL, "FROM lease_resources") || q.InTx {
		t.Errorf("the second recorded statement is %+v, want the lease_resources select outside a transaction", q)
	}
}

func TestEveryStatementOfEitherPoolIsRecordedWithItsSideAndTransaction(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	w, r := st.Writer(), st.Reader()

	if _, err := w.ExecContext(ctx, `CREATE TABLE probe (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe (x) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	var x int
	if err := tx.QueryRowContext(ctx, `SELECT x FROM probe`).Scan(&x); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rolled, err := w.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolled.ExecContext(ctx, `INSERT INTO probe (x) VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	if err := rolled.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe`).Scan(&x); err != nil || x != 1 {
		t.Fatalf("the reader sees %d rows, %v: want the committed 1", x, err)
	}
	prepared, err := r.PrepareContext(ctx, `SELECT x FROM probe WHERE x = ?`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := prepared.QueryRowContext(ctx, 1).Scan(&x); err != nil {
			t.Fatal(err)
		}
	}
	_ = prepared.Close()

	type rec3 struct {
		pool string
		kind storetest.Kind
		in   bool
		sql  string
	}
	var got []rec3
	for _, s := range rec.All() {
		got = append(got, rec3{s.Pool, s.Kind, s.InTx, s.SQL})
	}
	want := []rec3{
		{store.PoolWriter, storetest.Exec, false, `CREATE TABLE probe (x INTEGER)`},
		{store.PoolWriter, storetest.Begin, false, "BEGIN"},
		{store.PoolWriter, storetest.Exec, true, `INSERT INTO probe (x) VALUES (1)`},
		{store.PoolWriter, storetest.Query, true, `SELECT x FROM probe`},
		{store.PoolWriter, storetest.Commit, true, "COMMIT"},
		{store.PoolWriter, storetest.Begin, false, "BEGIN"},
		{store.PoolWriter, storetest.Exec, true, `INSERT INTO probe (x) VALUES (2)`},
		{store.PoolWriter, storetest.Rollback, true, "ROLLBACK"},
		{store.PoolReader, storetest.Query, false, `SELECT COUNT(*) FROM probe`},
		{store.PoolReader, storetest.Query, false, `SELECT x FROM probe WHERE x = ?`},
		{store.PoolReader, storetest.Query, false, `SELECT x FROM probe WHERE x = ?`},
	}
	if len(got) != len(want) {
		t.Fatalf("%d records, want %d:\n got %+v\nwant %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := rec.Statements(""); n != 7 {
		t.Errorf("Statements(all pools) = %d, want 7", n)
	}
	if n := rec.Count("", storetest.Begin); n != 2 {
		t.Errorf("Begin records on all pools = %d, want 2", n)
	}
	rec.Reset()
	if n := len(rec.All()); n != 0 {
		t.Errorf("%d records after Reset", n)
	}
}

func TestAStoreOpenedWithoutTheHookIsUnchanged(t *testing.T) {
	st, err := store.OpenWrapped("sqlite://"+filepath.Join(t.TempDir(), "plain.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	// An unsupported scheme is refused with or without a wrapper.
	if _, _, err := storetest.Open("mysql://x"); err == nil {
		t.Error("Open(mysql://) succeeded")
	}
}
