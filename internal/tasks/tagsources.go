package tasks

import (
	"context"
	"fmt"
	"strings"

	"github.com/n-orlov/village-hall/internal/domain"
)

// TagSources answers, for the tasks the status keeps (the list's status filter, not_closed when empty), which
// sources each tag has on them: task (an own tag), initiative (a tag of the task's initiative) and actor (the
// derived kind of a holder of a lease on it). The sources of a tag are in domain order (task, initiative, actor)
// and a tag no kept task carries is left out. With tags given, only those tags are answered (trimmed and
// lowercased as a task tag is, the tag grammar not enforced: a tag nothing carries has no entry); with none, every
// tag is. It runs three statements whatever the number of tags.
func (s Store) TagSources(ctx context.Context, status string, tags []string) (map[string][]string, error) {
	f := &filter{now: s.now(), dialect: s.Store.Dialect()}
	if err := f.statusAndKind(Query{Status: status}); err != nil {
		return nil, err
	}
	where := f.where()
	want := map[string]bool{}
	for _, tag := range tags {
		want[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	has := map[string]map[string]bool{}
	note := func(tag, source string) {
		if len(want) > 0 && !want[tag] {
			return
		}
		if has[tag] == nil {
			has[tag] = map[string]bool{}
		}
		has[tag][source] = true
	}
	q := s.Store.ReaderFor(ctx)
	for _, part := range []struct{ source, query string }{
		{domain.TagSourceTask, `SELECT DISTINCT g.tag FROM task_tags g JOIN tasks t ON t.id = g.task_id` + where},
		{domain.TagSourceInitiative, `SELECT DISTINCT g.tag FROM initiative_tags g JOIN tasks t ON t.initiative_id = g.initiative_id` + where},
		{domain.TagSourceActor, `SELECT DISTINCT a.handle FROM leases l JOIN actors a ON a.id = l.actor_id JOIN tasks t ON t.id = l.task_id` + where},
	} {
		rows, err := q.QueryContext(ctx, s.Store.Dialect().Rebind(part.query), f.args...)
		if err != nil {
			return nil, fmt.Errorf("read tag sources: %w", err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("read tag sources: %w", err)
			}
			if part.source == domain.TagSourceActor {
				if v = domain.ActorKind(v); v == "" {
					continue
				}
			}
			note(v, part.source)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("read tag sources: %w", err)
		}
	}
	out := make(map[string][]string, len(has))
	for tag, sources := range has {
		for _, source := range []string{domain.TagSourceTask, domain.TagSourceInitiative, domain.TagSourceActor} {
			if sources[source] {
				out[tag] = append(out[tag], source)
			}
		}
	}
	return out, nil
}
