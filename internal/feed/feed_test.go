package feed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/store"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// fakeSource is an events table the test fills by hand, with gaps if it likes.
// writers names the transactions inside an event write; onWriters, when set,
// runs right after Writers has answered (a writer ending at that moment).
type fakeSource struct {
	mu        sync.Mutex
	items     []events.Event
	err       error
	writers   []string
	writerErr error
	onWriters func()
	asked     int
}

func (s *fakeSource) Writers(context.Context) ([]string, error) {
	s.mu.Lock()
	s.asked++
	w, err, after := slices.Clone(s.writers), s.writerErr, s.onWriters
	s.onWriters = nil
	s.mu.Unlock()
	if after != nil {
		after()
	}
	return w, err
}

func (s *fakeSource) setWriters(w ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = w
}

// commit inserts the events ns where n puts them, as a late commit does.
func (s *fakeSource) commit(ns ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range ns {
		s.items = append(s.items, events.Event{N: n, ID: fmt.Sprintf("id%d", n)})
	}
	slices.SortFunc(s.items, func(a, b events.Event) int { return int(a.N - b.N) })
}

func (s *fakeSource) put(ns ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range ns {
		s.items = append(s.items, events.Event{N: n, ID: fmt.Sprintf("id%d", n)})
	}
}

func (s *fakeSource) After(_ context.Context, n int64, limit int) ([]events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var out []events.Event
	for _, e := range s.items {
		if e.N > n && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *fakeSource) Last(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last int64
	for _, e := range s.items {
		last = max(last, e.N)
	}
	return last, s.err
}

func drain(s *Subscription) []int64 {
	var got []int64
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				return got
			}
			got = append(got, e.N)
		default:
			return got
		}
	}
}

func equal(a, b []int64) bool { return slices.Equal(a, b) }

func TestParseInterval(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultPoll, false}, {"  ", DefaultPoll, false}, {"250ms", 250 * time.Millisecond, false}, {"3s", 3 * time.Second, false},
		{"soon", 0, true}, {"0", 0, true}, {"0s", 0, true}, {"-1s", 0, true}, {"off", 0, true}, {"5", 0, true},
	} {
		got, err := ParseInterval(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseInterval(%q) = %v, %v; want %v, error %v", tc.in, got, err, tc.want, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "VH_STREAM_POLL") {
			t.Errorf("error %q does not name VH_STREAM_POLL", err)
		}
	}
}

func TestTheIdleTickIsABackstopOfAtLeastTenSeconds(t *testing.T) {
	if DefaultPoll < 10*time.Second {
		t.Errorf("DefaultPoll = %v, want at least 10s", DefaultPoll)
	}
	if got, err := ParseInterval(""); err != nil || got != DefaultPoll {
		t.Errorf("ParseInterval(\"\") = %v, %v; want DefaultPoll", got, err)
	}
	if got, err := ParseInterval("250ms"); err != nil || got != 250*time.Millisecond {
		t.Errorf("VH_STREAM_POLL=250ms gave %v, %v", got, err)
	}
}

// fakeClock is a clock the test advances by hand: its ticker fires once for
// every whole interval the clock has moved, and a poll never sleeps.
type fakeClock struct {
	mu       sync.Mutex
	now      time.Time
	interval time.Duration // what the feed asked its ticker for
	next     time.Time
	fired    int // ticks sent so far
	ch       chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: t0, ch: make(chan time.Time, 100)} }

func (c *fakeClock) tick(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interval, c.next = d, c.now.Add(d)
	return c.ch, func() {}
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for c.interval > 0 && !c.next.After(c.now) {
		c.ch <- c.next
		c.fired++
		c.next = c.next.Add(c.interval)
	}
}

func (c *fakeClock) ticks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fired
}

func (c *fakeClock) asked() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interval
}

// countingSource counts the reads of the table and says when one has finished.
type countingSource struct {
	*fakeSource
	reads  chan int
	mu     sync.Mutex
	nReads int
}

func (s *countingSource) After(ctx context.Context, n int64, limit int) ([]events.Event, error) {
	got, err := s.fakeSource.After(ctx, n, limit)
	s.mu.Lock()
	s.nReads++
	k := s.nReads
	s.mu.Unlock()
	s.reads <- k
	return got, err
}

func (s *countingSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nReads
}

// awaitRead waits for the read numbered want to finish.
func awaitRead(t *testing.T, src *countingSource, want int) {
	t.Helper()
	for {
		select {
		case k := <-src.reads:
			if k >= want {
				return
			}
		case <-time.After(testWait):
			t.Fatalf("read %d never finished (%d reads)", want, src.count())
		}
	}
}

func TestTheIdleTickRunsNoPollBeforeTheBackstopButANotifyDoesAndAPollIntervalSetsTheTick(t *testing.T) {
	src := &countingSource{fakeSource: &fakeSource{}, reads: make(chan int, 100)}
	clock := newFakeClock()
	f := NewAt(src, 0, Options{Tick: clock.tick}) // the production default interval
	s := f.Subscribe()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); waitDone(t, done) }()

	awaitRead(t, src, 1) // the initial poll
	if got := clock.asked(); got != DefaultPoll {
		t.Fatalf("the feed asked for a tick every %v, want DefaultPoll %v", got, DefaultPoll)
	}
	// The loop polls only on a tick or a Notify, so a 9 s advance that fires no
	// tick runs no poll; the Notify read below is then the second read exactly.
	clock.advance(9 * time.Second)
	if n := clock.ticks(); n != 0 {
		t.Fatalf("advancing 9s fired %d ticks, want none before the backstop", n)
	}
	if n := src.count(); n != 1 {
		t.Fatalf("%d reads after 9s with no write, want the initial one only", n)
	}

	src.put(1)
	f.Notify() // the clock does not move
	awaitRead(t, src, 2)
	if n := src.count(); n != 2 {
		t.Fatalf("%d reads after the Notify, want the initial one and the Notify's", n)
	}
	if e := receive(t, s); e.N != 1 {
		t.Errorf("got %+v", e)
	}

	// The backstop still fires once the interval has passed.
	src.put(2)
	clock.advance(DefaultPoll)
	if e := receive(t, s); e.N != 2 {
		t.Errorf("got %+v after the backstop", e)
	}
}

func TestAStreamPollIntervalIsWhatTheFeedAsksItsTickerFor(t *testing.T) {
	d, err := ParseInterval("3s")
	if err != nil {
		t.Fatal(err)
	}
	src := &countingSource{fakeSource: &fakeSource{}, reads: make(chan int, 100)}
	clock := newFakeClock()
	f := NewAt(src, 0, Options{Poll: d, Tick: clock.tick})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); waitDone(t, done) }()
	awaitRead(t, src, 1)
	if got := clock.asked(); got != 3*time.Second {
		t.Fatalf("the feed asked for a tick every %v, want 3s", got)
	}
	clock.advance(3 * time.Second)
	awaitRead(t, src, 2)
}

func TestPollDeliversInOrderOnceToEverySubscriber(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	a, b := f.Subscribe(), f.Subscribe()
	src.put(1, 2, 3)
	pollWithin(context.Background(), t, f)
	pollWithin(context.Background(), t, f)
	src.put(4)
	pollWithin(context.Background(), t, f)
	for name, s := range map[string]*Subscription{"a": a, "b": b} {
		if got := drain(s); !equal(got, []int64{1, 2, 3, 4}) {
			t.Errorf("subscriber %s got %v", name, got)
		}
	}
}

func TestSubscriptionAfterMarksWhereTheFeedWas(t *testing.T) {
	src := &fakeSource{}
	src.put(1, 2, 3)
	f := NewAt(src, 2, Options{})
	s := f.Subscribe()
	if s.After != 2 {
		t.Fatalf("After = %d, want 2", s.After)
	}
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{3}) {
		t.Errorf("got %v, want only the event after 2", got)
	}
	if late := f.Subscribe(); late.After != 3 {
		t.Errorf("a later subscriber starts at %d, want 3", late.After)
	}
}

func TestPositionIsTheLastEventDelivered(t *testing.T) {
	src := &fakeSource{}
	src.put(1, 2, 3, 4)
	f := NewAt(src, 1, Options{})
	if got := f.Position(); got != 1 {
		t.Fatalf("Position before a poll = %d, want 1", got)
	}
	s := f.Subscribe()
	pollWithin(context.Background(), t, f)
	if got := f.Position(); got != 4 {
		t.Errorf("Position after delivering 2..4 = %d, want 4", got)
	}
	if got := drain(s); !equal(got, []int64{2, 3, 4}) {
		t.Errorf("subscriber got %v, want 2..4", got)
	}
}

func TestNewStartsAtTheNewestEventAndReportsAnUnreadableTable(t *testing.T) {
	src := &fakeSource{}
	src.put(1, 2, 3, 4)
	f, err := New(context.Background(), src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subscribe()
	src.put(5)
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{5}) {
		t.Errorf("got %v, want only 5", got)
	}
	src.err = errors.New("db down")
	if _, err := New(context.Background(), src, Options{}); err == nil {
		t.Error("New on an unreadable table did not fail")
	}
}

func TestALazyFeedStartsAtTheNewestEventOnceTheTableCanBeRead(t *testing.T) {
	src := &fakeSource{}
	src.put(1, 2, 3)
	src.err = errors.New("no such table: events")
	f := NewLazy(src, Options{})
	s := f.Subscribe()
	if s.After != 0 {
		t.Errorf("a subscription before the first read starts at %d, want 0", s.After)
	}
	pollWithin(context.Background(), t, f)
	if got := drain(s); len(got) != 0 {
		t.Errorf("an unreadable table delivered %v", got)
	}
	src.err = nil
	pollWithin(context.Background(), t, f)
	if got := drain(s); len(got) != 0 {
		t.Errorf("the first readable poll replayed %v, want none of the existing events", got)
	}
	src.put(4)
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{4}) {
		t.Errorf("got %v, want only 4", got)
	}
	if late := f.Subscribe(); late.After != 4 {
		t.Errorf("a later subscriber starts at %d, want 4", late.After)
	}
}

func TestAGapHoldsLaterEventsForAsLongAsAWriterThatHeldItIsOpen(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	src.put(1, 3, 4)
	src.setWriters("w2", "w9")
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{1}) {
		t.Fatalf("before the gap closes got %v, want [1]", got)
	}
	// However long it stays open, and whoever else comes and goes.
	for range 50 {
		src.setWriters("w2", "w10")
		pollWithin(context.Background(), t, f)
	}
	if got := drain(s); len(got) != 0 {
		t.Fatalf("delivered %v across a gap whose writer is still open", got)
	}
	src.commit(2)
	src.setWriters("w10")
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{2, 3, 4}) {
		t.Errorf("after the gap closed got %v, want [2 3 4]", got)
	}
}

func TestAGapClosedByItsOwnCommitNeedsNoWriterToEnd(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	src.put(1, 3)
	src.setWriters("w2", "w4")
	pollWithin(context.Background(), t, f)
	src.commit(2)
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
	// The closed gap leaves nothing behind: a later gap waits on its own writers.
	src.commit(5)
	src.setWriters("w4")
	pollWithin(context.Background(), t, f)
	if got := drain(s); len(got) != 0 {
		t.Fatalf("delivered %v across the new gap at 4", got)
	}
	src.setWriters()
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{5}) {
		t.Errorf("got %v, want [5] once w4 ended without committing 4", got)
	}
}

func TestAGapWhoseWritersHaveAllEndedIsARolledBackWrite(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	src.put(1, 3, 4, 6)
	src.setWriters("w2")
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{1}) {
		t.Fatalf("got %v, want [1]", got)
	}
	src.setWriters("w7") // w2 ended; w7 began after the read and draws above 6
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{3, 4, 6}) {
		t.Fatalf("got %v, want [3 4 6]", got)
	}
	// 8 skips 7, which w7 may still commit: a new gap, with its own writers.
	src.put(8)
	pollWithin(context.Background(), t, f)
	if got := drain(s); len(got) != 0 {
		t.Fatalf("a second gap was skipped at once: %v", got)
	}
	src.setWriters()
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{8}) {
		t.Errorf("got %v, want [8]", got)
	}
}

func TestAGapWithNoWriterLeftIsResolvedInTheSamePoll(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	src.put(1, 3)
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{1, 3}) {
		t.Errorf("got %v, want [1 3]: no writer can still commit 2", got)
	}
}

func TestAWriterThatCommitsAsTheGapSettlesIsNotSkipped(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	src.put(1, 3, 4)
	src.setWriters("w2")
	pollWithin(context.Background(), t, f)
	// w2 commits 2 right after the feed sees it gone from the writers: the
	// table must be read after that check, or 2 would be lost.
	src.mu.Lock()
	src.writers = nil
	src.onWriters = func() { src.commit(2) }
	src.mu.Unlock()
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{1, 2, 3, 4}) {
		t.Errorf("got %v, want [1 2 3 4]", got)
	}
}

func TestAnUnreadableWriterListIsReportedAndSkipsNothing(t *testing.T) {
	src := &fakeSource{writerErr: errors.New("pg_locks down")}
	var reported []error
	f := NewAt(src, 0, Options{Report: func(err error) { reported = append(reported, err) }})
	s := f.Subscribe()
	src.put(1, 3)
	pollWithin(context.Background(), t, f) // opening the gap fails
	src.mu.Lock()
	src.writerErr = nil
	src.writers = []string{"w2"}
	src.mu.Unlock()
	pollWithin(context.Background(), t, f) // opens it
	src.mu.Lock()
	src.writerErr = errors.New("pg_locks down")
	src.writers = nil
	src.mu.Unlock()
	pollWithin(context.Background(), t, f) // cannot tell whether w2 ended
	if len(reported) != 2 {
		t.Errorf("reported %v, want two errors", reported)
	}
	if got := drain(s); !equal(got, []int64{1}) {
		t.Fatalf("got %v, want [1] while the writers are unknown", got)
	}
	src.mu.Lock()
	src.writerErr = nil
	src.mu.Unlock()
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{3}) {
		t.Errorf("got %v, want [3]", got)
	}
}

func TestNoGapAsksForNoWriters(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	src.put(1, 2, 3)
	pollWithin(context.Background(), t, f)
	pollWithin(context.Background(), t, f)
	if src.asked != 0 {
		t.Errorf("asked for the writers %d times without a gap", src.asked)
	}
}

func TestAFullBatchIsFollowedByTheNext(t *testing.T) {
	src := &fakeSource{}
	var all []int64
	for n := int64(1); n <= 3*batchSize+7; n++ {
		all = append(all, n)
	}
	src.put(all...)
	f := NewAt(src, 0, Options{Buffer: len(all)})
	s := f.Subscribe()
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, all) {
		t.Errorf("delivered %d events, want %d", len(got), len(all))
	}
}

func TestASlowSubscriberIsEndedAndTheOthersAreNot(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{Buffer: 2})
	slow := f.Subscribe()
	src.put(1, 2)
	pollWithin(context.Background(), t, f)
	fast := f.Subscribe()
	src.put(3, 4)
	pollWithin(context.Background(), t, f)
	if got := drain(slow); !equal(got, []int64{1, 2}) {
		t.Errorf("slow got %v", got)
	}
	if err := slow.Err(); !errors.Is(err, ErrOverflow) {
		t.Errorf("slow.Err() = %v, want ErrOverflow", err)
	}
	if got := drain(fast); !equal(got, []int64{3, 4}) {
		t.Errorf("fast got %v", got)
	}
	if err := fast.Err(); err != nil {
		t.Errorf("fast.Err() = %v while it is live", err)
	}
}

func TestCloseEndsASubscriptionOnceAndNothingMoreArrives(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	s.Close()
	s.Close()
	if stillOpen(t, s) {
		t.Error("the channel is open after Close")
	}
	if err := s.Err(); err != nil {
		t.Errorf("Err after Close = %v", err)
	}
	src.put(1)
	pollWithin(context.Background(), t, f)
	if len(f.subs) != 0 {
		t.Errorf("%d subscriptions left", len(f.subs))
	}
}

func TestAFailedPollIsReportedAndTheNextOneRecovers(t *testing.T) {
	src := &fakeSource{err: errors.New("db down")}
	var reported []error
	f := NewAt(src, 0, Options{Report: func(err error) { reported = append(reported, err) }})
	s := f.Subscribe()
	pollWithin(context.Background(), t, f)
	if len(reported) != 1 {
		t.Fatalf("reported %v", reported)
	}
	src.err = nil
	src.put(1)
	pollWithin(context.Background(), t, f)
	if got := drain(s); !equal(got, []int64{1}) {
		t.Errorf("got %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src.err = errors.New("canceled")
	pollWithin(ctx, t, f)
	if len(reported) != 1 {
		t.Errorf("an error after the context ended was reported: %v", reported)
	}
}

func TestRunEndsEverySubscriptionWhenItsContextEnds(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(src, 0, Options{})
	s := f.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if stillOpen(t, s) {
		t.Error("the channel is open after Run ended")
	}
	if err := s.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", err)
	}
}

// ---- against a real database ----

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// appendEvent commits one event in its own transaction.
func appendEvent(t *testing.T, st *store.Store, summary string) {
	t.Helper()
	tx, err := st.Writer().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	insertEvent(t, tx, st, summary)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// insertEvent appends one event in tx, as the actor "tester".
func insertEvent(t *testing.T, tx *sql.Tx, st *store.Store, summary string) {
	t.Helper()
	ctx := context.Background()
	id, err := audit.UpsertActor(ctx, tx, st.Dialect(), audit.Actor{Handle: "tester"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.AppendAs(ctx, tx, st.Dialect(), id, t0, audit.Event{Principal: "tester", Type: "note_posted", Summary: summary}); err != nil {
		t.Fatal(err)
	}
}

// testWait bounds every wait on the feed's goroutines: a feed that stops
// delivering or never returns fails the test at once, instead of leaving a
// blocked test to the go test timeout (a mutant of the feed that stalls is
// then killed in seconds, not timed out).
const testWait = 5 * time.Second

// pollWithin is f.Poll with a bound: a poll loop that never ends (the
// mutant of a stop condition) fails the test after testWait rather than
// holding it until the go test timeout. The spinning goroutine ends with the
// test binary.
func pollWithin(ctx context.Context, t *testing.T, f *Feed) {
	t.Helper()
	done := make(chan struct{})
	go func() { f.Poll(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatalf("Poll did not return within %v", testWait)
	}
}

// waitDone waits for the goroutine running the feed to end.
func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Errorf("Run did not return within %v", testWait)
	}
}

// awaitPoll waits for the signalled source to report a finished read.
func awaitPoll(t *testing.T, polled <-chan struct{}) {
	t.Helper()
	select {
	case <-polled:
	case <-time.After(testWait):
		t.Fatalf("the feed read nothing within %v", testWait)
	}
}

// sendTick fires one poll tick by hand.
func sendTick(t *testing.T, ch chan<- time.Time, at time.Time) {
	t.Helper()
	select {
	case ch <- at:
	case <-time.After(testWait):
		t.Fatalf("the feed took no tick within %v", testWait)
	}
}

// stillOpen reports whether the subscription's channel is open, waiting for
// it to deliver or close for a bounded time.
func stillOpen(t *testing.T, s *Subscription) bool {
	t.Helper()
	select {
	case _, ok := <-s.Events():
		return ok
	case <-time.After(testWait):
		t.Fatalf("the channel neither delivered nor closed within %v", testWait)
		return true
	}
}

func receive(t *testing.T, s *Subscription) events.Event {
	t.Helper()
	const within = 5 * time.Second
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatalf("subscription closed: %v", s.Err())
		}
		return e
	case <-time.After(within):
		t.Fatalf("no event within %v", within)
		return events.Event{}
	}
}

func TestStoreSourceReadsTheTableInOrderOfN(t *testing.T) {
	st := newStore(t)
	src := StoreSource{Store: st}
	if n, err := src.Last(context.Background()); err != nil || n != 0 {
		t.Fatalf("Last on an empty table = %d, %v", n, err)
	}
	for _, s := range []string{"one", "two", "three"} {
		appendEvent(t, st, s)
	}
	if n, err := src.Last(context.Background()); err != nil || n != 3 {
		t.Fatalf("Last = %d, %v", n, err)
	}
	got, err := src.After(context.Background(), 1, 10)
	if err != nil || len(got) != 2 || got[0].N != 2 || got[0].Summary != "two" || got[1].N != 3 {
		t.Errorf("After(1) = %v, %v", got, err)
	}
	if got, _ := src.After(context.Background(), 0, 2); len(got) != 2 || got[1].N != 2 {
		t.Errorf("After(0) with limit 2 = %v", got)
	}
}

// manualTicks is a poll ticker the test fires by hand.
type manualTicks struct {
	ch chan time.Time
}

func (m *manualTicks) tick(time.Duration) (<-chan time.Time, func()) { return m.ch, func() {} }

// signalled is a Source that says each time a read of the table has finished.
type signalled struct {
	Source
	polled chan struct{}
}

func (s signalled) After(ctx context.Context, n int64, limit int) ([]events.Event, error) {
	got, err := s.Source.After(ctx, n, limit)
	select {
	case s.polled <- struct{}{}:
	default:
	}
	return got, err
}

func TestASubscriberReceivesAWriteAtTheNextPollTick(t *testing.T) {
	st := newStore(t)
	ticks := &manualTicks{ch: make(chan time.Time)}
	src := signalled{Source: StoreSource{Store: st}, polled: make(chan struct{}, 1)}
	f, err := New(context.Background(), src, Options{Poll: time.Hour, Tick: ticks.tick})
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subscribe()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); waitDone(t, done) }()

	awaitPoll(t, src.polled) // the first read is over: the feed now waits for a tick
	appendEvent(t, st, "remote write")
	select {
	case e := <-s.Events():
		t.Fatalf("delivered %q before any tick or wake", e.Summary)
	case <-time.After(50 * time.Millisecond):
	}
	sendTick(t, ticks.ch, t0) // the one tick that ends the poll interval
	if e := receive(t, s); e.Summary != "remote write" || e.N != 1 {
		t.Errorf("got %+v", e)
	}
}

func TestASubscriberReceivesAWriteWithinOnePollIntervalOnARealTimer(t *testing.T) {
	st := newStore(t)
	const poll = 100 * time.Millisecond
	f, err := New(context.Background(), StoreSource{Store: st}, Options{Poll: poll})
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subscribe()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); waitDone(t, done) }()

	start := time.Now()
	appendEvent(t, st, "tick me")
	receive(t, s)
	// One poll interval, plus the scheduling slack of a loaded test host.
	if took := time.Since(start); took > poll+900*time.Millisecond {
		t.Errorf("delivered after %v, with a poll interval of %v", took, poll)
	}
}

func TestALocalWriteIsDeliveredBeforeThePollIntervalElapses(t *testing.T) {
	st := newStore(t)
	ticks := &manualTicks{ch: make(chan time.Time)} // never fires: the poll interval never elapses
	src := signalled{Source: StoreSource{Store: st}, polled: make(chan struct{}, 1)}
	f, err := New(context.Background(), src, Options{Poll: time.Hour, Tick: ticks.tick})
	if err != nil {
		t.Fatal(err)
	}
	s := f.Subscribe()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); waitDone(t, done) }()

	awaitPoll(t, src.polled)
	appendEvent(t, st, "local write")
	f.Notify()
	f.Notify() // wakes coalesce and never block
	if e := receive(t, s); e.Summary != "local write" {
		t.Errorf("got %+v", e)
	}
}

// goroutineSlack is how many goroutines a count may exceed another by and
// still be taken as the same: the process runs goroutines that come and go
// with the feed's subscribers not involved (the database pool's openers and
// cleaners, timers, the test runner), and a quiet moment to sample may not
// come on a loaded host. It is far below the 50 subscribers the test runs, so
// a goroutine per subscriber still shows.
const goroutineSlack = 10

// settledGoroutines is the fewest goroutines the process runs over several
// samples, polling for up to two seconds until that is at most atMost. A
// goroutine that only comes and goes is missing from at least one sample, so
// it does not count; one that stays does.
func settledGoroutines(atMost int) int {
	low := func() int {
		n := runtime.NumGoroutine()
		for range 9 {
			time.Sleep(5 * time.Millisecond)
			n = min(n, runtime.NumGoroutine())
		}
		return n
	}
	n := low()
	for deadline := time.Now().Add(2 * time.Second); n > atMost && time.Now().Before(deadline); {
		n = min(n, low())
	}
	return n
}

func TestAnUnsubscribedSubscriberLeaksNoGoroutine(t *testing.T) {
	st := newStore(t)
	f, err := New(context.Background(), StoreSource{Store: st}, Options{Poll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	defer func() { cancel(); waitDone(t, done) }()
	appendEvent(t, st, "warm up")
	warm := f.Subscribe()
	receive(t, warm)
	warm.Close()

	base := settledGoroutines(0)
	subs := make([]*Subscription, 50)
	for i := range subs {
		subs[i] = f.Subscribe()
	}
	appendEvent(t, st, "to all")
	f.Notify()
	for _, s := range subs {
		receive(t, s)
	}
	if n := settledGoroutines(base + goroutineSlack); n > base+goroutineSlack {
		t.Errorf("50 live subscribers run %d goroutines against a base of %d: a subscriber costs a goroutine", n, base)
	}
	for _, s := range subs {
		s.Close()
	}
	if n := settledGoroutines(base + goroutineSlack); n > base+goroutineSlack {
		t.Errorf("%d goroutines after unsubscribing, %d before subscribing", n, base)
	}
}

// replaying is a Source whose reads also return rows the feed already has.
type replaying struct{ *fakeSource }

func (r replaying) After(ctx context.Context, _ int64, limit int) ([]events.Event, error) {
	return r.fakeSource.After(ctx, 0, limit)
}

func TestARowAtOrBelowThePositionIsNeverDeliveredAgain(t *testing.T) {
	src := &fakeSource{}
	f := NewAt(replaying{src}, 0, Options{})
	s := f.Subscribe()
	src.put(1, 2)
	pollWithin(context.Background(), t, f)
	src.put(4)
	pollWithin(context.Background(), t, f) // 3 never committed and nobody holds it
	if got := drain(s); !equal(got, []int64{1, 2, 4}) {
		t.Errorf("got %v, want [1 2 4] with nothing twice", got)
	}
}
