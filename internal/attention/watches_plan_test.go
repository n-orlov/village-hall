package attention

import (
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// TestWatchesReadPlansWithIndexesAndNoTempSort is the R1 row e EXPLAIN QUERY PLAN
// test for watchesSection. Run it with:
// ci/run.sh go test -count=1 -run TestWatchesReadPlansWithIndexesAndNoTempSort ./internal/attention/
// It captures the SQL overdueWatches really issues on a store holding an overdue
// watch with several checks, and fails if its plan scans events or tasks, or
// sorts through a temp B-tree.
func TestWatchesReadPlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	cadence := int64(3600)
	w := must(e.Tasks.Create(ctx, bob, "key:x", tasks.New{Initiative: "bb", Title: "watch", Kind: "watch", CadenceSeconds: &cadence}))
	must(e.Tasks.Check(ctx, me, "key:x", w.Ref, "first look", nil))
	must(e.Tasks.Check(ctx, me, "key:x", w.Ref, "second look", nil))
	e.tick(90 * time.Minute)
	rec.Reset()
	got, err := e.A.overdueWatches(ctx, scope{}, e.Clock.now)
	if err != nil || len(got) != 1 || got[0].LastCheck == nil || got[0].LastCheck.Summary != "second look" {
		t.Fatalf("overdueWatches = %+v, %v, want the watch with its newest check", got, err)
	}
	var reads []storetest.Statement
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query {
			reads = append(reads, s)
		}
	}
	if len(reads) != 1 || !strings.Contains(reads[0].SQL, "check_recorded") {
		t.Fatalf("overdueWatches issued %+v, want one reader query on the checks", rec.All())
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
		for _, bad := range []string{"SCAN events", "SCAN e", "SCAN tasks", "SCAN t", "SCAN c", "USE TEMP B-TREE FOR ORDER BY"} {
			if d == bad || strings.HasPrefix(d, bad+" ") || (strings.HasPrefix(bad, "USE") && strings.Contains(d, bad)) {
				t.Errorf("plan step %q is %q:\n%s", d, bad, strings.Join(plan, "\n"))
			}
		}
	}
}
