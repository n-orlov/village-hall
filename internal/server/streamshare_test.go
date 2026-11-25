package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/feed"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// flushWriter is the response of an in-process stream: every flush hands the
// text written since the last one to blocks, as one block of an sse.
type flushWriter struct {
	header http.Header
	mu     sync.Mutex
	buf    strings.Builder
	blocks chan string
}

func (w *flushWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *flushWriter) WriteHeader(int) {}
func (w *flushWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *flushWriter) FlushError() error {
	w.mu.Lock()
	text := strings.TrimSpace(w.buf.String())
	w.buf.Reset()
	w.mu.Unlock()
	if text != "" {
		w.blocks <- text
	}
	return nil
}

// countsLog records every counts computation the hub makes, by whether the
// query was the whole board, and signals each one on done.
type countsLog struct {
	mu               sync.Mutex
	unscoped, scoped int
	done             chan struct{}
}

func (l *countsLog) read() (unscoped, scoped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unscoped, l.scoped
}

func (l *countsLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unscoped, l.scoped = 0, 0
}

// shareRig is one database, its change feed and a stream hub whose counts
// computations are counted, with streams opened in-process.
type shareRig struct {
	t      *testing.T
	feed   *feed.Feed
	hub    *streamHub
	log    *countsLog
	clock  *testClock
	ticks  chan time.Time
	tasks  *tasks.Store
	leases *leases.Store
	inits  *initiatives.Store
}

func newShareRig(t *testing.T, dbs databases) *shareRig {
	t.Helper()
	st := openStore(t, dbs.New(t))
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f, err := feed.New(ctx, feed.StoreSource{Store: st}, feed.Options{Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fed := make(chan struct{})
	go func() { defer close(fed); f.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-fed })
	clock := newTestClock()
	attn := &attention.Store{Store: st, Now: clock.Now}
	log := &countsLog{done: make(chan struct{}, 64)}
	r := &shareRig{t: t, feed: f, log: log, clock: clock, ticks: make(chan time.Time),
		tasks: &tasks.Store{Store: st, Now: clock.Now}, leases: &leases.Store{Store: st, Now: clock.Now}, inits: &initiatives.Store{Store: st}}
	r.hub = &streamHub{feed: f, attention: attn, heartbeat: time.Hour, stop: make(chan struct{}),
		countsOf: func(ctx context.Context, q attention.Query) (attention.Counts, error) {
			c, err := attn.Counts(ctx, q)
			log.mu.Lock()
			if len(q.Initiatives) == 0 && len(q.Tags) == 0 {
				log.unscoped++
			} else {
				log.scoped++
			}
			log.mu.Unlock()
			log.done <- struct{}{}
			return c, err
		},
		ticks: func() (<-chan time.Time, func()) { return r.ticks, func() {} }}
	t.Cleanup(r.hub.Close)
	return r
}

var (
	shareActorA = audit.Actor{Handle: "a@host", WayBack: "pid 1"}
	shareActorB = audit.Actor{Handle: "b@host", WayBack: "pid 2"}
)

// initiative creates the initiative with a task named slug-1 and one named slug-2.
func (r *shareRig) initiative(slug string) {
	r.t.Helper()
	ctx := context.Background()
	if _, err := r.inits.Create(ctx, shareActorA, "key:"+slug, initiatives.New{Slug: slug, Title: "Title of " + slug}); err != nil {
		r.t.Fatal(err)
	}
	for _, title := range []string{"first", "second"} {
		if _, err := r.tasks.Create(ctx, shareActorA, "key:"+slug+title, tasks.New{Initiative: slug, Title: title}); err != nil {
			r.t.Fatal(err)
		}
	}
}

// lease starts one lease on task by actor, holding resource, and wakes the feed
// the way a request through the server does.
func (r *shareRig) lease(actor audit.Actor, task, resource string) {
	r.t.Helper()
	if _, err := r.leases.Start(context.Background(), actor, "key:"+actor.Handle, leases.Start{Task: task, Intent: "work", Resources: []string{resource}}); err != nil {
		r.t.Fatal(err)
	}
	r.feed.Notify()
}

// settle waits until the feed has delivered every event written so far.
func (r *shareRig) settle() {
	r.t.Helper()
	r.feed.Notify()
	deadline := time.Now().Add(5 * time.Second)
	for !r.feedCaughtUp() {
		if time.Now().After(deadline) {
			r.t.Fatalf("the feed did not catch up (at %d)", r.feed.Position())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// feedCaughtUp reports whether the feed's position is the newest event's.
func (r *shareRig) feedCaughtUp() bool {
	var newest int64
	err := r.hub.attention.Store.Reader().QueryRowContext(context.Background(), `SELECT COALESCE(MAX(n), 0) FROM events`).Scan(&newest)
	return err == nil && r.feed.Position() >= newest
}

// open starts an attention stream scoped to query and returns once its counts
// of the connection have arrived (and are returned).
func (r *shareRig) open(query attention.Query) (*sse, AttentionCounts) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &flushWriter{blocks: make(chan string, 64)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = streamResponse{ctx: ctx, hub: r.hub, topics: streamTopics{attention: true}, query: query}.VisitGetStreamResponse(w)
	}()
	r.t.Cleanup(func() { cancel(); <-done })
	s := &sse{t: r.t, blocks: w.blocks}
	return s, counts(r.t, s.nextMessage())
}

func TestUnscopedAttentionStreamsShareOneCountsComputationPerFeedBatch(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r := newShareRig(t, dbs)
		r.initiative("aa")
		r.initiative("bb")
		r.lease(shareActorA, "bb-1", "db:y")
		r.settle()
		var open []*sse
		for range 3 {
			s, connect := r.open(attention.Query{})
			if connect != (AttentionCounts{}) {
				t.Fatalf("counts on connect = %+v, want all zero", connect)
			}
			open = append(open, s)
		}
		onAA, _ := r.open(attention.Query{Initiatives: []string{"aa"}})
		onBB, connectBB := r.open(attention.Query{Initiatives: []string{"bb"}})
		if connectBB != (AttentionCounts{}) {
			t.Fatalf("counts of bb on connect = %+v, want all zero", connectBB)
		}
		r.log.reset()

		r.lease(shareActorB, "bb-2", "db:y") // a second holder of db:y: one conflict, in bb
		want := AttentionCounts{Conflicts: 1, Total: 1}
		for i, s := range open {
			if got := counts(t, s.nextMessage()); got != want {
				t.Errorf("unscoped stream %d got %+v, want %+v", i, got, want)
			}
		}
		if got := counts(t, onBB.nextMessage()); got != want {
			t.Errorf("stream scoped to bb got %+v, want its own counts %+v", got, want)
		}
		onAA.quiet(300 * time.Millisecond) // nothing of aa changed: its own scoped counts are equal
		unscoped, scoped := r.log.read()
		if unscoped != 1 {
			t.Errorf("unscoped Counts computations for one lease write with 3 unscoped streams = %d, want 1", unscoped)
		}
		if scoped != 2 {
			t.Errorf("scoped Counts computations = %d, want one per scoped stream (2)", scoped)
		}
	})
}

func TestUnscopedAttentionStreamsShareOneCountsComputationPerRecheckTick(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r := newShareRig(t, dbs)
		r.initiative("aa")
		cadence := int64(3600)
		if _, err := r.tasks.Create(context.Background(), shareActorA, "key:watch", tasks.New{Initiative: "aa", Title: "watch the build", Kind: "watch", CadenceSeconds: &cadence}); err != nil {
			t.Fatal(err)
		}
		r.settle()
		var open []*sse
		for range 3 {
			s, connect := r.open(attention.Query{})
			if connect.OverdueWatches != 0 {
				t.Fatalf("counts on connect = %+v, want no overdue watch yet", connect)
			}
			open = append(open, s)
		}
		r.log.reset()
		for len(r.log.done) > 0 {
			<-r.log.done
		}
		r.clock.Advance(90 * time.Minute)
		sendTick(t, r)
		want := AttentionCounts{OverdueWatches: 1, Total: 1}
		for i, s := range open {
			if got := counts(t, s.nextMessage()); got != want {
				t.Errorf("stream %d got %+v after the recheck, want %+v", i, got, want)
			}
		}
		sendTick(t, r) // the counts are equal now: computed, sent to nobody
		select {
		case <-r.log.done: // the computation of the first tick
		case <-time.After(2 * time.Second):
			t.Fatal("no computation for the first tick")
		}
		select {
		case <-r.log.done: // the computation of the second tick
		case <-time.After(2 * time.Second):
			t.Fatal("no computation for the second tick")
		}
		time.Sleep(100 * time.Millisecond)
		if unscoped, scoped := r.log.read(); unscoped != 2 || scoped != 0 {
			t.Errorf("computations for 2 ticks with 3 unscoped streams = %d unscoped, %d scoped, want 2 and 0", unscoped, scoped)
		}
		for _, s := range open {
			s.quiet(50 * time.Millisecond)
		}
	})
}

func sendTick(t *testing.T, r *shareRig) {
	t.Helper()
	select {
	case r.ticks <- time.Now():
	case <-time.After(2 * time.Second):
		t.Fatal("the recheck loop did not take the tick")
	}
}

func TestAScopedAttentionStreamRechecksItsOwnCountsWhenOnlyTheClockMovesAWatchOverdue(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		r := newShareRig(t, dbs)
		r.hub.heartbeat = 50 * time.Millisecond // a scoped stream rechecks on its own ticker, which follows the heartbeat
		r.initiative("aa")
		cadence := int64(3600)
		if _, err := r.tasks.Create(context.Background(), shareActorA, "key:watch", tasks.New{Initiative: "aa", Title: "watch the build", Kind: "watch", CadenceSeconds: &cadence}); err != nil {
			t.Fatal(err)
		}
		r.settle()
		s, connect := r.open(attention.Query{Initiatives: []string{"aa"}})
		if connect.OverdueWatches != 0 {
			t.Fatalf("counts on connect = %+v, want no overdue watch yet", connect)
		}
		r.clock.Advance(90 * time.Minute)
		if got, want := counts(t, s.nextMessage()), (AttentionCounts{OverdueWatches: 1, Total: 1}); got != want {
			t.Errorf("counts of the scoped stream after the clock moved = %+v, want %+v", got, want)
		}
	})
}
