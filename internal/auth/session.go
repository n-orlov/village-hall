package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/n-orlov/village-hall/internal/oidc"
	"github.com/n-orlov/village-hall/internal/store"
)

// Session lifetimes when none is configured (SPEC 3.2).
const (
	DefaultSessionTTL  = 168 * time.Hour
	DefaultSessionIdle = 24 * time.Hour
)

// ErrNoSession is returned for a session ID no live row holds: unknown,
// logged out, or past its absolute or idle expiry.
var ErrNoSession = errors.New("no such session")

// Session is one live browser session. It never holds the session ID.
type Session struct {
	Email     string
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Sessions reads and writes the sessions table of one store. The table keeps
// only the SHA-256 hash of a session ID, so a database read cannot be replayed
// as a cookie.
type Sessions struct {
	Store *store.Store
	// TTL is the absolute lifetime and Idle the idle timeout; zero means the
	// defaults.
	TTL, Idle time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s Sessions) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Create opens a session for email and returns its ID, which is shown once and
// stored nowhere, with the session.
func (s Sessions) Create(ctx context.Context, email string) (string, Session, error) {
	id, err := oidc.RandomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := oidc.RandomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	now := s.now()
	sess := Session{Email: email, CSRFToken: csrf, CreatedAt: now, ExpiresAt: now.Add(orDefault(s.TTL, DefaultSessionTTL))}
	d := s.Store.Dialect()
	cols := []string{"id_hash", "email", "csrf_token", "created_at", "last_seen_at", "expires_at"}
	_, err = s.Store.Writer().ExecContext(ctx, d.Rebind(d.Insert("sessions", cols)),
		HashKey(id), email, csrf, store.EncodeTime(now), store.EncodeTime(now), store.EncodeTime(sess.ExpiresAt))
	if err != nil {
		return "", Session{}, fmt.Errorf("create session: %w", err)
	}
	return id, sess, nil
}

// Lookup returns the live session id names and records that it was just used.
// A session past its absolute expiry, or idle for longer than the idle
// timeout, is deleted and reported as ErrNoSession. When ctx came from
// WithDeferredUsage, the last-seen write and the deletion wait for
// Usage.Commit, so a request that is refused changes no row.
func (s Sessions) Lookup(ctx context.Context, id string) (Session, error) {
	d := s.Store.Dialect()
	var sess Session
	var created, seen, expires int64
	err := s.Store.Reader().QueryRowContext(ctx,
		d.Rebind(`SELECT email, csrf_token, created_at, last_seen_at, expires_at FROM sessions WHERE id_hash = ?`), HashKey(id)).
		Scan(&sess.Email, &sess.CSRFToken, &created, &seen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("look up session: %w", err)
	}
	sess.CreatedAt, sess.ExpiresAt = store.DecodeTime(created), store.DecodeTime(expires)
	now, idle := s.now(), orDefault(s.Idle, DefaultSessionIdle)
	if !now.Before(sess.ExpiresAt) || !now.Before(store.DecodeTime(seen).Add(idle)) {
		later(ctx, func(ctx context.Context) { _ = s.Delete(ctx, id) })
		return Session{}, ErrNoSession
	}
	if now.Sub(store.DecodeTime(seen)) >= idle/10 {
		later(ctx, func(ctx context.Context) { s.touch(ctx, id, now) })
	}
	return sess, nil
}

// touch records that the session id names was used at now. A failed write is
// not a failed request: the next use tries again.
func (s Sessions) touch(ctx context.Context, id string, now time.Time) {
	d := s.Store.Dialect()
	_, _ = s.Store.Writer().ExecContext(ctx,
		d.Rebind(`UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`), store.EncodeTime(now), HashKey(id))
}

// Delete ends the session id names. Ending one that does not exist is not an
// error.
func (s Sessions) Delete(ctx context.Context, id string) error {
	d := s.Store.Dialect()
	_, err := s.Store.Writer().ExecContext(ctx, d.Rebind(`DELETE FROM sessions WHERE id_hash = ?`), HashKey(id))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpired removes every session past its absolute expiry and returns how
// many it removed. A reader probe finds out whether any is due, and only then
// is the writer taken. A session only the idle timeout has ended is removed when it
// is next looked up (Lookup) by a request that is not refused, since the idle
// clock is not indexed.
func (s Sessions) DeleteExpired(ctx context.Context) (int, error) {
	d := s.Store.Dialect()
	cutoff := store.EncodeTime(s.now())
	// A reader probe first: with nothing due, the sweep never takes the writer.
	var due int
	switch err := s.Store.Reader().QueryRowContext(ctx, d.Rebind(`SELECT 1 FROM sessions WHERE expires_at <= ? LIMIT 1`), cutoff).Scan(&due); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	res, err := s.Store.Writer().ExecContext(ctx, d.Rebind(`DELETE FROM sessions WHERE expires_at <= ?`), cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}
