package attention

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// TestRecentReadPlansWithIndexesAndNoTempSort is the R1 row a EXPLAIN QUERY PLAN
// test. Run it with: ci/run.sh go test -count=1 -run TestRecentReadPlansWithIndexesAndNoTempSort ./internal/attention/
// It captures the SQL recent() really issues on a seeded store and fails if its
// plan scans events or tasks, or sorts through a temp B-tree.
func TestRecentReadPlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	e.tick(time.Hour)
	l := e.lease(t, me, "aa-1")
	e.tick(time.Second)
	if _, err := e.Leases.End(ctx, me, "key:x", l.Ref, "x"); err != nil {
		t.Fatal(err)
	}
	rec.Reset()
	if _, err := e.A.recent(ctx, scope{}, e.Clock.now, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var reads []storetest.Statement
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query {
			reads = append(reads, s)
		}
	}
	if len(reads) != 1 {
		t.Fatalf("recent issued %d reader queries, want 1: %+v", len(reads), rec.All())
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

// TestRecentReadAnswersTheRowsTheCorrelatedFormDid pins the row-a rewrite: on a
// seeded store with several events per task, a live lease and a task outside the
// window, recent() answers the same tasks, in the same order, with the same last
// events as the original task-first correlated read.
func TestRecentReadAnswersTheRowsTheCorrelatedFormDid(t *testing.T) {
	e := quiet(t)
	old := e.lease(t, me, "bb-1")
	e.tick(time.Second)
	if _, err := e.Leases.End(ctx, me, "key:x", old.Ref, "x"); err != nil {
		t.Fatal(err)
	}
	e.tick(30 * time.Hour)
	b := e.lease(t, bob, "aa-2")
	a := e.lease(t, me, "aa-1")
	for range 2 {
		e.tick(time.Second)
		if _, err := e.Leases.Renew(ctx, me, "key:x", a.Ref, leases.Renew{}); err != nil {
			t.Fatal(err)
		}
	}
	e.tick(time.Second)
	if _, err := e.Leases.End(ctx, me, "key:x", a.Ref, "x"); err != nil {
		t.Fatal(err)
	}
	_ = b // aa-2 stays live: never recent
	span := 24 * time.Hour
	got, err := e.A.recent(ctx, scope{}, e.Clock.now, span)
	if err != nil {
		t.Fatal(err)
	}
	live, args := liveCond(e.Clock.now)
	args = append([]any{store.EncodeBound(e.Clock.now.Add(-span))}, args...)
	original := section{
		cols: flightCols + `, e.type, e.summary, e.at, a.handle, a.way_back, og.handle`,
		from: `tasks t JOIN initiatives i ON i.id = t.initiative_id
		 JOIN events e ON e.n = (SELECT MAX(c.n) FROM events c WHERE c.task_id = t.id)
		 JOIN actors a ON a.id = e.actor_id LEFT JOIN actors og ON og.id = e.origin
		 WHERE e.at >= ? AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.task_id = t.id AND ` + live + `)`,
		order: `e.at DESC, e.n DESC`, args: args,
	}
	want, err := collect(ctx, e.A, original, func(r rowScanner) (Recent, error) {
		var (
			ev     LastEvent
			at     int64
			wb, og sql.NullString
		)
		tk, err := scanTask(r, &ev.Type, &ev.Summary, &at, &ev.Actor.Handle, &wb, &og)
		ev.Actor.WayBack, ev.Origin, ev.At = wb.String, og.String, store.DecodeTime(at)
		return Recent{Task: tk, LastEvent: ev}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got, want) || got[0].Task.Ref != "aa-1" || got[0].LastEvent.Type != "lease_ended" {
		t.Fatalf("recent = %+v, original read = %+v; want the same, aa-1 alone with its lease_ended", got, want)
	}
}
