package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// boardOfSize is a database with n tasks in each of two initiatives; a third of
// them have a live lease, a third an ended one and the rest are untouched
// since they were made. It returns the Now read's store and how many statements
// the pools have run.
func boardOfSize(t *testing.T, n int, alerts ...string) (attention.Store, *atomic.Int64, func(time.Duration)) {
	t.Helper()
	return boardOn(t, "sqlite", n, alerts...)
}

// boardOn is boardOfSize on the database of the leg, "sqlite" or "postgres".
func boardOn(t *testing.T, leg string, n int, alerts ...string) (attention.Store, *atomic.Int64, func(time.Duration)) {
	t.Helper()
	ctx := context.Background()
	var count atomic.Int64
	var st *store.Store
	var err error
	if leg == "postgres" {
		st, err = store.OpenCountingPostgres(newPostgresDatabase(t), &count)
	} else {
		st, err = store.OpenCounting(filepath.Join(t.TempDir(), "b.db"), &count)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if alerts != nil {
		st.SetNotify(alerts)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	op := audit.Actor{Handle: "nik@host", Operator: true}
	agent := audit.Actor{Handle: "claude@host", WayBack: "tmux 1"}
	ini, tk, ls := initiatives.Store{Store: st, Now: clock}, tasks.Store{Store: st, Now: clock}, leases.Store{Store: st, Now: clock}
	for _, slug := range []string{"aa", "bb"} {
		if _, err := ini.Create(ctx, op, "key:x", initiatives.New{Slug: slug, Title: slug, Tags: []string{"web"}}); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= n; i++ {
			if _, err := tk.Create(ctx, op, "key:x", tasks.New{Initiative: slug, Title: fmt.Sprintf("task %d", i), Tags: []string{"own"}}); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Second)
			if i%3 == 0 {
				continue
			}
			l, err := ls.Start(ctx, agent, "key:x", leases.Start{Task: fmt.Sprintf("%s-%d", slug, i), Intent: "work", Resources: []string{"repo:x"}})
			if err != nil {
				t.Fatal(err)
			}
			if i%3 == 2 {
				if _, err := ls.End(ctx, agent, "key:x", l.Lease.Ref, "done for now"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return attention.Store{Store: st, Now: clock}, &count, func(d time.Duration) { now = now.Add(d) }
}

// queriesOfBoard is how many statements one Now read runs on a board of n tasks
// in each of two initiatives, and how many items it answered with.
func queriesOfBoard(t *testing.T, n int, q attention.Query) (int64, int) {
	t.Helper()
	a, count, _ := boardOfSize(t, n)
	before := count.Load()
	b, err := a.Board(context.Background(), q, attention.Window24h)
	if err != nil {
		t.Fatal(err)
	}
	return count.Load() - before, len(b.InFlight) + len(b.Recent)
}

func TestTheNowReadRunsTheSameNumberOfQueriesAtAnyStoreSize(t *testing.T) {
	for name, q := range map[string]attention.Query{
		"no filter":          {},
		"initiative and tag": {Initiatives: []string{"aa"}, Tags: []string{"own"}},
	} {
		small, smallItems := queriesOfBoard(t, 5, q)
		large, largeItems := queriesOfBoard(t, 50, q)
		if largeItems <= smallItems {
			t.Fatalf("%s: the large board answered %d items and the small one %d, so it proves nothing", name, largeItems, smallItems)
		}
		if small != large {
			t.Errorf("%s: %d queries at 5 tasks per initiative but %d at 50: the read is not constant", name, small, large)
		}
		if small == 0 {
			t.Errorf("%s: the read counted no queries, so the counter is not wired", name)
		}
	}
}

// queriesWithDueLeases is queriesOfBoard after the clock has passed the expiry
// of every live lease, so the read meets the due leases and expires them. It
// also says how many lease_expired events the read wrote.
func queriesWithDueLeases(t *testing.T, n int, alerts ...string) (int64, int) {
	t.Helper()
	a, count, advance := boardOfSize(t, n, alerts...)
	advance(48 * time.Hour)
	before := count.Load()
	if _, err := a.Board(context.Background(), attention.Query{}, attention.Window24h); err != nil {
		t.Fatal(err)
	}
	used := count.Load() - before
	var events int
	if err := a.Store.ReaderFor(context.Background()).QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE type = 'lease_expired'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return used, events
}

func TestTheNowReadRunsTheSameNumberOfQueriesWhenItMeetsDueLeases(t *testing.T) {
	for name, alerts := range map[string][]string{"alerts off": nil, "stranded alerts on": {store.NotifyStranded}} {
		small, smallEvents := queriesWithDueLeases(t, 5, alerts...)
		large, largeEvents := queriesWithDueLeases(t, 50, alerts...)
		if smallEvents == 0 || largeEvents <= smallEvents {
			t.Fatalf("%s: the read expired %d leases at 5 tasks per initiative and %d at 50, so it proves nothing", name, smallEvents, largeEvents)
		}
		if small != large {
			t.Errorf("%s: %d queries with %d due leases but %d with %d: the read is not constant", name, small, smallEvents, large, largeEvents)
		}
	}
}

// leaseMix is what seedLeases put on the board.
type leaseMix struct{ due, newestDue, liveNotDue, ended int }

// seedLeases adds tasks to initiative aa straight through SQL, each with the
// leases of every state the Now read meets, all an hour or more old: one live
// lease that falls due an hour after the board's latest start; on every fourth
// task an older live lease due before it (not the newest on its task, so no
// stranded alert); on every fifth an ended lease; on every second a later live
// lease not due for days, which leaves the due one not the newest either.
func seedLeases(t *testing.T, st *store.Store, tasks int) leaseMix {
	t.Helper()
	ctx := context.Background()
	tx, err := st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var ini, actor string
	var base int64
	if err := tx.QueryRowContext(ctx, `SELECT t.initiative_id, l.actor_id, (SELECT MAX(started_at) FROM leases) FROM leases l
		JOIN tasks t ON t.id = l.task_id JOIN initiatives i ON i.id = t.initiative_id WHERE i.slug = 'aa' LIMIT 1`).Scan(&ini, &actor, &base); err != nil {
		t.Fatal(err)
	}
	const hour = int64(time.Hour / time.Millisecond)
	// Set-based: one recursive count feeds one insert per kind of row, a handful
	// of statements whatever the size (33000 tasks and their 100000 leases took
	// 96 s under -race as one statement each). The rows are the same.
	const nums = `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) `
	if _, err := tx.ExecContext(ctx, nums+`INSERT INTO tasks (id, initiative_id, seq, title, created_by, created_at, updated_at)
		SELECT 'seeded-' || i, ?, 1000 + i, 'seeded seeded-' || i, ?, ?, ? FROM n`, tasks, ini, actor, base-4*hour, base-4*hour); err != nil {
		t.Fatal(err)
	}
	lease := func(n int, every int, state string, started, expires int64) {
		if _, err := tx.ExecContext(ctx, nums+`INSERT INTO leases (id, task_id, actor_id, intent, ttl_seconds, started_at, renewed_at, expires_at, state, ended_at)
			SELECT 'seeded-' || i || '-l' || ?, 'seeded-' || i, ?, 'bulk', 3600, ?, ?, ?, ?, CASE WHEN ? = 'ended' THEN ? END FROM n WHERE i % ? = 0`,
			tasks, n, actor, started, started, expires, state, state, expires, every); err != nil {
			t.Fatal(err)
		}
	}
	lease(0, 5, "ended", base-3*hour, base-2*hour)
	lease(1, 4, "live", base-2*hour, base-hour)
	lease(2, 1, "live", base-hour, base+hour)
	lease(3, 2, "live", base+hour, base+100*hour)
	mix := leaseMix{
		ended:      tasks / 5,
		due:        tasks/4 + tasks, // the older live lease of every fourth, the one of every task
		liveNotDue: tasks / 2,       // every second task's later lease
		newestDue:  tasks - tasks/2, // the other tasks, whose due lease is the newest
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return mix
}

// dueRead is one Now read, 48 hours on, of a board of five tasks per initiative
// plus seeded tasks with their leases: how many statements it ran, what the
// seed held, and what the read left behind.
type dueRead struct {
	statements                                       int64
	mix                                              leaseMix
	expiredEvents, alerts, stillLive, ended, expired int
}

func readWithSeededLeases(t *testing.T, seeded int, alerts ...string) dueRead {
	t.Helper()
	a, count, advance := boardOfSize(t, 5, alerts...)
	var r dueRead
	r.mix = seedLeases(t, a.Store, seeded)
	advance(48 * time.Hour)
	before := count.Load()
	if _, err := a.Board(context.Background(), attention.Query{}, attention.Window24h); err != nil {
		t.Fatal(err)
	}
	r.statements = count.Load() - before
	if err := a.Store.Reader().QueryRowContext(context.Background(), `SELECT
		(SELECT COUNT(*) FROM events WHERE type = 'lease_expired'), (SELECT COUNT(*) FROM notifications WHERE kind = 'stranded'),
		(SELECT COUNT(*) FROM leases WHERE id LIKE 'seeded-%' AND state = 'live'), (SELECT COUNT(*) FROM leases WHERE id LIKE 'seeded-%' AND state = 'ended'),
		(SELECT COUNT(*) FROM leases WHERE id LIKE 'seeded-%' AND state = 'expired')`).
		Scan(&r.expiredEvents, &r.alerts, &r.stillLive, &r.ended, &r.expired); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTheNowReadRunsTheSameNumberOfStatementsPastAThousandDueLeases(t *testing.T) {
	for name, alerts := range map[string][]string{"alerts off": nil, "stranded alerts on": {store.NotifyStranded}} {
		// every kind of lease, live and due, ended, live and not yet due, is
		// on both boards
		small := readWithSeededLeases(t, 20, alerts...)
		// 2100 seeded tasks hold 2625 due leases, 1050 of them the newest on
		// their task, and 1050 live leases not due: past a thousand rows of
		// events, of alerts and of live leases in flight (and their old chunks
		// of 1000 rows and 500 leases)
		large := readWithSeededLeases(t, 2100, alerts...)
		if large.mix.due <= 2000 || large.mix.newestDue <= 1000 || large.mix.liveNotDue <= 1000 {
			t.Fatalf("%s: the large seed holds %+v: it crosses no thousand", name, large.mix)
		}
		if small.statements != large.statements {
			t.Errorf("%s: %d statements with %d due leases but %d with %d: the read is not constant",
				name, small.statements, small.mix.due, large.statements, large.mix.due)
		}
		for _, r := range []dueRead{small, large} {
			// the board of five tasks per initiative adds its own due leases:
			// two thirds of ten tasks were leased and half of those ended
			const boardDue = 4
			wantAlerts := 0
			if alerts != nil {
				wantAlerts = r.mix.newestDue + boardDue
			}
			if r.expired != r.mix.due || r.expiredEvents != r.mix.due+boardDue || r.alerts != wantAlerts ||
				r.stillLive != r.mix.liveNotDue || r.ended != r.mix.ended {
				t.Errorf("%s: seeded %+v; read left %d expired with %d events and %d alerts, %d still live, %d ended",
					name, r.mix, r.expired, r.expiredEvents, r.alerts, r.stillLive, r.ended)
			}
		}
	}
}

func TestTheNowReadAnswersABoardPastEveryBoundParameterLimit(t *testing.T) {
	// 33000 seeded tasks are all on the board, past SQLite's 32766 bound
	// parameters per statement, which a list of one parameter per task cannot
	// pass
	small := readWithSeededLeases(t, 20)
	large := readWithSeededLeases(t, 33000)
	if small.statements != large.statements {
		t.Errorf("%d statements with %d due leases but %d with %d: the read is not constant",
			small.statements, small.mix.due, large.statements, large.mix.due)
	}
	if large.expired != large.mix.due || large.stillLive != large.mix.liveNotDue {
		t.Errorf("seeded %+v; the read left %d expired and %d live", large.mix, large.expired, large.stillLive)
	}
}
