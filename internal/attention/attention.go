package attention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// Errors a caller tells apart.
var (
	ErrInvalid  = errors.New("invalid attention query")
	ErrNotFound = errors.New("initiative not found")
)

// InvalidError is a field the query got wrong. It satisfies errors.Is(err,
// ErrInvalid), its text is the sentence to show the caller and Next says what to
// send instead.
type InvalidError struct{ Msg, Next string }

// Error is the sentence to show the caller.
func (e InvalidError) Error() string { return e.Msg }

// Is makes errors.Is(err, ErrInvalid) true for every InvalidError.
func (e InvalidError) Is(target error) bool { return target == ErrInvalid }

// Actor is a person or agent involved in an item: the handle and the way back
// the board has on record for it, empty when it has none.
type Actor struct {
	Handle  string
	WayBack string
}

// OpenAsk is an open ask. Task is empty for an ask on no task. Initiative is the
// slug of the initiative the ask belongs to (its task's, or the one it was raised
// on directly), empty for a global ask. Age is the time
// since the ask was raised.
type OpenAsk struct {
	Ref          string
	Question     string
	Task         string
	Initiative   string
	Asker        Actor
	Mode         string
	AskerWaiting bool
	Blocking     bool
	CreatedAt    time.Time
	Age          time.Duration
}

// LastLease is the last ended lease of a task: the holder and what they wrote
// when they ended it (empty when they wrote nothing).
type LastLease struct {
	Lease   string
	Holder  Actor
	Outcome string
	EndedAt time.Time
}

// DoneTask is a task marked done and not yet closed. LastLease is nil when no
// lease on the task ended. Tags and TagMarks are the task's own tags and every
// mark, as a task read gives them.
type DoneTask struct {
	Task      string
	Title     string
	DoneBy    Actor
	DoneAt    time.Time
	LastLease *LastLease
	Tags      []string
	TagMarks  []tasks.TagMark
}

// ExpiredLease is a lease that ran out, was not acknowledged and has no later
// lease on its task. Tags and TagMarks are its task's, as for DoneTask.
type ExpiredLease struct {
	Lease     string
	Holder    Actor
	Task      string
	Title     string
	Intent    string
	RenewedAt time.Time
	ExpiredAt time.Time
	// Notice is true when the lease was a notice (§4.4); it expires and is acknowledged like any other.
	Notice   bool
	Tags     []string
	TagMarks []tasks.TagMark
}

// LastCheck is the last check_recorded event of a watch.
type LastCheck struct {
	Summary string
	By      Actor
	At      time.Time
}

// OverdueWatch is a watch whose next check is due. Owner is who created it.
// LastCheck is nil when no check was ever recorded. Tags and TagMarks are the
// task's, as for DoneTask.
type OverdueWatch struct {
	Task        string
	Title       string
	Owner       Actor
	NextCheckAt time.Time
	LastCheck   *LastCheck
	Tags        []string
	TagMarks    []tasks.TagMark
}

// Holder is one live lease on a contested resource.
type Holder struct {
	Lease     string
	Actor     Actor
	Task      string
	Intent    string
	ExpiresAt time.Time
}

// Conflict is a resource that two or more live leases hold, with the holders in
// lease-number order and the distinct tasks they work on in the same order. Since is when the
// contest began: the newest start among the holders, the moment the last of them took the resource.
type Conflict struct {
	Resource string
	Holders  []Holder
	Tasks    []string
	Since    time.Time
}

// Counts is the size of each section and their sum.
type Counts struct {
	OpenAsks       int
	DoneNotClosed  int
	ExpiredLeases  int
	OverdueWatches int
	Conflicts      int
	Total          int
}

// Result is the five sections and their counts. Every section is empty, never nil.
type Result struct {
	OpenAsks       []OpenAsk
	DoneNotClosed  []DoneTask
	ExpiredLeases  []ExpiredLease
	OverdueWatches []OverdueWatch
	Conflicts      []Conflict
	Counts         Counts
}

// Query narrows the sections. Initiatives are slugs or ULIDs: an item is kept
// when it belongs to any of them. Tags are initiative tags: the item's
// initiative must carry every one. A conflict is kept when any lease that holds
// the resource is on a kept task.
type Query struct {
	Initiatives []string
	Tags        []string
}

// Store reads the attention sections of one database.
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

// begin resolves the query's scope and expires the due leases. It returns the
// scope and the instant the sections are judged at.
func (s Store) begin(ctx context.Context, q Query) (scope, time.Time, error) {
	sc, err := s.scope(ctx, q)
	if err != nil {
		return scope{}, time.Time{}, err
	}
	now, err := s.prepare(ctx)
	return sc, now, err
}

// Get returns the five sections and their counts. It returns ErrInvalid for a tag
// that is malformed and ErrNotFound for an initiative that names nothing. It
// first expires the leases that ran out, so none counts as live past its expiry.
// Each item that is a task (a done task, an expired lease's, a watch) carries the
// task's tags and marks, read in the statements of one tasks.Store.TaggedByRef.
func (s Store) Get(ctx context.Context, q Query) (Result, error) {
	sc, now, err := s.begin(ctx, q)
	if err != nil {
		return Result{}, err
	}
	r, err := s.get(ctx, sc, now)
	if err != nil {
		return Result{}, err
	}
	byRef, err := s.taggedByRef(ctx, attentionRefs(r))
	if err != nil {
		return Result{}, err
	}
	fillResult(&r, byRef)
	return r, nil
}

// get reads the five sections of a scope at the instant now.
func (s Store) get(ctx context.Context, sc scope, now time.Time) (Result, error) {
	d, err := s.derive(ctx, sc, now)
	if err != nil {
		return Result{}, err
	}
	r := Result{OpenAsks: d.OpenAsks, DoneNotClosed: d.DoneNotClosed, ExpiredLeases: d.ExpiredLeases}
	for _, read := range []func() error{
		func() (err error) { r.OverdueWatches, err = s.overdueWatches(ctx, sc, now); return },
		func() (err error) { r.Conflicts, err = s.conflicts(ctx, sc); return },
	} {
		if err := read(); err != nil {
			return Result{}, err
		}
	}
	r.Counts = total(Counts{OpenAsks: len(r.OpenAsks), DoneNotClosed: len(r.DoneNotClosed), ExpiredLeases: len(r.ExpiredLeases),
		OverdueWatches: len(r.OverdueWatches), Conflicts: len(r.Conflicts)})
	return r, nil
}

// total sets Total to the sum of the five sections.
func total(c Counts) Counts {
	c.Total = c.OpenAsks + c.DoneNotClosed + c.ExpiredLeases + c.OverdueWatches + c.Conflicts
	return c
}

// Counts returns only the size of each section, without reading the items of the
// four that SQL can count.
func (s Store) Counts(ctx context.Context, q Query) (Counts, error) {
	return s.count4(ctx, q)
}

// count4 counts the sections.
func (s Store) count4(ctx context.Context, q Query) (Counts, error) {
	sc, now, err := s.begin(ctx, q)
	if err != nil {
		return Counts{}, err
	}
	var c Counts
	for _, p := range []struct {
		into *int
		sec  section
	}{{&c.OpenAsks, asksSection(sc)}, {&c.DoneNotClosed, doneSection(sc)}, {&c.ExpiredLeases, expiredSection(sc)},
		{&c.OverdueWatches, watchesSection(sc, now)}} {
		if *p.into, err = s.count(ctx, p.sec); err != nil {
			return Counts{}, err
		}
	}
	c.Conflicts, err = s.conflictCount(ctx, sc, now)
	return total(c), err
}

// prepare expires the due leases and returns the instant the sections are judged at.
func (s Store) prepare(ctx context.Context) (time.Time, error) {
	now := s.now()
	l := leases.Store{Store: s.Store, Now: func() time.Time { return now }}
	if _, err := l.ExpireDue(ctx); err != nil {
		return time.Time{}, err
	}
	return now, nil
}

// scope is the resolved initiative and tag filter of a query.
type scope struct {
	initiatives []string
	tags        []string
}

func (s Store) scope(ctx context.Context, q Query) (scope, error) {
	tags, err := domain.NormalizeTags(q.Tags)
	if err != nil {
		return scope{}, InvalidError{Msg: err.Error(), Next: initiatives.NextTags}
	}
	sc := scope{tags: tags}
	for _, ref := range q.Initiatives {
		ini, err := (initiatives.Store{Store: s.Store, Now: s.Now}).Get(ctx, ref)
		if errors.Is(err, initiatives.ErrNotFound) {
			return scope{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
		} else if err != nil {
			return scope{}, err
		}
		sc.initiatives = append(sc.initiatives, ini.ID)
	}
	return sc, nil
}

func (sc scope) empty() bool { return len(sc.initiatives) == 0 && len(sc.tags) == 0 }

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func anys(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// and is the AND conditions that keep the rows whose initiative column col is in
// scope, with their arguments; both are empty for an empty scope.
func (sc scope) and(col string) (string, []any) {
	var cond string
	var args []any
	if len(sc.initiatives) > 0 {
		cond += ` AND ` + col + ` IN (` + marks(len(sc.initiatives)) + `)`
		args = append(args, anys(sc.initiatives)...)
	}
	if tagCond, tagArgs := store.TagMatch(`t.id`, col, sc.tags); tagCond != "" {
		cond += ` AND ` + tagCond
		args = append(args, tagArgs...)
	}
	return cond, args
}

// section is one SELECT over the rows of an attention section.
type section struct {
	cols, from, order string
	args              []any
}

func (c section) list() string {
	return `SELECT ` + c.cols + ` FROM ` + c.from + ` ORDER BY ` + c.order
}
func (c section) count() string { return `SELECT COUNT(*) FROM ` + c.from }

// scoped builds a section whose rows are kept by the initiative column col and,
// for a tag, by the task t of the row (NULL for an ask on no task).
func scoped(sc scope, col, cols, from, order string, args ...any) section {
	cond, more := sc.and(col)
	return section{cols: cols, from: from + cond, order: order, args: append(args, more...)}
}

type rowScanner interface{ Scan(dest ...any) error }

// collect runs a section and scans its rows in order.
func collect[T any](ctx context.Context, s Store, c section, scan func(rowScanner) (T, error)) ([]T, error) {
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, s.Store.Dialect().Rebind(c.list()), c.args...)
	if err != nil {
		return nil, fmt.Errorf("read attention: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("read attention: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read attention: %w", err)
	}
	return out, nil
}

func (s Store) count(ctx context.Context, c section) (int, error) {
	var n int
	err := s.Store.ReaderFor(ctx).QueryRowContext(ctx, s.Store.Dialect().Rebind(c.count()), c.args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count attention: %w", err)
	}
	return n, nil
}

func actor(handle string, wayBack sql.NullString) Actor {
	return Actor{Handle: handle, WayBack: wayBack.String}
}
