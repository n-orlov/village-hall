package events

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

var (
	t0  = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ctx = context.Background()
	ann = audit.Actor{Handle: "ann@host"}
	bob = audit.Actor{Handle: "bob@host"}
)

type fixture struct {
	ev Store
	tk tasks.Store
	in initiatives.Store
}

// newFixture is a migrated SQLite database whose clock advances one second per
// call, so every event has its own time.
func newFixture(t *testing.T) fixture {
	t.Helper()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return newFixtureOn(st)
}

// newFixtureOn is the fixture on an already migrated store.
func newFixtureOn(st *store.Store) fixture {
	n := 0
	now := func() time.Time { n++; return t0.Add(time.Duration(n) * time.Second) }
	return fixture{ev: Store{Store: st, Now: now}, tk: tasks.Store{Store: st, Now: now}, in: initiatives.Store{Store: st, Now: now}}
}

func (f fixture) must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seed writes, in order: E1 initiative aa created, E2 task aa-1 created, E3
// task aa-1 updated, E4 aa-1 done, E5 aa-1 reopened, E6 aa-1 closed, E7
// initiative bb created, E8 bb-1 created (by bob), E9 a milestone on bb-1, E10
// the watch task bb-2 created and E11 a check on bb-2.
func (f fixture) seed(t *testing.T) {
	t.Helper()
	_, err := f.in.Create(ctx, ann, "key:ann", initiatives.New{Slug: "aa", Title: "A"})
	f.must(t, err)
	tk, err := f.tk.Create(ctx, ann, "key:ann", tasks.New{Initiative: "aa", Title: "one"})
	f.must(t, err)
	_, err = f.tk.Patch(ctx, ann, "key:ann", tk.Ref, tasks.Patch{Description: tasks.Field[string]{Set: true, Value: "d"}})
	f.must(t, err)
	_, err = f.tk.Done(ctx, ann, "key:ann", tk.Ref, tasks.DoneOptions{})
	f.must(t, err)
	_, err = f.tk.Reopen(ctx, ann, "key:ann", tk.Ref)
	f.must(t, err)
	_, err = f.tk.Close(ctx, ann, "key:ann", tk.Ref, "completed")
	f.must(t, err)
	_, err = f.in.Create(ctx, ann, "key:ann", initiatives.New{Slug: "bb", Title: "B"})
	f.must(t, err)
	b1, err := f.tk.Create(ctx, bob, "oidc:bob@example.com", tasks.New{Initiative: "bb", Title: "two"})
	f.must(t, err)
	_, err = f.tk.Milestone(ctx, bob, "key:bob", b1.Ref, "shipped", map[string]any{"v": "1"})
	f.must(t, err)
	cad := int64(3600)
	w, err := f.tk.Create(ctx, bob, "key:bob", tasks.New{Initiative: "bb", Title: "watch", Kind: domain.KindWatch, CadenceSeconds: &cad})
	f.must(t, err)
	_, err = f.tk.Check(ctx, bob, "key:bob", w.Ref, "still red", nil)
	f.must(t, err)
}

func refs(p Page) []string {
	out := []string{}
	for _, e := range p.Items {
		out = append(out, e.Ref)
	}
	return out
}

func list(t *testing.T, f fixture, q Query) Page {
	t.Helper()
	p, err := f.ev.List(ctx, q)
	if err != nil {
		t.Fatalf("List(%+v): %v", q, err)
	}
	return p
}

func TestForTaskListsTheTasksEventsInOrder(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	p, err := f.ev.ForTask(ctx, "AA-1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range p.Items {
		types = append(types, e.Type)
		if e.Task != "aa-1" || e.Initiative != "aa" {
			t.Errorf("event %s task %q initiative %q", e.Ref, e.Task, e.Initiative)
		}
	}
	want := []string{"task_created", "task_updated", "task_done", "task_reopened", "task_closed"}
	if !reflect.DeepEqual(types, want) || !reflect.DeepEqual(refs(p), []string{"E2", "E3", "E4", "E5", "E6"}) {
		t.Fatalf("types %v refs %v, want %v and E2..E6", types, refs(p), want)
	}
	if p.NextAfter != "" {
		t.Errorf("NextAfter = %q on the only page", p.NextAfter)
	}
	if _, err := f.ev.ForTask(ctx, "aa-9", "", 0); !errors.Is(err, tasks.ErrNotFound) {
		t.Errorf("ForTask on an unknown task = %v, want tasks.ErrNotFound", err)
	}
	if q, err := f.ev.ForTask(ctx, "aa-1", "E4", 1); err != nil || !reflect.DeepEqual(refs(q), []string{"E5"}) || q.NextAfter != q.Items[0].ID {
		t.Errorf("ForTask after E4 limit 1 = %v, %v", q, err)
	}
}

func TestEventsCarryTheirFields(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	p := list(t, f, Query{Task: "bb-1", Types: []string{"milestone"}})
	if len(p.Items) != 1 {
		t.Fatalf("milestones = %+v", p.Items)
	}
	e := p.Items[0]
	if e.Ref != "E9" || e.N != 9 || e.Type != "milestone" || e.Actor != "bob@host" || e.Principal != "key:bob" || e.Initiative != "bb" ||
		e.Task != "bb-1" || e.TaskID == "" || e.RefID != e.TaskID || e.OnBehalfOf != "" || !e.At.Equal(e.At.UTC()) || e.At.Before(t0) ||
		e.Summary != "milestone on bb-1: shipped" || !reflect.DeepEqual(e.Data["data"], any(map[string]any{"v": "1"})) {
		t.Fatalf("event = %+v", e)
	}
	first := list(t, f, Query{Limit: 1}).Items[0]
	if first.Ref != "E1" || first.Task != "" || first.TaskID != "" || first.Initiative != "aa" {
		t.Errorf("first event = %+v", first)
	}
}

func TestEveryEventHasAOneLineSummary(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	all := list(t, f, Query{})
	if len(all.Items) != 11 {
		t.Fatalf("events = %v", refs(all))
	}
	for _, e := range all.Items {
		if strings.TrimSpace(e.Summary) == "" || strings.ContainsAny(e.Summary, "\r\n") || e.Ref != domain.FormatEventRef(e.N) {
			t.Errorf("event %s summary %q", e.Ref, e.Summary)
		}
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	aa1, _ := f.tk.Get(ctx, "aa-1")
	for _, tc := range []struct {
		name string
		q    Query
		want []string
	}{
		{"none", Query{}, []string{"E1", "E2", "E3", "E4", "E5", "E6", "E7", "E8", "E9", "E10", "E11"}},
		{"initiative slug", Query{Initiative: "bb"}, []string{"E7", "E8", "E9", "E10", "E11"}},
		{"initiative slug in upper case", Query{Initiative: "AA"}, []string{"E1", "E2", "E3", "E4", "E5", "E6"}},
		{"task ref", Query{Task: "aa-1"}, []string{"E2", "E3", "E4", "E5", "E6"}},
		{"task ULID in lower case", Query{Task: strings.ToLower(aa1.ID)}, []string{"E2", "E3", "E4", "E5", "E6"}},
		{"one type", Query{Types: []string{"task_created"}}, []string{"E2", "E8", "E10"}},
		{"two types", Query{Types: []string{"milestone", "check_recorded"}}, []string{"E9", "E11"}},
		{"a type nothing has", Query{Types: []string{"lease_started"}}, []string{}},
		{"actor", Query{Actor: "bob@host"}, []string{"E8", "E9", "E10", "E11"}},
		{"an actor nobody is", Query{Actor: "nobody"}, []string{}},
		{"initiative and type and actor", Query{Initiative: "bb", Types: []string{"task_created"}, Actor: "bob@host"}, []string{"E8", "E10"}},
		{"task and type", Query{Task: "aa-1", Types: []string{"task_done", "task_closed"}}, []string{"E4", "E6"}},
	} {
		if got := refs(list(t, f, tc.q)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if _, err := f.ev.List(ctx, Query{Task: "aa-9"}); !errors.Is(err, tasks.ErrNotFound) {
		t.Errorf("task filter on an unknown task = %v", err)
	}
	if _, err := f.ev.List(ctx, Query{Initiative: "zz"}); !errors.Is(err, initiatives.ErrNotFound) {
		t.Errorf("initiative filter on an unknown initiative = %v", err)
	}
	if _, err := f.ev.List(ctx, Query{Types: []string{"task_done", " "}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty type = %v, want ErrInvalid", err)
	}
}

func TestSinceTakesAnEventULIDAnEventRefInEitherCaseOrATime(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	e6 := list(t, f, Query{Limit: 6}).Items[5]
	if e6.Ref != "E6" {
		t.Fatalf("sixth event is %s", e6.Ref)
	}
	tail := []string{"E7", "E8", "E9", "E10", "E11"}
	for _, tc := range []struct {
		name  string
		since string
		want  []string
	}{
		{"event ULID", e6.ID, tail},
		{"event ULID in lower case", strings.ToLower(e6.ID), tail},
		{"E<n> in upper case", "E6", tail},
		{"E<n> in lower case", "e6", tail},
		{"E<n> past the end", "E11", []string{}},
		{"RFC 3339 time at the event", e6.At.Format(time.RFC3339), append([]string{"E6"}, tail...)},
		{"RFC 3339 time with an offset", e6.At.Add(time.Second).In(time.FixedZone("x", 2*3600)).Format(time.RFC3339), tail},
		{"RFC 3339 time before everything", t0.Format(time.RFC3339), append([]string{"E1", "E2", "E3", "E4", "E5", "E6"}, tail...)},
		{"RFC 3339 time after everything", t0.Add(time.Hour).Format(time.RFC3339), []string{}},
	} {
		if got := refs(list(t, f, Query{Since: tc.since})); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: since %q got %v, want %v", tc.name, tc.since, got, tc.want)
		}
	}
	for _, bad := range []string{"yesterday", "E0", "E", "2026-11-06", "123", "01ARZ3NDEKTSV4RRFFQ69G5FA!"} {
		_, err := f.ev.List(ctx, Query{Since: bad})
		var ie InvalidError
		if !errors.Is(err, ErrInvalid) || !errors.As(err, &ie) || ie.Next == "" || !strings.Contains(err.Error(), bad) {
			t.Errorf("since %q = %v, want ErrInvalid naming it with a next step", bad, err)
		}
	}
}

func TestLimitAndAfterPageThroughTheLog(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	var got []string
	after := ""
	pages := 0
	for {
		p := list(t, f, Query{Limit: 4, After: after})
		pages++
		got = append(got, refs(p)...)
		if p.NextAfter == "" {
			break
		}
		if len(p.Items) != 4 {
			t.Fatalf("a page with a successor has %d items", len(p.Items))
		}
		after = p.NextAfter
	}
	if pages != 3 || !reflect.DeepEqual(got, []string{"E1", "E2", "E3", "E4", "E5", "E6", "E7", "E8", "E9", "E10", "E11"}) {
		t.Fatalf("pages = %d, events = %v", pages, got)
	}
	if got := refs(list(t, f, Query{Limit: 11})); len(got) != 11 {
		t.Errorf("a page of exactly all events = %v", got)
	}
	if p := list(t, f, Query{Limit: 11}); p.NextAfter != "" {
		t.Errorf("NextAfter = %q on an exact fit", p.NextAfter)
	}
	if got := refs(list(t, f, Query{After: "e9"})); !reflect.DeepEqual(got, []string{"E10", "E11"}) {
		t.Errorf("after e9 = %v", got)
	}
	if got := refs(list(t, f, Query{After: "E3", Since: "E6", Limit: 2})); !reflect.DeepEqual(got, []string{"E7", "E8"}) {
		t.Errorf("after E3 and since E6 = %v", got)
	}
	for _, q := range []Query{{Limit: -1}, {Limit: 1001}, {After: "yesterday"}, {After: "2026-11-06T12:00:00Z"}} {
		if _, err := f.ev.List(ctx, q); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%+v) = %v, want ErrInvalid", q, err)
		}
	}
}

func TestOnBehalfOfAndAnEventWhoseTaskRowIsGone(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	w := f.ev.Store.Writer()
	if _, err := w.ExecContext(ctx, `UPDATE events SET on_behalf_of = (SELECT id FROM actors WHERE handle = 'ann@host') WHERE n = 9`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecContext(ctx, `DELETE FROM tasks WHERE seq = 1 AND initiative_id = (SELECT id FROM initiatives WHERE slug = 'bb')`); err != nil {
		t.Fatal(err)
	}
	e := list(t, f, Query{Since: "E8", Limit: 1}).Items[0]
	if e.Ref != "E9" || e.OnBehalfOf != "ann@host" || e.Task != e.TaskID || e.TaskID == "" {
		t.Errorf("event = %+v, want on_behalf_of ann@host and the task id as its task", e)
	}
}

func TestAnEventShowsTheHandleOfItsOriginAndNoneWhenItHasNone(t *testing.T) {
	f := newFixture(t)
	op := audit.Actor{Handle: "alice-op", Operator: true}
	_, err := f.in.Create(ctx, op, "oidc:a@example.com", initiatives.New{Slug: "by-op", Title: "I"})
	f.must(t, err)
	_, err = f.in.Create(audit.WithOrigin(ctx, "alice-op"), ann, "key:k", initiatives.New{Slug: "by-key", Title: "I"})
	f.must(t, err)
	_, err = f.in.Create(audit.WithOrigin(ctx, ""), bob, "key:j", initiatives.New{Slug: "by-orphan", Title: "I"})
	f.must(t, err)
	got := map[string]string{}
	page, err := f.ev.List(ctx, Query{})
	f.must(t, err)
	for _, e := range page.Items {
		got[e.Initiative] = e.Origin
	}
	want := map[string]string{"by-op": "alice-op", "by-key": "alice-op", "by-orphan": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("origins = %v, want %v", got, want)
	}
}
