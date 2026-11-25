package audit

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/n-orlov/village-hall/internal/store"
)

type originContextKey struct{}

// originState is what WithOrigin puts on a request's context: the handle of the
// operator behind the request and, once known, the id of that operator's row as
// the transaction tx saw it. The id is only trusted for the transaction that
// resolved it (a rolled-back one may have created the row), so a request that
// writes in one transaction resolves its origin once, however many events that
// transaction appends.
type originState struct {
	handle string

	mu sync.Mutex
	tx *sql.Tx
	id any // the operator row's id, or nil when the handle names no operator
}

// remember records the id of the operator the request names, as tx saw it.
func (s *originState) remember(tx *sql.Tx, id any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tx, s.id = tx, id
}

// forget drops what remember recorded.
func (s *originState) forget() { s.remember(nil, nil) }

// known returns the id remember recorded for tx, and whether it recorded one.
func (s *originState) known(tx *sql.Tx) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id, s.tx != nil && s.tx == tx
}

func stateFrom(ctx context.Context) *originState {
	s, _ := ctx.Value(originContextKey{}).(*originState)
	return s
}

// WithOrigin returns ctx carrying the handle of the operator behind the request
// (SPEC 3.1): a session's own operator, or the owner of the key it came through.
// An empty handle says the request has no known operator, so what it writes
// carries a null origin. A ctx that never passed through WithOrigin belongs to
// no request (a lease expiring on its own, say).
func WithOrigin(ctx context.Context, handle string) context.Context {
	return context.WithValue(ctx, originContextKey{}, &originState{handle: handle})
}

// OriginFrom returns the handle WithOrigin recorded, and whether it recorded one.
func OriginFrom(ctx context.Context) (string, bool) {
	s := stateFrom(ctx)
	if s == nil {
		return "", false
	}
	return s.handle, true
}

// OperatorID returns the id of the operator actor with the handle, or "" when
// no operator has it.
func OperatorID(ctx context.Context, tx *sql.Tx, d store.Dialect, handle string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, d.Rebind(`SELECT id FROM actors WHERE handle = ? AND kind = 'operator'`), handle).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// eventOrigin is the origin column of an event the actor row actorID writes: the
// operator the request carries, or null when it carries none. A write outside
// any request takes the actor's own: an operator row is its own origin, an
// agent row's origin is the one its latest write left.
func eventOrigin(ctx context.Context, tx *sql.Tx, d store.Dialect, actorID string) (any, error) {
	if origin, ok, err := requestOrigin(ctx, tx, d); ok || err != nil {
		return origin, err
	}
	var origin sql.NullString
	err := tx.QueryRowContext(ctx, d.Rebind(`SELECT CASE WHEN kind = 'operator' THEN id ELSE origin END FROM actors WHERE id = ?`), actorID).Scan(&origin)
	if err != nil || !origin.Valid {
		return nil, err
	}
	return origin.String, nil
}

func idOrNil(id string, err error) (any, error) {
	if err != nil || id == "" {
		return nil, err
	}
	return id, nil
}

// requestOrigin is the origin the request carries, as the id of the operator it
// names; fromRequest is false for a write outside any request. The operator is
// looked up once per transaction: the actor upsert of an agent row already
// resolved it, and a lookup here is remembered for the events that follow.
func requestOrigin(ctx context.Context, tx *sql.Tx, d store.Dialect) (origin any, fromRequest bool, err error) {
	st := stateFrom(ctx)
	if st == nil {
		return nil, false, nil
	}
	if st.handle == "" {
		return nil, true, nil
	}
	if id, ok := st.known(tx); ok {
		return id, true, nil
	}
	origin, err = idOrNil(OperatorID(ctx, tx, d, st.handle))
	if err != nil {
		return nil, true, err
	}
	st.remember(tx, origin)
	return origin, true, nil
}
