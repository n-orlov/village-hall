package timeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
	"github.com/n-orlov/village-hall/internal/timeline"
)

// seededRecorded is a recording store with two initiatives (aa tagged), two tasks each in the window, a lease
// and a milestone on aa-1.
func seededRecorded(t *testing.T) (env, *storetest.Recorder) {
	t.Helper()
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	e.initiative(t, "aa", "web")
	e.initiative(t, "bb")
	var first string
	for _, slug := range []string{"aa", "bb"} {
		for _, title := range []string{"one", "two"} {
			id := e.task(t, slug, title, from.Add(-48*time.Hour), time.Time{})
			if first == "" {
				first = id
			}
		}
	}
	e.lease(t, "aa-1", "codex@host", "wire it", "live", from.Add(time.Hour), now.Add(time.Hour))
	e.event(t, first, "milestone", "milestone on aa-1: line", `{"summary":"the label"}`, from.Add(time.Hour))
	return e, rec
}

// readerQueries are the reader statements since the last Reset, in order.
func readerQueries(rec *storetest.Recorder) []string {
	var out []string
	for _, s := range rec.All() {
		if s.Pool == store.PoolReader && s.Kind == storetest.Query {
			out = append(out, strings.Join(strings.Fields(s.SQL), " "))
		}
	}
	return out
}

// isLookup is whether the statement is part of looking an initiative up by its ref: the row, then its tags. A
// lookup of one ref is lookupStatements of them.
func isLookup(sql string) bool {
	return strings.Contains(sql, "FROM initiatives i JOIN actors c") || strings.HasPrefix(sql, "SELECT initiative_id, tag FROM initiative_tags WHERE initiative_id IN")
}

const lookupStatements = 2

// timelineReads is how many statements the Read of a window costs once the initiatives are resolved: the tasks (which
// resolve the kept task ids), their deps and three tag sources, then the initiatives and their tags, the leases and
// the markers. The count is pinned on a store that holds tasks.
const timelineReads = 9

// TestAReadResolvesTheKeptTaskIdsOnce pins the statement count of a read and that only the tasks statement
// applies the filter: every other statement is keyed by the ids it returned.
func TestAReadResolvesTheKeptTaskIdsOnce(t *testing.T) {
	e, rec := seededRecorded(t)
	for name, q := range map[string]timeline.Query{
		"default":        {},
		"active":         {Active: true},
		"tagged":         {Tags: []string{"web"}},
		"actor":          {Actor: "codex@host"},
		"all rows":       {Rows: timeline.RowsAll},
		"closed":         {ClosedWithin: "7d"},
		"one initiative": {Initiatives: []string{"aa"}},
	} {
		rec.Reset()
		got := e.read(t, q)
		if len(got.Tasks) == 0 {
			t.Fatalf("%s: the seeded window kept no task", name)
		}
		stmts := readerQueries(rec)
		lookups := 0
		var reads []string
		for _, s := range stmts {
			if isLookup(s) {
				lookups++
				continue
			}
			reads = append(reads, s)
		}
		if len(reads) != timelineReads {
			t.Errorf("%s: a read issued %d timeline statements, want %d:\n%s", name, len(reads), timelineReads, strings.Join(reads, "\n"))
			continue
		}
		if (len(q.Initiatives) > 0) != (lookups > 0) {
			t.Errorf("%s: %d initiative lookups for initiatives %v", name, lookups, q.Initiatives)
		}
		if !strings.Contains(reads[0], "t.status <> 'closed'") {
			t.Errorf("%s: the first statement is not the tasks read: %s", name, reads[0])
		}
		for _, s := range reads[1:] {
			if strings.Contains(s, "t.status") || strings.Contains(s, "created_at") || strings.Contains(s, "initiative_id IN") {
				t.Errorf("%s: a later statement repeats the filter: %s", name, s)
			}
			if !strings.Contains(s, "json_each") {
				t.Errorf("%s: a later statement is not keyed by the resolved ids: %s", name, s)
			}
		}
	}
}

// TestAnEmptyWindowStopsAfterTheTasksRead pins that nothing kept means one statement: no other statement can
// hold a row of a task that was not kept.
func TestAnEmptyWindowStopsAfterTheTasksRead(t *testing.T) {
	e, rec := seededRecorded(t)
	rec.Reset()
	got := e.read(t, timeline.Query{From: "2001-01-01T00:00:00Z", To: "2001-01-02T00:00:00Z"})
	if len(got.Tasks)+len(got.Initiatives)+len(got.Leases)+len(got.Markers) != 0 {
		t.Fatalf("an empty window kept %+v", got)
	}
	if n := len(readerQueries(rec)); n != 1 {
		t.Errorf("an empty window issued %d statements, want 1", n)
	}
}

// TestPrepareThenReadResolvesTheInitiativesOnce pins that checking a query and then reading it looks the
// initiatives up as often as a read alone does, where Check and Read did it twice.
func TestPrepareThenReadResolvesTheInitiativesOnce(t *testing.T) {
	e, rec := seededRecorded(t)
	q := timeline.Query{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Initiatives: []string{"aa", "bb"}}
	lookups := func() int {
		n := 0
		for _, s := range readerQueries(rec) {
			if isLookup(s) {
				n++
			}
		}
		return n
	}
	rec.Reset()
	if _, err := e.tl.Read(ctx, q); err != nil {
		t.Fatal(err)
	}
	once := lookups()
	if once != 2*lookupStatements {
		t.Fatalf("a read of two initiatives issued %d lookup statements, want %d", once, 2*lookupStatements)
	}
	rec.Reset()
	p, err := e.tl.Prepare(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	checked := readerQueries(rec)
	got, err := p.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := lookups(); n != once {
		t.Errorf("Prepare then Read issued %d lookup statements, want %d", n, once)
	}
	if len(readerQueries(rec)) != len(checked)+timelineReads || len(got.Tasks) != 4 {
		t.Errorf("Prepare then Read issued %d statements after the check's %d and kept %d tasks", len(readerQueries(rec))-len(checked), len(checked), len(got.Tasks))
	}
	if _, err := e.tl.Prepare(ctx, timeline.Query{Initiatives: []string{"nope"}}); err == nil {
		t.Error("Prepare of an unknown initiative was not refused")
	}
}
