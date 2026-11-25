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
	has := newTagSourceSet(tags)
	for _, part := range []struct{ source, query string }{
		{domain.TagSourceTask, `SELECT DISTINCT g.tag FROM task_tags g JOIN tasks t ON t.id = g.task_id` + where},
		{domain.TagSourceInitiative, `SELECT DISTINCT g.tag FROM initiative_tags g JOIN tasks t ON t.initiative_id = g.initiative_id` + where},
		{domain.TagSourceActor, `SELECT DISTINCT a.handle FROM leases l JOIN actors a ON a.id = l.actor_id JOIN tasks t ON t.id = l.task_id` + where},
	} {
		if err := s.readTagSource(ctx, has, part.source, s.Store.Dialect().Rebind(part.query), f.args); err != nil {
			return nil, err
		}
	}
	return has.ordered(), nil
}

// tagSourceSet collects which sources each tag has, keeping only the wanted tags (all when none are given).
type tagSourceSet struct {
	want map[string]bool
	has  map[string]map[string]bool
}

func newTagSourceSet(tags []string) *tagSourceSet {
	want := map[string]bool{}
	for _, tag := range tags {
		want[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	return &tagSourceSet{want: want, has: map[string]map[string]bool{}}
}

func (t *tagSourceSet) note(tag, source string) {
	if len(t.want) > 0 && !t.want[tag] {
		return
	}
	if t.has[tag] == nil {
		t.has[tag] = map[string]bool{}
	}
	t.has[tag][source] = true
}

// ordered lists each tag's sources in domain order (task, initiative, actor).
func (t *tagSourceSet) ordered() map[string][]string {
	out := make(map[string][]string, len(t.has))
	for tag, sources := range t.has {
		for _, source := range []string{domain.TagSourceTask, domain.TagSourceInitiative, domain.TagSourceActor} {
			if sources[source] {
				out[tag] = append(out[tag], source)
			}
		}
	}
	return out
}

// readTagSource runs one of the three statements and notes each value it returns as a tag with the source.
func (s Store) readTagSource(ctx context.Context, into *tagSourceSet, source, query string, args []any) error {
	rows, err := s.Store.ReaderFor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("read tag sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("read tag sources: %w", err)
		}
		if source == domain.TagSourceActor {
			if v = domain.ActorKind(v); v == "" {
				continue
			}
		}
		into.note(v, source)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read tag sources: %w", err)
	}
	return nil
}
