package tasks

import (
	"sort"
	"strings"
	"testing"
	"time"

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

// explain returns the EXPLAIN QUERY PLAN steps of sql on st's reader, every placeholder bound to a dummy value.
func explain(t *testing.T, st *store.Store, sql string) []string {
	t.Helper()
	args := make([]any, strings.Count(sql, "?"))
	for i := range args {
		args[i] = "x"
	}
	rows, err := st.Reader().QueryContext(ctx, `EXPLAIN QUERY PLAN `+sql, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", sql, err)
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
	return plan
}

// TestRefFilteredReadsPlanWithIndexesAndNoTempSort is the R1 row g EXPLAIN QUERY PLAN test.
// Run it with: ci/run.sh go test -count=1 -run TestRefFilteredReadsPlanWithIndexesAndNoTempSort ./internal/tasks/
// It captures the SQL the ref filters really issue on a seeded store (the list's refs and onlyRefs conditions, and
// tasksByRef through TaggedByRef) and fails if a plan scans events or tasks, or sorts through a temp B-tree. The
// timeline's attention refs are pinned the same way by TestActiveFilterPlansWithIndexesAndNoTempSort.
func TestRefFilteredReadsPlanWithIndexesAndNoTempSort(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	initiative(t, s, "bb")
	for _, title := range []string{"one", "two", "three"} {
		mk(t, s, "aa", title)
	}
	mk(t, s, "bb", "four")
	yes, no := true, false
	for _, c := range []struct {
		name string
		run  func() error
	}{
		{"onlyRefs on the default list", func() error { _, err := s.List(ctx, Query{Refs: []string{"AA-1", "bb-1"}}); return err }},
		{"onlyRefs on every status", func() error {
			_, err := s.List(ctx, Query{Refs: []string{"aa-2"}, Status: StatusAll})
			return err
		}},
		{"refs wanted", func() error {
			_, err := s.List(ctx, Query{Attention: &yes, AttentionRefs: []string{"aa-1", "aa-3"}, Status: StatusAll})
			return err
		}},
		// not wanted is read on the default status: every task but a few is the answer, and the not closed
		// ranges are what keep it off a scan of tasks.
		{"refs not wanted", func() error {
			_, err := s.List(ctx, Query{Attention: &no, AttentionRefs: []string{"aa-1"}})
			return err
		}},
		{"tasksByRef", func() error { _, err := s.TaggedByRef(ctx, []string{"aa-1", "bb-1"}); return err }},
	} {
		rec.Reset()
		if err := c.run(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var reads []storetest.Statement
		for _, r := range rec.All() {
			if r.Pool == store.PoolReader && r.Kind == storetest.Query && strings.Contains(r.SQL, "FROM tasks t JOIN initiatives i") {
				reads = append(reads, r)
			}
		}
		if len(reads) != 1 {
			t.Fatalf("%s: issued %d reader queries on tasks, want 1: %+v", c.name, len(reads), rec.All())
		}
		plan := explain(t, st, reads[0].SQL)
		if len(plan) == 0 {
			t.Fatalf("%s: empty plan", c.name)
		}
		for _, d := range plan {
			for _, bad := range []string{"SCAN events", "SCAN e", "SCAN tasks", "SCAN t", "SCAN rt", "SCAN ri", "SCAN i", "USE TEMP B-TREE FOR ORDER BY"} {
				if d == bad || strings.HasPrefix(d, bad+" ") || (strings.HasPrefix(bad, "USE") && strings.Contains(d, bad)) {
					t.Errorf("%s: plan step %q is %q:\n%s", c.name, d, bad, strings.Join(plan, "\n"))
				}
			}
		}
	}
}

// TestRefFiltersAnswerTheRowsOfTheComputedRefText pins that the ref filters keep the tasks the condition on the
// computed ref text kept (`i.slug || '-' || seq IN (...)`): a ref names a task only when it is exactly its short
// ref, so a leading zero, a ULID, no number, an unknown task or an empty list name none, and the list's onlyRefs
// takes either letter case. The expectation is read with that old condition on the same seeded store.
func TestRefFiltersAnswerTheRowsOfTheComputedRefText(t *testing.T) {
	st, _ := storetest.OpenMigrated(t)
	s := storeOn(st)
	initiative(t, s, "aa")
	initiative(t, s, "b-b")
	for _, title := range []string{"one", "two", "three"} {
		mk(t, s, "aa", title)
	}
	mk(t, s, "b-b", "four")
	mk(t, s, "b-b", "five")
	made := mk(t, s, "aa", "six")
	yes, no := true, false
	old := func(refs []string) string {
		var ids []string
		seen := map[string]bool{}
		for _, ref := range refs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			var id string
			err := st.Reader().QueryRowContext(ctx, `SELECT t.id FROM tasks t JOIN initiatives i ON i.id = t.initiative_id
				WHERE (i.slug || '-' || CAST(t.seq AS TEXT)) = ?`, ref).Scan(&id)
			if err == nil {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	for _, refs := range [][]string{
		{"aa-1"}, {"aa-1", "b-b-2", "aa-3"}, {"aa-01"}, {"aa-"}, {"aa"}, {"aa-0"}, {"zz-1"}, {"aa-99", "b-b-1"},
		{made.ID}, {"b-b-1", "b-b-1"}, {"aa-1", "aa-01", "-1", "aa--1", "aa-+1", "aa- 1"}, nil,
	} {
		ids := func(p Page, err error) string {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, it := range p.Items {
				out = append(out, it.ID)
			}
			sort.Strings(out)
			return strings.Join(out, ",")
		}
		want := old(refs)
		if len(refs) > 0 {
			if got := ids(s.List(ctx, Query{Refs: refs, Status: StatusAll})); got != want {
				t.Errorf("Refs %q listed %q, the computed ref text keeps %q", refs, got, want)
			}
		}
		if got := ids(s.List(ctx, Query{Attention: &yes, AttentionRefs: refs, Status: StatusAll})); got != want {
			t.Errorf("AttentionRefs %q wanted listed %q, the computed ref text keeps %q", refs, got, want)
		}
		all := ids(s.List(ctx, Query{Status: StatusAll}))
		if got := ids(s.List(ctx, Query{Attention: &no, AttentionRefs: refs, Status: StatusAll})); got != remove(all, want) {
			t.Errorf("AttentionRefs %q unwanted listed %q, want every task but %q", refs, got, want)
		}
		tagged, err := s.TaggedByRef(ctx, refs)
		if err != nil || len(tagged) != len(strings.FieldsFunc(want, func(r rune) bool { return r == ',' })) {
			t.Errorf("TaggedByRef %q = %v, %v, want one entry per task %q", refs, tagged, err, want)
		}
	}
	if got, err := s.List(ctx, Query{Refs: []string{"AA-1", "B-B-2"}, Status: StatusAll}); err != nil || len(got.Items) != 2 {
		t.Errorf("onlyRefs in capitals listed %v, %v, want aa-1 and b-b-2", got, err)
	}
}

// remove is the ids of the comma-separated list all that are not in the list drop.
func remove(all, drop string) string {
	gone := map[string]bool{}
	for _, id := range strings.Split(drop, ",") {
		gone[id] = true
	}
	var keep []string
	for _, id := range strings.Split(all, ",") {
		if !gone[id] {
			keep = append(keep, id)
		}
	}
	return strings.Join(keep, ",")
}
