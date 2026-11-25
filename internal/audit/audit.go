package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
)

var (
	eventColumns = []string{"id", "at", "actor_id", "principal", "on_behalf_of", "initiative_id", "task_id", "type", "ref_id", "summary", "data", "origin"}
)

// Actor is who a write acts as: a handle, and whether it is the operator.
type Actor struct {
	Handle   string
	Operator bool
	// WayBack and Meta are the optional Board-Actor-Way-Back and Board-Actor-Meta
	// the request sent. A non-empty value replaces what the row holds; an empty
	// one leaves it. Meta is a JSON object.
	WayBack string
	Meta    string
	// OnBehalfOf is the handle of the actor the Board-On-Behalf-Of header names,
	// or empty. Only the operations that record it read it.
	OnBehalfOf string
}

// Event is one row of the append-only events table. InitiativeID, TaskID and
// RefID are empty when the event has none. Data holds the type-specific fields.
type Event struct {
	Principal string
	// OnBehalfOfID is the actors row the event is made on behalf of, or empty;
	// OnBehalf returns it.
	OnBehalfOfID string
	Type         string
	InitiativeID string
	TaskID       string
	RefID        string
	Summary      string
	Data         map[string]any
}

// UpsertActor makes sure the actor exists and returns its id. The row is
// created on first use of a handle and last_seen_at moves forward on every
// use. A write that acts as the operator (a browser session) makes its row the
// operator row, promoting an agent row an earlier key write created under the
// same handle; an operator row is never demoted, so a later key write with the
// operator's handle leaves it.
//
// An agent row also takes the origin the request carries (WithOrigin): the
// owner of the key behind its latest write, null when that key has none.
func UpsertActor(ctx context.Context, tx *sql.Tx, d store.Dialect, a Actor, now time.Time) (string, error) {
	var origin *string
	if h, ok := OriginFrom(ctx); ok {
		origin = &h
	}
	return upsertActor(ctx, tx, d, a, now, origin)
}

// upsertActor is UpsertActor with the origin handle to record on an agent row,
// or nil to leave the row's origin as it is. It is one statement,
// INSERT ... ON CONFLICT DO UPDATE ... RETURNING: it creates the row or moves
// last_seen_at, promotes an agent row a session acts as, takes the origin an
// agent row's latest write carries (the operator the handle names, else null),
// and replaces the way back and meta a request sent, whichever the row held.
func upsertActor(ctx context.Context, tx *sql.Tx, d store.Dialect, a Actor, now time.Time, origin *string) (string, error) {
	id, err := domain.NewULID(now)
	if err != nil {
		return "", err
	}
	handle := strings.TrimSpace(a.Handle)
	at := store.EncodeTime(now)
	q, args := actorUpsert(a, id, handle, at, origin)
	var actorID, kind string
	var rowOrigin sql.NullString
	if err := tx.QueryRowContext(ctx, d.Rebind(q), args...).Scan(&actorID, &kind, &rowOrigin); err != nil {
		return "", fmt.Errorf("record actor: %w", err)
	}
	if st := stateFrom(ctx); st != nil && origin != nil {
		if kind == "agent" {
			// The statement set the row's origin to the operator the request names.
			st.remember(tx, nullable(rowOrigin.String))
		} else {
			// An operator row may have appeared, or been promoted, in this tx.
			st.forget()
		}
	}
	return actorID, nil
}

// actorUpsert is the upsert statement and its arguments. The insert holds the
// columns the row is created with; the update holds what a use of an existing
// row changes. An operator row never takes an origin, and an agent row takes one
// only when the use carries a handle (origin is not nil), null when it names no
// operator, so a write through an ownerless key clears what an earlier key left.
func actorUpsert(a Actor, id, handle string, at int64, origin *string) (string, []any) {
	kind := "agent"
	if a.Operator {
		kind = "operator"
	}
	cols := []string{"id", "handle", "kind", "first_seen_at", "last_seen_at"}
	marks := []string{"?", "?", "?", "?", "?"}
	args := []any{id, handle, kind, at, at}
	set := []string{"last_seen_at = excluded.last_seen_at"}
	if a.Operator {
		set = append(set, "kind = 'operator'")
	} else if origin != nil {
		cols = append(cols, "origin")
		marks = append(marks, "(SELECT o.id FROM actors o WHERE o.handle = ? AND o.kind = 'operator')")
		args = append(args, *origin)
		set = append(set, "origin = CASE WHEN actors.kind = 'agent' THEN excluded.origin ELSE actors.origin END")
	}
	for _, c := range []struct{ column, value string }{{"way_back", strings.TrimSpace(a.WayBack)}, {"meta", strings.TrimSpace(a.Meta)}} {
		if c.value == "" {
			continue
		}
		cols = append(cols, c.column)
		marks = append(marks, "?")
		args = append(args, c.value)
		set = append(set, c.column+" = excluded."+c.column)
	}
	return "INSERT INTO actors (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(marks, ", ") +
		") ON CONFLICT (handle) DO UPDATE SET " + strings.Join(set, ", ") + " RETURNING id, kind, origin", args
}

// OnBehalf makes sure the actor a.OnBehalfOf names exists and returns its id, or
// "" when the write names none. The actor is an ordinary agent row unless an
// earlier use made it the operator's.
func OnBehalf(ctx context.Context, tx *sql.Tx, d store.Dialect, a Actor, now time.Time) (string, error) {
	handle := strings.TrimSpace(a.OnBehalfOf)
	if handle == "" {
		return "", nil
	}
	return upsertActor(ctx, tx, d, Actor{Handle: handle}, now, nil)
}

// nullable is the column value of an optional text field.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Append upserts the actor and appends e, made by that actor, in tx.
func Append(ctx context.Context, tx *sql.Tx, d store.Dialect, a Actor, now time.Time, e Event) error {
	actorID, err := UpsertActor(ctx, tx, d, a, now)
	if err != nil {
		return err
	}
	return AppendAs(ctx, tx, d, actorID, now, e)
}

// AppendAs appends e, made by the actor row actorID that UpsertActor returned.
// It first takes the dialect's event-write lock, which tx holds until it ends,
// so the change feed can tell an uncommitted event number from a rolled-back one.
func AppendAs(ctx context.Context, tx *sql.Tx, d store.Dialect, actorID string, now time.Time, e Event) error {
	_, err := AppendAsID(ctx, tx, d, actorID, now, e)
	return err
}

// lockEvents takes the dialect's event-write lock, if it has one.
func lockEvents(ctx context.Context, tx *sql.Tx, d store.Dialect) error {
	lock := d.LockEventWrite()
	if lock == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, lock)
	return err
}

// AppendAsID is AppendAs that returns the id of the event it appended, for a
// write that records something about the event in the same transaction.
func AppendAsID(ctx context.Context, tx *sql.Tx, d store.Dialect, actorID string, now time.Time, e Event) (string, error) {
	id, err := domain.NewULID(now)
	if err != nil {
		return "", err
	}
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("record event: %w", err)
	}
	origin, err := eventOrigin(ctx, tx, d, actorID)
	if err != nil {
		return "", fmt.Errorf("record event: %w", err)
	}
	if err := lockEvents(ctx, tx, d); err != nil {
		return "", fmt.Errorf("record event: %w", err)
	}
	_, err = tx.ExecContext(ctx, d.Rebind(d.Insert("events", eventColumns)),
		id, store.EncodeTime(now), actorID, e.Principal, nullable(e.OnBehalfOfID), nullable(e.InitiativeID), nullable(e.TaskID), e.Type, nullable(e.RefID), e.Summary, string(payload), origin)
	if err != nil {
		return "", fmt.Errorf("record event: %w", err)
	}
	return id, nil
}
