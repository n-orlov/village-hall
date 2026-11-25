package attention

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// checkEvent is the type of the event a watch check writes.
const checkEvent = tasks.EventCheck

func timeOf(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := store.DecodeTime(ms.Int64)
	return &t
}

func taskRef(slug sql.NullString, seq sql.NullInt64) string {
	if !slug.Valid {
		return ""
	}
	return domain.FormatTaskRef(slug.String, int(seq.Int64))
}

// --- open asks, newest first

func asksSection(sc scope) section {
	return scoped(sc, `COALESCE(t.initiative_id, k.initiative_id)`,
		`k.n, k.question, k.mode, k.blocking, k.poll_seen_at, k.created_at, ti.slug, t.seq, a.handle, a.way_back, ki.slug`,
		`asks k JOIN actors a ON a.id = k.actor_id LEFT JOIN tasks t ON t.id = k.task_id
		 LEFT JOIN initiatives ti ON ti.id = t.initiative_id
		 LEFT JOIN initiatives ki ON ki.id = k.initiative_id
		 WHERE k.state = 'open' AND (k.task_id IS NULL OR t.status <> 'closed')`,
		`k.created_at DESC, k.n DESC`)
}

func (s Store) openAsks(ctx context.Context, sc scope, now time.Time) ([]OpenAsk, error) {
	waiting := asks.Store{Store: s.Store, Now: func() time.Time { return now }}
	return collect(ctx, s, asksSection(sc), func(r rowScanner) (OpenAsk, error) {
		var (
			k           OpenAsk
			n           int64
			slug, wb    sql.NullString
			iniSlug     sql.NullString
			seq, polled sql.NullInt64
			created     int64
		)
		if err := r.Scan(&n, &k.Question, &k.Mode, &k.Blocking, &polled, &created, &slug, &seq, &k.Asker.Handle, &wb, &iniSlug); err != nil {
			return OpenAsk{}, err
		}
		k.Ref, k.Task, k.Asker.WayBack = domain.FormatAskRef(n), taskRef(slug, seq), wb.String
		k.Initiative = slug.String
		if !slug.Valid {
			k.Initiative = iniSlug.String
		}
		k.CreatedAt = store.DecodeTime(created)
		k.Age = now.Sub(k.CreatedAt)
		k.AskerWaiting = waiting.Waiting(asks.Ask{State: domain.AskOpen, Mode: k.Mode, PollSeenAt: timeOf(polled)})
		return k, nil
	})
}

// --- tasks done and not closed, most recently done first

func doneSection(sc scope) section {
	return scoped(sc, `t.initiative_id`,
		`t.seq, i.slug, t.title, t.done_at, da.handle, da.way_back, ll.n, la.handle, la.way_back, ll.outcome, ll.ended_at`,
		`tasks t JOIN initiatives i ON i.id = t.initiative_id
		 LEFT JOIN actors da ON da.id = t.done_by
		 LEFT JOIN leases ll ON ll.n = (SELECT l.n FROM leases l WHERE l.task_id = t.id AND l.state = 'ended'
		     ORDER BY l.ended_at DESC, l.n DESC LIMIT 1)
		 LEFT JOIN actors la ON la.id = ll.actor_id WHERE t.status = 'done'`,
		`t.done_at DESC, t.n DESC`)
}

func (s Store) doneNotClosed(ctx context.Context, sc scope) ([]DoneTask, error) {
	return collect(ctx, s, doneSection(sc), func(r rowScanner) (DoneTask, error) {
		var (
			d                      DoneTask
			seq                    int
			slug                   string
			done, leaseN, leaseEnd sql.NullInt64
			doneBy, doneWB, holder sql.NullString
			holderWB, outcome      sql.NullString
		)
		err := r.Scan(&seq, &slug, &d.Title, &done, &doneBy, &doneWB, &leaseN, &holder, &holderWB, &outcome, &leaseEnd)
		if err != nil {
			return DoneTask{}, err
		}
		d.Task = domain.FormatTaskRef(slug, seq)
		d.DoneBy = actor(doneBy.String, doneWB)
		if at := timeOf(done); at != nil {
			d.DoneAt = *at
		}
		if leaseN.Valid {
			ended := timeOf(leaseEnd)
			d.LastLease = &LastLease{Lease: domain.FormatLeaseRef(leaseN.Int64), Holder: actor(holder.String, holderWB), Outcome: outcome.String}
			if ended != nil {
				d.LastLease.EndedAt = *ended
			}
		}
		return d, nil
	})
}

// --- expired leases nobody acknowledged and nobody replaced, latest expiry first

func expiredSection(sc scope) section {
	return scoped(sc, `t.initiative_id`,
		`l.n, i.slug, t.seq, t.title, l.intent, l.renewed_at, l.expires_at, a.handle, a.way_back, l.notice`,
		`leases l JOIN tasks t ON t.id = l.task_id JOIN initiatives i ON i.id = t.initiative_id
		 JOIN actors a ON a.id = l.actor_id
		 WHERE l.state = 'expired' AND l.acked_at IS NULL AND t.status <> 'closed'
		 AND NOT EXISTS (SELECT 1 FROM leases later WHERE later.task_id = l.task_id
		     AND (later.started_at > l.started_at OR (later.started_at = l.started_at AND later.id > l.id)))`,
		`l.expires_at DESC, l.n DESC`)
}

func (s Store) expiredLeases(ctx context.Context, sc scope) ([]ExpiredLease, error) {
	return collect(ctx, s, expiredSection(sc), func(r rowScanner) (ExpiredLease, error) {
		var (
			l                ExpiredLease
			n                int64
			slug             string
			seq              int
			renewed, expires int64
			wb               sql.NullString
		)
		if err := r.Scan(&n, &slug, &seq, &l.Title, &l.Intent, &renewed, &expires, &l.Holder.Handle, &wb, &l.Notice); err != nil {
			return ExpiredLease{}, err
		}
		l.Lease, l.Task, l.Holder.WayBack = domain.FormatLeaseRef(n), domain.FormatTaskRef(slug, seq), wb.String
		l.RenewedAt, l.ExpiredAt = store.DecodeTime(renewed), store.DecodeTime(expires)
		return l, nil
	})
}

// --- overdue watches, the most recently due first

func watchesSection(sc scope, now time.Time) section {
	return scoped(sc, `t.initiative_id`,
		`t.seq, i.slug, t.title, t.next_check_at, oa.handle, oa.way_back, e.at, e.data, ca.handle, ca.way_back`,
		`tasks t JOIN initiatives i ON i.id = t.initiative_id JOIN actors oa ON oa.id = t.created_by
		 LEFT JOIN events e ON e.n = (SELECT MAX(c.n) FROM events c WHERE c.task_id = t.id AND c.type = '`+checkEvent+`')
		 LEFT JOIN actors ca ON ca.id = e.actor_id
		 WHERE t.kind = 'watch' AND t.status <> 'closed' AND t.next_check_at IS NOT NULL AND t.next_check_at < ?`,
		`t.next_check_at DESC, t.n DESC`, store.EncodeBound(now))
}

func (s Store) overdueWatches(ctx context.Context, sc scope, now time.Time) ([]OverdueWatch, error) {
	return collect(ctx, s, watchesSection(sc, now), func(r rowScanner) (OverdueWatch, error) {
		var (
			w               OverdueWatch
			seq, next       int
			slug            string
			at              sql.NullInt64
			owner, data, by sql.NullString
			ownerWB, byWB   sql.NullString
		)
		if err := r.Scan(&seq, &slug, &w.Title, &next, &owner, &ownerWB, &at, &data, &by, &byWB); err != nil {
			return OverdueWatch{}, err
		}
		w.Task, w.Owner = domain.FormatTaskRef(slug, seq), actor(owner.String, ownerWB)
		w.NextCheckAt = store.DecodeTime(int64(next))
		if !at.Valid {
			return w, nil
		}
		check, err := lastCheck(*timeOf(at), data.String, actor(by.String, byWB))
		w.LastCheck = &check
		return w, err
	})
}

// lastCheck is the check an event row of type check_recorded describes.
func lastCheck(at time.Time, data string, by Actor) (LastCheck, error) {
	var body struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(data), &body); err != nil {
		return LastCheck{}, fmt.Errorf("read check data: %w", err)
	}
	return LastCheck{Summary: body.Summary, By: by, At: at}, nil
}
