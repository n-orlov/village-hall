package tasks

import (
	"time"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// storeOn is a Store over st whose clock advances one second per call, so ids sort in creation order.
func storeOn(st *store.Store) Store {
	n := 0
	return Store{Store: st, Now: func() time.Time { n++; return t0.Add(time.Duration(n) * time.Second) }}
}

// TestDefaultListReadPlansWithIndexesAndNoTempSort is the R1 row f EXPLAIN QUERY PLAN test.
// Run it with: ci/run.sh go test -count=1 -run TestDefaultListReadPlansWithIndexesAndNoTempSort ./internal/tasks/
// It captures the SQL the default task list (status not closed, ordered by id, one page) really issues on a
// seeded store and fails if its plan scans events or tasks, or sorts through a temp B-tree.
func TestDefaultListReadPlansWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	initiative(t, s, "bb")
	for _, title := range []string{"one", "two", "three"} {
		mk(t, s, "aa", title)
	}
	mk(t, s, "bb", "four")
	rec.Reset()
	if _, err := s.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	var reads []storetest.Statement
	for _, r := range rec.All() {
		if r.Pool == store.PoolReader && r.Kind == storetest.Query && strings.Contains(r.SQL, "FROM tasks t JOIN initiatives i") {
			reads = append(reads, r)
		}
	}
	if len(reads) != 1 {
		t.Fatalf("the default list issued %d reader queries on tasks, want 1: %+v", len(reads), rec.All())
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

// TestDefaultListAnswersTheRowsOfStatusNotClosed pins that the default list returns the tasks the plain
// status <> 'closed' condition returned, in id order and across pages, over open, done and closed tasks.
func TestDefaultListAnswersTheRowsOfStatusNotClosed(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	initiative(t, s, "bb")
	var made []Task
	for i, slug := range []string{"aa", "bb", "aa", "bb", "aa", "aa", "bb"} {
		made = append(made, mk(t, s, slug, "task "+string(rune('a'+i))))
	}
	if _, err := s.Done(ctx, who, "key:c", made[1].Ref, DoneOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{2, 5} {
		if _, err := s.Close(ctx, who, "key:c", made[i].Ref, "dropped"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.Reader().QueryContext(ctx, `SELECT id FROM tasks WHERE status <> 'closed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}
	_ = rows.Close()
	if len(want) != 5 {
		t.Fatalf("seeded %d tasks that are not closed, want 5", len(want))
	}
	for _, q := range []Query{{}, {Status: StatusNotClosed}} {
		got, err := s.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, it := range got.Items {
			ids = append(ids, it.ID)
		}
		if strings.Join(ids, ",") != strings.Join(want, ",") {
			t.Errorf("status %q listed %v, want %v", q.Status, ids, want)
		}
	}
	var paged []string
	after := ""
	for {
		p, err := s.List(ctx, Query{Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range p.Items {
			paged = append(paged, it.ID)
		}
		if p.NextAfter == "" {
			break
		}
		after = p.NextAfter
	}
	if strings.Join(paged, ",") != strings.Join(want, ",") {
		t.Errorf("pages of 2 listed %v, want %v", paged, want)
	}
}
