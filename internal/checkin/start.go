package checkin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// NewTask is the task a start creates and starts on: the slug or ULID of an
// open initiative, and the title.
type NewTask struct {
	Initiative string
	Title      string
	// Tags are the new task's own tags, under the initiative tag rule.
	Tags []string
}

// StartRequest is a composite start (SPEC 8.4): the lease of leases.Start on
// either an existing Task or the NewTask it creates, never both. A nil TTL is
// the default and nil Resources holds none.
type StartRequest struct {
	Task      string
	NewTask   *NewTask
	Intent    string
	TTL       *int64
	Resources []string
	// Notice starts a notice lease (SPEC 4.4) instead of an ordinary one.
	Notice bool
}

// StartResult is what a composite start answers: the lease with its conflicts,
// whether it created the task, the actor's check-in after the write, the
// protocol version the server embeds and the one the actor has read.
type StartResult struct {
	Lease        leases.Result
	TaskCreated  bool
	Now          Result
	ProtocolSeen int
}

// Start starts the actor's lease, creating the task first when the request
// names a new one. The task, the lease and their events rows are written in one
// transaction: a request that fails anywhere writes nothing. The check-in and
// the actor's protocol_seen are read after the commit. It returns the errors of
// leases.Start (ErrInvalid, ErrNotFound, ErrConflict) and of tasks.Create
// (ErrInvalid, ErrNoInitiative, ErrInitiativeClosed). The request needs
// Tasks, Leases and the other stores of a check-in.
func (s Store) Start(ctx context.Context, actor audit.Actor, principal string, in StartRequest) (StartResult, error) {
	if (in.Task == "") == (in.NewTask == nil) {
		return StartResult{}, leases.InvalidError{Msg: "a start needs a task or a new_task, and not both",
			Next: "Send {\"task\": \"<ref>\", \"intent\": \"...\"} or {\"new_task\": {\"initiative\": \"<slug>\", \"title\": \"...\"}, \"intent\": \"...\"}."}
	}
	var out StartResult
	tx, err := s.Leases.Store.Begin(ctx)
	if err != nil {
		return StartResult{}, fmt.Errorf("begin: %w", err)
	}
	if out.Lease, out.TaskCreated, err = s.write(ctx, tx.Tx, actor, principal, in); err != nil {
		_ = tx.Rollback()
		return StartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StartResult{}, fmt.Errorf("commit: %w", err)
	}
	if out.Now, err = s.Check(ctx, actor.Handle, nil); err != nil {
		return StartResult{}, err
	}
	out.ProtocolSeen, err = s.protocolSeen(ctx, actor.Handle)
	return out, err
}

// write creates the task a new_task names and starts the lease, in tx.
func (s Store) write(ctx context.Context, tx *sql.Tx, actor audit.Actor, principal string, in StartRequest) (leases.Result, bool, error) {
	created := in.NewTask != nil
	if created {
		t, err := s.Tasks.CreateIn(ctx, tx, actor, principal, tasks.New{Initiative: in.NewTask.Initiative, Title: in.NewTask.Title, Tags: in.NewTask.Tags})
		if err != nil {
			return leases.Result{}, false, err
		}
		in.Task = t.Ref
	}
	res, err := s.Leases.StartIn(ctx, tx, actor, principal, leases.Start{Task: in.Task, Intent: in.Intent, TTL: in.TTL, Resources: in.Resources, Notice: in.Notice})
	return res, created, err
}

// protocolSeen is the highest agent protocol version the actor with the handle
// has read: 0 when it has recorded none.
func (s Store) protocolSeen(ctx context.Context, handle string) (int, error) {
	st := s.Leases.Store
	var seen sql.NullInt64
	err := st.ReaderFor(ctx).QueryRowContext(ctx, st.Dialect().Rebind(`SELECT protocol_seen FROM actors WHERE handle = ?`), handle).Scan(&seen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read protocol seen: %w", err)
	}
	return int(seen.Int64), nil
}
