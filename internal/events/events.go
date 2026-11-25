package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// ErrInvalid is a query the caller got wrong.
var ErrInvalid = errors.New("invalid events query")

// InvalidError is a parameter the query got wrong. It satisfies errors.Is(err,
// ErrInvalid), its text is the sentence to show the caller and Next says what to
// send instead.
type InvalidError struct{ Msg, Next string }

// Error is the sentence to show the caller.
func (e InvalidError) Error() string { return e.Msg }

// Is makes errors.Is(err, ErrInvalid) true for every InvalidError.
func (e InvalidError) Is(target error) bool { return target == ErrInvalid }

// Event is one row of the events table, as the API shows it. Ref is E<n>. The
// people are actor handles, Initiative is a slug and Task a task ref; the
// optional ones are empty when the event has none.
type Event struct {
	N         int64
	ID        string
	Ref       string
	At        time.Time
	Type      string
	Actor     string
	Principal string
	// Origin is the handle of the operator behind the write, or empty.
	Origin       string
	OnBehalfOf   string
	Initiative   string
	InitiativeID string
	Task         string
	TaskID       string
	RefID        string
	Summary      string
	Data         map[string]any
}

// Query selects a page of events in insertion order (the order of `n`).
//
// Since keeps the events after an event (its ULID or E<n>, either case) or from
// an RFC 3339 time on. After is the cursor of the previous page: an event ULID
// or E<n>. Initiative is a slug or ULID and Task a task ref or ULID. Types keeps
// the events of any of the types and Actor those of one handle. Limit 0 is the
// default page size.
type Query struct {
	Since      string
	After      string
	Initiative string
	Task       string
	Types      []string
	Actor      string
	Limit      int
}

// Page is one page of events. NextAfter is the ULID to send as `after` for the
// following page, empty on the last one.
type Page struct {
	Items     []Event
	NextAfter string
}

// Store reads the events of one database.
type Store struct {
	Store *store.Store
	// Now is the clock the task lookups use; nil means time.Now.
	Now func() time.Time
}

const selectEvent = `SELECT e.n, e.id, e.at, e.type, a.handle, e.principal, og.handle, ob.handle, i.slug, e.initiative_id,
	ti.slug, t.seq, e.task_id, e.ref_id, e.summary, e.data
	FROM events e JOIN actors a ON a.id = e.actor_id LEFT JOIN actors ob ON ob.id = e.on_behalf_of
	LEFT JOIN actors og ON og.id = e.origin
	LEFT JOIN initiatives i ON i.id = e.initiative_id LEFT JOIN tasks t ON t.id = e.task_id
	LEFT JOIN initiatives ti ON ti.id = t.initiative_id`

// ForTask returns one page of the events of the task ref names, in order. It
// returns tasks.ErrNotFound for an unknown task.
func (s Store) ForTask(ctx context.Context, ref, after string, limit int) (Page, error) {
	return s.List(ctx, Query{Task: ref, After: after, Limit: limit})
}

// List returns one page of the events q selects. It returns ErrInvalid for a
// since or after that is not readable, a limit out of range or a type that is
// empty, tasks.ErrNotFound for a task filter naming no task and
// initiatives.ErrNotFound for an initiative filter naming no initiative.
func (s Store) List(ctx context.Context, q Query) (Page, error) {
	size, err := domain.PageSize(q.Limit)
	if err != nil {
		return Page{}, InvalidError{Msg: err.Error(), Next: "Send a limit between 1 and 1000, or leave it out for 100."}
	}
	conds, args, err := s.conditions(ctx, q)
	if err != nil {
		return Page{}, err
	}
	query := selectEvent + where(conds) + ` ORDER BY e.n LIMIT ` + fmt.Sprint(size+1)
	items, err := s.rows(ctx, query, args)
	if err != nil {
		return Page{}, err
	}
	p := Page{Items: items}
	if len(items) > size {
		p.Items = items[:size]
		p.NextAfter = items[size-1].ID
	}
	return p, nil
}

func where(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(conds, ` AND `)
}

// conditions turns the query into WHERE conditions and their arguments.
func (s Store) conditions(ctx context.Context, q Query) ([]string, []any, error) {
	var b builder
	if err := b.point(q.Since, "since", false); err != nil {
		return nil, nil, err
	}
	if err := b.point(q.After, "after", true); err != nil {
		return nil, nil, err
	}
	if err := b.types(q.Types); err != nil {
		return nil, nil, err
	}
	if q.Actor != "" {
		b.add(`a.handle = ?`, q.Actor)
	}
	if err := s.scope(ctx, &b, q); err != nil {
		return nil, nil, err
	}
	return b.conds, b.args, nil
}

// scope adds the initiative and task conditions, which need the database to
// resolve a ref to an id.
func (s Store) scope(ctx context.Context, b *builder, q Query) error {
	if q.Initiative != "" {
		ini, err := initiatives.Store{Store: s.Store, Now: s.Now}.Get(ctx, q.Initiative)
		if err != nil {
			return err
		}
		b.add(`e.initiative_id = ?`, ini.ID)
	}
	if q.Task != "" {
		t, err := tasks.Store{Store: s.Store, Now: s.Now}.Get(ctx, q.Task)
		if err != nil {
			return err
		}
		b.add(`e.task_id = ?`, t.ID)
	}
	return nil
}

// builder collects WHERE conditions and their arguments.
type builder struct {
	conds []string
	args  []any
}

func (b *builder) add(cond string, args ...any) {
	b.conds = append(b.conds, cond)
	b.args = append(b.args, args...)
}

// point adds the condition for a since or after value, if there is one.
func (b *builder) point(value, name string, onlyEvents bool) error {
	if value == "" {
		return nil
	}
	p, err := domain.ParseEventPoint(value, onlyEvents)
	if err != nil {
		return InvalidError{Msg: fmt.Sprintf("%s %q is not readable: %s", name, value, err), Next: pointNext(onlyEvents)}
	}
	switch p.Kind {
	case domain.PointN:
		b.add(`e.n > ?`, p.N)
	case domain.PointID:
		b.add(`e.id > ?`, p.ID)
	default:
		// e.n is bounded by the first event at or after the time, found on
		// events_at_n, so the read walks the rowid range in n order: no scan
		// of the table and no sort. The at condition stays, because n and at
		// need not rise together.
		at := store.EncodeTime(p.At)
		b.add(`e.n >= (SELECT MIN(f.n) FROM events f WHERE f.at >= ?)`, at)
		b.add(`e.at >= ?`, at)
	}
	return nil
}

func pointNext(onlyEvents bool) string {
	if onlyEvents {
		return "Send the id or E<n> ref of the last event you have."
	}
	return "Send an event id, an E<n> ref such as E42, or an RFC 3339 time such as 2026-11-06T12:00:00Z."
}

// types adds the condition that keeps the events of any of the types.
func (b *builder) types(types []string) error {
	if len(types) == 0 {
		return nil
	}
	marks := make([]string, len(types))
	args := make([]any, len(types))
	for i, t := range types {
		if strings.TrimSpace(t) == "" {
			return InvalidError{Msg: "type must not be empty", Next: "Send an event type such as task_done, or leave type out."}
		}
		marks[i], args[i] = "?", t
	}
	b.add(`e.type IN (`+strings.Join(marks, ", ")+`)`, args...)
	return nil
}

// rows runs a query of selectEvent's columns and scans every row.
func (s Store) rows(ctx context.Context, query string, args []any) ([]Event, error) {
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, s.Store.Dialect().Rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []Event{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list events: %w", err)
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return items, nil
}
