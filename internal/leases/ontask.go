package leases

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/store"
)

// LiveOnTaskIn lists the live leases on the task taskID inside the transaction
// tx, oldest first, after expiring the ones that are due (each with its one
// lease_expired event), so a lease past its expiry is never listed. It is for
// a task transition that must see, and end, the leases in its own transaction.
func (s Store) LiveOnTaskIn(ctx context.Context, tx *sql.Tx, taskID string, now time.Time) ([]Lease, error) {
	if _, err := s.expireDue(ctx, tx, now); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, s.Store.Dialect().Rebind(selectLease+` WHERE l.task_id = ? AND l.state = 'live' AND l.expires_at >= ? ORDER BY l.n`),
		taskID, store.EncodeBound(now))
	if err != nil {
		return nil, fmt.Errorf("read leases: %w", err)
	}
	out, err := scanAll(rows)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EndIn ends the live lease cur inside the transaction tx with the outcome and
// appends its lease_ended event. With byCaller, ended_by is the actor row
// actorID and the event data names handle, whoever holds the lease (a forced
// done, a close); without it neither is recorded (the holder's own done). The
// caller has read cur live in the same transaction.
func (s Store) EndIn(ctx context.Context, tx *sql.Tx, actorID, principal string, cur Lease, outcome, handle string, now time.Time, byCaller bool) error {
	return s.writeEnd(ctx, tx, actorID, principal, cur, outcome, handle, now, byCaller)
}

// HeldIn is the live lease the actor with the handle holds on a task, read in
// the transaction tx after expiring the leases that are due. ref names the task
// (a ref or ULID); an empty ref means the only task the actor holds a live lease
// on. The task's row is locked before the lease is judged, so a done or close
// that ends the lease first is seen. It returns ErrNotFound for an unknown task
// and ErrConflict, with a Next, for an actor that holds no live lease (on that
// task, or at all) and, when ref is empty, for one that holds several: the Next
// lists them.
func (s Store) HeldIn(ctx context.Context, tx *sql.Tx, handle, ref string) (Lease, error) {
	now := s.now()
	taskID, ref, err := s.heldTask(ctx, tx, handle, ref, now)
	if err != nil {
		return Lease{}, err
	}
	held, err := s.heldBy(ctx, tx, handle, now)
	if err != nil {
		return Lease{}, err
	}
	for _, l := range held {
		if l.TaskID == taskID {
			return l, nil
		}
	}
	return Lease{}, ConflictError{Msg: "you hold no live lease on task " + ref,
		Next: "Start one first with vh start " + ref + " \"<intent>\", or name a task you hold a lease on."}
}

// heldTask locks the task the finish is on and returns its id and ref: the one
// ref names, or the only one the actor holds a live lease on.
func (s Store) heldTask(ctx context.Context, tx *sql.Tx, handle, ref string, now time.Time) (string, string, error) {
	if ref != "" {
		t, err := s.lockTask(ctx, tx, ref)
		return t.ID, t.Ref, err
	}
	held, err := s.heldBy(ctx, tx, handle, now)
	if err != nil {
		return "", "", err
	}
	switch len(held) {
	case 0:
		return "", "", ConflictError{Msg: "you hold no live lease",
			Next: "Start one first with vh start <task> \"<intent>\", or name the task you meant."}
	case 1:
		t, err := s.lockTask(ctx, tx, held[0].TaskID)
		return t.ID, t.Ref, err
	}
	parts := make([]string, len(held))
	for i, l := range held {
		parts[i] = l.Task + " (" + l.Ref + ")"
	}
	return "", "", ConflictError{Msg: fmt.Sprintf("you hold %d live leases", len(held)),
		Next: "Name the task to finish: " + strings.Join(parts, ", ") + "."}
}

// heldBy lists the live leases of the actor with the handle, oldest first,
// after expiring the ones that are due (each with its one lease_expired event).
func (s Store) heldBy(ctx context.Context, tx *sql.Tx, handle string, now time.Time) ([]Lease, error) {
	if _, err := s.expireDue(ctx, tx, now); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, s.Store.Dialect().Rebind(selectLease+` WHERE a.handle = ? AND l.state = 'live' AND l.expires_at >= ? ORDER BY l.n`),
		handle, store.EncodeBound(now))
	if err != nil {
		return nil, fmt.Errorf("read leases: %w", err)
	}
	return scanAll(rows)
}

// EndHeldIn ends the live lease l, which the actor holds and HeldIn read in tx,
// with the outcome and appends its lease_ended event. It commits nothing.
func (s Store) EndHeldIn(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal string, l Lease, outcome string) error {
	now := s.now()
	actorID, err := audit.UpsertActor(ctx, tx, s.Store.Dialect(), actor, now)
	if err != nil {
		return err
	}
	return s.writeEnd(ctx, tx, actorID, principal, l, outcome, actor.Handle, now, false)
}

// RenewHeldIn renews every live lease the actor with the handle holds, inside
// the transaction tx, to now plus its own ttl, each with its lease_renewed
// event, and returns them as renewed, oldest first. It commits nothing. It
// returns ErrConflict, with a Next, for an actor that holds no live lease, and
// writes nothing then.
func (s Store) RenewHeldIn(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal string) ([]Lease, error) {
	now := s.now()
	held, err := s.heldBy(ctx, tx, actor.Handle, now)
	if err != nil {
		return nil, err
	}
	if len(held) == 0 {
		return nil, ConflictError{Msg: "you hold no live lease",
			Next: "Start one first with vh start <task> \"<intent>\"."}
	}
	actorID, err := audit.UpsertActor(ctx, tx, s.Store.Dialect(), actor, now)
	if err != nil {
		return nil, err
	}
	return s.renewEach(ctx, tx, actorID, principal, held, now)
}

// renewEach renews each of the held leases in tx and returns them as renewed.
func (s Store) renewEach(ctx context.Context, tx *sql.Tx, actorID string, principal string, held []Lease, now time.Time) ([]Lease, error) {
	out := make([]Lease, len(held))
	for i, l := range held {
		task, err := s.lockTask(ctx, tx, l.TaskID)
		if err != nil {
			return nil, err
		}
		cur, err := s.find(ctx, tx, l.ID)
		if err != nil {
			return nil, err
		}
		if out[i], err = s.renew(ctx, tx, actorID, principal, cur, Renew{}, task, now); err != nil {
			return nil, err
		}
	}
	return out, nil
}
