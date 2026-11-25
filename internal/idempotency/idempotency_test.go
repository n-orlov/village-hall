package idempotency

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
)

// env is a migrated SQLite database, the idempotency store on it and the clock
// the store reads.
type env struct {
	Store
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{now: t0}
	e.Store = Store{Store: st, Now: func() time.Time { return e.now }}
	return e
}

func (e *env) rows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.Store.Store.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_keys`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func claim(t *testing.T, e *env, r Request) (Outcome, Answer) {
	t.Helper()
	o, a, err := e.Claim(ctx, r)
	if err != nil {
		t.Fatalf("claim %+v: %v", r, err)
	}
	return o, a
}

var req = Request{Principal: "key:caller", Key: "k-1", Fingerprint: "f1"}

func TestFirstClaimThenInFlightThenReplay(t *testing.T) {
	e := newEnv(t)
	if o, _ := claim(t, e, req); o != Claimed {
		t.Fatalf("first claim = %v, want Claimed", o)
	}
	if o, _ := claim(t, e, req); o != InFlight {
		t.Fatalf("second claim while running = %v, want InFlight", o)
	}
	want := Answer{Status: 201, ContentType: "application/json", Body: []byte(`{"key":"brd_secret"}`)}
	if err := e.Complete(ctx, req, want); err != nil {
		t.Fatal(err)
	}
	o, got := claim(t, e, req)
	if o != Replay || got.Status != want.Status || got.ContentType != want.ContentType || !bytes.Equal(got.Body, want.Body) {
		t.Fatalf("claim after complete = %v %+v, want Replay of %+v", o, got, want)
	}
	if e.rows(t) != 1 {
		t.Errorf("rows = %d, want 1", e.rows(t))
	}
}

func TestADifferentRequestWithTheKeyIsAMismatch(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	other := req
	other.Fingerprint = "f2"
	if o, _ := claim(t, e, other); o != Mismatch {
		t.Errorf("a different request while running = %v, want Mismatch", o)
	}
	if err := e.Complete(ctx, req, Answer{Status: 200}); err != nil {
		t.Fatal(err)
	}
	if o, _ := claim(t, e, other); o != Mismatch {
		t.Errorf("a different request after the answer = %v, want Mismatch", o)
	}
}

func TestAnotherPrincipalWithTheSameKeyIsNew(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	other := req
	other.Principal = "key:other"
	if o, _ := claim(t, e, other); o != Claimed {
		t.Errorf("same key, other principal = %v, want Claimed", o)
	}
	if e.rows(t) != 2 {
		t.Errorf("rows = %d, want 2", e.rows(t))
	}
}

func TestReleaseLetsTheKeyStartAgainButNeverTakesAnAnswerBack(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	if err := e.Release(ctx, req); err != nil {
		t.Fatal(err)
	}
	if o, _ := claim(t, e, req); o != Claimed {
		t.Fatalf("claim after release = %v, want Claimed", o)
	}
	if err := e.Complete(ctx, req, Answer{Status: 200, Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := e.Release(ctx, req); err != nil {
		t.Fatal(err)
	}
	if o, _ := claim(t, e, req); o != Replay {
		t.Errorf("claim after release of an answered key = %v, want Replay", o)
	}
}

func TestAKeyOlderThanADayIsNewAndTheSweeperPurgesIt(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	if err := e.Complete(ctx, req, Answer{Status: 200, Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	e.now = t0.Add(Retention)
	if o, _ := claim(t, e, req); o != Replay {
		t.Fatalf("claim at exactly 24 h = %v, want Replay", o)
	}
	if n, err := e.DeleteExpired(ctx); err != nil || n != 0 {
		t.Fatalf("DeleteExpired at exactly 24 h = %d, %v, want 0", n, err)
	}
	e.now = t0.Add(Retention + time.Millisecond)
	if n, err := e.DeleteExpired(ctx); err != nil || n != 1 || e.rows(t) != 0 {
		t.Fatalf("DeleteExpired past 24 h = %d, %v with %d rows left, want 1 and none", n, err, e.rows(t))
	}

	if o, _ := claim(t, e, req); o != Claimed {
		t.Fatalf("claim after the purge = %v, want Claimed", o)
	}
	e.now = e.now.Add(Retention + time.Millisecond)
	if o, _ := claim(t, e, req); o != Claimed {
		t.Errorf("claim of an unswept key older than 24 h = %v, want Claimed", o)
	}
	if e.rows(t) != 1 {
		t.Errorf("rows = %d, want the one new row", e.rows(t))
	}
}

func TestTheStoredAnswerIsSealedUnderTheKey(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	secret := "brd_0123456789abcdef"
	if err := e.Complete(ctx, req, Answer{Status: 201, Body: []byte(`{"key":"` + secret + `"}`)}); err != nil {
		t.Fatal(err)
	}
	var key, principal, body string
	if err := e.Store.Store.Reader().QueryRowContext(ctx, `SELECT key_hash, principal, body FROM idempotency_keys`).Scan(&key, &principal, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, secret) || strings.Contains(key, req.Key) || key != hash(req.Principal, req.Key) || principal != req.Principal {
		t.Errorf("row = key_hash %q principal %q body %q: the key or the answer is readable", key, principal, body)
	}
}

func TestSealOpenRoundTripAndRefusals(t *testing.T) {
	sealed, err := seal(req, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	again, _ := seal(req, []byte("hello"))
	if sealed == again {
		t.Error("two seals of one body are equal: the nonce is not random")
	}
	if got, err := open(req, sealed); err != nil || string(got) != "hello" {
		t.Fatalf("open = %q, %v", got, err)
	}
	other := req
	other.Key = "k-2"
	for name, c := range map[string]struct {
		r      Request
		stored string
	}{
		"another key":    {other, sealed},
		"another user":   {Request{Principal: "key:x", Key: req.Key}, sealed},
		"not base64":     {req, "%%%"},
		"too short":      {req, "AAAA"},
		"empty":          {req, ""},
		"tampered":       {req, sealed[:len(sealed)-4] + "AAAA"},
		"truncated tail": {req, sealed[:len(sealed)-8]},
	} {
		if _, err := open(c.r, c.stored); err == nil {
			t.Errorf("open with %s succeeded", name)
		}
	}
}

func TestFingerprintSeparatesMethodTargetActorAndBody(t *testing.T) {
	base := Fingerprint("POST", "/api/v1/notes", "a", []byte("b"))
	if base != Fingerprint("POST", "/api/v1/notes", "a", []byte("b")) {
		t.Fatal("the fingerprint of one request varies")
	}
	for name, other := range map[string]string{
		"method": Fingerprint("PUT", "/api/v1/notes", "a", []byte("b")),
		"target": Fingerprint("POST", "/api/v1/notes?x=1", "a", []byte("b")),
		"actor":  Fingerprint("POST", "/api/v1/notes", "c", []byte("b")),
		"body":   Fingerprint("POST", "/api/v1/notes", "a", []byte("d")),
		"shift":  Fingerprint("POST", "/api/v1/notesa", "", []byte("b")),
	} {
		if other == base {
			t.Errorf("a different %s has the same fingerprint", name)
		}
	}
}

func TestCompleteOfAnUnclaimedKeyWritesNothing(t *testing.T) {
	e := newEnv(t)
	if err := e.Complete(ctx, req, Answer{Status: 200}); !errors.Is(err, ErrNotHeld) || e.rows(t) != 0 {
		t.Errorf("Complete without a claim = %v with %d rows, want ErrNotHeld and none", err, e.rows(t))
	}
}

func TestAKeyThatVanishesBetweenTheInsertAndTheReadIsInFlight(t *testing.T) {
	e := newEnv(t)
	if o, _, found, err := e.judge(ctx, e.Store.Store.Writer(), req, hash(req.Principal, req.Key)); err != nil || o != InFlight || found {
		t.Errorf("judge on a missing row = %v, found %v, %v, want InFlight, not found", o, found, err)
	}
}

func TestAnAnswerThatCannotBeOpenedIsAnError(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	if err := e.Complete(ctx, req, Answer{Status: 200, Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Store.Writer().ExecContext(ctx, `UPDATE idempotency_keys SET body = 'not sealed'`); err != nil {
		t.Fatal(err)
	}
	if o, _, err := e.Claim(ctx, req); err == nil {
		t.Errorf("claim over a corrupt body = %v, nil, want an error", o)
	}
}

func TestTheStoreReadsTheSystemClockWhenNoneIsSet(t *testing.T) {
	e := newEnv(t)
	s := Store{Store: e.Store.Store}
	if o, _, err := s.Claim(ctx, req); err != nil || o != Claimed {
		t.Fatalf("claim = %v, %v", o, err)
	}
	var created int64
	if err := s.Store.Reader().QueryRowContext(ctx, `SELECT created_at FROM idempotency_keys`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if age := time.Since(time.UnixMilli(created)); age < 0 || age > time.Minute {
		t.Errorf("created %v ago, want just now", age)
	}
	if n, err := s.DeleteExpired(ctx); err != nil || n != 0 {
		t.Errorf("DeleteExpired of a fresh row = %d, %v, want 0", n, err)
	}
}

func TestCompleteOfAnAnsweredKeyIsNotHeldAndKeepsTheFirstAnswer(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	first := Answer{Status: 201, ContentType: "application/json", Body: []byte(`{"n":1}`)}
	if err := e.Complete(ctx, req, first); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(ctx, req, Answer{Status: 200, Body: []byte(`{"n":2}`)}); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("second Complete = %v, want ErrNotHeld", err)
	}
	if o, got := claim(t, e, req); o != Replay || got.Status != 201 || !bytes.Equal(got.Body, first.Body) {
		t.Errorf("claim = %v %+v, want the first answer replayed", o, got)
	}
}

// exec runs a statement on the writer of e's database.
func (e *env) exec(t *testing.T, q string) {
	t.Helper()
	if _, err := e.Store.Store.Writer().ExecContext(ctx, q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestAKeyClaimedBetweenTheReadAndTheInsertIsInFlight(t *testing.T) {
	e := newEnv(t)
	// The trigger stands for another request that claims the key after this
	// one found no row on the readers and before its own insert.
	e.exec(t, `CREATE TRIGGER race BEFORE INSERT ON idempotency_keys
		WHEN NOT EXISTS (SELECT 1 FROM idempotency_keys WHERE key_hash = NEW.key_hash)
		BEGIN INSERT INTO idempotency_keys (principal, key_hash, fingerprint, state, created_at)
		VALUES (NEW.principal, NEW.key_hash, NEW.fingerprint, 'running', NEW.created_at); END`)
	if o, _ := claim(t, e, req); o != InFlight || e.rows(t) != 1 {
		t.Errorf("claim that lost the race = %v with %d rows, want InFlight and the other's row", o, e.rows(t))
	}
	other := req
	other.Fingerprint = "f2"
	if o, _ := claim(t, e, other); o != Mismatch {
		t.Errorf("a different request on the raced key = %v, want Mismatch", o)
	}
}

func TestAClaimWhoseStatementsFailIsAnError(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		e := newEnv(t)
		_ = e.Store.Store.Close()
		if o, _, err := e.Claim(ctx, req); err == nil {
			t.Errorf("claim on a closed store = %v, nil, want an error", o)
		}
	})
	t.Run("delete", func(t *testing.T) {
		e := newEnv(t)
		claim(t, e, req)
		e.now = t0.Add(Retention + time.Minute)
		e.exec(t, `CREATE TRIGGER nodelete BEFORE DELETE ON idempotency_keys BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		if o, _, err := e.Claim(ctx, req); err == nil {
			t.Errorf("claim whose expired row cannot be deleted = %v, nil, want an error", o)
		}
	})
	t.Run("insert", func(t *testing.T) {
		e := newEnv(t)
		e.exec(t, `CREATE TRIGGER noinsert BEFORE INSERT ON idempotency_keys BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		if o, _, err := e.Claim(ctx, req); err == nil || e.rows(t) != 0 {
			t.Errorf("claim whose insert fails = %v, %v, %d rows, want an error and none", o, err, e.rows(t))
		}
	})
}

func TestAnExpiredKeyIsNotJudgedOnTheReadersAndIsClaimedAfresh(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	e.now = t0.Add(Retention + time.Second)
	other := req
	other.Fingerprint = "f2"
	if o, _ := claim(t, e, other); o != Claimed || e.rows(t) != 1 {
		t.Errorf("claim of an expired key with another request = %v with %d rows, want Claimed over the old row", o, e.rows(t))
	}
}

func TestABoundKeyIsInFlightUntilCompleteStoresItsAnswerAndIsNeverReleased(t *testing.T) {
	e := newEnv(t)
	claim(t, e, req)
	if err := e.Bind(ctx, req); err != nil {
		t.Fatal(err)
	}
	if o, _ := claim(t, e, req); o != InFlight {
		t.Errorf("claim of a bound key without its answer = %v, want InFlight", o)
	}
	if err := e.Release(ctx, req); err != nil || e.rows(t) != 1 {
		t.Errorf("release of a bound key = %v, %d rows, want it kept", err, e.rows(t))
	}
	if err := e.Bind(ctx, req); !errors.Is(err, ErrNotHeld) {
		t.Errorf("a second bind = %v, want ErrNotHeld", err)
	}
	want := Answer{Status: 201, ContentType: "application/json", Body: []byte(`{"n":1}`)}
	if err := e.Complete(ctx, req, want); err != nil {
		t.Fatal(err)
	}
	if o, got := claim(t, e, req); o != Replay || got.Status != want.Status || !bytes.Equal(got.Body, want.Body) {
		t.Errorf("claim after the answer = %v %+v, want Replay of %+v", o, got, want)
	}
	if err := e.Complete(ctx, req, Answer{Status: 200}); !errors.Is(err, ErrNotHeld) {
		t.Errorf("a second complete = %v, want ErrNotHeld", err)
	}
}

func TestBindOfAnUnclaimedKeyWritesNothingAndAFailedBindIsAnError(t *testing.T) {
	e := newEnv(t)
	if err := e.Bind(ctx, req); !errors.Is(err, ErrNotHeld) || e.rows(t) != 0 {
		t.Errorf("bind of an unclaimed key = %v, %d rows, want ErrNotHeld and none", err, e.rows(t))
	}
	claim(t, e, req)
	e.exec(t, `CREATE TRIGGER no_bind BEFORE UPDATE ON idempotency_keys BEGIN SELECT RAISE(ABORT, 'no bind'); END`)
	if err := e.Bind(ctx, req); err == nil || errors.Is(err, ErrNotHeld) {
		t.Errorf("bind whose statement fails = %v, want the statement's error", err)
	}
}
