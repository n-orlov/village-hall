package checkin

import (
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// TestPreviousCatchupReadPlansWithIndexesAndNoTempSort is the R1 row d EXPLAIN
// QUERY PLAN test. Run it with:
// ci/run.sh go test -count=1 -run TestPreviousCatchupReadPlansWithIndexesAndNoTempSort ./internal/checkin/
// It captures the SQL previousCatchup really issues on a store that already holds
// a catch-up, and fails if its plan scans events or tasks, or sorts through a temp
// B-tree.
func TestPreviousCatchupReadPlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	k := e.ask(t, "me", "q?")
	e.answer(t, k.Ref)
	e.catchup(t)
	rec.Reset()
	if _, found, err := e.S.previousCatchup(ctx, me.Handle); err != nil || !found {
		t.Fatalf("previousCatchup = %v, %v, want found", found, err)
	}
	var reads []storetest.Statement
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query {
			reads = append(reads, s)
		}
	}
	if len(reads) != 1 || !strings.Contains(reads[0].SQL, "FROM events") {
		t.Fatalf("previousCatchup issued %+v, want one reader query on events", rec.All())
	}
	args := make([]any, strings.Count(reads[0].SQL, "?"))
	for i := range args {
		args[i] = "x"
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


// TestPreviousCatchupAnswersTheNewestCatchupOfOnlyThatActor checks the rows the
// read answers: of several catchup events it returns the newest one of the
// actor asked about, never another actor's, and none for an actor that never
// caught up.
func TestPreviousCatchupAnswersTheNewestCatchupOfOnlyThatActor(t *testing.T) {
	e := newEnv(t)
	if _, found, err := e.S.previousCatchup(ctx, me.Handle); err != nil || found {
		t.Fatalf("before any catch-up = %v, %v, want not found", found, err)
	}
	var through []int64
	for i := 0; i < 3; i++ {
		k := e.ask(t, "me", "q")
		e.answer(t, k.Ref)
		e.catchup(t)
		got, found, err := e.S.previousCatchup(ctx, me.Handle)
		if err != nil || !found {
			t.Fatalf("after catch-up %d = %v, %v, want found", i, found, err)
		}
		through = append(through, got)
	}
	if through[0] >= through[1] || through[1] >= through[2] {
		t.Errorf("answers_through after each catch-up = %v, want the newest catch-up each time", through)
	}
	e.note(t, me, notes.New{Summary: "n"})
	if _, err := e.S.Catchup(ctx, bob, "k"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := e.S.previousCatchup(ctx, me.Handle); got != through[2] {
		t.Errorf("after another actor's catch-up = %d, want mine still %d", got, through[2])
	}
}
