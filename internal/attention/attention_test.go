package attention

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

var (
	t0  = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	ctx = context.Background()
	me  = audit.Actor{Handle: "claude@host", WayBack: "tmux 3"}
	bob = audit.Actor{Handle: "codex@host", WayBack: "pid 9"}
	op  = audit.Actor{Handle: "nik@host", Operator: true}
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// env is a migrated SQLite database with initiatives aa (tag ops) and bb, tasks
// aa-1, aa-2 and bb-1, and a clock that moves only when a test moves it.
type env struct {
	A      Store
	Asks   asks.Store
	Tasks  tasks.Store
	Leases leases.Store
	Clock  *clock
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

// newEnvOn seeds the standard initiatives and tasks on an already migrated store.
func newEnvOn(t *testing.T, st *store.Store) env {
	t.Helper()
	c := &clock{now: t0}
	ini := initiatives.Store{Store: st, Now: c.Now}
	for _, in := range []initiatives.New{{Slug: "aa", Title: "A", Tags: []string{"ops"}}, {Slug: "bb", Title: "B"}} {
		if _, err := ini.Create(ctx, op, "key:x", in); err != nil {
			t.Fatal(err)
		}
	}
	e := env{A: Store{Store: st, Now: c.Now}, Asks: asks.Store{Store: st, Now: c.Now}, Tasks: tasks.Store{Store: st, Now: c.Now},
		Leases: leases.Store{Store: st, Now: c.Now}, Clock: c}
	for _, slug := range []string{"aa", "aa", "bb"} {
		if _, err := e.Tasks.Create(ctx, op, "key:x", tasks.New{Initiative: slug, Title: "task in " + slug}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// tick moves the clock d on.
func (e env) tick(d time.Duration) { e.Clock.now = e.Clock.now.Add(d) }

func (e env) get(t *testing.T, q Query) Result {
	t.Helper()
	r, err := e.A.Get(ctx, q)
	if err != nil {
		t.Fatalf("get %+v: %v", q, err)
	}
	c, err := e.A.Counts(ctx, q)
	if err != nil {
		t.Fatalf("counts %+v: %v", q, err)
	}
	if c != r.Counts {
		t.Fatalf("Counts = %+v, but Get counted %+v", c, r.Counts)
	}
	live, err := e.Leases.Live(ctx)
	if err != nil {
		t.Fatalf("live leases: %v", err)
	}
	if c, err = e.A.CountsOf(ctx, q, live); err != nil || c != r.Counts {
		t.Fatalf("CountsOf = %+v, %v, but Get counted %+v", c, err, r.Counts)
	}
	return r
}

func (e env) counts(t *testing.T) Counts { t.Helper(); return e.get(t, Query{}).Counts }

// must returns v, or fails the test (by panic, which the test runner reports as a
// failure of the running test) when err is not nil: Go cannot pass t and a
// two-value call to one generic function.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (e env) ask(t *testing.T, a audit.Actor, in asks.New) asks.Ask {
	t.Helper()
	e.tick(time.Second)
	return must(e.Asks.Raise(ctx, a, "key:x", in))
}

func (e env) lease(t *testing.T, a audit.Actor, task string, resources ...string) leases.Lease {
	t.Helper()
	e.tick(time.Second)
	ttl := int64(60)
	return must(e.Leases.Start(ctx, a, "key:x", leases.Start{Task: task, Intent: "work on " + task, TTL: &ttl, Resources: resources})).Lease
}

func TestAnEmptyBoardHasZeroInEverySectionAndInTotal(t *testing.T) {
	e := newEnv(t)
	r := e.get(t, Query{})
	if r.Counts != (Counts{}) {
		t.Errorf("counts = %+v, want all zero (open_asks, done_not_closed, expired_leases, overdue_watches, conflicts, total)", r.Counts)
	}
	if r.OpenAsks == nil || r.DoneNotClosed == nil || r.ExpiredLeases == nil || r.OverdueWatches == nil || r.Conflicts == nil {
		t.Errorf("a section is nil, want empty lists: %+v", r)
	}
	if len(r.OpenAsks)+len(r.DoneNotClosed)+len(r.ExpiredLeases)+len(r.OverdueWatches)+len(r.Conflicts) != 0 {
		t.Errorf("an empty board has items: %+v", r)
	}
}

func TestAnOpenAskIsListedWithItsFieldsAndClearedByAnswerOrWithdrawal(t *testing.T) {
	e := newEnv(t)
	first := e.ask(t, me, asks.New{Task: "aa-1", Question: "which db?", Mode: "poll", Blocking: true})
	if err := e.Asks.Touch(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second := e.ask(t, bob, asks.New{Initiative: "bb", Question: "ship it?"})
	e.tick(30 * time.Second)

	r := e.get(t, Query{})
	if r.Counts.OpenAsks != 2 || r.Counts.Total != 2 {
		t.Fatalf("counts = %+v, want 2 open asks and a total of 2", r.Counts)
	}
	if r.OpenAsks[0].Ref != second.Ref || r.OpenAsks[1].Ref != first.Ref {
		t.Errorf("order = %s, %s, want newest first (%s, %s)", r.OpenAsks[0].Ref, r.OpenAsks[1].Ref, second.Ref, first.Ref)
	}
	got := r.OpenAsks[1]
	want := OpenAsk{Ref: first.Ref, Question: "which db?", Task: "aa-1", Initiative: "aa", Asker: Actor{Handle: "claude@host", WayBack: "tmux 3"},
		Mode: "poll", AskerWaiting: true, Blocking: true, CreatedAt: got.CreatedAt, Age: 31 * time.Second}
	if got != want {
		t.Errorf("open ask = %+v, want %+v", got, want)
	}
	if r.OpenAsks[0].Task != "" || r.OpenAsks[0].Initiative != "bb" || r.OpenAsks[0].AskerWaiting || r.OpenAsks[0].Asker.Handle != "codex@host" {
		t.Errorf("ask without a task = %+v", r.OpenAsks[0])
	}

	if _, err := e.Asks.Answer(ctx, op, "oidc:n", first.Ref, asks.Reply{Answer: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	if c := e.counts(t); c.OpenAsks != 1 || c.Total != 1 {
		t.Errorf("after the answer counts = %+v, want 1 open ask", c)
	}
	if _, err := e.Asks.Withdraw(ctx, bob, "key:x", second.Ref, ""); err != nil {
		t.Fatal(err)
	}
	if c := e.counts(t); c != (Counts{}) {
		t.Errorf("after the withdrawal counts = %+v, want all zero", c)
	}
}

func TestAnOpenAsksInitiativeIsKeptWhetherOrNotItHasATask(t *testing.T) {
	e := newEnv(t)
	onTask := e.ask(t, me, asks.New{Task: "bb-1", Question: "on a task?"})
	direct := e.ask(t, me, asks.New{Initiative: "aa", Question: "on an initiative?"})
	global := e.ask(t, me, asks.New{Question: "anywhere?"})
	both := e.ask(t, me, asks.New{Task: "aa-2", Initiative: "aa", Question: "task and initiative?"})

	want := map[string]OpenAsk{
		onTask.Ref: {Task: "bb-1", Initiative: "bb"},
		direct.Ref: {Task: "", Initiative: "aa"},
		global.Ref: {Task: "", Initiative: ""},
		both.Ref:   {Task: "aa-2", Initiative: "aa"},
	}
	r := e.get(t, Query{})
	if len(r.OpenAsks) != len(want) {
		t.Fatalf("open asks = %d, want %d", len(r.OpenAsks), len(want))
	}
	for _, k := range r.OpenAsks {
		if w := want[k.Ref]; k.Task != w.Task || k.Initiative != w.Initiative {
			t.Errorf("%s: task %q initiative %q, want task %q initiative %q", k.Ref, k.Task, k.Initiative, w.Task, w.Initiative)
		}
	}
	// The scope filter and the reported initiative agree: asking for aa keeps exactly the asks that report aa.
	for _, k := range e.get(t, Query{Initiatives: []string{"aa"}}).OpenAsks {
		if k.Initiative != "aa" {
			t.Errorf("%s kept by the aa filter reports initiative %q", k.Ref, k.Initiative)
		}
	}
}

func TestADoneTaskIsListedWithItsLastEndedLeaseAndClearedByClosing(t *testing.T) {
	e := newEnv(t)
	l := e.lease(t, me, "aa-1")
	e.tick(time.Second)
	must(e.Leases.End(ctx, me, "key:x", l.Ref, "shipped the thing"))
	e.tick(time.Second)
	must(e.Tasks.Done(ctx, me, "key:x", "aa-1", tasks.DoneOptions{}))
	e.tick(time.Second)
	must(e.Tasks.Done(ctx, bob, "key:x", "aa-2", tasks.DoneOptions{}))

	r := e.get(t, Query{})
	if r.Counts.DoneNotClosed != 2 || r.Counts.Total != 2 {
		t.Fatalf("counts = %+v, want 2 done tasks", r.Counts)
	}
	if r.DoneNotClosed[0].Task != "aa-2" || r.DoneNotClosed[1].Task != "aa-1" {
		t.Errorf("order = %s, %s, want the task done last first", r.DoneNotClosed[0].Task, r.DoneNotClosed[1].Task)
	}
	got := r.DoneNotClosed[1]
	if got.Title != "task in aa" || got.DoneBy != (Actor{Handle: "claude@host", WayBack: "tmux 3"}) || !got.DoneAt.Equal(t0.Add(3*time.Second)) {
		t.Errorf("done task = %+v", got)
	}
	wantLease := &LastLease{Lease: l.Ref, Holder: Actor{Handle: "claude@host", WayBack: "tmux 3"}, Outcome: "shipped the thing", EndedAt: t0.Add(2 * time.Second)}
	if !reflect.DeepEqual(got.LastLease, wantLease) {
		t.Errorf("last lease = %+v, want %+v", got.LastLease, wantLease)
	}
	if r.DoneNotClosed[0].LastLease != nil {
		t.Errorf("a task with no ended lease has last lease %+v", r.DoneNotClosed[0].LastLease)
	}

	must(e.Tasks.Close(ctx, op, "key:x", "aa-1", "completed"))
	if c := e.counts(t); c.DoneNotClosed != 1 {
		t.Errorf("after closing aa-1 counts = %+v, want 1", c)
	}
	must(e.Tasks.Close(ctx, op, "key:x", "aa-2", "dropped"))
	if c := e.counts(t); c != (Counts{}) {
		t.Errorf("after closing both counts = %+v, want all zero", c)
	}
}

func TestAnExpiredLeaseIsListedAndClearedByAckOrByALaterLeaseOnItsTask(t *testing.T) {
	e := newEnv(t)
	old := e.lease(t, me, "aa-1")
	other := e.lease(t, bob, "bb-1")
	e.tick(2 * time.Minute)

	r := e.get(t, Query{})
	if r.Counts.ExpiredLeases != 2 || r.Counts.Total != 2 {
		t.Fatalf("counts = %+v, want 2 expired leases (read expires the due ones)", r.Counts)
	}
	got := r.ExpiredLeases[1]
	want := ExpiredLease{Lease: old.Ref, Holder: Actor{Handle: "claude@host", WayBack: "tmux 3"}, Task: "aa-1", Title: "task in aa",
		Intent: "work on aa-1", RenewedAt: old.RenewedAt, ExpiredAt: old.ExpiresAt,
		Tags: []string{}, TagMarks: []tasks.TagMark{{Tag: "ops", Source: domain.TagSourceInitiative}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expired lease = %+v, want %+v", got, want)
	}
	if r.ExpiredLeases[0].Lease != other.Ref {
		t.Errorf("order: first is %s, want the lease that ran out last (%s)", r.ExpiredLeases[0].Lease, other.Ref)
	}

	must(e.Leases.Ack(ctx, op, "oidc:n", other.Ref))
	if c := e.counts(t); c.ExpiredLeases != 1 {
		t.Errorf("after the ack counts = %+v, want 1", c)
	}
	e.lease(t, bob, "aa-1")
	if c := e.counts(t); c.ExpiredLeases != 0 || c.Total != 0 {
		t.Errorf("after a later lease on the task counts = %+v, want 0", c)
	}
}

func TestAnOverdueWatchIsListedWithItsLastCheckAndClearedByRecordingACheck(t *testing.T) {
	e := newEnv(t)
	cadence := int64(3600)
	w := must(e.Tasks.Create(ctx, bob, "key:x", tasks.New{Initiative: "bb", Title: "watch the build", Kind: "watch", CadenceSeconds: &cadence}))
	must(e.Tasks.Check(ctx, me, "key:x", w.Ref, "first look", nil))
	if c := e.counts(t); c.OverdueWatches != 0 {
		t.Fatalf("a watch not yet due counts: %+v", c)
	}
	e.tick(90 * time.Minute)

	r := e.get(t, Query{})
	if r.Counts.OverdueWatches != 1 || r.Counts.Total != 1 {
		t.Fatalf("counts = %+v, want 1 overdue watch", r.Counts)
	}
	got := r.OverdueWatches[0]
	if got.Task != w.Ref || got.Title != "watch the build" || got.Owner != (Actor{Handle: "codex@host", WayBack: "pid 9"}) ||
		!got.NextCheckAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("overdue watch = %+v", got)
	}
	wantCheck := &LastCheck{Summary: "first look", By: Actor{Handle: "claude@host", WayBack: "tmux 3"}, At: t0}
	if !reflect.DeepEqual(got.LastCheck, wantCheck) {
		t.Errorf("last check = %+v, want %+v", got.LastCheck, wantCheck)
	}

	must(e.Tasks.Check(ctx, me, "key:x", w.Ref, "still red", nil))
	if c := e.counts(t); c != (Counts{}) {
		t.Errorf("after recording a check counts = %+v, want all zero", c)
	}
}

func TestAnOverdueWatchWithNoCheckHasNoLastCheck(t *testing.T) {
	e := newEnv(t)
	cadence := int64(60)
	must(e.Tasks.Create(ctx, bob, "key:x", tasks.New{Initiative: "aa", Title: "w", Kind: "watch", CadenceSeconds: &cadence}))
	e.tick(2 * time.Minute)
	r := e.get(t, Query{})
	if len(r.OverdueWatches) != 1 || r.OverdueWatches[0].LastCheck != nil {
		t.Errorf("watches = %+v, want one with no last check", r.OverdueWatches)
	}
}

func TestAConflictListsTheHoldersOfASharedResourceAndEndsWithOneOfThem(t *testing.T) {
	e := newEnv(t)
	a := e.lease(t, me, "aa-1", "repo:board", "host:x")
	b := e.lease(t, bob, "bb-1", "repo:board")
	e.lease(t, bob, "aa-2", "repo:other")

	r := e.get(t, Query{})
	if r.Counts.Conflicts != 1 || r.Counts.Total != 1 {
		t.Fatalf("counts = %+v, want 1 conflict", r.Counts)
	}
	got := r.Conflicts[0]
	if got.Resource != "repo:board" || !reflect.DeepEqual(got.Tasks, []string{"aa-1", "bb-1"}) || len(got.Holders) != 2 {
		t.Fatalf("conflict = %+v", got)
	}
	if h := got.Holders[0]; h.Lease != a.Ref || h.Actor != (Actor{Handle: "claude@host", WayBack: "tmux 3"}) || h.Task != "aa-1" ||
		h.Intent != "work on aa-1" || !h.ExpiresAt.Equal(a.ExpiresAt) {
		t.Errorf("first holder = %+v", h)
	}
	if got.Holders[1].Lease != b.Ref || got.Holders[1].Actor.WayBack != "pid 9" {
		t.Errorf("second holder = %+v", got.Holders[1])
	}

	must(e.Leases.End(ctx, bob, "key:x", b.Ref, ""))
	if c := e.counts(t); c != (Counts{}) {
		t.Errorf("after ending one of the conflicting leases counts = %+v, want all zero", c)
	}
}

func TestAConflictIsNeverMadeOfAnExpiredLease(t *testing.T) {
	e := newEnv(t)
	e.lease(t, me, "aa-1", "repo:board")
	e.tick(2 * time.Minute)
	e.lease(t, bob, "bb-1", "repo:board")
	if c := e.counts(t); c.Conflicts != 0 || c.ExpiredLeases != 1 {
		t.Errorf("counts = %+v, want no conflict and the one expired lease", c)
	}
}

func TestFiltersKeepTheItemsOfTheInitiativesAndTagsAndTheTotalFollows(t *testing.T) {
	e := newEnv(t)
	e.ask(t, me, asks.New{Task: "aa-1", Question: "in aa"})
	e.ask(t, me, asks.New{Task: "bb-1", Question: "in bb"})
	e.ask(t, me, asks.New{Question: "on no initiative"})
	must(e.Tasks.Done(ctx, me, "key:x", "aa-2", tasks.DoneOptions{}))
	must(e.Tasks.Done(ctx, me, "key:x", "bb-1", tasks.DoneOptions{}))
	must(e.Tasks.Create(ctx, op, "key:x", tasks.New{Initiative: "bb", Title: "second task in bb"}))
	e.lease(t, me, "aa-1", "repo:board")
	e.lease(t, bob, "bb-2", "repo:board")

	if c := e.counts(t); c.OpenAsks != 3 || c.DoneNotClosed != 2 || c.Conflicts != 1 || c.Total != 6 {
		t.Fatalf("unfiltered counts = %+v", c)
	}
	for name, q := range map[string]Query{
		"initiative": {Initiatives: []string{"aa"}},
		"tag":        {Tags: []string{"ops"}},
		"both":       {Initiatives: []string{"aa"}, Tags: []string{"OPS"}},
	} {
		r := e.get(t, q)
		if r.Counts.OpenAsks != 1 || r.OpenAsks[0].Question != "in aa" || r.Counts.DoneNotClosed != 1 || r.DoneNotClosed[0].Task != "aa-2" ||
			r.Counts.Conflicts != 1 || r.Counts.Total != 3 {
			t.Errorf("%s filter: %+v", name, r)
		}
	}
	if r := e.get(t, Query{Initiatives: []string{"aa", "bb"}}); r.Counts.OpenAsks != 2 || r.Counts.DoneNotClosed != 2 {
		t.Errorf("two initiatives: %+v", r.Counts)
	}
	if r := e.get(t, Query{Initiatives: []string{"bb"}, Tags: []string{"ops"}}); r.Counts != (Counts{}) {
		t.Errorf("an initiative without the tag: %+v", r.Counts)
	}
}

func TestAConflictOutsideTheScopeIsLeftOut(t *testing.T) {
	e := newEnv(t)
	e.lease(t, me, "aa-1", "repo:board")
	e.lease(t, bob, "bb-1", "repo:board")
	e.lease(t, me, "aa-2", "x")
	if r := e.get(t, Query{Tags: []string{"nope"}}); r.Counts.Conflicts != 0 {
		t.Errorf("conflicts under a tag nothing carries: %+v", r.Counts)
	}
}

func TestABadTagOrAnUnknownInitiativeIsRefused(t *testing.T) {
	e := newEnv(t)
	if _, err := e.A.Get(ctx, Query{Tags: []string{"Bad Tag"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad tag: %v, want ErrInvalid", err)
	}
	var inv InvalidError
	if _, err := e.A.Counts(ctx, Query{Tags: []string{"!"}}); !errors.As(err, &inv) || inv.Next == "" {
		t.Errorf("bad tag in counts: %v, want an InvalidError with a next step", err)
	}
	if _, err := e.A.Get(ctx, Query{Initiatives: []string{"zz"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown initiative: %v, want ErrNotFound", err)
	}
}

func hold(n int64, task, resource string) leases.Lease {
	return leases.Lease{N: n, Ref: fmt.Sprintf("L%d", n), Task: task, TaskID: task, Actor: "h" + task, Resources: []string{resource}}
}

func TestConflictsAreOrderedByTheirNewestLeaseThenByResource(t *testing.T) {
	live := []leases.Lease{hold(1, "t1", "b"), hold(2, "t2", "b"), hold(3, "t3", "a"), hold(4, "t4", "a"), hold(5, "t5", "c"), hold(6, "t6", "c"), hold(7, "t7", "z")}
	got := contested(live, nil, map[string]string{"ht1": "back"})
	var order []string
	for _, c := range got {
		order = append(order, c.Resource)
	}
	if want := []string{"c", "a", "b"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v (z is held once and is no conflict)", order, want)
	}
	if got[2].Holders[0].Actor != (Actor{Handle: "ht1", WayBack: "back"}) {
		t.Errorf("holder = %+v, want the way back from the map", got[2].Holders[0])
	}
	tie := contested([]leases.Lease{hold(1, "t1", "b"), hold(1, "t2", "b"), hold(1, "t3", "a"), hold(1, "t4", "a")}, nil, nil)
	if tie[0].Resource != "a" || tie[1].Resource != "b" {
		t.Errorf("equal newest leases: %v, %v, want the resource name to break the tie", tie[0].Resource, tie[1].Resource)
	}
}

func TestTheClockDefaultsToTheWallClock(t *testing.T) {
	if d := time.Since(Store{}.now()); d < 0 || d > time.Minute {
		t.Errorf("default clock is %v away from now", d)
	}
}

func TestAnInvalidErrorReadsAsItsMessage(t *testing.T) {
	if got := (InvalidError{Msg: "bad", Next: "n"}).Error(); got != "bad" {
		t.Errorf("Error = %q", got)
	}
}

func TestAMalformedCheckEventIsAnError(t *testing.T) {
	if _, err := lastCheck(t0, "{", Actor{}); err == nil {
		t.Error("lastCheck accepted data that is not JSON")
	}
}

func TestAConflictIsSinceTheNewestStartAmongItsHoldersWhateverTheLeaseOrder(t *testing.T) {
	e := newEnv(t)
	first := e.lease(t, me, "aa-1", "repo:board")
	e.tick(5 * time.Second)
	second := e.lease(t, bob, "bb-1", "repo:board")
	e.lease(t, me, "aa-2", "repo:old")
	e.tick(20 * time.Second)
	third := e.lease(t, bob, "aa-2", "repo:old")

	r := e.get(t, Query{})
	if len(r.Conflicts) != 2 {
		t.Fatalf("conflicts = %+v, want 2", r.Conflicts)
	}
	since := map[string]time.Time{}
	for _, c := range r.Conflicts {
		since[c.Resource] = c.Since
	}
	if got := since["repo:board"]; !got.Equal(second.StartedAt) || !got.After(first.StartedAt) {
		t.Errorf("repo:board since = %v, want the second lease's start %v", got, second.StartedAt)
	}
	if got := since["repo:old"]; !got.Equal(third.StartedAt) {
		t.Errorf("repo:old since = %v, want the later holder's start %v", got, third.StartedAt)
	}
}
