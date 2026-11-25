package timeline_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
	"github.com/n-orlov/village-hall/internal/timeline"
)

var (
	ctx = context.Background()
	// now is the clock of the timeline, from and to bound the window the tests read.
	now  = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	from = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	who  = audit.Actor{Handle: "claude@host"}
)

type env struct {
	st *store.Store
	tl timeline.Store
	tk tasks.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return newEnvOn(t, st)
}

// newEnvOn builds the timeline test env on an already migrated store.
func newEnvOn(t *testing.T, st *store.Store) env {
	t.Helper()
	n := 0
	clock := func() time.Time { n++; return from.Add(-time.Hour * 24 * 60).Add(time.Duration(n) * time.Second) }
	return env{st: st, tl: timeline.Store{Store: st, Now: func() time.Time { return now }}, tk: tasks.Store{Store: st, Now: clock}}
}

func (e env) sql(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.st.Writer().ExecContext(ctx, e.st.Dialect().Rebind(query), args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func ms(at time.Time) int64 { return store.EncodeTime(at) }

func (e env) initiative(t *testing.T, slug string, tags ...string) {
	t.Helper()
	_, err := initiatives.Store{Store: e.st, Now: e.tk.Now}.Create(ctx, who, "key:caller", initiatives.New{Slug: slug, Title: "I " + slug, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
}

// task creates a task and places its span: created, closed (zero: open), plan.
func (e env) task(t *testing.T, slug, title string, created, closed time.Time) string {
	t.Helper()
	got, err := e.tk.Create(ctx, who, "key:caller", tasks.New{Initiative: slug, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	e.sql(t, `UPDATE tasks SET created_at = ? WHERE id = ?`, ms(created), got.ID)
	if !closed.IsZero() {
		e.sql(t, `UPDATE tasks SET status = 'closed', closed_at = ? WHERE id = ?`, ms(closed), got.ID)
	}
	return got.ID
}

func (e env) plan(t *testing.T, id string, start, end *time.Time) {
	t.Helper()
	var s, f any
	if start != nil {
		s = ms(*start)
	}
	if end != nil {
		f = ms(*end)
	}
	e.sql(t, `UPDATE tasks SET planned_start = ?, planned_end = ? WHERE id = ?`, s, f, id)
}

// lease starts a lease on the task ref as the handle and places its span: state
// live, ended or expired, with the end time ended_at (ended) or expires_at.
func (e env) lease(t *testing.T, ref, handle, intent, state string, started, end time.Time) string {
	t.Helper()
	ls := leases.Store{Store: e.st, Now: e.tk.Now}
	got, err := ls.Start(ctx, audit.Actor{Handle: handle}, "key:caller", leases.Start{Task: ref, Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	ended := any(nil)
	if state == "ended" {
		ended = ms(end)
	}
	e.sql(t, `UPDATE leases SET state = ?, started_at = ?, renewed_at = ?, expires_at = ?, ended_at = ? WHERE id = ?`,
		state, ms(started), ms(started), ms(end), ended, got.Lease.ID)
	return got.Lease.Ref
}

// event writes one event of the type on the task at the time, with data JSON.
func (e env) event(t *testing.T, taskID, typ, summary, data string, at time.Time) {
	t.Helper()
	var iniID, actorID string
	if err := e.st.Reader().QueryRowContext(ctx, e.st.Dialect().Rebind(`SELECT initiative_id FROM tasks WHERE id = ?`), taskID).Scan(&iniID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Reader().QueryRowContext(ctx, e.st.Dialect().Rebind(`SELECT id FROM actors LIMIT 1`)).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewULID(at)
	if err != nil {
		t.Fatal(err)
	}
	e.sql(t, `INSERT INTO events (id, at, actor_id, principal, initiative_id, task_id, type, ref_id, summary, data) VALUES (?, ?, ?, 'key:caller', ?, ?, ?, ?, ?, ?)`,
		id, ms(at), actorID, iniID, taskID, typ, taskID, summary, data)
}

func (e env) read(t *testing.T, q timeline.Query) timeline.Result {
	t.Helper()
	if q.From == "" {
		q.From = from.Format(time.RFC3339)
	}
	if q.To == "" {
		q.To = to.Format(time.RFC3339)
	}
	got, err := e.tl.Read(ctx, q)
	if err != nil {
		t.Fatalf("read %+v: %v", q, err)
	}
	return got
}

func refs(r timeline.Result) []string {
	out := []string{}
	for _, t := range r.Tasks {
		out = append(out, t.Ref)
	}
	return out
}

func leaseRefs(r timeline.Result) []string {
	out := []string{}
	for _, l := range r.Leases {
		out = append(out, l.Ref)
	}
	return out
}

func markers(r timeline.Result) []string {
	out := []string{}
	for _, m := range r.Markers {
		out = append(out, m.Task+":"+m.Type+":"+m.Label)
	}
	return out
}

func is(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestTheWindowDefaultsToFourteenDaysBackAndSevenAhead(t *testing.T) {
	e := newEnv(t)
	got, err := e.tl.Read(ctx, timeline.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.From.Equal(now.Add(-14*24*time.Hour)) || !got.To.Equal(now.Add(7*24*time.Hour)) {
		t.Errorf("window %s to %s, want 14 days back to 7 ahead of %s", got.From, got.To, now)
	}
	got, err = e.tl.Read(ctx, timeline.Query{From: "2026-11-01T00:00:00+01:00", To: "2026-11-02T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.From.Equal(time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)) || got.From.Location() != time.UTC {
		t.Errorf("from %s is not read as RFC 3339 and shown in UTC", got.From)
	}
	got, err = e.tl.Read(ctx, timeline.Query{From: "2026-11-01T00:00:00Z"})
	if err != nil || !got.To.Equal(now.Add(7*24*time.Hour)) {
		t.Errorf("a from alone keeps the default to: %v %v", got.To, err)
	}
}

func TestTheClockDefaultsToTheWallClock(t *testing.T) {
	e := newEnv(t)
	got, err := timeline.Store{Store: e.st}.Read(ctx, timeline.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(got.To.Add(-7 * 24 * time.Hour)); d < 0 || d > time.Minute {
		t.Errorf("the default window is not around the wall clock: to is %s", got.To)
	}
}

func TestAWindowThatIsWrongIsInvalid(t *testing.T) {
	e := newEnv(t)
	base := "2030-01-01T00:00:00Z"
	for name, c := range map[string]struct {
		q    timeline.Query
		part string
	}{
		"to before from":       {timeline.Query{From: base, To: "2029-12-31T00:00:00Z"}, "after from"},
		"to equal to from":     {timeline.Query{From: base, To: base}, "after from"},
		"401 days":             {timeline.Query{From: base, To: "2031-02-06T00:00:00Z"}, "400 days"},
		"a millisecond over":   {timeline.Query{From: base, To: "2031-02-05T00:00:00.001Z"}, "400 days"},
		"from is not a time":   {timeline.Query{From: "yesterday"}, `from "yesterday"`},
		"to is not a time":     {timeline.Query{To: "2030-02-01"}, `to "2030-02-01"`},
		"a bad closed_within":  {timeline.Query{ClosedWithin: "soon"}, "closed_within"},
		"a bad tag":            {timeline.Query{Tags: []string{"No Good"}}, "tag"},
		"a bad rows":           {timeline.Query{Rows: "some"}, `rows "some"`},
		"from default past to": {timeline.Query{From: "2040-01-01T00:00:00Z"}, "after from"},
	} {
		_, err := e.tl.Read(ctx, c.q)
		var bad timeline.InvalidError
		if !errors.Is(err, timeline.ErrInvalid) || !errors.As(err, &bad) {
			t.Errorf("%s: err = %v, want an InvalidError", name, err)
			continue
		}
		if !strings.Contains(bad.Msg, c.part) || bad.Next == "" || bad.Error() != bad.Msg {
			t.Errorf("%s: %+v does not say %q and what to send", name, bad, c.part)
		}
	}
	if _, err := e.tl.Read(ctx, timeline.Query{From: base, To: "2031-02-05T00:00:00Z"}); err != nil {
		t.Errorf("400 days exactly is answered: %v", err)
	}
	if _, err := e.tl.Read(ctx, timeline.Query{Initiatives: []string{"nope"}}); !errors.Is(err, timeline.ErrNoInitiative) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("an unknown initiative: %v", err)
	}
}

func TestTasksAreInTheWindowByTheirActualOrPlannedSpan(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	day := 24 * time.Hour
	e.task(t, "aa", "created at to", to, time.Time{})                                    // aa-1 in
	e.task(t, "aa", "created after to", to.Add(time.Millisecond), time.Time{})           // aa-2 out
	e.task(t, "aa", "closed at from", from.Add(-5*day), from)                            // aa-3 closed, in with closed_within
	e.task(t, "aa", "closed before from", from.Add(-5*day), from.Add(-time.Millisecond)) // aa-4 out
	p5 := e.task(t, "aa", "plan ends at from", to.Add(day), time.Time{})                 // aa-5 in
	e.plan(t, p5, nil, ptr(from))
	p6 := e.task(t, "aa", "plan ends before from", to.Add(day), time.Time{}) // aa-6 out
	e.plan(t, p6, nil, ptr(from.Add(-time.Millisecond)))
	p7 := e.task(t, "aa", "plan starts at to", to.Add(day), time.Time{}) // aa-7 in
	e.plan(t, p7, ptr(to), nil)
	p8 := e.task(t, "aa", "plan starts after to", to.Add(day), time.Time{}) // aa-8 out
	e.plan(t, p8, ptr(to.Add(time.Millisecond)), ptr(to.Add(day)))
	p9 := e.task(t, "aa", "plan spans the window", to.Add(day), time.Time{}) // aa-9 in
	e.plan(t, p9, ptr(from.Add(-day)), ptr(to.Add(day)))
	e.task(t, "aa", "open long ago", from.Add(-30*day), time.Time{}) // aa-10 in: still open

	got := e.read(t, timeline.Query{})
	is(t, "tasks", refs(got), []string{"aa-1", "aa-5", "aa-7", "aa-9", "aa-10"})
	got = e.read(t, timeline.Query{ClosedWithin: "90d"})
	is(t, "tasks with closed_within", refs(got), []string{"aa-1", "aa-3", "aa-5", "aa-7", "aa-9", "aa-10"})
	one := got.Tasks[0]
	if one.Ref != "aa-1" || one.Initiative != "aa" || one.Title != "created at to" || one.Status != "open" || one.Kind != "work" ||
		one.PlannedStart != nil || one.PlannedEnd != nil || one.ClosedAt != nil || !one.CreatedAt.Equal(to) || one.Deps == nil || len(one.Deps) != 0 {
		t.Errorf("task aa-1 = %+v", one)
	}
	closed := got.Tasks[1]
	if closed.Status != "closed" || closed.ClosedAt == nil || !closed.ClosedAt.Equal(from) {
		t.Errorf("task aa-3 = %+v", closed)
	}
	planned := got.Tasks[2]
	if planned.PlannedStart != nil || planned.PlannedEnd == nil || !planned.PlannedEnd.Equal(from) {
		t.Errorf("task aa-5 = %+v", planned)
	}
}

func TestAllRowsKeepsTheTasksWhereverTheirSpansFall(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	day := 24 * time.Hour
	e.task(t, "aa", "in the window", from.Add(day), time.Time{})                       // aa-1
	e.task(t, "aa", "created after to", to.Add(day), time.Time{})                      // aa-2
	p3 := e.task(t, "aa", "plan starts after to", to.Add(day), time.Time{})            // aa-3
	e.plan(t, p3, ptr(to.Add(2*day)), ptr(to.Add(3*day)))                              //
	e.task(t, "aa", "closed before from", from.Add(-5*day), from.Add(-day))            // aa-4
	e.task(t, "aa", "closed long before from", from.Add(-200*day), from.Add(-100*day)) // aa-5
	e.task(t, "aa", "open, held before the window", from.Add(-10*day), time.Time{})    // aa-6
	h := time.Hour
	e.lease(t, "aa-6", "codex@host", "before the window", "ended", from.Add(-3*day), from.Add(-3*day+h))
	inside := e.lease(t, "aa-1", "claude@host", "inside", "ended", from.Add(h), from.Add(2*h))
	e.event(t, p3, "milestone", "after the window", "", to.Add(2*day))

	is(t, "the window", refs(e.read(t, timeline.Query{Rows: timeline.RowsWindow})), []string{"aa-1", "aa-6"})
	got := e.read(t, timeline.Query{Rows: timeline.RowsAll})
	is(t, "all rows", refs(got), []string{"aa-1", "aa-2", "aa-3", "aa-6"})
	is(t, "leases stay in the window", leaseRefs(got), []string{inside})
	is(t, "markers stay in the window", markers(got), []string{})
	is(t, "all rows with closed_within", refs(e.read(t, timeline.Query{Rows: timeline.RowsAll, ClosedWithin: "30d"})), []string{"aa-1", "aa-2", "aa-3", "aa-4", "aa-6"})
	got = e.read(t, timeline.Query{Rows: timeline.RowsAll, ClosedWithin: "30d", Actor: "codex@host"})
	is(t, "the tasks the actor ever held", refs(got), []string{"aa-6"})
	is(t, "and only its leases in the window", leaseRefs(got), []string{})
	is(t, "the actor in the window", refs(e.read(t, timeline.Query{ClosedWithin: "30d", Actor: "codex@host"})), []string{})
}

func TestClosedWithinKeepsTheTasksClosedThatRecently(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	day := 24 * time.Hour
	since := now.AddDate(0, 0, -7)
	wide := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	farTo := now.Add(day).Format(time.RFC3339)
	e.task(t, "aa", "closed at the edge", wide, since)                                 // aa-1
	e.task(t, "aa", "closed just before the edge", wide, since.Add(-time.Millisecond)) // aa-2
	e.task(t, "aa", "still open", wide, time.Time{})                                   // aa-3
	for within, want := range map[string][]string{"7d": {"aa-1", "aa-3"}, "7": {"aa-1", "aa-3"}, "8": {"aa-1", "aa-2", "aa-3"}, "30d": {"aa-1", "aa-2", "aa-3"}} {
		got := e.read(t, timeline.Query{From: wide.Format(time.RFC3339), To: farTo, ClosedWithin: within})
		is(t, "closed_within "+within, refs(got), want)
	}
	got := e.read(t, timeline.Query{From: wide.Format(time.RFC3339), To: farTo})
	is(t, "without closed_within", refs(got), []string{"aa-3"})
}

func TestInitiativesTagsAndDepsAreReadForTheKeptTasks(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa", "web", "team")
	e.initiative(t, "bb", "web")
	e.initiative(t, "cc")
	e.initiative(t, "dd", "web")
	e.task(t, "aa", "one", from, time.Time{})
	e.task(t, "aa", "two", from, time.Time{})
	e.task(t, "bb", "three", from, time.Time{})
	e.task(t, "cc", "four", from, time.Time{})
	e.task(t, "dd", "five", to.Add(time.Hour), time.Time{}) // after the window: dd is not shown
	e.sql(t, `UPDATE initiatives SET status = 'closed' WHERE slug = 'bb'`)
	for _, dep := range [][2]string{{"aa-2", "bb-1"}, {"aa-2", "aa-1"}, {"cc-1", "aa-1"}} {
		if _, err := e.tk.AddDep(ctx, who, "key:caller", dep[0], dep[1]); err != nil {
			t.Fatal(err)
		}
	}

	got := e.read(t, timeline.Query{})
	if want := []timeline.Initiative{
		{Slug: "aa", Title: "I aa", Tags: []string{"team", "web"}},
		{Slug: "bb", Title: "I bb", Tags: []string{"web"}, Closed: true},
		{Slug: "cc", Title: "I cc", Tags: []string{}},
	}; !reflect.DeepEqual(got.Initiatives, want) {
		t.Errorf("initiatives = %+v, want %+v", got.Initiatives, want)
	}
	is(t, "tasks", refs(got), []string{"aa-1", "aa-2", "bb-1", "cc-1"})
	is(t, "deps of aa-2", got.Tasks[1].Deps, []string{"aa-1", "bb-1"})
	is(t, "deps of cc-1", got.Tasks[3].Deps, []string{"aa-1"})

	is(t, "initiative bb", refs(e.read(t, timeline.Query{Initiatives: []string{"bb"}})), []string{"bb-1"})
	is(t, "initiatives cc and aa", refs(e.read(t, timeline.Query{Initiatives: []string{"cc", "aa"}})), []string{"aa-1", "aa-2", "cc-1"})
	is(t, "tag team", refs(e.read(t, timeline.Query{Tags: []string{"team"}})), []string{"aa-1", "aa-2"})
	is(t, "tag web", refs(e.read(t, timeline.Query{Tags: []string{"web"}})), []string{"aa-1", "aa-2", "bb-1"})
	is(t, "tags web and team, in capitals", refs(e.read(t, timeline.Query{Tags: []string{"WEB", "team"}})), []string{"aa-1", "aa-2"})
	is(t, "tag none", refs(e.read(t, timeline.Query{Tags: []string{"web", "nothing"}})), []string{})
	is(t, "initiative and tag", refs(e.read(t, timeline.Query{Initiatives: []string{"aa", "bb"}, Tags: []string{"team"}})), []string{"aa-1", "aa-2"})
}

func TestLeasesAreInTheWindowWhenTheirSpanOverlapsIt(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	for _, title := range []string{"one", "two", "three", "four"} {
		e.task(t, "aa", title, from.Add(-48*time.Hour), time.Time{})
	}
	h := time.Hour
	l1 := e.lease(t, "aa-1", "claude@host", "ends at from", "ended", from.Add(-3*h), from)
	l2 := e.lease(t, "aa-1", "claude@host", "ends before from", "ended", from.Add(-3*h), from.Add(-time.Millisecond))
	l3 := e.lease(t, "aa-1", "codex@host", "expired before from", "expired", from.Add(-3*h), from.Add(-time.Millisecond))
	l4 := e.lease(t, "aa-1", "codex@host", "starts at to", "live", to, to.Add(h))
	l5 := e.lease(t, "aa-2", "codex@host", "starts after to", "live", to.Add(time.Millisecond), to.Add(h))
	l6 := e.lease(t, "aa-4", "codex@host", "expired inside", "expired", from.Add(h), from.Add(2*h))
	l7 := e.lease(t, "aa-3", "claude@host", "live since long ago", "live", from.Add(-30*24*h), now.Add(h))
	got := e.read(t, timeline.Query{})
	is(t, "leases", leaseRefs(got), []string{l1, l4, l6, l7})
	_, _, _ = l2, l3, l5

	byRef := map[string]timeline.Lease{}
	for _, l := range got.Leases {
		byRef[l.Ref] = l
	}
	if l := byRef[l1]; l.State != "ended" || l.EndedAt == nil || !l.EndedAt.Equal(from) || !l.StartedAt.Equal(from.Add(-3*h)) ||
		l.Task != "aa-1" || l.Holder != "claude@host" || l.Intent != "ends at from" {
		t.Errorf("ended lease = %+v", l)
	}
	if l := byRef[l4]; l.State != "live" || l.EndedAt != nil {
		t.Errorf("live lease = %+v", l)
	}
	if l := byRef[l6]; l.State != "expired" || l.EndedAt == nil || !l.EndedAt.Equal(from.Add(2*h)) {
		t.Errorf("expired lease ends at its expiry: %+v", l)
	}

	got = e.read(t, timeline.Query{Actor: "codex@host"})
	is(t, "tasks of codex", refs(got), []string{"aa-1", "aa-4"})
	is(t, "leases of the task of codex", leaseRefs(got), []string{l1, l4, l6})
	is(t, "tasks of nobody", refs(e.read(t, timeline.Query{Actor: "nobody@host"})), []string{})
	got = e.read(t, timeline.Query{From: from.Add(3 * time.Hour).Format(time.RFC3339), To: from.Add(4 * time.Hour).Format(time.RFC3339)})
	is(t, "leases in an hour in the middle", leaseRefs(got), []string{l7})
	is(t, "tasks of claude in the middle", refs(e.read(t, timeline.Query{Actor: "claude@host", From: from.Add(3 * time.Hour).Format(time.RFC3339), To: from.Add(4 * time.Hour).Format(time.RFC3339)})), []string{"aa-3"})
	is(t, "tasks of codex in the middle", refs(e.read(t, timeline.Query{Actor: "codex@host", From: from.Add(3 * time.Hour).Format(time.RFC3339), To: from.Add(4 * time.Hour).Format(time.RFC3339)})), []string{})
}

func TestMarkersAreTheMilestoneAskCheckDoneAndClosedEventsInTheWindow(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	e.initiative(t, "bb")
	a := e.task(t, "aa", "one", from.Add(-48*time.Hour), time.Time{})
	b := e.task(t, "bb", "two", from.Add(-48*time.Hour), time.Time{})
	h := time.Hour
	e.event(t, a, "milestone", "milestone on aa-1: line", `{"ref":"aa-1","summary":"the label"}`, from.Add(h))
	e.event(t, a, "ask_raised", "ask A1 raised on aa-1: line", `{"question":"Which one?"}`, from.Add(2*h))
	e.event(t, a, "check_recorded", "check on aa-1: line", `{"summary":"all fine"}`, from.Add(3*h))
	e.event(t, a, "task_done", "task aa-1 marked done", `{}`, from.Add(4*h))
	e.event(t, b, "task_closed", "task bb-1 closed as completed", `{}`, from.Add(4*h)) // the same instant: by event order
	e.event(t, a, "milestone", "milestone on aa-1: at from", `{"summary":"at from"}`, from)
	e.event(t, a, "milestone", "milestone on aa-1: before", `{"summary":"before"}`, from.Add(-time.Millisecond))
	e.event(t, a, "milestone", "milestone on aa-1: at to", `{"summary":"at to"}`, to)
	e.event(t, a, "milestone", "milestone on aa-1: after", `{"summary":"after"}`, to.Add(time.Millisecond))
	e.event(t, a, "milestone", "milestone on aa-1: broken data", `not json`, from.Add(5*h))
	e.event(t, a, "milestone", "milestone on aa-1: no summary", `{"summary":""}`, from.Add(6*h))
	e.event(t, a, "ask_raised", "ask A2 raised on aa-1: no question", `{"summary":"not the question"}`, from.Add(7*h))
	e.event(t, a, "task_updated", "task aa-1 updated: title", `{}`, from.Add(8*h))
	e.event(t, a, "lease_started", "L1 started", `{}`, from.Add(9*h))

	got := e.read(t, timeline.Query{})
	is(t, "markers", markers(got), []string{
		"aa-1:milestone:at from",
		"aa-1:milestone:the label",
		"aa-1:ask:Which one?",
		"aa-1:check:all fine",
		"aa-1:done:task aa-1 marked done",
		"bb-1:closed:task bb-1 closed as completed",
		"aa-1:milestone:milestone on aa-1: broken data",
		"aa-1:milestone:milestone on aa-1: no summary",
		"aa-1:ask:ask A2 raised on aa-1: no question",
		"aa-1:milestone:at to",
	})
	at := got.Markers[0].At
	if !at.Equal(from) {
		t.Errorf("first marker at %s, want %s", at, from)
	}
	is(t, "markers of bb", markers(e.read(t, timeline.Query{Initiatives: []string{"bb"}})), []string{"bb-1:closed:task bb-1 closed as completed"})
}

func TestTheMarkersAndLeasesOfATaskThatIsLeftOutAreLeftOutWithIt(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	c := e.task(t, "aa", "closed", from.Add(-48*time.Hour), time.Time{})
	e.event(t, c, "task_closed", "task aa-1 closed", `{}`, from.Add(time.Hour))
	e.lease(t, "aa-1", "claude@host", "work", "ended", from.Add(-time.Hour), from.Add(time.Minute))
	e.sql(t, `UPDATE tasks SET status = 'closed', closed_at = ? WHERE id = ?`, ms(from.Add(time.Hour)), c)
	got := e.read(t, timeline.Query{})
	if len(got.Tasks)+len(got.Leases)+len(got.Markers)+len(got.Initiatives) != 0 {
		t.Errorf("a closed task left everything it owns in: %+v", got)
	}
	got = e.read(t, timeline.Query{ClosedWithin: "90d"})
	if len(got.Tasks) != 1 || len(got.Leases) != 1 || len(got.Markers) != 1 || len(got.Initiatives) != 1 {
		t.Errorf("closed_within did not bring its task back with its lease and marker: %+v", got)
	}
}

func TestAnEmptyBoardAnswersEmptyListsNotNull(t *testing.T) {
	e := newEnv(t)
	got := e.read(t, timeline.Query{})
	if got.Tasks == nil || got.Leases == nil || got.Markers == nil || got.Initiatives == nil {
		t.Errorf("a list is nil: %+v", got)
	}
	if !got.From.Equal(from) || !got.To.Equal(to) {
		t.Errorf("window %s to %s", got.From, got.To)
	}
}

func TestPlannedOnlyTasksKeepBothEnds(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa")
	id := e.task(t, "aa", "planned", to.Add(time.Hour), time.Time{})
	e.plan(t, id, ptr(from.Add(time.Hour)), ptr(from.Add(2*time.Hour)))
	got := e.read(t, timeline.Query{})
	if len(got.Tasks) != 1 || got.Tasks[0].PlannedStart == nil || got.Tasks[0].PlannedEnd == nil ||
		!got.Tasks[0].PlannedStart.Equal(from.Add(time.Hour)) || !got.Tasks[0].PlannedEnd.Equal(from.Add(2*time.Hour)) {
		t.Errorf("tasks = %+v", got.Tasks)
	}
}

func TestValidRowsAcceptsOnlyWindowAndAll(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{timeline.RowsWindow, timeline.RowsAll} {
		if err := timeline.ValidRows(ok); err != nil {
			t.Errorf("ValidRows(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "some", "ALL", "Window", " all", "all ", "window,all"} {
		err := timeline.ValidRows(bad)
		var inv timeline.InvalidError
		if !errors.As(err, &inv) || inv.Next == "" || !strings.Contains(inv.Msg, "window") {
			t.Errorf("ValidRows(%q) = %v, want an InvalidError naming window and all", bad, err)
		}
	}
}

func TestCheckRefusesWhatReadRefusesAndAcceptsWhatItReads(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.initiative(t, "aa")
	base := "2030-01-01T00:00:00Z"
	for name, q := range map[string]timeline.Query{
		"a bad rows":                  {Rows: "some"},
		"a bad from":                  {From: "yesterday"},
		"an empty window":             {From: base, To: base},
		"a bad closed":                {ClosedWithin: "soon"},
		"a bad tag":                   {Tags: []string{"No Good"}},
		"a bad window":                {Window: "2h"},
		"a bad window with active on": {Window: "1d", Active: true},
	} {
		if err := e.tl.Check(ctx, q); !errors.Is(err, timeline.ErrInvalid) {
			t.Errorf("%s: Check = %v, want ErrInvalid", name, err)
		}
	}
	if err := e.tl.Check(ctx, timeline.Query{Initiatives: []string{"nope"}}); !errors.Is(err, timeline.ErrNoInitiative) {
		t.Errorf("an initiative that names nothing: Check = %v, want ErrNoInitiative", err)
	}
	for name, q := range map[string]timeline.Query{
		"no query": {}, "rows window": {Rows: timeline.RowsWindow}, "rows all": {Rows: timeline.RowsAll, Initiatives: []string{"aa"}},
	} {
		if err := e.tl.Check(ctx, q); err != nil {
			t.Errorf("%s: Check = %v, want nil", name, err)
		}
	}
}

// activeEnv seeds six open tasks of "aa" whose own events are all old, so that
// each is active (or not) for exactly one reason: aa-1 a live lease, aa-2 an
// attention reason the caller names, aa-3 an event 5h old, aa-4 an event 30h
// old, aa-5 nothing, aa-6 a lease that ran out.
func activeEnv(t *testing.T) env {
	t.Helper()
	e := newEnv(t)
	e.initiative(t, "aa")
	ids := make([]string, 7)
	for i := 1; i <= 6; i++ {
		ids[i] = e.task(t, "aa", fmt.Sprintf("t%d", i), now.Add(-90*24*time.Hour), time.Time{})
	}
	e.lease(t, "aa-1", "claude@host", "live", "live", now.Add(-2*time.Hour), now.Add(time.Hour))
	e.lease(t, "aa-6", "claude@host", "gone", "expired", now.Add(-48*time.Hour), now.Add(-47*time.Hour))
	e.event(t, ids[3], "milestone", "recent", "{}", now.Add(-5*time.Hour))
	e.event(t, ids[4], "milestone", "older", "{}", now.Add(-30*time.Hour))
	// Move every lease event of the seeded leases out of the windows too.
	e.sql(t, `UPDATE events SET at = ? WHERE type LIKE 'lease%'`, ms(now.Add(-100*24*time.Hour)))
	return e
}

func TestActiveKeepsALiveLeaseAnAttentionReasonAndAnEventInTheWindow(t *testing.T) {
	e := activeEnv(t)
	for _, c := range []struct {
		name string
		q    timeline.Query
		want []string
	}{
		{"off keeps every task", timeline.Query{}, []string{"aa-1", "aa-2", "aa-3", "aa-4", "aa-5", "aa-6"}},
		{"a window alone changes nothing", timeline.Query{Window: "8h"}, []string{"aa-1", "aa-2", "aa-3", "aa-4", "aa-5", "aa-6"}},
		{"24h by default", timeline.Query{Active: true, Attention: []string{"aa-2"}}, []string{"aa-1", "aa-2", "aa-3"}},
		{"8h", timeline.Query{Active: true, Window: "8h", Attention: []string{"aa-2"}}, []string{"aa-1", "aa-2", "aa-3"}},
		{"3d reaches the older event", timeline.Query{Active: true, Window: "3d", Attention: []string{"aa-2"}}, []string{"aa-1", "aa-2", "aa-3", "aa-4"}},
		{"no attention refs", timeline.Query{Active: true}, []string{"aa-1", "aa-3"}},
	} {
		is(t, c.name, refs(e.read(t, c.q)), c.want)
	}
}

func TestActiveNarrowsTheLeasesAndMarkersWithItsTasksAndAddsToTheOtherFilters(t *testing.T) {
	e := activeEnv(t)
	got := e.read(t, timeline.Query{Active: true, Window: "8h", From: now.Add(-24 * time.Hour).Format(time.RFC3339), To: now.Add(time.Hour).Format(time.RFC3339)})
	is(t, "tasks", refs(got), []string{"aa-1", "aa-3"})
	is(t, "leases", leaseRefs(got), []string{"L1"})
	if g := e.read(t, timeline.Query{Active: true, Actor: "nobody@host"}); len(g.Tasks) != 0 {
		t.Errorf("active with an actor nobody is: %v, want none", refs(g))
	}
}

func marksOf(task timeline.Task) []string {
	out := []string{}
	for _, m := range task.TagMarks {
		out = append(out, m.Source+":"+m.Tag)
	}
	return out
}

func TestEachTaskCarriesItsOwnTagsAndSourceMarkedOwnInitiativeAndActorTags(t *testing.T) {
	e := newEnv(t)
	e.initiative(t, "aa", "web", "team")
	e.initiative(t, "bb")
	e.task(t, "aa", "one", from, time.Time{})
	e.task(t, "aa", "two", from, time.Time{})
	e.task(t, "bb", "three", from, time.Time{})
	for _, add := range [][2]string{{"aa-1", "zeta"}, {"aa-1", "web"}, {"aa-1", "alpha"}, {"bb-1", "solo"}} {
		if _, err := e.tk.AddTag(ctx, who, "key:caller", add[0], add[1]); err != nil {
			t.Fatal(err)
		}
	}
	e.lease(t, "aa-1", "claude:w1", "x", "live", from, to)
	e.lease(t, "aa-1", "ralph:r1", "x", "ended", from, from.Add(time.Hour))
	e.lease(t, "aa-1", "foo:nobody", "x", "ended", from, from.Add(time.Hour))
	e.lease(t, "bb-1", "codex:c1", "x", "expired", from, from.Add(time.Hour))

	got := e.read(t, timeline.Query{})
	is(t, "tasks", refs(got), []string{"aa-1", "aa-2", "bb-1"})
	is(t, "own tags of aa-1", got.Tasks[0].Tags, []string{"alpha", "web", "zeta"})
	is(t, "marks of aa-1", marksOf(got.Tasks[0]), []string{"task:alpha", "task:web", "task:zeta", "initiative:team", "initiative:web", "actor:ralph", "actor:claude"})
	is(t, "own tags of a task with none", got.Tasks[1].Tags, []string{})
	is(t, "marks of aa-2", marksOf(got.Tasks[1]), []string{"initiative:team", "initiative:web"})
	is(t, "own tags of bb-1", got.Tasks[2].Tags, []string{"solo"})
	is(t, "marks of bb-1: an expired lease still gives its kind", marksOf(got.Tasks[2]), []string{"task:solo", "actor:codex"})
	// a filter narrows the marks read along with the tasks
	is(t, "marks of the tasks a filter keeps", marksOf(e.read(t, timeline.Query{Initiatives: []string{"bb"}}).Tasks[0]), []string{"task:solo", "actor:codex"})
}
