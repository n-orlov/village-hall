package leases

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
)

// State filters of a list. The empty state is StateLive.
const (
	StateLive    = domain.LeaseLive
	StateEnded   = domain.LeaseEnded
	StateExpired = domain.LeaseExpired
	StateAll     = "all"
)

// Query selects a page of leases. State is live (the default), ended, expired or
// all; Actor is a handle, Task a task ref or ULID and Resource one resource,
// each empty for no filter. After is the ref (L<n>) or ULID of the last lease of
// the previous page; Limit 0 is the default page size.
type Query struct {
	State    string
	Actor    string
	Task     string
	Resource string
	// Notice keeps the notices (true) or the ordinary leases (false); nil keeps both.
	Notice *bool
	After  string
	Limit  int
}

// Page is one page of leases, oldest first. NextAfter is the ULID to send as
// After for the following page, empty on the last page.
type Page struct {
	Items     []Lease
	NextAfter string
}

const nextState = "Send state=live, ended, expired or all, or leave state out for live."

// List returns the page of leases q selects, ordered by lease number, after
// expiring the leases that are due so none is shown live past its expiry. It
// returns ErrInvalid for a state that is none of the four, a limit out of range
// or an after that names no lease, and ErrNotFound for a task filter naming no
// task.
func (s Store) List(ctx context.Context, q Query) (Page, error) {
	size, err := domain.PageSize(q.Limit)
	if err != nil {
		return Page{}, InvalidError{Msg: err.Error(), Next: "Send a limit between 1 and 1000, or leave it out for 100."}
	}
	if _, err := s.ExpireDue(ctx); err != nil {
		return Page{}, err
	}
	f, err := s.conditions(ctx, q)
	if err != nil {
		return Page{}, err
	}
	items, err := s.listRows(ctx, f, size+1)
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

// filter is the WHERE conditions of a query and their arguments.
type filter struct {
	conds []string
	args  []any
}

func (f *filter) add(cond string, arg ...any) {
	f.conds = append(f.conds, cond)
	f.args = append(f.args, arg...)
}

// state adds the condition for a state filter: live when none is sent.
func (f *filter) state(state string) error {
	switch state {
	case "", StateLive:
		f.add(`l.state = 'live'`)
	case StateEnded, StateExpired:
		f.add(`l.state = ?`, state)
	case StateAll:
	default:
		return InvalidError{Msg: fmt.Sprintf("state %q is not a lease state", state), Next: nextState}
	}
	return nil
}

// conditions turns the query into WHERE conditions and their arguments.
func (s Store) conditions(ctx context.Context, q Query) (*filter, error) {
	f := &filter{}
	if err := f.state(q.State); err != nil {
		return nil, err
	}
	if q.Actor != "" {
		f.add(`a.handle = ?`, q.Actor)
	}
	if q.Resource != "" {
		f.add(`EXISTS (SELECT 1 FROM lease_resources r WHERE r.lease_id = l.id AND r.resource = ?)`, q.Resource)
	}
	if q.Notice != nil {
		f.add(`l.notice = ?`, *q.Notice)
	}
	if err := s.taskFilter(ctx, f, q.Task); err != nil {
		return nil, err
	}
	if err := s.afterFilter(ctx, f, q.After); err != nil {
		return nil, err
	}
	return f, nil
}

// taskFilter adds the condition for a task ref or ULID, or returns ErrNotFound.
func (s Store) taskFilter(ctx context.Context, f *filter, ref string) error {
	if ref == "" {
		return nil
	}
	task, err := s.findTask(ctx, s.Store.ReaderFor(ctx), ref)
	if err != nil {
		return err
	}
	f.add(`l.task_id = ?`, task.ID)
	return nil
}

// afterFilter adds the condition for the lease an after cursor names: its ref
// (L<n>) or its ULID.
func (s Store) afterFilter(ctx context.Context, f *filter, after string) error {
	if after == "" {
		return nil
	}
	n, ok := domain.ParseLeaseRef(after)
	if !ok {
		l, err := s.Get(ctx, after)
		if err != nil {
			return InvalidError{Msg: fmt.Sprintf("after %q is not a lease ref or id", after),
				Next: "Send the next_after of the previous page, or a lease ref such as L3."}
		}
		n = l.N
	}
	f.add(`l.n > ?`, n)
	return nil
}

// listRows reads up to limit leases that meet the filter, oldest first.
func (s Store) listRows(ctx context.Context, f *filter, limit int) ([]Lease, error) {
	query := selectLease
	if len(f.conds) > 0 {
		query += ` WHERE ` + strings.Join(f.conds, ` AND `)
	}
	return s.readLeases(ctx, query+` ORDER BY l.n LIMIT `+fmt.Sprint(limit), f.args)
}

// readLeases runs a query that selects selectLease's columns and returns the
// leases with their resources.
func (s Store) readLeases(ctx context.Context, query string, args []any) ([]Lease, error) {
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, s.Store.Dialect().Rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("read leases: %w", err)
	}
	out, err := scanAll(rows)
	if err != nil {
		return nil, err
	}
	// Resources are read after the cursor is closed: the reader may be one connection.
	if err := s.attachResources(ctx, s.Store.ReaderFor(ctx), out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachResources sets the Resources of every lease in list, each sorted, in one
// query however many leases there are, none included.
func (s Store) attachResources(ctx context.Context, q queryer, list []Lease) error {
	by, err := s.resourcesOfAll(ctx, q, list)
	if err != nil {
		return err
	}
	for i := range list {
		list[i].Resources = by[list[i].ID]
		if list[i].Resources == nil {
			list[i].Resources = []string{}
		}
	}
	return nil
}

// resourcesOfAll reads the resources of the leases in one query, by lease id.
func (s Store) resourcesOfAll(ctx context.Context, q queryer, list []Lease) (map[string][]string, error) {
	ids := make([]string, len(list))
	for i, l := range list {
		ids[i] = l.ID
	}
	rows, err := q.QueryContext(ctx, s.Store.Dialect().Rebind(
		`SELECT lease_id, resource FROM lease_resources WHERE lease_id IN (`+s.Store.Dialect().InSet()+`) ORDER BY lease_id, resource`), store.TextSet(ids))
	if err != nil {
		return nil, fmt.Errorf("read lease resources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var id, r string
		if err := rows.Scan(&id, &r); err != nil {
			return nil, fmt.Errorf("read lease resources: %w", err)
		}
		out[id] = append(out[id], r)
	}
	return out, rows.Err()
}

// scanAll reads and closes rows.
func scanAll(rows *sql.Rows) ([]Lease, error) {
	defer func() { _ = rows.Close() }()
	out := []Lease{}
	for rows.Next() {
		l, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("read leases: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read leases: %w", err)
	}
	return out, nil
}
