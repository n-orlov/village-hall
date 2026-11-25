package idempotency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
)

// Retention is how long a key is remembered. A key older than this counts as
// new, and DeleteExpired removes its row.
const Retention = 24 * time.Hour

// Outcome is what Claim found.
type Outcome int

// The four things a key can be.
const (
	// Claimed: the key is new, now held by the caller, who must Complete it
	// (after a Bind, when it has writes to commit) or Release it.
	Claimed Outcome = iota
	// Replay: the same request already has its answer; Claim returned it.
	Replay
	// Mismatch: the key came with a different request.
	Mismatch
	// InFlight: the first request with the key has not been answered yet.
	InFlight
)

// Request names one use of a key: who sent it, the key, and the fingerprint of
// the request it came with.
type Request struct {
	Principal   string
	Key         string
	Fingerprint string
}

// Answer is the response stored for a key.
type Answer struct {
	Status      int
	ContentType string
	Body        []byte
}

// Store reads and writes the idempotency rows of one database.
type Store struct {
	Store *store.Store
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// cutoff is the stored time before which a row is older than Retention.
func (s Store) cutoff() int64 { return store.EncodeBound(s.now().Add(-Retention)) }

// Fingerprint identifies a request: its method, target (path and query), actor
// and body.
func Fingerprint(method, target, actor string, body []byte) string {
	h := sha256.New()
	for _, part := range []string{method, target, actor} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// hash is the stored form of a key: the key itself is never written.
func hash(principal, key string) string {
	sum := sha256.Sum256([]byte("vh-idempotency-key\x00" + principal + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

// Claim takes the key for r when it is new (a key older than Retention is new:
// its row is replaced). For a key already held it returns Mismatch when the
// request differs, Replay with the stored answer when the first request is
// answered, and InFlight when it is not.
//
// A key already held is judged on the readers first, so a duplicate of a
// request still running is answered at once even on SQLite, where the running
// request's unit of work holds the one writer connection until it commits.
func (s Store) Claim(ctx context.Context, r Request) (Outcome, Answer, error) {
	id := hash(r.Principal, r.Key)
	if outcome, answer, found, err := s.judge(ctx, s.Store.ReaderFor(ctx), r, id); err != nil || found {
		return outcome, answer, err
	}
	db, d := s.Store.WriterFor(ctx), s.Store.Dialect()
	if _, err := db.ExecContext(ctx, d.Rebind(`DELETE FROM idempotency_keys WHERE principal = ? AND key_hash = ? AND created_at < ?`),
		r.Principal, id, s.cutoff()); err != nil {
		return 0, Answer{}, err
	}
	res, err := db.ExecContext(ctx, d.Rebind(`INSERT INTO idempotency_keys (principal, key_hash, fingerprint, state, created_at)
		VALUES (?, ?, ?, 'running', ?) ON CONFLICT (principal, key_hash) DO NOTHING`),
		r.Principal, id, r.Fingerprint, store.EncodeTime(s.now()))
	if err != nil {
		return 0, Answer{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return Claimed, Answer{}, err
	}
	// Released between the insert and this read, the first request is gone:
	// still in flight as far as this one can tell.
	outcome, answer, _, err := s.judge(ctx, db, r, id)
	return outcome, answer, err
}

// judge reads the row of a key that is not older than Retention through q and
// judges r by it; found is false when there is no such row, and the outcome is
// then InFlight.
func (s Store) judge(ctx context.Context, q store.Querier, r Request, id string) (outcome Outcome, answer Answer, found bool, err error) {
	var (
		fingerprint, state string
		status             sql.NullInt64
		contentType, body  sql.NullString
	)
	err = q.QueryRowContext(ctx, s.Store.Dialect().Rebind(
		`SELECT fingerprint, state, status, content_type, body FROM idempotency_keys WHERE principal = ? AND key_hash = ? AND created_at >= ?`),
		r.Principal, id, s.cutoff()).Scan(&fingerprint, &state, &status, &contentType, &body)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return InFlight, Answer{}, false, nil
	case err != nil:
		return 0, Answer{}, false, err
	case fingerprint != r.Fingerprint:
		return Mismatch, Answer{}, true, nil
	case state != "done" || !status.Valid:
		// Running, or bound with its writes committed and its answer not yet
		// stored: not answered yet.
		return InFlight, Answer{}, true, nil
	}
	plain, err := open(r, body.String)
	if err != nil {
		return 0, Answer{}, false, err
	}
	return Replay, Answer{Status: int(status.Int64), ContentType: contentType.String, Body: plain}, true, nil
}

// ErrNotHeld is what Bind and Complete return for a key the caller does not
// hold: its row is gone or already answered, so nothing was stored.
var ErrNotHeld = errors.New("the idempotency key is not held")

// Bind marks the running key done, with its answer still to come, in the unit
// of work that holds the request's writes (SPEC 6.1): it is the statement that
// commits with them, so the writes never commit while the key can still be
// released and run again, and a key whose answer cannot be stored fails here,
// before anything commits. A bound key is in flight to Claim until Complete
// stores its answer, and Release leaves it alone. It returns ErrNotHeld when
// there is no running row to bind.
func (s Store) Bind(ctx context.Context, r Request) error {
	res, err := s.Store.WriterFor(ctx).ExecContext(ctx, s.Store.Dialect().Rebind(
		`UPDATE idempotency_keys SET state = 'done' WHERE principal = ? AND key_hash = ? AND state = 'running'`),
		r.Principal, hash(r.Principal, r.Key))
	return held(res, err)
}

// Complete stores the answer to the request that holds the key, running or
// bound, and seals it: the key then replays a. It returns ErrNotHeld when there
// is no row without an answer to store it in.
func (s Store) Complete(ctx context.Context, r Request, a Answer) error {
	sealed, err := seal(r, a.Body)
	if err != nil {
		return err
	}
	res, err := s.Store.WriterFor(ctx).ExecContext(ctx, s.Store.Dialect().Rebind(
		`UPDATE idempotency_keys SET state = 'done', status = ?, content_type = ?, body = ?
		 WHERE principal = ? AND key_hash = ? AND status IS NULL`),
		a.Status, a.ContentType, sealed, r.Principal, hash(r.Principal, r.Key))
	return held(res, err)
}

// held is the error of an update of one key's row: ErrNotHeld when it changed
// no row.
func held(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotHeld
	}
	return nil
}

// Release gives the key up, for a request that failed before it answered: the
// same key then starts again.
func (s Store) Release(ctx context.Context, r Request) error {
	_, err := s.Store.WriterFor(ctx).ExecContext(ctx, s.Store.Dialect().Rebind(
		`DELETE FROM idempotency_keys WHERE principal = ? AND key_hash = ? AND state = 'running'`),
		r.Principal, hash(r.Principal, r.Key))
	return err
}

// DeleteExpired removes the rows older than Retention and returns how many. A
// reader probe finds out whether any is due, and only then is the writer taken.
func (s Store) DeleteExpired(ctx context.Context) (int64, error) {
	cutoff := s.cutoff()
	// A reader probe first: with nothing due, the sweep never takes the writer.
	var due int
	probe := s.Store.Dialect().Rebind(`SELECT 1 FROM idempotency_keys WHERE created_at < ? LIMIT 1`)
	switch err := s.Store.ReaderFor(ctx).QueryRowContext(ctx, probe, cutoff).Scan(&due); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, err
	}
	res, err := s.Store.WriterFor(ctx).ExecContext(ctx, s.Store.Dialect().Rebind(`DELETE FROM idempotency_keys WHERE created_at < ?`), cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
