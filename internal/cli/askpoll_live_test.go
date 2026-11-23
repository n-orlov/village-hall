package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/feed"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/server"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// The real-clock live cases of `vh ask --poll` are few: four witnesses, one
// per branch (an end committed before the deadline, one committed after it, one
// the CLI observes late, and one whose first read is queued behind it), each
// run once. Every other decision the live table made is a named row of
// serverAnswers on the fake clock (askpoll_test.go), and the server's own
// decisions are asserted by internal/server's tests on its real store.
// maxLiveCases is that budget; TestLiveCasesStayWithinTheBudget and TestMain
// fail the package when a fifth live board is opened.
const maxLiveCases = 4

// liveBoards counts the live boards the package's tests have opened.
var liveBoards atomic.Int32

// liveAskBoard is the real server over a real SQLite store, in real time:
// `vh ask --poll` raises and waits through it, while the test answers or
// withdraws the ask straight through the store (w), as another server on the
// same database would. The change feed is never woken by those writes (it
// polls once an hour), so the server learns of an end only by reading the ask:
// at the start of a wait, or as the wait's until passes.
type liveAskBoard struct {
	t     *testing.T
	path  string // the database file
	st    *store.Store
	reads *asks.Store // the server's
	w     *asks.Store // the ends', with clock as its clock
	srv   *httptest.Server
	mu    sync.Mutex
	// delay holds back every response to a read of the ask this long (not the
	// raise's, so the deadline stays a second after the raise)
	delay time.Duration
	// early holds back every read of the ask this long before the server
	// handles it, so the end can take the writer first
	early time.Duration
	// holdTx, when set, holds the end's transaction open from its first clock
	// read until it is closed, every read in it giving the time of the first
	holdTx chan struct{}
	frozen time.Time
}

func newLiveAskBoard(t *testing.T) *liveAskBoard {
	liveBoards.Add(1)
	path := filepath.Join(t.TempDir(), "b.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	me := audit.Actor{Handle: "claude@host"}
	if _, err := (&initiatives.Store{Store: st}).Create(context.Background(), me, "key:x", initiatives.New{Slug: "aa", Title: "I"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&tasks.Store{Store: st}).Create(context.Background(), me, "key:x", tasks.New{Initiative: "aa", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f, err := feed.New(ctx, feed.StoreSource{Store: st}, feed.Options{Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	go f.Run(ctx)
	b := &liveAskBoard{t: t, path: path, st: st, reads: &asks.Store{Store: st}}
	b.w = &asks.Store{Store: st, Now: b.clock}
	verify := func(context.Context, string) (string, error) { return "key:x", nil }
	h := server.NewHandler(func(context.Context) error { return nil }, verify, nil, nil,
		server.WithAsks(b.reads), server.WithStream(f, time.Minute), server.WithIdempotency(&idempotency.Store{Store: st}))
	b.srv = httptest.NewServer(b.delayed(h))
	t.Cleanup(b.srv.Close)
	return b
}

// delayed serves h, holding every response to a GET back by b.delay once h is
// done.
func (b *liveAskBoard) delayed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		d, early := b.delay, b.early
		b.mu.Unlock()
		if r.Method == http.MethodGet && early > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(early):
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if r.Method != http.MethodGet {
			d = 0
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(d):
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// clock is the clock of the store the ends are written through (see holdTx).
func (b *liveAskBoard) clock() time.Time {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state() != "open" { // read after the end committed
		return now
	}
	if hold := b.holdTx; hold != nil {
		b.holdTx, b.frozen = nil, now
		b.mu.Unlock()
		<-hold
		b.mu.Lock()
	}
	if !b.frozen.IsZero() {
		return b.frozen
	}
	return now
}

// state is the committed state of the first ask, as a reader sees it.
func (b *liveAskBoard) state() string {
	k, err := b.reads.Get(context.Background(), "A1")
	if err != nil {
		return "open"
	}
	return k.State
}

// end answers or withdraws the ask through the store, at after from start.
func (b *liveAskBoard) end(start time.Time, after time.Duration, withdraw bool) error {
	time.Sleep(time.Until(start.Add(after)))
	ctx := context.Background()
	if withdraw {
		_, err := b.w.Withdraw(ctx, audit.Actor{Handle: "claude@host"}, "key:x", "A1", "moved on")
		return err
	}
	_, err := b.w.Answer(ctx, audit.Actor{Handle: "nik@host", Operator: true}, "key:x", "A1", asks.Reply{Answer: "8080"})
	return err
}

// endAt ends the ask at after from start in the background, and reports the
// end's error once it returns.
func (b *liveAskBoard) endAt(start time.Time, after time.Duration, withdraw bool) <-chan error {
	done := make(chan error, 1)
	go func() { done <- b.end(start, after, withdraw) }()
	return done
}

// stateAt reports the committed state of the ask at after from start.
func (b *liveAskBoard) stateAt(start time.Time, after time.Duration) <-chan string {
	out := make(chan string, 1)
	go func() {
		time.Sleep(time.Until(start.Add(after)))
		out <- b.state()
	}()
	return out
}

// runLivePoll runs `vh ask aa-1 "Which port?" --poll 1s` against b, and
// returns its exit code and output and how long it took. The poll's deadline is
// a second after the raise returns, so it is later than start plus a second.
func (b *liveAskBoard) runLivePoll() (int, string, time.Duration) {
	started := time.Now()
	code, out, _ := runEnv((&fakeBoard{}).env(b.srv), "ask", "aa-1", "Which port?", "--poll", "1s")
	return code, out, time.Since(started)
}

// liveWitness is one real-clock case: the branch of the poll it witnesses end
// to end (real CLI, real server, real SQLite store, real time), and what it runs.
type liveWitness struct {
	branch string
	run    func(t *testing.T)
}

// liveWitnesses are all the real-clock live cases. Each one's decision on both
// legs (answer, withdrawal) and at every offset the live table once varied is a
// named row of serverAnswers.
var liveWitnesses = []liveWitness{
	{"an end committed before the deadline counts", func(t *testing.T) {
		b := newLiveAskBoard(t)
		start := time.Now()
		done := b.endAt(start, 300*time.Millisecond, false)
		code, out, took := b.runLivePoll()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if code != ExitOK || out != "ok A1 answered by nik@host: 8080\n" {
			t.Errorf("got %d %q after %s, want exit 0 and the answer", code, out, took)
		}
	}},
	{"an end committed after the deadline is exit 3", func(t *testing.T) {
		// the end's transaction takes its stamp before the deadline and commits
		// after it
		b := newLiveAskBoard(t)
		release := make(chan struct{})
		b.holdTx = release
		start := time.Now()
		done := b.endAt(start, 500*time.Millisecond, true)
		atDeadline := b.stateAt(start, 1050*time.Millisecond)
		go func() {
			time.Sleep(time.Until(start.Add(1400 * time.Millisecond)))
			close(release)
		}()
		code, out, took := b.runLivePoll()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if s := <-atDeadline; s != "open" {
			t.Fatalf("the ask was %q at the deadline, want still open", s)
		}
		k, err := b.reads.Get(context.Background(), "A1")
		if err != nil || k.State == "open" || k.WithdrawnAt == nil || !k.WithdrawnAt.Before(start.Add(time.Second)) {
			t.Fatalf("the end is %q stamped %v (%v), want committed and stamped before the deadline", k.State, k.WithdrawnAt, err)
		}
		if code != ExitTimeout || out != stillOpen || took < time.Second {
			t.Errorf("got %d %q after %s, want exit 3 at the deadline", code, out, took)
		}
	}},
	{"an end committed in time is counted though the answer is observed late", func(t *testing.T) {
		// every response is held back past the deadline
		b := newLiveAskBoard(t)
		b.delay = 1100 * time.Millisecond
		start := time.Now()
		done := b.endAt(start, 300*time.Millisecond, false)
		committed := b.stateAt(start, time.Second) // before the deadline
		code, out, took := b.runLivePoll()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if s := <-committed; s == "open" {
			t.Fatal("the end had not committed before the deadline")
		}
		if code != ExitOK || out != "ok A1 answered by nik@host: 8080\n" || took < 2*time.Second {
			t.Errorf("got %d %q after %s, want exit 0 and the answer once the delayed answer is back", code, out, took)
		}
	}},
	{"a first read queued behind an end committed after the deadline is exit 3", func(t *testing.T) {
		// the end holds the writer before the poll's first read reaches the
		// server, and commits after the deadline, by which time that first
		// read (held back 1.6 s) is still to reach the server
		code, out, took := livePollEnd(t, true, 100*time.Millisecond, 1400*time.Millisecond, func(b *liveAskBoard, _ time.Time) {
			b.early = 1600 * time.Millisecond
		})
		if code != ExitTimeout || out != stillOpen || took < 1600*time.Millisecond {
			t.Errorf("got %d %q after %s, want exit 3 once the queued read is answered", code, out, took)
		}
	}},
}

// TestAskPollLiveWitnesses runs the real-clock live cases, in parallel: the
// end-to-end witnesses of the poll's branches.
func TestAskPollLiveWitnesses(t *testing.T) {
	t.Parallel()
	for _, w := range liveWitnesses {
		t.Run(w.branch, func(t *testing.T) {
			t.Parallel()
			w.run(t)
		})
	}
}

// Real-clock live cases are slow (each waits out a second of wall time), so
// they stay at maxLiveCases. A new decision of the poll goes into
// serverAnswers on the fake clock, not here.
func TestLiveCasesStayWithinTheBudget(t *testing.T) {
	if len(liveWitnesses) > maxLiveCases {
		t.Errorf("%d real-clock live cases, at most %d allowed: move the rest onto the fake clock (serverAnswers in askpoll_test.go)", len(liveWitnesses), maxLiveCases)
	}
	if n := int(liveBoards.Load()); n > maxLiveCases {
		t.Errorf("%d live boards opened so far, at most %d allowed", n, maxLiveCases)
	}
}

// watcher is a second store on b's database, with its own connections, so the
// test can read the committed state of the ask while the server's writer is
// taken.
func (b *liveAskBoard) watcher() *asks.Store {
	st, err := store.Open("sqlite://" + b.path)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { _ = st.Close() })
	k := &asks.Store{Store: st}
	// Read once now, so the read the test times later reuses an open
	// connection rather than opening one in its window.
	if _, err := k.Get(context.Background(), "A1"); err != nil && !errors.Is(err, asks.ErrNotFound) {
		b.t.Fatal(err)
	}
	return k
}

// stateBy reports the committed state of the ask at after from start, read
// through k.
func stateBy(k *asks.Store, start time.Time, after time.Duration) <-chan string {
	out := make(chan string, 1)
	go func() {
		time.Sleep(time.Until(start.Add(after)))
		got, err := k.Get(context.Background(), "A1")
		if err != nil {
			out <- "unread: " + err.Error()
			return
		}
		out <- got.State
	}()
	return out
}

// livePollEnd runs one poll in which the end takes the writer at endAt (its
// transaction then held open, its stamps frozen there) and commits once
// released at release; prep sets up whatever else the case delays. It checks
// the ask was still open at the deadline and committed by the end of the
// poll, and returns the poll's exit code, output and duration.
func livePollEnd(t *testing.T, withdraw bool, endAt, release time.Duration, prep func(b *liveAskBoard, start time.Time)) (int, string, time.Duration) {
	b := newLiveAskBoard(t)
	watch := b.watcher()
	hold := make(chan struct{})
	b.holdTx = hold
	start := time.Now()
	prep(b, start)
	done := b.endAt(start, endAt, withdraw)
	atDeadline := stateBy(watch, start, 1050*time.Millisecond)
	go func() {
		time.Sleep(time.Until(start.Add(release)))
		close(hold)
	}()
	code, out, took := b.runLivePoll()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := <-atDeadline; s != "open" {
		t.Fatalf("withdraw=%v: the ask was %q at the deadline, want still open", withdraw, s)
	}
	if k, err := watch.Get(context.Background(), "A1"); err != nil || k.State == "open" || k.EndCommitAt == nil {
		t.Fatalf("withdraw=%v: the end did not commit with its mark (%+v, %v)", withdraw, k, err)
	}
	return code, out, took
}
