package checkin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

var (
	ctx = context.Background()
	me  = audit.Actor{Handle: "claude@host"}
	bob = audit.Actor{Handle: "codex@host"}
	t0  = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
)

type env struct {
	S     Store
	Now   *time.Time
	Notes notes.Store
	Asks  asks.Store
	Cur   cursors.Store
}

// newEnv is a migrated SQLite database with initiative aa and tasks aa-1 and
// aa-2, and a clock the test moves.
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

// newEnvOn seeds the standard initiative and tasks on an already migrated store.
func newEnvOn(t *testing.T, st *store.Store) env {
	t.Helper()
	now := t0
	clock := func() time.Time { return now }
	if _, err := (initiatives.Store{Store: st, Now: clock}).Create(ctx, me, "k", initiatives.New{Slug: "aa", Title: "I"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := (tasks.Store{Store: st, Now: clock}).Create(ctx, me, "k", tasks.New{Initiative: "aa", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	e := env{Now: &now, Notes: notes.Store{Store: st, Now: clock}, Asks: asks.Store{Store: st, Now: clock}, Cur: cursors.Store{Store: st, Now: clock}}
	e.S = Store{
		Leases: &leases.Store{Store: st, Now: clock}, Asks: &e.Asks, Notes: &e.Notes, Cursors: &e.Cur,
		Attention: &attention.Store{Store: st, Now: clock}, Tasks: &tasks.Store{Store: st, Now: clock}, Now: clock,
	}
	return e
}

func (e env) advance(d time.Duration) { *e.Now = e.Now.Add(d) }

func (e env) note(t *testing.T, a audit.Actor, in notes.New) notes.Note {
	t.Helper()
	e.advance(time.Second)
	k, err := e.Notes.Post(ctx, a, "k", in)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (e env) check(t *testing.T) Result {
	t.Helper()
	r, err := e.S.Check(ctx, me.Handle, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func noteRefs(items []notes.Note) string {
	out := make([]string, len(items))
	for i, k := range items {
		out[i] = k.Ref
	}
	return strings.Join(out, ",")
}

func TestAnEmptyBoardChecksInWithEmptyPartsAndZeroCounts(t *testing.T) {
	r := newEnv(t).check(t)
	if r.MyAsks == nil || len(r.MyAsks) != 0 || r.Notes == nil || len(r.Notes) != 0 || r.NotesMore != 0 || r.Counts.Total != 0 {
		t.Errorf("empty check-in = %+v", r)
	}
}

func TestNotesAreThoseThatConcernMeAfterMyCursor(t *testing.T) {
	e := newEnv(t)
	e.advance(time.Second)
	if _, err := e.S.Leases.Start(ctx, me, "k", leases.Start{Task: "aa-1", Intent: "work", Resources: []string{"db:x"}}); err != nil {
		t.Fatal(err)
	}
	acked := e.note(t, bob, notes.New{Task: "aa-1", Summary: "acked"})
	onTask := e.note(t, bob, notes.New{Task: "aa-1", Summary: "on my task"})
	e.note(t, bob, notes.New{Task: "aa-2", Summary: "not mine"})
	onRes := e.note(t, bob, notes.New{Task: "aa-2", Resource: "db:x", Summary: "my resource"})
	mention := e.note(t, bob, notes.New{Task: "aa-2", Summary: "hey", Mentions: []string{me.Handle}})
	cast := e.note(t, bob, notes.New{Summary: "all"})
	if r := e.check(t); noteRefs(r.Notes) != "N1,N2,N4,N5,N6" {
		t.Fatalf("before a cursor, notes = %s", noteRefs(r.Notes))
	}
	if _, err := e.Cur.Set(ctx, me, cursors.StreamNotes, acked.ID); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{onTask.Ref, onRes.Ref, mention.Ref, cast.Ref}, ",")
	r := e.check(t)
	if noteRefs(r.Notes) != want || r.NotesMore != 0 {
		t.Errorf("notes = %s (%d more), want %s", noteRefs(r.Notes), r.NotesMore, want)
	}
	if pos, _ := e.Cur.Get(ctx, me.Handle, cursors.StreamNotes); pos != acked.ID {
		t.Errorf("reading the check-in moved the cursor to %s", pos)
	}
}

func TestAtMostTwentyNotesAreListedAndTheRestCounted(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < NotesLimit+3; i++ {
		e.note(t, bob, notes.New{Summary: "b"})
	}
	r := e.check(t)
	if len(r.Notes) != NotesLimit || r.NotesMore != 3 || r.Notes[0].Ref != "N1" {
		t.Errorf("notes = %d (%d more) from %s", len(r.Notes), r.NotesMore, r.Notes[0].Ref)
	}
}

func TestMyAnsweredAsksStayForTheLaterOfTheCursorAndTwentyFourHours(t *testing.T) {
	e := newEnv(t)
	e.advance(time.Second)
	k, err := e.Asks.Raise(ctx, me, "k", asks.New{Task: "aa-1", Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Asks.Answer(ctx, bob, "k", k.Ref, asks.Reply{Answer: "yes"}); err != nil {
		t.Fatal(err)
	}
	cursorNote := e.note(t, bob, notes.New{Summary: "marker"})
	if _, err := e.Cur.Set(ctx, me, cursors.StreamNotes, cursorNote.ID); err != nil {
		t.Fatal(err)
	}
	if r := e.check(t); len(r.MyAsks) != 1 || r.MyAsks[0].Answer != "yes" {
		t.Fatalf("answered ask right away = %+v", r.MyAsks)
	}
	e.advance(AnswerWindow - time.Minute)
	if r := e.check(t); len(r.MyAsks) != 1 {
		t.Errorf("answered ask inside 24 h = %+v", r.MyAsks)
	}
	e.advance(2 * time.Minute)
	if r := e.check(t); len(r.MyAsks) != 0 {
		t.Errorf("an answer older than 24 h and not after the cursor stays: %+v", r.MyAsks)
	}
}

func TestACursorNoteOlderThanADayWidensTheAnswerWindowOnly(t *testing.T) {
	e := newEnv(t)
	e.advance(time.Second)
	cursorNote := e.note(t, bob, notes.New{Summary: "marker"})
	e.advance(48 * time.Hour)
	k, _ := e.Asks.Raise(ctx, me, "k", asks.New{Question: "q"})
	if _, err := e.Asks.Answer(ctx, bob, "k", k.Ref, asks.Reply{Answer: "y"}); err != nil {
		t.Fatal(err)
	}
	e.advance(30 * time.Hour)
	if r := e.check(t); len(r.MyAsks) != 0 {
		t.Fatalf("without a cursor a 30 h old answer is out: %+v", r.MyAsks)
	}
	if _, err := e.Cur.Set(ctx, me, cursors.StreamNotes, cursorNote.ID); err != nil {
		t.Fatal(err)
	}
	if r := e.check(t); len(r.MyAsks) != 1 {
		t.Errorf("an answer after the cursor note stays: %+v", r.MyAsks)
	}
}

func TestAttentionCountsRideAlong(t *testing.T) {
	e := newEnv(t)
	e.advance(time.Second)
	if _, err := e.Asks.Raise(ctx, bob, "k", asks.New{Task: "aa-1", Question: "q"}); err != nil {
		t.Fatal(err)
	}
	if r := e.check(t); r.Counts.OpenAsks != 1 || r.Counts.Total != 1 {
		t.Errorf("counts = %+v", r.Counts)
	}
}

func TestConcernNamesEachLeasedTaskAndResourceOnce(t *testing.T) {
	mine := []leases.Lease{
		{Task: "aa-1", TaskID: "T1", Resources: []string{"db:x", "repo:y"}},
		{Task: "aa-2", TaskID: "T2", Resources: []string{"db:x"}},
	}
	got := concern("me@host", "01CURSOR", mine)
	if strings.Join(got.TaskIDs, ",") != "T1,T2" || len(got.Tasks) != 0 || strings.Join(got.Resources, ",") != "db:x,repo:y" ||
		got.Handle != "me@host" || got.After != "01CURSOR" {
		t.Errorf("concern = %+v", got)
	}
	if none := concern("me@host", "", nil); len(none.TaskIDs) != 0 || len(none.Resources) != 0 {
		t.Errorf("with no lease concern = %+v", none)
	}
}
