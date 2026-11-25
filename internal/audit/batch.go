package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
)

// Written is one event of a batch with the actor row that makes it and that
// row's own origin as the caller already read it (the actor's id when it is an
// operator row, else its origin column; empty for none), so the batch needs no
// lookup of its own.
type Written struct {
	ActorID     string
	ActorOrigin string
	Event       Event
}

// AppendBatch appends the events, each made by its own actor row, in tx, with the
// same columns AppendAsID writes, and returns their ids in order. It runs a
// number of statements that does not depend on how many events there are (one
// INSERT per batchRows events, one more to name the origin the request
// carries), where AppendAsID costs a few per event.
func AppendBatch(ctx context.Context, tx *sql.Tx, d store.Dialect, now time.Time, events []Written) ([]string, error) {
	if len(events) == 0 {
		return nil, nil
	}
	requestOrigin, fromRequest, err := requestOrigin(ctx, tx, d)
	if err != nil {
		return nil, fmt.Errorf("record events: %w", err)
	}
	if err := lockEvents(ctx, tx, d); err != nil {
		return nil, fmt.Errorf("record events: %w", err)
	}
	ids := make([]string, len(events))
	args := make([]any, 0, len(events)*len(eventColumns))
	for i, w := range events {
		row, err := batchRow(now, w, requestOrigin, fromRequest)
		if err != nil {
			return nil, err
		}
		ids[i] = row[0].(string)
		args = append(args, row...)
	}
	if err := store.InsertMany(ctx, tx, d, "events", eventColumns, args); err != nil {
		return nil, fmt.Errorf("record events: %w", err)
	}
	return ids, nil
}

// batchRow is the column values of one event of a batch.
func batchRow(now time.Time, w Written, requestOrigin any, fromRequest bool) ([]any, error) {
	id, err := domain.NewULID(now)
	if err != nil {
		return nil, err
	}
	data := w.Event.Data
	if data == nil {
		data = map[string]any{}
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("record events: %w", err)
	}
	origin := requestOrigin
	if !fromRequest {
		origin = nullable(w.ActorOrigin)
	}
	e := w.Event
	return []any{id, store.EncodeTime(now), w.ActorID, e.Principal, nullable(e.OnBehalfOfID), nullable(e.InitiativeID),
		nullable(e.TaskID), e.Type, nullable(e.RefID), e.Summary, string(payload), origin}, nil
}
