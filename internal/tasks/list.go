package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/store"
)

// The status filters of a list. The empty status is StatusNotClosed.
const (
	StatusOpen      = "open"
	StatusDone      = "done"
	StatusClosed    = "closed"
	StatusNotClosed = "not_closed"
	StatusAll       = "all"
)

// ErrBadCursor is an after value that names no task.
var ErrBadCursor = errors.New("unknown cursor")

// Query selects a page of tasks ordered by id.
//
// Refs are task refs: when there are any, a task is kept only when its ref is one of them, in either letter case (a ref
// that names no task keeps nothing). Initiatives are slugs or ULIDs: a task is kept when it belongs to any of them.
// Tags are tags a task must match every one of: its own, its initiative's or a derived actor-kind tag. Status
// is open, done, closed, not_closed (the default when empty) or all, and Kind
// work or watch, empty for either. Active, Blocked and Overdue keep the tasks
// whose derived state equals the value, nil for either. Attention keeps the tasks whose ref is in AttentionRefs (true) or
// is not (false), nil for either; the caller derives AttentionRefs, the tasks that need a person. ClosedWithin is 7d, 30d,
// 90d or an integer of days and needs a status of closed or all. Q is a text the
// title or the description contains, in either letter case. After is the ULID or
// ref of the last task of the previous page. Limit 0 is the default page size.
type Query struct {
	Refs          []string
	Initiatives   []string
	Tags          []string
	Status        string
	Kind          string
	Active        *bool
	Blocked       *bool
	Overdue       *bool
	Attention     *bool
	AttentionRefs []string
	ClosedWithin  string
	Q             string
	After         string
	Limit         int
}

// Page is one page of tasks. NextAfter is the ULID to send as After for the
// following page, empty on the last page.
type Page struct {
	Items     []Task
	NextAfter string
}

// List returns the page of tasks q selects. It returns ErrInvalid for a field
// that is wrong, ErrNoInitiative for an initiative that names nothing and
// ErrBadCursor for an after that names no task. It reads the tasks as they are:
// a lease that has run out is expired by the caller first (leases.Store.ExpireDue).
func (s Store) List(ctx context.Context, q Query) (Page, error) {
	size, err := domain.PageSize(q.Limit)
	if err != nil {
		return Page{}, InvalidError{Msg: err.Error(), Next: "Send a limit between 1 and 1000, or leave it out for 100."}
	}
	f := &filter{now: s.now(), dialect: s.Store.Dialect()}
	if err := s.conditions(ctx, f, q); err != nil {
		return Page{}, err
	}
	query := selectTask + f.where() + ` ORDER BY t.id`
	if q.Q == "" {
		query += ` LIMIT ` + fmt.Sprint(size+1)
	}
	items, err := s.rows(ctx, query, f.args, f.now, textMatcher(q.Q), size+1)
	if err != nil {
		return Page{}, err
	}
	if err := s.loadTags(ctx, s.Store.ReaderFor(ctx), items); err != nil {
		return Page{}, err
	}
	p := Page{Items: items}
	if len(items) > size {
		p.Items = items[:size]
		p.NextAfter = items[size-1].ID
	}
	return p, nil
}

// rows runs a query of selectTask's columns and scans its rows in order, keeping
// the tasks keep accepts until limit are kept.
func (s Store) rows(ctx context.Context, query string, args []any, now time.Time, keep func(Task) bool, limit int) ([]Task, error) {
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, s.Store.Dialect().Rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []Task{}
	for len(items) < limit && rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		if !keep(t) {
			continue
		}
		t.Overdue = domain.Overdue(t.Kind, t.Status, t.NextCheckAt, now)
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return items, nil
}

// filter collects the WHERE conditions of a list and their arguments. now is
// the instant the derived states are judged at.
type filter struct {
	conds []string
	args  []any
	now   time.Time
	// dialect writes the parts of a condition that differ between databases.
	dialect store.Dialect
}

func (f *filter) add(cond string, args ...any) {
	f.conds = append(f.conds, cond)
	f.args = append(f.args, args...)
}

func (f *filter) where() string {
	if len(f.conds) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(f.conds, ` AND `)
}

// flag adds cond when want is true and its negation when it is false.
func (f *filter) flag(want *bool, cond string, args ...any) {
	switch {
	case want == nil:
	case *want:
		f.add(cond, args...)
	default:
		f.add(`NOT `+cond, args...)
	}
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func anys(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// conditions turns the query into WHERE conditions and their arguments.
func (s Store) conditions(ctx context.Context, f *filter, q Query) error {
	if err := s.scope(ctx, f, q); err != nil {
		return err
	}
	if err := f.statusAndKind(q); err != nil {
		return err
	}
	f.flag(q.Active, `EXISTS (SELECT 1 FROM leases l WHERE l.task_id = t.id AND l.state = 'live' AND l.expires_at >= ?)`, store.EncodeBound(f.now))
	f.flag(q.Blocked, `(`+blockedSQL+`)`)
	f.flag(q.Overdue, `(t.kind = 'watch' AND t.status <> 'closed' AND t.next_check_at IS NOT NULL AND t.next_check_at < ?)`, store.EncodeBound(f.now))
	f.refs(q.Attention, q.AttentionRefs)
	return s.afterFilter(ctx, f, q.After)
}

// scope adds the ref, initiative and tag conditions.
func (s Store) scope(ctx context.Context, f *filter, q Query) error {
	f.onlyRefs(q.Refs)
	if len(q.Initiatives) > 0 {
		ids := make([]string, 0, len(q.Initiatives))
		for _, ref := range q.Initiatives {
			ini, err := initiatives.Store{Store: s.Store, Now: s.Now}.Get(ctx, ref)
			if errors.Is(err, initiatives.ErrNotFound) {
				return fmt.Errorf("%w: %s", ErrNoInitiative, ref)
			} else if err != nil {
				return err
			}
			ids = append(ids, ini.ID)
		}
		f.add(`t.initiative_id IN (`+marks(len(ids))+`)`, anys(ids)...)
	}
	tags, err := domain.NormalizeTags(q.Tags)
	if err != nil {
		return InvalidError{Msg: err.Error(), Next: initiatives.NextTags}
	}
	if cond, args := store.TagMatch(`t.id`, `t.initiative_id`, tags); cond != "" {
		f.add(cond, args...)
	}
	return nil
}

// notClosedSQL keeps the tasks whose status is not closed. A status is open, done or closed (a CHECK on the column),
// so it is the open ids and the done ids, each a range of the (status, id) index: the planner then walks the tasks in
// id order through the primary key, with no scan of tasks and no temp B-tree for ORDER BY id. The plain
// status <> 'closed' it replaces plans as a SCAN of tasks.
const notClosedSQL = `t.id IN (SELECT id FROM tasks WHERE status = 'open' UNION ALL SELECT id FROM tasks WHERE status = 'done')`

// statusAndKind adds the status, closed_within and kind conditions.
func (f *filter) statusAndKind(q Query) error {
	switch q.Status {
	case "", StatusNotClosed:
		f.add(notClosedSQL)
	case StatusOpen, StatusDone, StatusClosed:
		f.add(`t.status = ?`, q.Status)
	case StatusAll:
	default:
		return InvalidError{Msg: fmt.Sprintf("status %q is not a task status filter", q.Status),
			Next: "Send status open, done, closed, not_closed or all, or leave it out for not_closed."}
	}
	if err := f.closedWithin(q); err != nil {
		return err
	}
	if q.Kind == "" {
		return nil
	}
	if err := domain.ValidateKind(q.Kind); err != nil {
		return InvalidError{Msg: err.Error(), Next: "Send kind work or watch, or leave it out for both."}
	}
	f.add(`t.kind = ?`, q.Kind)
	return nil
}

const nextClosedWithin = "Send closed_within as 7d, 30d, 90d or a number of days, with status closed or all."

// closedWithin adds the condition for the closed_within filter, if there is one.
func (f *filter) closedWithin(q Query) error {
	if q.ClosedWithin == "" {
		return nil
	}
	days, err := domain.ParseDays(q.ClosedWithin)
	if err != nil {
		return InvalidError{Msg: err.Error(), Next: nextClosedWithin}
	}
	since := store.EncodeTime(f.now.AddDate(0, 0, -days))
	switch q.Status {
	case StatusClosed:
		f.add(`t.closed_at >= ?`, since)
	case StatusAll:
		f.add(`(t.status <> 'closed' OR t.closed_at >= ?)`, since)
	default:
		return InvalidError{Msg: "closed_within needs status closed or all", Next: nextClosedWithin}
	}
	return nil
}

// fold is the letter-case-insensitive form of text: every rune in its simple
// lower case, reached through upper case so that the final sigma and the like
// meet their capitals. It is the same on every database because the match runs
// in Go: SQLite's LOWER folds ASCII only and Postgres's follows the collation.
func fold(text string) string { return strings.ToLower(strings.ToUpper(text)) }

// textMatcher keeps the tasks whose title or description contains q, in any
// letter case, as plain text: no character of q is a pattern. The empty q keeps
// every task.
func textMatcher(q string) func(Task) bool {
	if q == "" {
		return func(Task) bool { return true }
	}
	needle := fold(q)
	return func(t Task) bool {
		return strings.Contains(fold(t.Title), needle) || strings.Contains(fold(t.Description), needle)
	}
}

// afterFilter adds the condition that keeps the tasks after the one ref names: a
// task ref or ULID in either letter case. A ULID that names no task still pages
// (ids are keys, so it starts after that id); any other text that names no task
// is ErrBadCursor.
func (s Store) afterFilter(ctx context.Context, f *filter, ref string) error {
	if ref == "" {
		return nil
	}
	got, err := s.Get(ctx, ref)
	switch {
	case err == nil:
		f.add(`t.id > ?`, got.ID)
		return nil
	case !errors.Is(err, ErrNotFound):
		return err
	}
	if id, ok := domain.KeysetULID(ref); ok {
		f.add(`t.id > ?`, id)
		return nil
	}
	return fmt.Errorf("%w: there is no task %s", ErrBadCursor, ref)
}

// refs keeps the tasks whose ref is in refs when want is true and those whose
// ref is not when it is false; nil want keeps every task.
func (f *filter) refs(want *bool, refs []string) {
	if want == nil {
		return
	}
	sql, arg, ok := store.TaskIDsOfRefs(f.dialect, refs)
	switch {
	case ok && *want:
		f.add(`t.id IN (`+sql+`)`, arg)
	case ok:
		f.add(`t.id NOT IN (`+sql+`)`, arg)
	case *want:
		f.add(`1 = 0`)
	}
}

// onlyRefs keeps the tasks whose ref is one of refs, in either letter case; no refs keeps every task.
func (f *filter) onlyRefs(refs []string) {
	if len(refs) == 0 {
		return
	}
	lowered := make([]string, len(refs))
	for i, ref := range refs {
		lowered[i] = strings.ToLower(ref)
	}
	sql, arg, ok := store.TaskIDsOfRefs(f.dialect, lowered)
	if !ok {
		f.add(`1 = 0`)
		return
	}
	f.add(`t.id IN (`+sql+`)`, arg)
}
