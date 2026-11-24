package features

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// TestTwoStoresOnOnePostgresExpireALeaseOnce is the two-server case of SPEC
// 5.1: two stores open on one database, standing in for two board servers,
// run the expiry at the same moment, and the conditional update lets exactly
// one of them mark the lease expired and write its lease_expired event.
func TestTwoStoresOnOnePostgresExpireALeaseOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	url := emptyDatabaseOn(ctx, t)
	open := func() *store.Store {
		st, err := store.Open(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	a, b := open(), open()
	if err := a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	later := started.Add(time.Hour)
	actor := audit.Actor{Handle: "claude@laptop"}
	if _, err := (initiatives.Store{Store: a}).Create(ctx, actor, "key:x", initiatives.New{Slug: "aa", Title: "First"}); err != nil {
		t.Fatal(err)
	}
	const rounds = 5
	ttl := int64(60)
	for i := 1; i <= rounds; i++ {
		if _, err := (tasks.Store{Store: a}).Create(ctx, actor, "key:x", tasks.New{Initiative: "aa", Title: fmt.Sprint("task ", i)}); err != nil {
			t.Fatal(err)
		}
		if _, err := (leases.Store{Store: a}).Start(ctx, actor, "key:x", leases.Start{Task: fmt.Sprintf("aa-%d", i), Intent: "work", TTL: &ttl}); err != nil {
			t.Fatal(err)
		}
	}
	// Both servers' clocks say the hour has passed, so all five leases are due.
	servers := []leases.Store{{Store: a, Now: func() time.Time { return later }}, {Store: b, Now: func() time.Time { return later }}}
	var wg sync.WaitGroup
	start := make(chan struct{})
	expired := make([]int, len(servers))
	errs := make([]error, len(servers))
	for i := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			expired[i], errs[i] = servers[i].ExpireDue(ctx)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
	}
	if got := expired[0] + expired[1]; got != rounds {
		t.Errorf("the two servers expired %d + %d leases, want %d between them", expired[0], expired[1], rounds)
	}
	rows, err := a.Reader().QueryContext(ctx, `SELECT l.n, count(e.id) FROM leases l LEFT JOIN events e ON e.ref_id = l.id AND e.type = 'lease_expired' GROUP BY l.n ORDER BY l.n`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var n, events int
		if err := rows.Scan(&n, &events); err != nil {
			t.Fatal(err)
		}
		if events != 1 {
			t.Errorf("lease L%d has %d lease_expired events, want exactly 1", n, events)
		}
		seen++
	}
	if err := rows.Err(); err != nil || seen != rounds {
		t.Fatalf("read %d leases (%v), want %d", seen, err, rounds)
	}
}
