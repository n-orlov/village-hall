package events

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// TestListWithATimeSincePlansWithIndexesAndNoTempSort is the R1 row b EXPLAIN
// QUERY PLAN test. Run it with: ci/run.sh go test -count=1 -run TestListWithATimeSincePlansWithIndexesAndNoTempSort ./internal/events/
// It captures the SQL List really issues for a time since on a seeded store and
// fails if the plan scans events or tasks, or sorts through a temp B-tree.
func TestListWithATimeSincePlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	f := newFixtureOn(st)
	f.seed(t)
	rec.Reset()
	if _, err := f.ev.List(ctx, Query{Since: t0.Add(3 * time.Second).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var reads []storetest.Statement
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query {
			reads = append(reads, s)
		}
	}
	if len(reads) != 1 {
		t.Fatalf("List issued %d reader queries, want 1: %+v", len(reads), rec.All())
	}
	args := make([]any, strings.Count(reads[0].SQL, "?"))
	for i := range args {
		args[i] = int64(0)
	}
	rows, err := st.Reader().QueryContext(ctx, `EXPLAIN QUERY PLAN `+reads[0].SQL, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", reads[0].SQL, err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(plan) == 0 {
		t.Fatal("empty plan")
	}
	for _, d := range plan {
		for _, bad := range []string{"SCAN events", "SCAN e", "SCAN tasks", "SCAN t", "USE TEMP B-TREE FOR ORDER BY"} {
			if d == bad || strings.HasPrefix(d, bad+" ") || (strings.HasPrefix(bad, "USE") && strings.Contains(d, bad)) {
				t.Errorf("plan step %q is %q:\n%s", d, bad, strings.Join(plan, "\n"))
			}
		}
	}
}

// TestListWithATimeSinceAnswersTheRowsTheTimeScanDid pins the row-b rewrite: with
// events whose times do not rise with n (a late E3, an early E9), a time since
// still answers exactly the events at or after the time, in n order, as the
// plain `at >= ? ORDER BY n` read does.
func TestListWithATimeSinceAnswersTheRowsTheTimeScanDid(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	set := func(n int, at time.Time) {
		t.Helper()
		if _, err := f.ev.Store.Writer().ExecContext(ctx, `UPDATE events SET at = ? WHERE n = ?`, store.EncodeTime(at), n); err != nil {
			t.Fatal(err)
		}
	}
	set(3, t0.Add(time.Hour))
	set(9, t0.Add(-time.Hour))
	since := t0.Add(6 * time.Second)
	rows, err := f.ev.Store.Reader().QueryContext(ctx, `SELECT n FROM events WHERE at >= ? ORDER BY n`, store.EncodeTime(since))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var want []string
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		want = append(want, "E"+strings.TrimSpace(fmtInt(n)))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	got := refs(list(t, f, Query{Since: since.Format(time.RFC3339)}))
	if len(want) == 0 || want[0] != "E3" || len(got) != len(want) {
		t.Fatalf("reference read = %v, List = %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] || got[i] == "E9" {
			t.Fatalf("List = %v, want %v", got, want)
		}
	}
	if got := refs(list(t, f, Query{Since: t0.Add(2 * time.Hour).Format(time.RFC3339)})); len(got) != 0 {
		t.Errorf("since after every event = %v", got)
	}
}

func fmtInt(n int64) string { return strconv.FormatInt(n, 10) }
