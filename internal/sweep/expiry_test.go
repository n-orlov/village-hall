package sweep_test

import (
	"context"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
	"github.com/n-orlov/village-hall/internal/sweep"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// expiryEnv is a recorded, migrated store with the two expiry jobs the serve
// sweeper runs, on a clock the test moves.
type expiryEnv struct {
	st       *store.Store
	rec      *storetest.Recorder
	now      time.Time
	sessions auth.Sessions
	idem     idempotency.Store
	jobs     []sweep.Job
}

func newExpiryEnv(t *testing.T) *expiryEnv {
	t.Helper()
	st, rec := storetest.OpenMigrated(t)
	e := &expiryEnv{st: st, rec: rec, now: t0}
	clock := func() time.Time { return e.now }
	e.sessions = auth.Sessions{Store: st, TTL: time.Hour, Idle: time.Hour, Now: clock}
	e.idem = idempotency.Store{Store: st, Now: clock}
	e.jobs = []sweep.Job{
		func(ctx context.Context) error { _, err := e.sessions.DeleteExpired(ctx); return err },
		func(ctx context.Context) error { _, err := e.idem.DeleteExpired(ctx); return err },
	}
	return e
}

func (e *expiryEnv) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.st.Reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *expiryEnv) writerTransactions() int {
	return e.rec.Count(store.PoolWriter, storetest.Begin) + e.rec.Statements(store.PoolWriter)
}

func (e *expiryEnv) sweepOnce(t *testing.T) {
	t.Helper()
	sweep.Once(context.Background(), func(err error) { t.Errorf("sweep: %v", err) }, e.jobs...)
}

func TestASweepWithNothingExpiredOpensNoWriterTransaction(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	if _, _, err := e.sessions.Create(ctx, "a@example.com"); err != nil {
		t.Fatal(err)
	}
	r := idempotency.Request{Principal: "p", Key: "k", Fingerprint: "f"}
	if _, _, err := e.idem.Claim(ctx, r); err != nil {
		t.Fatal(err)
	}
	e.rec.Reset()
	e.sweepOnce(t)
	if n := e.writerTransactions(); n != 0 {
		t.Fatalf("a sweep with nothing expired made %d writer records, want 0: %+v", n, e.rec.All())
	}
	if n := e.rec.Count(store.PoolReader, storetest.Query); n != 2 {
		t.Errorf("reader probes = %d, want one per job (2): %+v", n, e.rec.All())
	}
	if s, k := e.count(t, "sessions"), e.count(t, "idempotency_keys"); s != 1 || k != 1 {
		t.Errorf("rows left: %d sessions, %d keys, want 1 and 1", s, k)
	}
}

func TestASweepDeletesAnExpiredSessionThroughTheWriter(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	if _, _, err := e.sessions.Create(ctx, "a@example.com"); err != nil {
		t.Fatal(err)
	}
	e.now = t0.Add(time.Hour + time.Minute)
	e.rec.Reset()
	e.sweepOnce(t)
	if n := e.count(t, "sessions"); n != 0 {
		t.Fatalf("%d sessions left after the sweep, want 0", n)
	}
	if n := e.rec.Count(store.PoolWriter, storetest.Exec); n != 1 {
		t.Errorf("writer deletes = %d, want 1: %+v", n, e.rec.All())
	}
}

func TestASweepDeletesAnExpiredIdempotencyKeyThroughTheWriter(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	r := idempotency.Request{Principal: "p", Key: "k", Fingerprint: "f"}
	if _, _, err := e.idem.Claim(ctx, r); err != nil {
		t.Fatal(err)
	}
	e.now = t0.Add(idempotency.Retention + time.Millisecond)
	e.rec.Reset()
	e.sweepOnce(t)
	if n := e.count(t, "idempotency_keys"); n != 0 {
		t.Fatalf("%d keys left after the sweep, want 0", n)
	}
	if n := e.rec.Count(store.PoolWriter, storetest.Exec); n != 1 {
		t.Errorf("writer deletes = %d, want 1: %+v", n, e.rec.All())
	}
}
