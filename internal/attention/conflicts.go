package attention

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
)

// conflicts lists the resources that two or more live leases hold, the one with
// the newest lease first. A conflict is kept when any of its leases is on a task
// the scope keeps.
func (s Store) conflicts(ctx context.Context, sc scope) ([]Conflict, error) {
	live, err := (leases.Store{Store: s.Store, Now: s.Now}).Live(ctx)
	if err != nil {
		return nil, err
	}
	kept, err := s.keptTasks(ctx, sc)
	if err != nil {
		return nil, err
	}
	return contested(live, kept), nil
}

// conflictCount is how many conflicts the scope keeps, in one statement: the
// resources that two or more live leases hold, where (with a scope) at least one
// of those leases is on a task the scope keeps. A notice lease is no part of a
// conflict (SPEC 5.2), so it is not counted. It reads no lease and needs no way
// backs.
func (s Store) conflictCount(ctx context.Context, sc scope, now time.Time) (int, error) {
	cond, scopeArgs := sc.and(`t.initiative_id`)
	query := `SELECT COUNT(*) FROM (SELECT lr.resource FROM lease_resources lr
		JOIN leases l ON l.id = lr.lease_id JOIN tasks t ON t.id = l.task_id
		WHERE l.state = 'live' AND l.expires_at >= ? AND l.notice = ? GROUP BY lr.resource
		HAVING COUNT(*) > 1`
	if cond != "" {
		query += ` AND SUM(CASE WHEN 1 = 1` + cond + ` THEN 1 ELSE 0 END) > 0`
	}
	query += `) shared`
	var n int
	err := s.Store.ReaderFor(ctx).QueryRowContext(ctx, s.Store.Dialect().Rebind(query),
		append([]any{store.EncodeBound(now), false}, scopeArgs...)...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count attention conflicts: %w", err)
	}
	return n, nil
}

// contested groups the leases by resource and returns the resources with two or
// more of them. kept is nil for no scope.
func contested(live []leases.Lease, kept map[string]bool) []Conflict {
	byResource := map[string][]leases.Lease{}
	for _, l := range live {
		if l.Notice {
			continue
		}
		for _, r := range l.Resources {
			byResource[r] = append(byResource[r], l)
		}
	}
	out := []Conflict{}
	for resource, held := range byResource {
		if len(held) > 1 && inScope(held, kept) {
			out = append(out, conflictOf(resource, held))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if ni, nj := newest(byResource[out[i].Resource]), newest(byResource[out[j].Resource]); ni != nj {
			return ni > nj
		}
		return out[i].Resource < out[j].Resource
	})
	return out
}

func inScope(held []leases.Lease, kept map[string]bool) bool {
	if kept == nil {
		return true
	}
	for _, l := range held {
		if kept[l.TaskID] {
			return true
		}
	}
	return false
}

// newest is the highest lease number among held.
func newest(held []leases.Lease) int64 {
	var n int64
	for _, l := range held {
		n = max(n, l.N)
	}
	return n
}

// conflictOf is the conflict on resource that the leases held hold, in lease-number order.
func conflictOf(resource string, held []leases.Lease) Conflict {
	c := Conflict{Resource: resource, Holders: []Holder{}, Tasks: []string{}}
	seen := map[string]bool{}
	for _, l := range held {
		c.Holders = append(c.Holders, Holder{Lease: l.Ref, Actor: Actor{Handle: l.Actor, WayBack: l.WayBack},
			Task: l.Task, Intent: l.Intent, ExpiresAt: l.ExpiresAt})
		if l.StartedAt.After(c.Since) {
			c.Since = l.StartedAt
		}
		if !seen[l.Task] {
			seen[l.Task] = true
			c.Tasks = append(c.Tasks, l.Task)
		}
	}
	return c
}

// keptTasks is the set of ids of the tasks the scope keeps, nil for an empty scope.
func (s Store) keptTasks(ctx context.Context, sc scope) (map[string]bool, error) {
	if sc.empty() {
		return nil, nil
	}
	cond, args := sc.and(`t.initiative_id`)
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, s.Store.Dialect().Rebind(`SELECT t.id FROM tasks t WHERE 1 = 1`+cond), args...)
	if err != nil {
		return nil, fmt.Errorf("read attention scope: %w", err)
	}
	defer func() { _ = rows.Close() }()
	kept := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read attention scope: %w", err)
		}
		kept[id] = true
	}
	return kept, rows.Err()
}
