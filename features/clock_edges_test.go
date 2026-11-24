package features

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// clockEdge is one clock offset from a stored instant (a lease's expires_at and
// a watch task's next_check_at, both stored in whole milliseconds) and whether
// the strict earlier-than-the-clock rule says that instant has passed.
type clockEdge struct {
	name   string
	offset time.Duration
	passed bool
}

var clockEdges = []clockEdge{
	{"a millisecond before", -time.Millisecond, false},
	{"equal", 0, false},
	{"one nanosecond after", time.Nanosecond, true},
	{"half a millisecond after", 500 * time.Microsecond, true},
	{"just under the next millisecond", time.Millisecond - time.Nanosecond, true},
	{"the next millisecond", time.Millisecond, true},
}

// TestExpiryAndOverdueAgreeAtTheClockEdges runs the SPEC 5.1 and 5.2 rule (an
// instant has passed only when it is strictly earlier than the clock) on both
// databases with an injected clock: the stored-row SQL predicates, the derived
// task fields and the lease reads must all give the same answer at every
// offset, and equality must not expire a lease or make a watch task overdue.
func TestExpiryAndOverdueAgreeAtTheClockEdges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Run("sqlite", func(t *testing.T) {
		st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
		if err != nil {
			t.Fatal(err)
		}
		checkClockEdges(ctx, t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		st, err := store.Open(emptyDatabaseOn(ctx, t))
		if err != nil {
			t.Fatal(err)
		}
		checkClockEdges(ctx, t, st)
	})
}

func checkClockEdges(ctx context.Context, t *testing.T, st *store.Store) {
	t.Helper()
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	actor := audit.Actor{Handle: "claude@laptop"}
	ls := leases.Store{Store: st, Now: clock}
	ts := tasks.Store{Store: st, Now: clock}
	for i, edge := range clockEdges {
		slug := fmt.Sprintf("e%d", i)
		t.Run(edge.name, func(t *testing.T) {
			now = time.Date(2026, 11, 5, 12+i, 0, 0, 0, time.UTC)
			if _, err := (initiatives.Store{Store: st, Now: clock}).Create(ctx, actor, "key:x", initiatives.New{Slug: slug, Title: "I"}); err != nil {
				t.Fatal(err)
			}
			work, err := ts.Create(ctx, actor, "key:x", tasks.New{Initiative: slug, Title: "work"})
			if err != nil {
				t.Fatal(err)
			}
			ttl := int64(60)
			res, err := ls.Start(ctx, actor, "key:x", leases.Start{Task: work.Ref, Intent: "edge", TTL: &ttl})
			if err != nil {
				t.Fatal(err)
			}
			due := res.Lease.ExpiresAt
			cadence := int64(60)
			watch, err := ts.Create(ctx, actor, "key:x", tasks.New{Initiative: slug, Title: "watch", Kind: "watch", CadenceSeconds: &cadence, NextCheckAt: &due})
			if err != nil {
				t.Fatal(err)
			}
			if !watch.NextCheckAt.Equal(due) {
				t.Fatalf("stored instants differ: lease %v, watch %v", due, watch.NextCheckAt)
			}
			now = due.Add(edge.offset)
			edgeAssertions(ctx, t, ls, ts, slug, work, watch, res.Lease, edge)
		})
	}
}

func tick(v bool) *bool { return &v }

func edgeAssertions(ctx context.Context, t *testing.T, ls leases.Store, ts tasks.Store, slug string, work, watch tasks.Task, lease leases.Lease, edge clockEdge) {
	t.Helper()
	// The SQL predicates and the derived fields, before anything expires a lease.
	for _, active := range []bool{true, false} {
		page, err := ts.List(ctx, tasks.Query{Initiatives: []string{slug}, Active: tick(active)})
		if err != nil {
			t.Fatal(err)
		}
		if got := hasRef(page, work.Ref); got != (active != edge.passed) {
			t.Errorf("active=%v lists %s = %v, passed = %v", active, work.Ref, got, edge.passed)
		}
	}
	for _, overdue := range []bool{true, false} {
		page, err := ts.List(ctx, tasks.Query{Initiatives: []string{slug}, Overdue: tick(overdue)})
		if err != nil {
			t.Fatal(err)
		}
		if got := hasRef(page, watch.Ref); got != (overdue == edge.passed) {
			t.Errorf("overdue=%v lists %s = %v, passed = %v", overdue, watch.Ref, got, edge.passed)
		}
		for _, it := range page.Items {
			if it.Overdue != overdue {
				t.Errorf("overdue=%v selected %s with overdue = %v", overdue, it.Ref, it.Overdue)
			}
		}
	}
	if got, err := ts.Get(ctx, watch.Ref); err != nil || got.Overdue != edge.passed {
		t.Errorf("Get %s overdue = %v, %v, want %v", watch.Ref, got.Overdue, err, edge.passed)
	}
	if live, err := ls.Live(ctx); err != nil || (len(live) == 1) == edge.passed || len(live) > 1 {
		t.Errorf("Live before any expiry = %d leases, %v, passed = %v", len(live), err, edge.passed)
	}
	if got, err := ls.Get(ctx, lease.Ref); err != nil || got.Expired(ls.Now()) != edge.passed {
		t.Errorf("Expired(clock) = %v, %v, want %v", got.Expired(ls.Now()), err, edge.passed)
	}
	// The lease reads expire a due lease, with exactly one event, before answering.
	if in, err := ls.CheckIn(ctx, "claude@laptop", nil); err != nil || (len(in.Mine) == 1) == edge.passed {
		t.Errorf("CheckIn lists %d leases, %v, passed = %v", len(in.Mine), err, edge.passed)
	}
	if got, err := ls.LiveOf(ctx, work.ID); err != nil || (len(got) == 1) == edge.passed {
		t.Errorf("LiveOf = %d leases, %v, passed = %v", len(got), err, edge.passed)
	}
	want, state := 0, "live"
	if edge.passed {
		want, state = 1, "expired"
	}
	if got, err := ls.Current(ctx, lease.Ref); err != nil || got.State != state {
		t.Errorf("Current state = %q, %v, want %q", got.State, err, state)
	}
	if n, err := ls.ExpireDue(ctx); err != nil || n != 0 {
		t.Errorf("a second ExpireDue expired %d, %v, want 0", n, err)
	}
	var events int
	row := ls.Store.Reader().QueryRowContext(ctx, ls.Store.Dialect().Rebind(`SELECT count(*) FROM events WHERE type = 'lease_expired' AND ref_id = ?`), lease.ID)
	if err := row.Scan(&events); err != nil || events != want {
		t.Errorf("lease_expired events = %d, %v, want %d", events, err, want)
	}
}

func hasRef(p tasks.Page, ref string) bool {
	for _, it := range p.Items {
		if it.Ref == ref {
			return true
		}
	}
	return false
}
