package timeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
	"github.com/n-orlov/village-hall/internal/timeline"
)

// TestMarkersReadPlansWithIndexesAndNoTempSort is the R1 row c EXPLAIN QUERY PLAN
// test. Run it with: ci/run.sh go test -count=1 -run TestMarkersReadPlansWithIndexesAndNoTempSort ./internal/timeline/
// It captures the SQL the markers read really issues on a seeded store and fails
// if its plan scans events or tasks, or sorts through a temp B-tree.
func TestMarkersReadPlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	e.initiative(t, "aa")
	a := e.task(t, "aa", "one", from.Add(-48*time.Hour), time.Time{})
	e.event(t, a, "milestone", "milestone on aa-1: line", `{"summary":"the label"}`, from.Add(time.Hour))
	rec.Reset()
	e.read(t, timeline.Query{})
	var reads []storetest.Statement
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query && strings.Contains(s.SQL, "e.type IN") {
			reads = append(reads, s)
		}
	}
	if len(reads) != 1 {
		t.Fatalf("markers issued %d reader queries on events, want 1: %+v", len(reads), rec.All())
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
