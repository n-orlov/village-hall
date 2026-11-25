package notify

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

var columns = []string{"id", "kind", "event_id", "payload", "state", "attempts", "next_attempt_at", "created_at"}

// Enqueue adds a pending notification of kind for the event eventID, with the payload of BuildPayload, in tx, when
// alerts are on and VH_NOTIFY_ON names the kind. It writes nothing otherwise.
// The outbox is unique on (event, kind), so an event never enqueues a kind twice.
// Once the write has committed (the unit's commit under ctx's unit of work,
// else the Commit of tx, which must come from Store.Begin) the delivery worker is
// woken, so the row is not left for the backstop tick.
func Enqueue(ctx context.Context, tx *sql.Tx, st *store.Store, eventID, kind string, now time.Time) error {
	if !st.Notifies(kind) {
		return nil
	}
	id, err := domain.NewULID(now)
	if err != nil {
		return err
	}
	payload, err := BuildPayload(ctx, tx, st, eventID, kind, st.PublicURL())
	if err != nil {
		return err
	}
	at := store.EncodeTime(now)
	_, err = tx.ExecContext(ctx, st.Dialect().Rebind(st.Dialect().Insert("notifications", columns)),
		id, kind, eventID, string(payload), "pending", 0, at, at)
	if err != nil {
		return fmt.Errorf("enqueue %s notification: %w", kind, err)
	}
	st.AfterTxCommit(ctx, tx, st.WakeNotify)
	return nil
}

// Stranded is a lease_expired event the caller has already read everything
// about: the event, the lease and the task it was on, the holder, and whether
// the lease is the newest on its task.
type Stranded struct {
	EventID   string
	Newest    bool
	Holder    Actor
	LeaseRef  string
	Intent    string
	ExpiredAt time.Time
	TaskRef   string
	TaskTitle string
}

// StrandedPayload is the JSON of the stranded alert of s, the same document
// BuildPayload reads from the database for the event of s. publicURL is
// VH_PUBLIC_URL (empty when unset).
func StrandedPayload(s Stranded, publicURL string) ([]byte, error) {
	p := Payload{Kind: store.NotifyStranded, Actor: s.Holder, Task: &TaskInfo{Ref: s.TaskRef, Title: s.TaskTitle},
		Lease: &LeaseInfo{Ref: s.LeaseRef, Intent: s.Intent, ExpiredAt: s.ExpiredAt.UTC().Format(time.RFC3339Nano)}}
	if base := strings.TrimRight(publicURL, "/"); base != "" {
		p.BoardURL = base + "/"
		p.Task.URL = base + "/#/t/" + s.TaskRef
	}
	p.Text = p.text()
	return json.Marshal(p)
}

// EnqueueStrandedBatch enqueues the stranded notification of each lease_expired
// event in list whose lease is the newest on its task, when alerts are on and
// VH_NOTIFY_ON names stranded. It runs a number of statements that does not
// depend on how many events there are, nor on how many of them are the newest
// on their task: exactly one INSERT (store.InsertMany), even when none is.
func EnqueueStrandedBatch(ctx context.Context, tx *sql.Tx, st *store.Store, list []Stranded, now time.Time) error {
	if !st.Notifies(store.NotifyStranded) {
		return nil
	}
	args, err := strandedArgs(st, list, now)
	if err != nil {
		return err
	}
	if err := store.InsertMany(ctx, tx, st.Dialect(), "notifications", columns, args); err != nil {
		return fmt.Errorf("enqueue %s notifications: %w", store.NotifyStranded, err)
	}
	if len(args) > 0 {
		st.AfterTxCommit(ctx, tx, st.WakeNotify)
	}
	return nil
}

// strandedArgs is the INSERT arguments of the stranded notification of each event in list whose lease is the
// newest on its task.
func strandedArgs(st *store.Store, list []Stranded, now time.Time) ([]any, error) {
	var args []any
	at := store.EncodeTime(now)
	for _, s := range list {
		if !s.Newest {
			continue
		}
		id, err := domain.NewULID(now)
		if err != nil {
			return nil, err
		}
		payload, err := StrandedPayload(s, st.PublicURL())
		if err != nil {
			return nil, err
		}
		args = append(args, id, store.NotifyStranded, s.EventID, string(payload), "pending", 0, at, at)
	}
	return args, nil
}
