package timeline

import (
	"strings"
	"time"

	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/store"
)

// activeSpan is how far back an event makes a task active, for the window the
// query names (8h, 24h by default, 3d). It is InvalidError for any other text,
// whether or not the query asks for active tasks: a window that does not read
// is refused as every other parameter is.
func activeSpan(q Query) (time.Duration, error) {
	window := q.Window
	if window == "" {
		window = attention.DefaultWindow
	}
	span, ok := attention.WindowSpan(window)
	if !ok {
		return 0, InvalidError{Msg: "window " + window + " is not 8h, 24h or 3d", Next: attention.NextWindow}
	}
	return span, nil
}

// activeFilter keeps only the active tasks when q asks for them: a task with a
// live lease, or one of q.Attention (the refs of the tasks that carry any
// attention reason), or with an event not older than the window.
//
// The active tasks are one id list, built from the leases and the events inside
// the window by index range reads, so the task read starts from those ids
// instead of asking every task whether it is active.
func (f *filter) activeFilter(q Query, now time.Time, span time.Duration) {
	if !q.Active {
		return
	}
	list := `SELECT al.task_id FROM leases al WHERE al.state = 'live' AND al.expires_at >= ?` +
		` UNION ALL SELECT ae.task_id FROM events ae WHERE ae.at >= ?`
	args := []any{store.EncodeBound(now), store.EncodeBound(now.Add(-span))}
	if refs := attentionRefs(q.Attention); len(refs) > 0 {
		list += ` UNION ALL SELECT rt.id FROM (VALUES ` + strings.TrimSuffix(strings.Repeat("(CAST(? AS TEXT), CAST(? AS INTEGER)), ", len(refs)), ", ") + `) AS r` +
			` JOIN initiatives ri ON ri.slug = r.column1 JOIN tasks rt ON rt.initiative_id = ri.id AND rt.seq = r.column2`
		for _, ref := range refs {
			args = append(args, ref.Slug, ref.Seq)
		}
	}
	f.add(`t.id IN (`+list+`)`, args...)
}

// attentionRefs are the refs of q.Attention as the (slug, seq) of the task each
// names, so the filter looks them up by the (initiative, seq) index. A ref that
// is not exactly the short ref of a task (a ULID, another case, a leading zero,
// no number) names no task, as it never matched the computed ref text.
func attentionRefs(refs []string) []domain.TaskRef {
	var out []domain.TaskRef
	for _, ref := range refs {
		if r, err := domain.ParseTaskRef(ref); err == nil && r.ID == "" && domain.FormatTaskRef(r.Slug, r.Seq) == ref {
			out = append(out, r)
		}
	}
	return out
}
