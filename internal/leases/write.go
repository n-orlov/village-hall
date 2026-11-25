package leases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
)

// Start is the request to start a lease. Task is a task ref or ULID. A nil TTL
// is the default and nil Resources holds none (or, on a start that renews, keeps
// the lease's resources).
type Start struct {
	Task      string
	Intent    string
	TTL       *int64
	Resources []string
	// Notice starts a notice lease (SPEC 4.4) instead of an ordinary one.
	Notice bool
}

// Renew is the request to renew a lease. A nil field keeps the stored value.
type Renew struct {
	TTL       *int64
	Intent    *string
	Resources *[]string
}

// Result is a lease with the live leases of others that share a resource with
// it. Renewed is true when a start renewed the actor's live lease on the task.
type Result struct {
	Lease     Lease
	Conflicts []domain.Holding
	Renewed   bool
}

// taskRow is the part of a task a lease needs.
type taskRow struct {
	ID, InitiativeID, Ref, Status string
}

// findTask reads the task ref names.
func (s Store) findTask(ctx context.Context, q queryer, ref string) (taskRow, error) {
	return s.readTask(ctx, q, ref, "")
}

// lockTask reads the task ref names and, on Postgres, locks its row until the
// transaction ends. A lease starts or renews only against a task status that no
// concurrent done or close can change before this transaction commits, and a
// done or close that follows reads the lease this one wrote.
func (s Store) lockTask(ctx context.Context, tx queryer, ref string) (taskRow, error) {
	return s.readTask(ctx, tx, ref, s.Store.Dialect().LockRow("t"))
}

func (s Store) readTask(ctx context.Context, tx queryer, ref, lock string) (taskRow, error) {
	parsed, err := domain.ParseTaskRef(ref)
	if err != nil {
		return taskRow{}, fmt.Errorf("%w: task %s", ErrNotFound, ref)
	}
	where, args := ` WHERE t.id = ?`, []any{parsed.ID}
	if parsed.ID == "" {
		where, args = ` WHERE i.slug = ? AND t.seq = ?`, []any{parsed.Slug, parsed.Seq}
	}
	var t taskRow
	var slug string
	var seq int
	err = tx.QueryRowContext(ctx, s.Store.Dialect().Rebind(`SELECT t.id, t.initiative_id, i.slug, t.seq, t.status FROM tasks t
		JOIN initiatives i ON i.id = t.initiative_id`+where+lock), args...).Scan(&t.ID, &t.InitiativeID, &slug, &seq, &t.Status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return taskRow{}, fmt.Errorf("%w: task %s", ErrNotFound, ref)
	case err != nil:
		return taskRow{}, fmt.Errorf("read task: %w", err)
	}
	t.Ref = domain.FormatTaskRef(slug, seq)
	return t, nil
}

func invalid(err error, next string) error {
	if err == nil {
		return nil
	}
	return InvalidError{Msg: err.Error(), Next: next}
}

const (
	nextIntent    = "Send an intent of 1 to 200 characters."
	nextResources = "Send at most 20 resources of 1 to 100 characters each."
)

// Start starts a lease for the actor on the task and returns it with its
// conflicts. When the actor already holds a live lease on the task, that lease
// is renewed instead (same lease, event lease_renewed, Renewed true); otherwise
// it is a new lease numbered L<n> and the event is lease_started. A live lease
// of the other kind (notice or ordinary) is a ConflictError naming it and
// saying to end it first; nothing is written then. It returns
// ErrInvalid for an intent or resource that is wrong (checked before anything is
// read; a missing task is ErrInvalid too), ErrNotFound for an unknown task, and ErrConflict for a done or closed task, and writes nothing then.
func (s Store) Start(ctx context.Context, actor audit.Actor, principal string, in Start) (Result, error) {
	in, resources, err := normalizeStart(in)
	if err != nil {
		return Result{}, err
	}
	var out Result
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = s.startChecked(ctx, tx, actor, principal, in, resources)
		return err
	})
	return out, err
}

// StartIn is Start inside the caller's transaction: it commits nothing and rolls
// nothing back, so the lease and its event stand or fall with tx. It is how a
// start and the task it starts on are written together.
func (s Store) StartIn(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal string, in Start) (Result, error) {
	in, resources, err := normalizeStart(in)
	if err != nil {
		return Result{}, err
	}
	return s.startChecked(ctx, tx, actor, principal, in, resources)
}

// normalizeStart checks the task, intent and resources of a start and returns
// the request with the intent trimmed and the resources normalized.
func normalizeStart(in Start) (Start, []string, error) {
	if strings.TrimSpace(in.Task) == "" {
		return in, nil, InvalidError{Msg: "a lease needs a task", Next: "Send the ref of the task the lease is on, such as aa-1."}
	}
	if err := domain.ValidateIntent(in.Intent); err != nil {
		return in, nil, invalid(err, nextIntent)
	}
	resources, err := domain.NormalizeResources(in.Resources)
	if err != nil {
		return in, nil, invalid(err, nextResources)
	}
	in.Intent = strings.TrimSpace(in.Intent)
	return in, resources, nil
}

// startChecked expires the due leases and starts the checked request in tx.
func (s Store) startChecked(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal string, in Start, resources []string) (Result, error) {
	if _, err := s.expireDue(ctx, tx, s.now()); err != nil {
		return Result{}, err
	}
	return s.start(ctx, tx, actor, principal, in, resources)
}

func (s Store) start(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal string, in Start, resources []string) (Result, error) {
	task, err := s.lockTask(ctx, tx, in.Task)
	if err != nil {
		return Result{}, err
	}
	if task.Status == "closed" {
		return Result{}, ConflictError{Msg: "cannot start a lease on task " + task.Ref + ": it is closed",
			Next: "Reopen the task first, or start the lease on an open task."}
	}
	if task.Status == "done" {
		return Result{}, ConflictError{Msg: "cannot start a lease on task " + task.Ref + ": it is done",
			Next: "Run vh task reopen " + task.Ref + " first, or start the lease on an open task."}
	}
	now := s.now()
	actorID, err := audit.UpsertActor(ctx, tx, s.Store.Dialect(), actor, now)
	if err != nil {
		return Result{}, err
	}
	held, renewing, err := s.liveOf(ctx, tx, task.ID, actorID, now)
	if err != nil {
		return Result{}, err
	}
	if renewing && held.Notice != in.Notice {
		return Result{}, otherKindHeld(held, task)
	}
	var l Lease
	if renewing {
		r := Renew{TTL: ttlOf(in.TTL), Intent: &in.Intent}
		if in.Resources != nil {
			r.Resources = &resources
		}
		l, err = s.renew(ctx, tx, actorID, principal, held, r, task, now)
	} else {
		var id string
		if id, err = s.insert(ctx, tx, actorID, principal, task, in, resources, now); err == nil {
			l, err = s.find(ctx, tx, id)
		}
	}
	if err != nil {
		return Result{}, err
	}
	return s.result(ctx, tx, l, now, renewing)
}

// otherKindHeld is the refusal of a start whose kind differs from the live
// lease the actor already holds on the task: a task holds one live lease per
// actor, so the held one is ended first.
func otherKindHeld(held Lease, task taskRow) error {
	have, asked := "an ordinary lease", "a notice"
	if held.Notice {
		have, asked = "a notice lease", "an ordinary lease"
	}
	return ConflictError{
		Msg:  fmt.Sprintf("you already hold %s on task %s, %s, and cannot start %s on it as well", held.Ref, task.Ref, have, asked),
		Next: fmt.Sprintf("Run vh lease end %s first, then start the new lease.", held.Ref),
	}
}

// ttlOf is the ttl a start asks for: the default when none was sent.
func ttlOf(requested *int64) *int64 {
	v := domain.ClampLeaseTTL(requested)
	return &v
}

// liveOf returns the live, unexpired lease the actor holds on the task, with its
// resources, and whether there is one.
func (s Store) liveOf(ctx context.Context, tx *sql.Tx, taskID, actorID string, now time.Time) (Lease, bool, error) {
	l, err := scan(tx.QueryRowContext(ctx, s.Store.Dialect().Rebind(selectLease+` WHERE l.task_id = ? AND l.actor_id = ?
		AND l.state = 'live' AND l.expires_at >= ?`), taskID, actorID, store.EncodeBound(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("read lease: %w", err)
	}
	if l.Resources, err = s.resourcesOf(ctx, tx, l.ID); err != nil {
		return Lease{}, false, err
	}
	return l, true, nil
}

// result is the lease l, as the write left it, with its conflicts.
func (s Store) result(ctx context.Context, tx *sql.Tx, l Lease, now time.Time, renewed bool) (Result, error) {
	c, err := s.conflictsOf(ctx, tx, l, now)
	return Result{Lease: l, Conflicts: c, Renewed: renewed}, err
}

func (s Store) insert(ctx context.Context, tx *sql.Tx, actorID, principal string, task taskRow, in Start, resources []string, now time.Time) (string, error) {
	d := s.Store.Dialect()
	id, err := domain.NewULID(now)
	if err != nil {
		return "", err
	}
	ttl := domain.ClampLeaseTTL(in.TTL)
	at := store.EncodeTime(now)
	expires := store.EncodeTime(now.Add(time.Duration(ttl) * time.Second))
	var n int64
	err = tx.QueryRowContext(ctx, d.Rebind(d.InsertReturning("leases",
		[]string{"id", "task_id", "actor_id", "intent", "ttl_seconds", "started_at", "renewed_at", "expires_at", "state", "notice"}, "n")),
		id, task.ID, actorID, in.Intent, ttl, at, at, expires, domain.LeaseLive, d.EncodeBool(in.Notice)).Scan(&n)
	if err != nil {
		return "", fmt.Errorf("start lease: %w", err)
	}
	if err := s.insertResources(ctx, tx, id, resources); err != nil {
		return "", err
	}
	ref := domain.FormatLeaseRef(n)
	data := map[string]any{"ref": ref, "task": task.Ref, "intent": in.Intent, "ttl_seconds": ttl,
		"expires_at": store.DecodeTime(expires).Format(time.RFC3339Nano), "resources": resources}
	if in.Notice {
		data["notice"] = true
	}
	return id, audit.AppendAs(ctx, tx, d, actorID, now, audit.Event{
		Principal: principal, Type: EventStarted, InitiativeID: task.InitiativeID, TaskID: task.ID, RefID: id,
		Summary: ref + " started on " + task.Ref + ": " + domain.OneLine(in.Intent),
		Data:    data,
	})
}

// setResources replaces the resources the lease holds.
func (s Store) setResources(ctx context.Context, tx *sql.Tx, id string, resources []string) error {
	if _, err := tx.ExecContext(ctx, s.Store.Dialect().Rebind(`DELETE FROM lease_resources WHERE lease_id = ?`), id); err != nil {
		return fmt.Errorf("set lease resources: %w", err)
	}
	return s.insertResources(ctx, tx, id, resources)
}

// insertResources adds the resources to the lease id in one statement, however
// many there are; a lease with none writes no statement.
func (s Store) insertResources(ctx context.Context, tx *sql.Tx, id string, resources []string) error {
	if len(resources) == 0 {
		return nil
	}
	args := make([]any, 0, 2*len(resources))
	for _, r := range resources {
		args = append(args, id, r)
	}
	if err := store.InsertMany(ctx, tx, s.Store.Dialect(), "lease_resources", []string{"lease_id", "resource"}, args); err != nil {
		return fmt.Errorf("set lease resources: %w", err)
	}
	return nil
}

// Renew pushes the expiry of the lease ref names to now plus its ttl (the
// stored one, or the request's clamped), optionally replacing the intent and the
// resources, and appends a lease_renewed event. It returns the lease with its
// conflicts. It returns ErrInvalid for a field that is wrong, ErrNotFound for an
// unknown lease, a GoneError (ErrGone, carrying the expiry time) for an expired
// one and ErrConflict for an ended one, and writes nothing then.
func (s Store) Renew(ctx context.Context, actor audit.Actor, principal, ref string, in Renew) (Result, error) {
	in, err := normalizeRenew(in)
	if err != nil {
		return Result{}, err
	}
	var out Result
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if _, err = s.expireDue(ctx, tx, s.now()); err != nil {
			return err
		}
		out, err = s.renewRef(ctx, tx, actor, principal, ref, in)
		return err
	})
	return out, err
}

// normalizeRenew checks the intent and resources of a renewal and clamps its ttl.
func normalizeRenew(in Renew) (Renew, error) {
	if in.Intent != nil {
		if err := domain.ValidateIntent(*in.Intent); err != nil {
			return Renew{}, invalid(err, nextIntent)
		}
		v := strings.TrimSpace(*in.Intent)
		in.Intent = &v
	}
	if in.Resources != nil {
		res, err := domain.NormalizeResources(*in.Resources)
		if err != nil {
			return Renew{}, invalid(err, nextResources)
		}
		in.Resources = &res
	}
	if in.TTL != nil {
		in.TTL = ttlOf(in.TTL)
	}
	return in, nil
}

func (s Store) renewRef(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal, ref string, in Renew) (Result, error) {
	first, err := s.findRow(ctx, tx, ref)
	if err != nil {
		return Result{}, err
	}
	// the task is locked before the lease is judged, so a done or close that
	// ends the lease first is seen, not renewed over
	task, err := s.lockTask(ctx, tx, first.TaskID)
	if err != nil {
		return Result{}, err
	}
	cur, err := s.find(ctx, tx, ref)
	if err != nil {
		return Result{}, err
	}
	now := s.now()
	if err := renewable(cur, now); err != nil {
		return Result{}, err
	}
	actorID, err := audit.UpsertActor(ctx, tx, s.Store.Dialect(), actor, now)
	if err != nil {
		return Result{}, err
	}
	renewed, err := s.renew(ctx, tx, actorID, principal, cur, in, task, now)
	if err != nil {
		return Result{}, err
	}
	return s.result(ctx, tx, renewed, now, false)
}

// renewable returns nil for a live lease, a GoneError for an expired one and a
// ConflictError for an ended one.
func renewable(l Lease, now time.Time) error {
	switch {
	case l.Expired(now):
		return GoneError{Ref: l.Ref, ExpiredAt: l.ExpiresAt}
	case l.State == domain.LeaseEnded:
		return ConflictError{Msg: "cannot renew lease " + l.Ref + ": it is ended", Next: "Start a new lease on the task."}
	}
	return nil
}

// renew writes the new expiry (and the optional intent and resources) of the
// lease cur, which the caller has just read with its resources, and its
// lease_renewed event. It returns cur as the write leaves it, so no read follows.
func (s Store) renew(ctx context.Context, tx *sql.Tx, actorID, principal string, cur Lease, in Renew, task taskRow, now time.Time) (Lease, error) {
	ttl := cur.TTLSeconds
	if in.TTL != nil {
		ttl = *in.TTL
	}
	intent := cur.Intent
	if in.Intent != nil {
		intent = *in.Intent
	}
	resources := cur.Resources
	renewedAt, expiresAt := store.EncodeTime(now), store.EncodeTime(now.Add(time.Duration(ttl)*time.Second))
	d := s.Store.Dialect()
	_, err := tx.ExecContext(ctx, d.Rebind(`UPDATE leases SET ttl_seconds = ?, intent = ?, renewed_at = ?, expires_at = ? WHERE id = ?`),
		ttl, intent, renewedAt, expiresAt, cur.ID)
	if err != nil {
		return Lease{}, fmt.Errorf("renew lease: %w", err)
	}
	if in.Resources != nil {
		if err := s.setResources(ctx, tx, cur.ID, *in.Resources); err != nil {
			return Lease{}, err
		}
		resources = *in.Resources
		// the database's order is the one a read shows, so ask it
		if cur.Resources, err = s.resourcesOf(ctx, tx, cur.ID); err != nil {
			return Lease{}, err
		}
	}
	cur.TTLSeconds, cur.Intent = ttl, intent
	cur.RenewedAt, cur.ExpiresAt = store.DecodeTime(renewedAt), store.DecodeTime(expiresAt)
	return cur, audit.AppendAs(ctx, tx, d, actorID, now, audit.Event{
		Principal: principal, Type: EventRenewed, InitiativeID: task.InitiativeID, TaskID: task.ID, RefID: cur.ID,
		Summary: cur.Ref + " renewed on " + task.Ref,
		Data: map[string]any{"ref": cur.Ref, "task": task.Ref, "intent": intent, "ttl_seconds": ttl,
			"expires_at": cur.ExpiresAt.Format(time.RFC3339Nano), "resources": resources},
	})
}
