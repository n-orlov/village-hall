package timeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
	"github.com/n-orlov/village-hall/internal/timeline"
)

// TestActiveFilterPlansWithIndexesAndNoTempSort is the R1 row e EXPLAIN QUERY PLAN
// test for the timeline's active filter. Run it with:
// ci/run.sh go test -count=1 -run TestActiveFilterPlansWithIndexesAndNoTempSort ./internal/timeline/
// It captures the SQL the task read really issues for an active view on a seeded
// store, attention refs included, and fails if its plan scans events or tasks, or
// sorts through a temp B-tree.
func TestActiveFilterPlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	e.initiative(t, "aa")
	a := e.task(t, "aa", "one", from.Add(-48*time.Hour), time.Time{})
	e.task(t, "aa", "two", from.Add(-48*time.Hour), time.Time{})
	e.event(t, a, "milestone", "milestone on aa-1: line", `{"summary":"the label"}`, now.Add(-time.Hour))
	rec.Reset()
	if got := refs(e.read(t, timeline.Query{Active: true, Attention: []string{"aa-2"}})); len(got) != 2 {
		t.Fatalf("active read = %v, want aa-1 and aa-2", got)
	}
	var reads []storetest.Statement
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query && strings.Contains(s.SQL, "SELECT t.id, i.slug, t.seq") {
			reads = append(reads, s)
		}
	}
	if len(reads) != 1 || !strings.Contains(reads[0].SQL, "events ae") {
		t.Fatalf("an active read issued %d task reads with the active filter, want 1: %+v", len(reads), rec.All())
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
	if testing.Verbose() {
		t.Log("\n" + strings.Join(plan, "\n"))
	}
	for _, d := range plan {
		for _, bad := range []string{"SCAN events", "SCAN e", "SCAN tasks", "SCAN t", "SCAN rt", "SCAN ae", "USE TEMP B-TREE FOR ORDER BY"} {
			if d == bad || strings.HasPrefix(d, bad+" ") || (strings.HasPrefix(bad, "USE") && strings.Contains(d, bad)) {
				t.Errorf("plan step %q is %q:\n%s", d, bad, strings.Join(plan, "\n"))
			}
		}
	}
}

// TestActiveAttentionRefsMatchOnlyTheExactShortRef pins the rows the rewritten
// filter keeps: an attention ref counts only when it is exactly the short ref of a
// task, as the computed ref text it was compared with before, so another case, a
// leading zero, a ULID, a missing number or an unknown task name no task.
func TestActiveAttentionRefsMatchOnlyTheExactShortRef(t *testing.T) {
	e := activeEnv(t)
	for _, c := range []struct {
		name string
		refs []string
		want []string
	}{
		{"exact", []string{"aa-2", "aa-4"}, []string{"aa-1", "aa-2", "aa-3", "aa-4"}},
		{"another case", []string{"AA-2"}, []string{"aa-1", "aa-3"}},
		{"leading zero", []string{"aa-02"}, []string{"aa-1", "aa-3"}},
		{"ulid", []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"}, []string{"aa-1", "aa-3"}},
		{"no number", []string{"aa-"}, []string{"aa-1", "aa-3"}},
		{"unknown task", []string{"aa-99", "zz-1"}, []string{"aa-1", "aa-3"}},
	} {
		is(t, c.name, refs(e.read(t, timeline.Query{Active: true, Window: "24h", Attention: c.refs})), c.want)
	}
}
