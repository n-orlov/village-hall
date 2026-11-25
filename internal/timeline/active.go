package timeline

import (
	"time"

	"github.com/n-orlov/village-hall/internal/attention"
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
func (f *filter) activeFilter(q Query, d store.Dialect, now time.Time, span time.Duration) {
	if !q.Active {
		return
	}
	list := `SELECT al.task_id FROM leases al WHERE al.state = 'live' AND al.expires_at >= ?` +
		` UNION ALL SELECT ae.task_id FROM events ae WHERE ae.at >= ?`
	args := []any{store.EncodeBound(now), store.EncodeBound(now.Add(-span))}
	if sql, a, ok := store.TaskIDsOfRefs(d, q.Attention); ok {
		list += ` UNION ALL ` + sql
		args = append(args, a)
	}
	f.add(`t.id IN (`+list+`)`, args...)
}
