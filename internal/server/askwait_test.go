package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/feed"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/oapi-codegen/runtime"
)

// testClock is a clock the test moves. As an AskClock it also fires the timers
// a wait takes its read at until by, when the test moves it to them.
type testClock struct {
	ms     atomic.Int64
	mu     sync.Mutex
	timers []clockTimer
	armed  atomic.Int64 // timers ever set
}

type clockTimer struct {
	at time.Time
	ch chan time.Time
}

func newTestClock() *testClock {
	c := &testClock{}
	c.ms.Store(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli())
	return c
}
func (c *testClock) Now() time.Time { return time.UnixMilli(c.ms.Load()).UTC() }

// Advance moves the clock and fires every timer it reaches.
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ms.Add(d.Milliseconds())
	now := c.Now()
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.at.After(now) {
			kept = append(kept, t)
			continue
		}
		t.ch <- now
	}
	c.timers = kept
}

// At is the timer of an AskClock: it fires when Advance reaches at, at once
// when the clock is already there.
func (c *testClock) At(at time.Time) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed.Add(1)
	t := clockTimer{at: at, ch: make(chan time.Time, 1)}
	if !at.After(c.Now()) {
		t.ch <- c.Now()
		return t.ch, func() {}
	}
	c.timers = append(c.timers, t)
	return t.ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, k := range c.timers {
			if k.ch == t.ch {
				c.timers = append(c.timers[:i], c.timers[i+1:]...)
				return
			}
		}
	}
}

// askRig is a rig that serves asks from a store with the clock, its ask waits
// renewing poll_seen_at every touch.
func askRig(t *testing.T, st *store.Store, token string, clock *testClock, touch time.Duration) (*rig, *asks.Store) {
	t.Helper()
	return askRigWaitUnit(t, st, token, clock, touch, 0)
}

// quickWaitUnit is how long one second of ?wait= lasts in the rigs that shorten
// it (askRigQuick): a wait of 1 that nothing ends lasts a quarter of a second.
const quickWaitUnit = 250 * time.Millisecond

// askRigQuick is askRig for a test whose waits are not ended by anything but
// their own length: each second of ?wait= lasts quickWaitUnit.
func askRigQuick(t *testing.T, st *store.Store, token string, clock *testClock, touch time.Duration) (*rig, *asks.Store) {
	t.Helper()
	return askRigWaitUnit(t, st, token, clock, touch, quickWaitUnit)
}

// askRigWaitUnit is askRig with each second of ?wait= lasting unit (a real
// second when 0).
func askRigWaitUnit(t *testing.T, st *store.Store, token string, clock *testClock, touch, unit time.Duration) (*rig, *asks.Store) {
	t.Helper()
	as := &asks.Store{Store: st, Now: clock.Now}
	cfg := streamSettings{poll: 100 * time.Millisecond, heartbeat: time.Minute, extra: func(*store.Store) []Option {
		return []Option{WithAsks(as), WithAskTouch(touch), WithAskWaitUnit(unit)}
	}}
	return startRig(t, st, token, cfg), as
}

// askClockRig is askRig with the waits reading the time from the test clock
// too, so until, the reads of the wait and its timer at until are the test's to
// order. Every call of the ask store's own clock (a poll mark's stamp) goes
// through asNow, which a test counts to see the wait reach its marking of the
// poll.
func askClockRig(t *testing.T, st *store.Store, token string, clock *testClock, touch time.Duration, asNow func() time.Time) *rig {
	t.Helper()
	as := &asks.Store{Store: st, Now: asNow}
	cfg := streamSettings{poll: 100 * time.Millisecond, heartbeat: time.Minute, extra: func(*store.Store) []Option {
		return []Option{WithAsks(as), WithAskTouch(touch), WithAskClock(clock)}
	}}
	return startRig(t, st, token, cfg)
}

// raiseAsk raises a poll-mode global ask through the server and returns its ref.
func (r *rig) raiseAsk() string {
	r.t.Helper()
	resp := r.do(http.MethodPost, "/api/v1/asks", `{"question":"Which one?","mode":"poll"}`, r.token)
	defer func() { _ = resp.Body.Close() }()
	var doc struct{ Ref string }
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || resp.StatusCode != http.StatusCreated {
		r.t.Fatalf("raise = %d, %v", resp.StatusCode, err)
	}
	return doc.Ref
}

// heldWait is a GET with ?wait= running in the background.
type heldWait struct {
	status int
	state  string
	done   chan struct{}
}

func (r *rig) startWait(ref string, seconds string) *heldWait {
	r.t.Helper()
	w := &heldWait{done: make(chan struct{})}
	go func() {
		defer close(w.done)
		resp := r.do(http.MethodGet, "/api/v1/asks/"+ref+"?wait="+seconds, "", r.token)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		var doc struct{ State string }
		_ = json.Unmarshal(b, &doc)
		w.status, w.state = resp.StatusCode, doc.State
	}()
	return w
}

func (w *heldWait) ends(t *testing.T) {
	t.Helper()
	const within = 5 * time.Second
	select {
	case <-w.done:
	case <-time.After(within):
		t.Fatalf("the wait was still held after %s", within)
	}
}

func (r *rig) answer(ref string) {
	r.t.Helper()
	resp := r.do(http.MethodPost, "/api/v1/asks/"+ref+"/answer", `{"answer":"yes"}`, r.token)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("answer = %d", resp.StatusCode)
	}
}

// polledAt reads poll_seen_at of the ask straight from the store.
func polledAt(t *testing.T, as *asks.Store, ref string) *time.Time {
	t.Helper()
	k, err := as.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return k.PollSeenAt
}

// eventually polls cond for up to 5 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within 5 s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pollingEvents counts the ask_polling events in the store: the changes of an
// asker's displayed waiting state that reached the change feed.
func pollingEvents(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE type = 'ask_polling'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// askerWaiting reads asker_waiting of the ask through the server.
func (r *rig) askerWaiting(ref string) bool {
	r.t.Helper()
	resp := r.do(http.MethodGet, "/api/v1/asks/"+ref, "", r.token)
	defer func() { _ = resp.Body.Close() }()
	var doc struct {
		AskerWaiting bool `json:"asker_waiting"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		r.t.Fatal(err)
	}
	return doc.AskerWaiting
}

func TestAWaitSetsPollSeenAtAtItsStartAndAtItsEnd(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock := newTestClock()
		// A touch interval of an hour: only the start and the end write.
		r, as := askRig(t, st, newToken(t, st), clock, time.Hour)
		ref := r.raiseAsk()
		if polledAt(t, as, ref) != nil {
			t.Fatal("poll_seen_at is set before any wait")
		}
		start := clock.Now()
		w := r.startWait(ref, "1")
		eventually(t, "poll_seen_at at the start", func() bool { p := polledAt(t, as, ref); return p != nil && p.Equal(start) })
		clock.Advance(7 * time.Second)
		end := clock.Now()
		w.ends(t)
		if w.status != http.StatusOK || w.state != "open" {
			t.Errorf("the wait ended with %d %q, want 200 open", w.status, w.state)
		}
		if p := polledAt(t, as, ref); p == nil || !p.Equal(end) {
			t.Errorf("poll_seen_at after the wait = %v, want the end of the wait %v", p, end)
		}
		// The start made the asker waiting: one event. The end's mark renews a
		// poll still counted as waiting: none.
		if n := pollingEvents(t, st); n != 1 {
			t.Errorf("ask_polling events after a wait's start and end = %d, want 1", n)
		}
		if !r.askerWaiting(ref) {
			t.Error("asker_waiting is false right after the wait")
		}
	})
}

func TestAHeldWaitRenewsPollSeenAtOnEveryInterval(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock := newTestClock()
		r, as := askRig(t, st, newToken(t, st), clock, 20*time.Millisecond)
		ref := r.raiseAsk()
		w := r.startWait(ref, "30")
		first := clock.Now()
		eventually(t, "poll_seen_at at the start", func() bool { return polledAt(t, as, ref) != nil })
		for i := 1; i <= 3; i++ {
			clock.Advance(10 * time.Second)
			want := clock.Now()
			eventually(t, "a renewal of poll_seen_at", func() bool { p := polledAt(t, as, ref); return p != nil && !p.Before(want) })
		}
		select {
		case <-w.done:
			t.Fatal("the wait ended while the ask was open")
		default:
		}
		if p := polledAt(t, as, ref); !p.After(first) {
			t.Errorf("poll_seen_at = %v, want later than %v", p, first)
		}
		if n := pollingEvents(t, st); n != 1 {
			t.Errorf("ask_polling events after the start and three renewals = %d, want 1", n)
		}
		r.answer(ref)
		w.ends(t)
		if w.state != "answered" {
			t.Errorf("state after the answer = %q", w.state)
		}
		if n := pollingEvents(t, st); n != 1 {
			t.Errorf("ask_polling events after the answer ended the wait = %d, want 1", n)
		}
	})
}

// Every change of the displayed asker_waiting state of a held wait reaches the
// feed: a renewal that finds the previous mark older than the 60 seconds the
// asker counts as waiting turns it from not waiting to waiting again, and that
// is an event, where a renewal inside the window is none.
func TestAHeldWaitAppendsAnEventForEveryReturnToWaiting(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := migrated(t, dbs)
		clock := newTestClock()
		r, as := askRig(t, st, newToken(t, st), clock, 20*time.Millisecond)
		ref := r.raiseAsk()
		w := r.startWait(ref, "30")
		eventually(t, "the first ask_polling event", func() bool { return pollingEvents(t, st) == 1 })
		clock.Advance(30 * time.Second) // inside the window: renewals append none
		eventually(t, "a renewal inside the window", func() bool { p := polledAt(t, as, ref); return p != nil && p.Equal(clock.Now()) })
		if n := pollingEvents(t, st); n != 1 {
			t.Fatalf("a renewal inside the 60 s window appended an event: %d events", n)
		}
		clock.Advance(61 * time.Second) // the asker stopped waiting, the next renewal is waiting again
		eventually(t, "the second ask_polling event", func() bool { return pollingEvents(t, st) == 2 })
		if !r.askerWaiting(ref) {
			t.Error("asker_waiting is false while the wait is held")
		}
		r.answer(ref)
		w.ends(t)
	})
}

// A wait on an ask that cannot show an asker waiting, a stop ask or an answered
// one, marks nothing that changes the display, so it appends no event; a second
// poll ask is counted on its own.
func TestAWaitAppendsItsEventToItsOwnAskOnly(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := migrated(t, dbs)
		r, _ := askRigQuick(t, st, newToken(t, st), newTestClock(), time.Hour)
		answered := r.raiseAsk()
		r.answer(answered)
		r.startWait(answered, "1").ends(t)
		if n := pollingEvents(t, st); n != 0 {
			t.Fatalf("a wait on an answered ask appended %d events", n)
		}
		a, b := r.raiseAsk(), r.raiseAsk()
		r.startWait(a, "1").ends(t)
		if n := pollingEvents(t, st); n != 1 || r.askerWaiting(b) || !r.askerWaiting(a) {
			t.Errorf("after a wait on one of two asks: events = %d, waiting(a) = %v, waiting(b) = %v; want 1, true, false", n, r.askerWaiting(a), r.askerWaiting(b))
		}
		r.startWait(b, "1").ends(t)
		if n := pollingEvents(t, st); n != 2 {
			t.Errorf("after a wait on each ask: events = %d, want 2", n)
		}
	})
}

func TestAWaitEndsWhenTheClientLeavesAndMarksThePoll(t *testing.T) {
	t.Parallel()
	// until is a wall-clock instant a minute after the request, taken when the
	// request is made: taken when the test started, it passed before a subtest
	// queued behind the package's other parallel tests got to run (#240), and
	// the hold ended at until, before the client left.
	queries := map[string]func() string{
		"wait": func() string { return "?wait=60" },
		"wait+until": func() string {
			return "?wait=60&until=" + url.QueryEscape(time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
		},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testAWaitEndsWhenTheClientLeaves(t, query)
		})
	}
}

func testAWaitEndsWhenTheClientLeaves(t *testing.T, query func() string) {
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock := newTestClock()
		r, as := askRig(t, st, newToken(t, st), clock, time.Hour)
		ref := r.raiseAsk()
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/api/v1/asks/"+ref+query(), nil)
		req.Header.Set("Authorization", "Bearer "+r.token)
		req.Header.Set(ActorHeader, "tester@host")
		done := make(chan struct{})
		go func() {
			defer close(done)
			if resp, err := noKeepAlive.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}()
		eventually(t, "poll_seen_at at the start", func() bool { return polledAt(t, as, ref) != nil })
		clock.Advance(3 * time.Second)
		left := clock.Now()
		cancel()
		<-done
		eventually(t, "poll_seen_at at the end of the abandoned wait", func() bool { p := polledAt(t, as, ref); return p != nil && p.Equal(left) })
		if n := pollingEvents(t, st); n != 1 {
			t.Errorf("ask_polling events after an abandoned wait = %d, want 1", n)
		}
	})
}

func TestAWaitHeldOnOneServerEndsWhenTheAnswerIsWrittenThroughAnotherOnOnePostgres(t *testing.T) {
	t.Parallel()
	onPostgres(t, func(t *testing.T, dbs *postgresDatabases) {
		url := dbs.New(t)
		a := openStore(t, url)
		if err := a.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		token := newToken(t, a)
		clock := newTestClock()
		writer, _ := askRig(t, a, token, clock, time.Hour)
		reader, as := askRig(t, openStore(t, url), token, clock, time.Hour)
		ref := writer.raiseAsk()
		w := reader.startWait(ref, "120")
		eventually(t, "the wait to be held", func() bool { return polledAt(t, as, ref) != nil })
		writer.answer(ref)
		// The reader's feed tails every 100 ms: the wait ends within a few of them, not 120 s.
		w.ends(t)
		if w.status != http.StatusOK || w.state != "answered" {
			t.Errorf("the wait on the other server ended with %d %q, want 200 answered", w.status, w.state)
		}
	})
}

func TestAWaitIsRefusedOutsideOneToThreeHundredAndAnswers503WithoutAFeed(t *testing.T) {
	t.Parallel()
	o := askOps{asks: &asks.Store{}}
	for _, n := range []int{0, 301, -5} {
		got, err := o.GetAsk(context.Background(), GetAskRequestObject{Ref: "A1", Params: GetAskParams{Wait: &n}})
		if p, ok := got.(problemResponse); err != nil || !ok || p.status != http.StatusBadRequest {
			t.Errorf("wait=%d = %#v, %v, want a 400 problem", n, got, err)
		}
	}
	until := time.Now()
	for _, n := range []int{1, 300} {
		if p := askWaitProblem(&n, nil); p != nil {
			t.Errorf("wait=%d refused: %+v", n, p)
		}
		if p := askWaitProblem(&n, &until); p != nil {
			t.Errorf("wait=%d with until refused: %+v", n, p)
		}
	}
	if askWaitProblem(nil, nil) != nil {
		t.Error("no wait refused")
	}
	got, err := o.GetAsk(context.Background(), GetAskRequestObject{Ref: "A1", Params: GetAskParams{Until: &until}})
	if p, ok := got.(problemResponse); err != nil || !ok || p.status != http.StatusBadRequest || p.next == "" {
		t.Errorf("until without wait = %#v, %v, want a 400 problem with a next", got, err)
	}
}

// untilQuery is ?wait=seconds&until=at.
func untilQuery(seconds string, at time.Time) string {
	return seconds + "&until=" + url.QueryEscape(at.UTC().Format(time.RFC3339Nano))
}

// A wait with until ends at until, sub-second included, with the ask still
// open, when nothing ends it first; until after the wait's own end changes
// nothing; until already past answers at once.
func TestAWaitWithUntilEndsAtUntil(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		testAWaitWithUntilEndsAtUntil(t, openStore(t, dbs.New(t)))
		testAWaitWithUntilPastAnswersAtOnce(t, openStore(t, dbs.New(t)))
	})
}

func testAWaitWithUntilEndsAtUntil(t *testing.T, st *store.Store) {
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// each second of ?wait= lasts half a second, so a wait of 1 is held for 500 ms
	const second = 500 * time.Millisecond
	r, as := askRigWaitUnit(t, st, newToken(t, st), newTestClock(), time.Hour, second)
	ref := r.raiseAsk()
	for _, c := range []struct {
		query    string
		min, max time.Duration
	}{
		{untilQuery("1", time.Now().Add(150*time.Millisecond)), 150 * time.Millisecond, 450 * time.Millisecond},
		{untilQuery("1", time.Now().Add(time.Hour)), second, 950 * time.Millisecond},
	} {
		started := time.Now()
		w := r.startWait(ref, c.query)
		w.ends(t)
		took := time.Since(started)
		if w.status != http.StatusOK || w.state != "open" || took < c.min || took > c.max {
			t.Errorf("%s: %d %q after %s, want 200 open after %s to %s", c.query, w.status, w.state, took, c.min, c.max)
		}
	}
	if polledAt(t, as, ref) == nil {
		t.Error("the waits did not mark the poll")
	}
}

// testAWaitWithUntilPastAnswersAtOnce: until already past answers at once,
// with the ask open and the poll marked. Time is the test clock's, which never
// moves here: the wait is 300 seconds long and nothing ends the ask, so the
// read at until, whose timer the clock fires at once because until is behind
// it, is the only thing that can end the wait. Its ending at all proves it was
// not held, and its timer being set proves until is what ended it; nothing here
// depends on how long the request takes on a loaded host (a real-clock window
// of 250 ms failed at 256 ms beside full mutation).
func testAWaitWithUntilPastAnswersAtOnce(t *testing.T, st *store.Store) {
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock := newTestClock()
	r := askClockRig(t, st, newToken(t, st), clock, time.Hour, clock.Now)
	ref := r.raiseAsk()
	w := r.startWait(ref, untilQuery("300", clock.Now().Add(-time.Minute)))
	w.ends(t)
	if w.status != http.StatusOK || w.state != "open" {
		t.Errorf("until a minute past: %d %q, want 200 open", w.status, w.state)
	}
	if clock.armed.Load() == 0 {
		t.Error("until a minute past: the wait ended without taking its read at until")
	}
	if polledAt(t, &asks.Store{Store: st}, ref) == nil {
		t.Error("until a minute past: the wait did not mark the poll")
	}
}

// WithAskWaitUnit sets how long a second of ?wait= lasts, and a real second
// when it is unset or not above zero: a wait of n is held for n units, and the
// range of 1 to 300 is in seconds whatever the unit. The option survives
// WithAsks, which builds the ask operations, whichever comes first.
func TestAskWaitUnitIsASecondUnlessATestShortensIt(t *testing.T) {
	t.Parallel()
	three := 3
	for _, c := range []struct {
		name string
		unit time.Duration
		want time.Duration
	}{
		{"unset", 0, time.Second},
		{"not above zero", -time.Millisecond, time.Second},
		{"shortened", 40 * time.Millisecond, 40 * time.Millisecond},
	} {
		o := askOps{unit: c.unit, waits: &streamHub{}}
		if got := o.waitUnit(); got != c.want {
			t.Errorf("%s: unit = %s, want %s", c.name, got, c.want)
		}
		held, ok := o.read(context.Background(), asks.Ask{State: domain.AskOpen}, time.Now(), &three, nil).(askWaitResponse)
		if !ok || held.max != 3*c.want {
			t.Errorf("%s: a wait of 3 on an open ask = %#v, want a hold of %s", c.name, held, 3*c.want)
		}
	}
	for name, order := range map[string][]Option{
		"hook after asks":  {WithAsks(&asks.Store{}), WithAskWaitUnit(40 * time.Millisecond)},
		"hook before asks": {WithAskWaitUnit(40 * time.Millisecond), WithAsks(&asks.Store{})},
	} {
		var ops operations
		for _, opt := range order {
			opt(&ops)
		}
		if got := ops.waitUnit(); got != 40*time.Millisecond {
			t.Errorf("%s: unit = %s, want 40ms", name, got)
		}
	}
	var ops operations
	WithAsks(&asks.Store{})(&ops)
	if got := ops.waitUnit(); got != time.Second {
		t.Errorf("a server with no hook: unit = %s, want a second", got)
	}
}

// A wait with until answers with the ask as read when until passes, whatever
// happens to it after: an end whose transaction is still open at until is not
// in the answer, though it commits before the server answers (on SQLite,
// marking the poll waits for the writer it holds); an end committed before
// until is.
func TestAWaitWithUntilAnswersWithTheAskAsReadAtUntil(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		for _, held := range []bool{true, false} {
			testAWaitWithUntilAnswersAtUntil(t, openStore(t, dbs.New(t)), held)
		}
	})
}

func testAWaitWithUntilAnswersAtUntil(t *testing.T, st *store.Store, held bool) {
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Time is the test clock's: until is 500 ms after the wait starts on it, it
	// passes when the test advances it, and the end's commit mark is the instant
	// the test gives it. Nothing here depends on how long anything takes.
	clock := newTestClock()
	var marked atomic.Int64
	r := askClockRig(t, st, newToken(t, st), clock, time.Hour, func() time.Time { marked.Add(1); return clock.Now() })
	as := &asks.Store{Store: st}
	ref := r.raiseAsk()
	until := clock.Now().Add(500 * time.Millisecond)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	// The end is stamped 400 ms before until either way. Held, its transaction
	// stays open until released and its COMMIT returns after until; not held, it
	// commits before until.
	commit := until.Add(-300 * time.Millisecond)
	if held {
		commit = until.Add(200 * time.Millisecond)
	}
	ender := &asks.Store{Store: st, MarkClock: func() time.Time { return commit }, Now: func() time.Time {
		if held {
			once.Do(func() { close(entered); <-release })
		}
		return until.Add(-400 * time.Millisecond)
	}}
	w := r.startWait(ref, untilQuery("5", until))
	eventually(t, "the wait to be held", func() bool { return polledAt(t, as, ref) != nil && clock.armed.Load() > 0 })
	ended := make(chan error, 1)
	go func() {
		_, err := ender.Answer(context.Background(), audit.Actor{Handle: "nik@host", Operator: true}, "key:x", ref, asks.Reply{Answer: "late"})
		ended <- err
	}()
	if held {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the end never began")
		}
		before := marked.Load()
		clock.Advance(time.Second) // until passes with the end's transaction open
		// The wait has taken its read as until passed once it marks the poll after it.
		eventually(t, "the wait to take its read at until", func() bool { return marked.Load() > before })
		close(release)
	}
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	w.ends(t)
	want := "answered"
	if held {
		want = "open"
	}
	if w.status != http.StatusOK || w.state != want {
		t.Errorf("held=%v: the wait ended with %d %q, want 200 %q", held, w.status, w.state, want)
	}
	if k, err := as.Get(context.Background(), ref); err != nil || k.State != "answered" {
		t.Errorf("held=%v: the end did not commit (%v)", held, err)
	}
}

// A wait that starts while an end already holds the writer (so marking the poll
// at its start waits for it) and whose end commits after until answers with the
// ask still open: until is timed from the wait's start, not from when that
// first write comes back. Answers and withdrawals; and the same when the end is
// held across a renewal of the poll instead (a touch interval shorter than the
// wait). An end committed before until, with the wait started after it, is
// still the answer.
func TestAWaitWithUntilIgnoresAnEndHoldingTheWriterBeforeTheWaitStarted(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		for _, withdraw := range []bool{false, true} {
			for _, touch := range []time.Duration{time.Hour, 50 * time.Millisecond} {
				testAWaitUntilWithEndHoldingTheWriter(t, openStore(t, dbs.New(t)), withdraw, touch)
			}
		}
	})
}

func testAWaitUntilWithEndHoldingTheWriter(t *testing.T, st *store.Store, withdraw bool, touch time.Duration) {
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Time is the test clock's, as in testAWaitWithUntilAnswersAtUntil: until
	// passes when the test advances the clock, after the wait has set its timer
	// and while the end still holds the writer.
	clock := newTestClock()
	r := askClockRig(t, st, newToken(t, st), clock, touch, clock.Now)
	ref := r.raiseAsk()
	start := clock.Now()
	until := start.Add(600 * time.Millisecond)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ender := &asks.Store{Store: st, MarkClock: func() time.Time { return until.Add(300 * time.Millisecond) }, Now: func() time.Time {
		once.Do(func() { close(entered); <-release })
		return start
	}}
	ended := make(chan error, 1)
	go func() {
		if withdraw {
			_, err := ender.Withdraw(context.Background(), audit.Actor{Handle: "claude@host"}, "key:x", ref, "moved on")
			ended <- err
			return
		}
		_, err := ender.Answer(context.Background(), audit.Actor{Handle: "nik@host", Operator: true}, "key:x", ref, asks.Reply{Answer: "late"})
		ended <- err
	}()
	select {
	case <-entered: // the end holds the writer now
	case <-time.After(5 * time.Second):
		t.Fatal("the end never began")
	}
	w := r.startWait(ref, untilQuery("5", until))
	// The wait times its read at until before its first write, which waits for
	// the end: once the timer is set, until may pass.
	eventually(t, "the wait to set its timer at until", func() bool { return clock.armed.Load() > 0 })
	clock.Advance(time.Second)
	close(release)
	w.ends(t)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if w.status != http.StatusOK || w.state != "open" {
		t.Errorf("withdraw=%v touch=%s: the wait ended with %d %q, want 200 open", withdraw, touch, w.status, w.state)
	}
	if k, err := (&asks.Store{Store: st}).Get(context.Background(), ref); err != nil || k.State == "open" {
		t.Errorf("withdraw=%v touch=%s: the end did not commit (%v)", withdraw, touch, err)
	}
	// started after the end committed, with until still ahead: the end counts
	w = r.startWait(ref, untilQuery("5", clock.Now().Add(time.Minute)))
	w.ends(t)
	if w.state == "open" {
		t.Errorf("withdraw=%v touch=%s: a wait started after the end's commit still saw the ask open", withdraw, touch)
	}
}

func TestAWaitEndsWithTheOpenAskWhenTheServerShutsDown(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		r, as := askRig(t, st, newToken(t, st), newTestClock(), time.Hour)
		ref := r.raiseAsk()
		w := r.startWait(ref, "120")
		eventually(t, "the wait to be held", func() bool { return polledAt(t, as, ref) != nil })
		if err := r.stop(); err != nil {
			t.Errorf("the server stopped with %v", err)
		}
		w.ends(t)
		if w.status != http.StatusOK || w.state != "open" {
			t.Errorf("the wait ended by the shutdown = %d %q, want 200 open", w.status, w.state)
		}
	})
}

func TestAWaitStartsOverWhenTheFeedEndsItsSubscription(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		st := openStore(t, dbs.New(t))
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock := newTestClock()
		as := &asks.Store{Store: st, Now: clock.Now}
		r, _ := askRig(t, st, newToken(t, st), clock, time.Hour)
		ref := r.raiseAsk()
		// A waiter of its own over a feed that is never run, so the test ends its subscription.
		f := feed.NewAt(nil, 0, feed.Options{})
		hub := &streamHub{feed: f, heartbeat: time.Hour, stop: make(chan struct{})}
		k, err := as.Get(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		w := &waiter{ctx: context.Background(), store: as, hub: hub, k: k, sub: f.Subscribe(), clock: clock}
		w.sub.Close() // the feed ended it: the channel is closed
		if done, err := w.step(nil, nil, nil); done || err != nil {
			t.Fatalf("step on an ended subscription = %v, %v, want to go on", done, err)
		}
		select {
		case _, open := <-w.sub.Events():
			if !open {
				t.Fatal("the waiter kept the ended subscription")
			}
		default:
		}
		w.sub.Close()
	})
}

func TestAWaitOnAnOpenAskWithoutAFeedAnswers503AndOnAnAnsweredOneAnswersAtOnce(t *testing.T) {
	t.Parallel()
	wait := 5
	o := askOps{asks: &asks.Store{}}
	got := o.read(context.Background(), asks.Ask{State: "open"}, time.Now(), &wait, nil)
	if p, ok := got.(problemResponse); !ok || p.status != http.StatusServiceUnavailable {
		t.Errorf("a wait without a feed = %#v, want a 503 problem", got)
	}
	got = o.read(context.Background(), asks.Ask{State: "answered"}, time.Now(), &wait, nil)
	if _, ok := got.(GetAsk200JSONResponse); !ok {
		t.Errorf("a wait on an answered ask = %#v, want the ask at once", got)
	}
	got = o.read(context.Background(), asks.Ask{State: "open"}, time.Now(), nil, nil)
	if _, ok := got.(GetAsk200JSONResponse); !ok {
		t.Errorf("an open ask without wait = %#v, want the ask at once", got)
	}
}

// until is bound to the instant it names to the nanosecond, sub-millisecond
// part included, the way the generated handler binds it: the read at until is
// timed at the client's deadline itself, never at a rounded one.
func TestUntilKeepsItsSubMillisecondPart(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Time{
		"2026-11-10T12:00:01.000123Z":       time.Date(2026, 11, 10, 12, 0, 1, 123_000, time.UTC),
		"2026-11-10T12:00:01.000000001Z":    time.Date(2026, 11, 10, 12, 0, 1, 1, time.UTC),
		"2026-11-10T14:00:01.9999999+02:00": time.Date(2026, 11, 10, 12, 0, 1, 999_999_900, time.UTC),
	} {
		var until *time.Time
		err := runtime.BindQueryParameterWithOptions("form", true, false, "until", url.Values{"until": {in}}, &until,
			runtime.BindQueryParameterOptions{Type: "string", Format: "date-time"})
		if err != nil || until == nil || !until.Equal(want) {
			t.Errorf("until=%s bound to %v (%v), want %s", in, until, err, want.Format(time.RFC3339Nano))
		}
	}
}
