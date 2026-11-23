package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/store"
)

// markHold holds the write of an end's commit mark (end_commit_ns), which is
// made after the end's COMMIT returned and the real clock was read, until it is
// released: the mark is then sampled in time but persisted late. On SQLite a
// trigger calls a scalar function that waits on the hold; on Postgres it waits
// for an advisory lock the test takes.
//
// queued says a write of the server (the poll mark a settling wait makes)
// waits behind the held one: the wait has read the ask as until passed, found
// the end without its mark, and is about to take the mark from a read made
// once the hold is released.
type markHold struct {
	arm     func()
	reached func() bool
	queued  func() bool
	release func()
}

// holdWait bounds how long the test waits for a held write to be reached or
// for a write to queue behind it. It is a deadline for a hang, not a margin:
// each wait ends as soon as what it waits for happens.
const holdWait = 30 * time.Second

// waitFor polls cond every 5 ms until it holds or holdWait passes.
func waitFor(cond func() bool) bool {
	for deadline := time.Now().Add(holdWait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return false
}

const pgMarkKey = 424243

var (
	markHolds    sync.Map // database file -> chan struct{} closed to release
	markReached  sync.Map // database file -> chan struct{} closed when a write waits
	markFuncOnce sync.Once
	markFuncErr  error
)

// registerMarkFunc registers the scalar function the SQLite trigger calls. A
// driver function reaches only connections opened after it is registered, so
// this runs before the store opens.
func registerMarkFunc(t *testing.T) {
	t.Helper()
	if err := installMarkFunc(); err != nil {
		t.Fatal(err)
	}
}

// installMarkFunc registers the scalar function once. TestMain runs it before
// any test opens a database: the driver reads its functions unlocked as it
// opens a connection, so registering one while a parallel test opens its
// store would be a data race.
func installMarkFunc() error {
	markFuncOnce.Do(func() {
		markFuncErr = sqlite.RegisterScalarFunction("hold_end_commit_mark", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			p, _ := args[0].(string)
			release, ok := markHolds.Load(p)
			if !ok {
				return int64(0), nil
			}
			reached, _ := markReached.Load(p)
			select {
			case <-reached.(chan struct{}):
			default:
				close(reached.(chan struct{}))
			}
			<-release.(chan struct{})
			return int64(0), nil
		})
	})
	return markFuncErr
}

func newMarkHold(t *testing.T, leg string, st *store.Store, rawURL string) *markHold {
	t.Helper()
	ctx := context.Background()
	if leg == "postgres" {
		return newPostgresMarkHold(t, st, rawURL)
	}
	path := strings.TrimPrefix(rawURL, "sqlite://")
	release, reached := make(chan struct{}), make(chan struct{})
	markHolds.Store(path, release)
	markReached.Store(path, reached)
	t.Cleanup(func() { markHolds.Delete(path); markReached.Delete(path) })
	trigger := fmt.Sprintf(`CREATE TRIGGER hold_mark BEFORE UPDATE OF end_commit_ns ON asks
		BEGIN SELECT hold_end_commit_mark('%s'); END`, strings.ReplaceAll(path, "'", "''"))
	if _, err := st.Writer().ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	// SQLite has one writer connection, which the held write keeps: a later
	// write of the server waits for it in the pool.
	var waits int64
	return &markHold{
		arm: func() {},
		reached: func() bool {
			ok := waitClosed(reached)
			waits = st.Writer().Stats().WaitCount
			return ok
		},
		queued:  func() bool { return waitFor(func() bool { return st.Writer().Stats().WaitCount > waits }) },
		release: func() { close(release) },
	}
}

func newPostgresMarkHold(t *testing.T, st *store.Store, rawURL string) *markHold {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		fmt.Sprintf(`CREATE FUNCTION hold_end_commit_mark() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN PERFORM pg_advisory_lock(%d); PERFORM pg_advisory_unlock(%d); RETURN NEW; END $$`, pgMarkKey, pgMarkKey),
		`CREATE TRIGGER hold_mark BEFORE UPDATE OF end_commit_ns ON asks FOR EACH ROW EXECUTE FUNCTION hold_end_commit_mark()`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("pgx", rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var lock *sql.Conn
	return &markHold{
		arm: func() {
			var err error
			if lock, err = db.Conn(ctx); err == nil {
				_, err = lock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", pgMarkKey)
			}
			if err != nil {
				t.Error(err)
			}
		},
		reached: func() bool {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				_ = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND objid = $1", pgMarkKey).Scan(&n)
				if n > 0 {
					return true
				}
				time.Sleep(5 * time.Millisecond)
			}
			return false
		},
		// the held UPDATE has the ask's row locked, so the poll mark's UPDATE
		// of that row waits for its transaction
		queued: func() bool {
			return waitFor(func() bool {
				var n int
				_ = db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
					AND wait_event_type = 'Lock' AND wait_event <> 'advisory' AND query LIKE '%poll_seen_at%'`).Scan(&n)
				return n > 0
			})
		},
		release: func() {
			_, _ = lock.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", pgMarkKey)
			_ = lock.Close()
		},
	}
}

// markUntil is how far out until is from the wait's start, and markRelease how
// long after until the held write of the mark is released: the real-time windows
// of these cases are 0.6 of what they were (1 s and 300 ms), every offset of a
// case from until scaled with them, so each keeps its hold : mark delay : until.
const (
	markUntil   = 600 * time.Millisecond
	markRelease = 180 * time.Millisecond
)

// markCase is one end whose commit mark is sampled at endAt from the wait's
// start (until is markUntil out) but written only after until.
type markCase struct {
	name     string
	withdraw bool
	endAt    time.Duration
	counts   bool
	readAt   time.Duration // when the wait's request reaches the server, from until; 0 is at the start
}

// An end whose COMMIT returned, and whose real-clock mark was sampled, at or
// before until counts even when the write of that mark is still held up when
// until passes (the read at until finds the end without a mark) and runs past
// it: the wait marks the poll behind that write and takes the mark from a read
// made after. An end whose COMMIT returned after until is never counted, and
// the mark persisted late by it is no proof. Both databases.
func TestAWaitCountsAnEndWhoseTimelyMarkIsPersistedAfterUntil(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, leg string, dbs databases) {
		for _, c := range []markCase{
			{"answer, mark 240ms early", false, 360 * time.Millisecond, true, 0},
			{"withdrawal, mark 240ms early", true, 360 * time.Millisecond, true, 0},
			{"answer, mark 90ms early", false, 510 * time.Millisecond, true, 0},
			{"withdrawal, mark 90ms early", true, 510 * time.Millisecond, true, 0},
			{"answer committed 60ms late", false, 660 * time.Millisecond, false, 0},
			{"withdrawal committed 60ms late", true, 660 * time.Millisecond, false, 0},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				testTimelyMarkPersistedLate(t, leg, dbs, c)
			})
		}
	})
}

// The same holds when the wait's request reaches the server only after until,
// finds the end already committed and its mark still being written: that read
// is no proof (it came back after until), so the wait takes the mark once the
// write lands, and counts the end only if the mark is at or before until.
//
// The timely cases set until from the end's mark (testMarkBeforeUntil), so
// no scheduler has to run the end by a set instant; the late ones start the
// end after until, which a loaded host only makes later.
func TestAWaitReachingTheServerAfterUntilCountsAnEndWhoseTimelyMarkIsPersistedLate(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, leg string, dbs databases) {
		for _, c := range []markCase{
			{"answer, mark 240ms early", false, 360 * time.Millisecond, true, 60 * time.Millisecond},
			{"withdrawal, mark 240ms early", true, 360 * time.Millisecond, true, 60 * time.Millisecond},
			{"answer, mark 90ms early", false, 510 * time.Millisecond, true, 60 * time.Millisecond},
			{"withdrawal, mark 90ms early", true, 510 * time.Millisecond, true, 60 * time.Millisecond},
			{"withdrawal committed 60ms late", true, 660 * time.Millisecond, false, 90 * time.Millisecond},
			{"answer committed 60ms late", false, 660 * time.Millisecond, false, 90 * time.Millisecond},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				if c.counts {
					testMarkBeforeUntil(t, leg, dbs, c)
					return
				}
				testTimelyMarkPersistedLate(t, leg, dbs, c)
			})
		}
	})
}

// testMarkBeforeUntil runs c, a timely end whose wait reaches the server
// after until, in an order set by events rather than by the clock. The end
// runs first; once its COMMIT has returned and its mark has been read off the
// real clock (and its write is held), until is set to the mark plus the
// case's lead (markUntil - endAt), so the mark is before until by exactly that much
// however late the host ran the end. The wait is sent at until plus readAt,
// so it reaches the server after until, and the hold is released only once
// the wait's own poll write queues behind it: the wait has read the ask as
// until passed and found the end without its mark. The wait must then take the
// mark from a read made after the write lands and count the end.
func testMarkBeforeUntil(t *testing.T, leg string, dbs databases, c markCase) {
	registerMarkFunc(t)
	url := dbs.New(t)
	st := openStore(t, url)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	hold := newMarkHold(t, leg, st, url)
	as := &asks.Store{Store: st}
	r := startRig(t, st, newToken(t, st), streamSettings{poll: time.Hour, heartbeat: time.Minute, extra: func(*store.Store) []Option {
		return []Option{WithAsks(as), WithAskTouch(time.Hour)}
	}})
	ref := r.raiseAsk()
	hold.arm()
	marks := make(chan time.Time, 1)
	ender := &asks.Store{Store: st, MarkClock: func() time.Time {
		m := time.Now()
		marks <- m
		return m
	}}
	ended := make(chan error, 1)
	go func() { ended <- endAsk(ender, c.withdraw, ref) }()
	if !hold.reached() {
		t.Fatal("the end's mark write never started")
	}
	until := (<-marks).Add(markUntil - c.endAt)
	time.Sleep(time.Until(until.Add(c.readAt)))
	got := make(chan markResult, 1)
	go func() {
		status, doc := r.getUntil(ref, until)
		got <- markResult{status, doc}
	}()
	if !hold.queued() {
		t.Fatal("the wait's poll write never queued behind the end's mark write")
	}
	hold.release()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	res := <-got
	want := "answered"
	if c.withdraw {
		want = "withdrawn"
	}
	if res.status != http.StatusOK || res.doc["state"] != want || !endFields(res.doc) {
		t.Errorf("got %d %v, want 200, state %q and the end's fields", res.status, res.doc, want)
	}
	if k, err := as.Get(context.Background(), ref); err != nil || k.EndCommitAt == nil || k.EndCommitAt.After(until) {
		t.Errorf("persisted mark %v (%v), want one at or before until %v", k.EndCommitAt, err, until)
	}
}

// timelyAttempts is how many times a case whose end is to be timely runs
// before it gives up: see testTimelyMarkPersistedLate.
const timelyAttempts = 5

// testTimelyMarkPersistedLate runs c. An end meant to be timely is started a
// fixed time before until on the real clock, but whether its COMMIT returns and
// its mark is sampled by until is up to the scheduler: on a loaded host the end
// can run late, and then it is not the case under test (it is a late end, which
// the other cases cover). The persisted mark says which it was, since the mark
// is the real clock read after the COMMIT returned: a mark after until means
// the end was late, and the attempt is run again on a fresh database rather
// than judged. Every attempt whose mark is at or before until is judged in
// full, so an end that is timely and not counted still fails.
func testTimelyMarkPersistedLate(t *testing.T, leg string, dbs databases, c markCase) {
	for attempt := 1; ; attempt++ {
		res, k, until, err := runTimelyMark(t, leg, dbs, c)
		late := err == nil && k.EndCommitAt != nil && k.EndCommitAt.After(until)
		if c.counts && late {
			if attempt < timelyAttempts {
				t.Logf("attempt %d: the end's mark %v came after until %v (the host ran it late), so it was not timely; again", attempt, *k.EndCommitAt, until)
				continue
			}
			t.Fatalf("in %d attempts the end's mark never came by until (last %v, until %v): the host is too loaded to run a timely end", attempt, *k.EndCommitAt, until)
		}
		want := "open"
		if c.counts {
			want = "answered"
			if c.withdraw {
				want = "withdrawn"
			}
		}
		if res.status != http.StatusOK || res.doc["state"] != want {
			t.Errorf("got %d %v, want 200 and state %q", res.status, res.doc, want)
		}
		if endFields(res.doc) != c.counts {
			t.Errorf("end fields present = %v in %v, want %v", endFields(res.doc), res.doc, c.counts)
		}
		if err != nil || k.EndCommitAt == nil || k.EndCommitAt.After(until) == c.counts {
			t.Errorf("persisted mark %v (%v), want one at or before until: %v", k.EndCommitAt, err, c.counts)
		}
		return
	}
}

// markResult is what a wait returned.
type markResult struct {
	status int
	doc    map[string]any
}

// runTimelyMark runs c once on a fresh database, and returns what the wait
// returned, the ask as persisted once the end's mark was written, and until.
func runTimelyMark(t *testing.T, leg string, dbs databases, c markCase) (markResult, asks.Ask, time.Time, error) {
	t.Helper()
	registerMarkFunc(t)
	url := dbs.New(t)
	st := openStore(t, url)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	hold := newMarkHold(t, leg, st, url)
	// the feed polls once an hour, so only the read at until sees the end
	as := &asks.Store{Store: st}
	r := startRig(t, st, newToken(t, st), streamSettings{poll: time.Hour, heartbeat: time.Minute, extra: func(*store.Store) []Option {
		return []Option{WithAsks(as), WithAskTouch(time.Hour)}
	}})
	ref := r.raiseAsk()
	hold.arm()
	until := time.Now().Add(markUntil)
	got := make(chan markResult, 1)
	go func() {
		if c.readAt != 0 {
			time.Sleep(time.Until(until.Add(c.readAt)))
		}
		status, doc := r.getUntil(ref, until)
		got <- markResult{status, doc}
	}()
	ended := make(chan error, 1)
	go func() {
		time.Sleep(time.Until(until.Add(c.endAt - markUntil)))
		ended <- endAsk(&asks.Store{Store: st}, c.withdraw, ref)
	}()
	if !hold.reached() {
		t.Fatal("the end's mark write never started")
	}
	time.Sleep(time.Until(until.Add(markRelease)))
	hold.release()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	res := <-got
	k, err := as.Get(context.Background(), ref)
	return res, k, until, err
}
