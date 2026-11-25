package store

import (
	"encoding/json"

	"github.com/n-orlov/village-hall/internal/domain"
)

// TaskIDsOfRefs is a SELECT of the ids of the tasks the refs name, written to sit inside `t.id IN (...)`, with its one
// parameter, a JSON array of the (slug, seq) of each ref. Each ref is looked up as the (initiative slug, seq) it is,
// through the unique slug and the (initiative_id, seq) key, so the read starts from the refs instead of computing the
// ref text of every task and scanning for it; it is one parameter however many refs there are. A ref counts only when
// it is exactly the short ref of a task (`<slug>-<seq>`, the slug as stored, no leading zero): a ULID, another letter
// case, a leading zero or no number names no task, as it never equalled the computed ref text. ok is false when no
// ref can name a task, and then there is nothing to select.
func TaskIDsOfRefs(d Dialect, refs []string) (sql string, param string, ok bool) {
	var pairs [][2]any
	for _, ref := range refs {
		r, err := domain.ParseTaskRef(ref)
		if err != nil || r.ID != "" || domain.FormatTaskRef(r.Slug, r.Seq) != ref {
			continue
		}
		pairs = append(pairs, [2]any{r.Slug, r.Seq})
	}
	if len(pairs) == 0 {
		return "", "", false
	}
	b, _ := json.Marshal(pairs) // strings and integers always marshal
	return `SELECT rt.id FROM (` + d.RefSet() + `) AS r JOIN initiatives ri ON ri.slug = r.slug` +
		` JOIN tasks rt ON rt.initiative_id = ri.id AND rt.seq = r.seq`, string(b), true
}
