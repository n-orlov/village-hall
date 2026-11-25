package checkin

import (
	"context"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/tasks"
)

const (
	// NotesLimit is the most note summaries a check-in lists.
	NotesLimit = 20
	// AnswerWindow is how long an answered ask stays in my_asks without the
	// notes cursor having passed its answer.
	AnswerWindow = 24 * time.Hour
)

// Store reads the five stores a check-in is made of. Now is the clock; nil
// means time.Now.
type Store struct {
	Leases    *leases.Store
	Asks      *asks.Store
	Notes     *notes.Store
	Cursors   *cursors.Store
	Attention *attention.Store
	// Tasks is read by Start (the store a new_task is created in) and by a
	// finish that marks the task done.
	Tasks *tasks.Store
	Now   func() time.Time
}

// Result is a check-in: the lease and conflict parts, the actor's asks (open
// ones and recently answered ones), the notes that concern it after its notes
// cursor (at most NotesLimit, oldest first, without bodies) with how many more
// there are, and the attention counts of the whole board.
type Result struct {
	Leases    leases.Now
	MyAsks    []asks.Ask
	Notes     []notes.Note
	NotesMore int
	Counts    attention.Counts
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Check answers the check-in of the actor with the handle me, with queried
// resources to check before starting. It expires the due leases (the only write)
// and moves no cursor.
func (s Store) Check(ctx context.Context, me string, queried []string) (Result, error) {
	var (
		r   Result
		err error
	)
	if r.Leases, err = s.Leases.CheckIn(ctx, me, queried); err != nil {
		return Result{}, err
	}
	cursor, err := s.Cursors.Get(ctx, me, cursors.StreamNotes)
	if err != nil {
		return Result{}, err
	}
	if r.MyAsks, err = s.myAsks(ctx, me, cursor); err != nil {
		return Result{}, err
	}
	if r.Notes, r.NotesMore, err = s.Notes.Concerning(ctx, concern(me, cursor, held(me, r.Leases)), NotesLimit); err != nil {
		return Result{}, err
	}
	r.Counts, err = s.Attention.Counts(ctx, attention.Query{})
	return r, err
}

// held is the live leases of the actor me, notices included: a note on the task
// of a notice the actor holds concerns it like one on any other lease it holds.
func held(me string, n leases.Now) []leases.Lease {
	out := append([]leases.Lease{}, n.Mine...)
	for _, l := range n.Notices {
		if l.Actor == me {
			out = append(out, l)
		}
	}
	return out
}

// concern is what the notes of a check-in must concern: the tasks and resources
// of the actor's live leases, its handle, and every broadcast.
func concern(me, cursor string, mine []leases.Lease) notes.Concern {
	c := notes.Concern{Handle: me, After: cursor}
	seen := map[string]bool{}
	for _, l := range mine {
		c.TaskIDs = append(c.TaskIDs, l.TaskID)
		for _, res := range l.Resources {
			if !seen[res] {
				seen[res] = true
				c.Resources = append(c.Resources, res)
			}
		}
	}
	return c
}

// myAsks lists the actor's open asks and those answered since the notes cursor
// note was posted or in the last AnswerWindow, whichever reaches further back.
func (s Store) myAsks(ctx context.Context, me, cursor string) ([]asks.Ask, error) {
	since := s.now().Add(-AnswerWindow)
	if cursor != "" {
		n, err := s.Notes.Get(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if n.CreatedAt.Before(since) {
			since = n.CreatedAt
		}
	}
	return s.Asks.Mine(ctx, me, since)
}
