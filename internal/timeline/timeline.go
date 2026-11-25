package timeline

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

// ErrNoInitiative is an initiative filter that names nothing.
var ErrNoInitiative = errors.New("initiative not found")

// Marker types.
const (
	MarkerMilestone = "milestone"
	MarkerAsk       = "ask"
	MarkerCheck     = "check"
	MarkerDone      = "done"
	MarkerClosed    = "closed"
)

// Query selects a window. From and To are RFC 3339 texts, empty for the default
// end. Initiatives are slugs or ULIDs; Tags are initiative tags that all must be
// carried; Actor is a handle that must have held a lease on the task in the
// window; ClosedWithin (7d, 30d, 90d or days) adds the tasks closed that
// recently, and without it closed tasks are left out. Rows is RowsWindow (or
// empty) to keep the tasks in the window, RowsAll to keep them wherever their
// spans fall: the window then picks only the leases and markers, and Actor
// keeps the tasks the actor ever held a lease on.
type Query struct {
	From, To     string
	Initiatives  []string
	Tags         []string
	Actor        string
	ClosedWithin string
	Rows         string
	// Active keeps only the active tasks: a live lease, a ref in Attention, or an
	// event not older than Window (8h, 24h when empty, or 3d). Attention is the
	// refs of the tasks that carry any attention reason, which the caller derives.
	Active    bool
	Window    string
	Attention []string
}

// The values of Query.Rows.
const (
	RowsWindow = "window"
	RowsAll    = "all"
)

// Initiative is an initiative that holds a task of the window.
type Initiative struct {
	Slug, Title string
	Tags        []string
	Closed      bool
}

// Task is a task of the window. Deps are the refs of the tasks it is blocked by.
type Task struct {
	ID, Ref, Initiative, Title, Status, Kind string
	PlannedStart, PlannedEnd, ClosedAt       *time.Time
	CreatedAt                                time.Time
	Deps                                     []string
	// Tags are the task's own tags, sorted, never nil; TagMarks are its own, its
	// initiative's and its derived actor-kind tags with their sources (SPEC 5.5),
	// never nil.
	Tags     []string
	TagMarks []tasks.TagMark
}

// Lease is a lease that overlaps the window. EndedAt is nil while it is live; for
// an expired lease it is the expiry.
type Lease struct {
	Ref, Task, Holder, State, Intent string
	StartedAt                        time.Time
	EndedAt                          *time.Time
}

// Marker is a point in time on a task.
type Marker struct {
	Task, Type, Label string
	At                time.Time
}

// Result is the window and what falls in it, in stable order: initiatives in
// board order, tasks by initiative then number, leases by number, markers by
// time then event.
type Result struct {
	From, To    time.Time
	Initiatives []Initiative
	Tasks       []Task
	Leases      []Lease
	Markers     []Marker
}

// Store reads the timeline of one database.
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

// Check is the refusal Read would answer q with, or nil, without reading the
// tasks, leases or markers and without writing: a caller that writes before it
// reads (the expiry of the leases that ran out) calls it first, so a refused
// request changes nothing.
func (s Store) Check(ctx context.Context, q Query) error {
	_, err := s.Prepare(ctx, q)
	return err
}

// Prepared is a query that was checked: the initiatives it names are resolved,
// so reading it does not look them up again.
type Prepared struct {
	s    Store
	q    Query
	inis []any
}

// Prepare checks q as Check does and keeps what the check resolved (the ids of
// the initiatives q names) for Prepared.Read, so a caller that checks and then
// reads resolves the initiative scope once.
func (s Store) Prepare(ctx context.Context, q Query) (Prepared, error) {
	now := s.now()
	from, to, err := window(q, now)
	if err != nil {
		return Prepared{}, err
	}
	p := Prepared{s: s, q: q}
	if _, err = s.scope(ctx, q, now, from, to, p.resolve); err != nil {
		return Prepared{}, err
	}
	return p, nil
}

// WithAttention is the prepared query with Query.Attention set to refs, which
// the caller derives after it checked (and expired the leases that ran out).
func (p Prepared) WithAttention(refs []string) Prepared {
	p.q.Attention = refs
	return p
}

// resolve is the initiative lookup of a prepared query's scope: it looks the
// initiatives up once and answers from the first answer after that.
func (p *Prepared) resolve(ctx context.Context, refs []string) ([]any, error) {
	if p.inis == nil {
		ids, err := p.s.initiativeIDs(ctx, refs)
		if err != nil {
			return nil, err
		}
		p.inis = ids
	}
	return p.inis, nil
}

// Read answers the window q selects. It returns ErrInvalid for a time that does
// not read, a window that is empty or over 400 days, a bad closed_within or a
// bad tag, and ErrNoInitiative for an initiative that names nothing. It reads
// the leases as they are: the caller expires those that ran out first
// (leases.Store.ExpireDue).
func (s Store) Read(ctx context.Context, q Query) (Result, error) {
	p := Prepared{s: s, q: q}
	return p.Read(ctx)
}

// Read answers the window of the prepared query as Store.Read does. The tasks
// statement resolves the kept task ids once; the statements that fill in the
// rest of the answer take those ids instead of repeating the filter.
func (p Prepared) Read(ctx context.Context) (Result, error) {
	s := p.s
	now := s.now()
	from, to, err := window(p.q, now)
	if err != nil {
		return Result{}, err
	}
	f, err := s.scope(ctx, p.q, now, from, to, p.resolve)
	if err != nil {
		return Result{}, err
	}
	out := Result{From: from, To: to, Initiatives: []Initiative{}, Leases: []Lease{}, Markers: []Marker{}}
	ids, tasks, err := s.tasks(ctx, f)
	if err != nil {
		return Result{}, err
	}
	out.Tasks = tasks
	if len(ids.list) == 0 {
		return out, nil
	}
	if out.Initiatives, err = s.initiatives(ctx, ids); err != nil {
		return Result{}, err
	}
	if out.Leases, err = s.leases(ctx, ids, from, to); err != nil {
		return Result{}, err
	}
	if out.Markers, err = s.markers(ctx, ids, from, to); err != nil {
		return Result{}, err
	}
	return out, nil
}

// filter is the WHERE conditions that pick the tasks of a window, over the
// aliases t (tasks) and i (initiatives), and their arguments.
type filter struct {
	conds []string
	args  []any
}

func (f *filter) add(cond string, args ...any) {
	f.conds = append(f.conds, cond)
	f.args = append(f.args, args...)
}

func (f *filter) where() string { return ` WHERE ` + strings.Join(f.conds, ` AND `) }

// leaseOverlap holds when lease l overlaps [from, to]: it started by to and is
// live or ended (or expired) at from or later. Its arguments are to and from.
const leaseOverlap = `l.started_at <= ? AND (l.state = 'live' OR COALESCE(l.ended_at, l.expires_at) >= ?)`

// scope builds the conditions of the tasks in the window: the status and span,
// then the initiative, tag and actor filters.
func (s Store) scope(ctx context.Context, q Query, now, from, to time.Time, resolve func(context.Context, []string) ([]any, error)) (*filter, error) {
	f := &filter{}
	all, err := allRows(q)
	if err != nil {
		return nil, err
	}
	span, err := activeSpan(q)
	if err != nil {
		return nil, err
	}
	f.activeFilter(q, s.Store.Dialect(), now, span)
	if !all {
		f.span(from, to)
	}
	if err := f.closed(q, now); err != nil {
		return nil, err
	}
	if err := f.initiativeFilter(ctx, q.Initiatives, resolve); err != nil {
		return nil, err
	}
	if err := f.tagFilter(q.Tags); err != nil {
		return nil, err
	}
	f.actorFilter(q.Actor, all, from, to)
	return f, nil
}

// actorFilter keeps the tasks on which the actor held a lease: in the window, or at any time when all rows are kept.
func (f *filter) actorFilter(actor string, all bool, from, to time.Time) {
	switch {
	case actor == "":
	case all:
		f.add(`EXISTS (SELECT 1 FROM leases l JOIN actors a ON a.id = l.actor_id WHERE l.task_id = t.id AND a.handle = ?)`, actor)
	default:
		f.add(`EXISTS (SELECT 1 FROM leases l JOIN actors a ON a.id = l.actor_id WHERE l.task_id = t.id AND a.handle = ? AND `+leaseOverlap+`)`,
			actor, store.EncodeTime(to), store.EncodeTime(from))
	}
}

const nextRows = "Send rows as window (the tasks in the window, the default) or all (the tasks wherever their spans fall), or leave it out."

// allRows is whether the query keeps its tasks wherever their spans fall
// (rows=all) rather than only those in the window.
func allRows(q Query) (bool, error) {
	if q.Rows == "" {
		return false, nil
	}
	if err := ValidRows(q.Rows); err != nil {
		return false, err
	}
	return q.Rows == RowsAll, nil
}

// ValidRows is nil for a rows value that was supplied and is window or all, and
// otherwise the InvalidError a request carrying it is refused with. The empty
// text is invalid here: a caller that left rows out does not call it.
func ValidRows(rows string) error {
	switch rows {
	case RowsWindow, RowsAll:
		return nil
	}
	return InvalidError{Msg: fmt.Sprintf("rows %q is neither window nor all", rows), Next: nextRows}
}

// span keeps the tasks whose actual span (created to closed, open while it is not
// closed) or planned span overlaps [from, to].
func (f *filter) span(from, to time.Time) {
	lo, hi := store.EncodeTime(from), store.EncodeTime(to)
	f.add(`((t.created_at <= ? AND (t.closed_at IS NULL OR t.closed_at >= ?))`+
		` OR ((t.planned_start IS NOT NULL OR t.planned_end IS NOT NULL)`+
		` AND COALESCE(t.planned_start, t.planned_end) <= ? AND COALESCE(t.planned_end, t.planned_start) >= ?))`,
		hi, lo, hi, lo)
}

// closed leaves closed tasks out, or keeps those closed within closed_within.
func (f *filter) closed(q Query, now time.Time) error {
	at, on, err := since(q, now)
	if err != nil {
		return err
	}
	if on {
		f.add(`(t.status <> 'closed' OR t.closed_at >= ?)`, store.EncodeTime(at))
		return nil
	}
	f.add(`t.status <> 'closed'`)
	return nil
}

// initiativeFilter keeps the tasks of the initiatives refs name, which resolve looks up.
func (f *filter) initiativeFilter(ctx context.Context, refs []string, resolve func(context.Context, []string) ([]any, error)) error {
	if len(refs) == 0 {
		return nil
	}
	ids, err := resolve(ctx, refs)
	if err != nil {
		return err
	}
	f.add(`t.initiative_id IN (`+marks(len(ids))+`)`, ids...)
	return nil
}

// initiativeIDs looks up the ids of the initiatives refs name, ErrNoInitiative for one that names nothing.
func (s Store) initiativeIDs(ctx context.Context, refs []string) ([]any, error) {
	ids := make([]any, 0, len(refs))
	for _, ref := range refs {
		ini, err := initiatives.Store{Store: s.Store, Now: s.Now}.Get(ctx, ref)
		if errors.Is(err, initiatives.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNoInitiative, ref)
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, ini.ID)
	}
	return ids, nil
}

func (f *filter) tagFilter(raw []string) error {
	tags, err := domain.NormalizeTags(raw)
	if err != nil {
		return InvalidError{Msg: err.Error(), Next: initiatives.NextTags}
	}
	if len(tags) == 0 {
		return nil
	}
	cond, args := store.TagMatch(`t.id`, `t.initiative_id`, tags)
	f.add(cond, args...)
	return nil
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }
