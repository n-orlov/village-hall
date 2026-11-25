package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/timeline"
)

func (p problemResponse) VisitGetTimelineResponse(w http.ResponseWriter) error { return p.write(w) }

// timelineOps is GET /api/v1/timeline (SPEC 6.10). Any authenticated principal
// may use it: v1 has no ACL.
type timelineOps struct {
	timeline *timeline.Store
	// leases is expired before the read, so no lease is shown live past its expiry.
	leases *leases.Store
	// reasons derives the attention list of each task; nil means none needs a person.
	derived *attention.Store
}

// ready answers 503 when the operation has no store behind it.
func (o timelineOps) ready() (problemResponse, bool) {
	return failure(http.StatusServiceUnavailable, "the timeline is not available"), o.timeline == nil || o.leases == nil
}

// timelineQuery is the store's query for the parameters of GET /api/v1/timeline.
func timelineQuery(p GetTimelineParams) timeline.Query {
	return timeline.Query{
		From: text(p.From), To: text(p.To), Initiatives: orEmpty(p.Initiative), Tags: orEmpty(p.Tag),
		Actor: text(p.Actor), ClosedWithin: text(p.ClosedWithin), Rows: rowsText(p.Rows),
		Active: p.Active != nil && *p.Active, Window: windowText(p.Window),
	}
}

// timelineFailure is the problem a failed read is answered with, or false when
// the failure is the server's.
func timelineFailure(err error) (problemResponse, bool) {
	var bad timeline.InvalidError
	switch {
	case errors.As(err, &bad):
		return failure(http.StatusBadRequest, bad.Msg).withNext(bad.Next), true
	case errors.Is(err, timeline.ErrNoInitiative):
		return failure(http.StatusNotFound, "there is no initiative "+afterColon(err)).
			withNext("Send the slug of an initiative from GET /api/v1/initiatives."), true
	}
	return problemResponse{}, false
}

// GetTimeline reads the tasks, leases and markers of a window.
func (o timelineOps) GetTimeline(ctx context.Context, req GetTimelineRequestObject) (GetTimelineResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	// A refusal comes before the expiry below, which writes: a refused request changes nothing.
	prepared, p, bad := o.refusal(ctx, req.Params)
	if bad {
		return p, nil
	}
	reasons, err := o.expiredAndDerivation(ctx)
	if err != nil {
		return nil, err
	}
	got, err := prepared.WithAttention(attentionRefs(reasons)).Read(ctx)
	if p, ok := timelineFailure(err); ok {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return GetTimeline200JSONResponse(document(got, reasons)), nil
}

// expiredAndDerivation expires the due leases once and derives the attention.
// The derivation expires them as part of its own read, so the handler does it
// itself only when there is no derivation.
func (o timelineOps) expiredAndDerivation(ctx context.Context) (attention.Derivation, error) {
	if o.derived == nil {
		_, err := o.leases.ExpireDue(ctx)
		return attention.Derivation{}, err
	}
	return o.derivation(ctx)
}

// derivation is the attention derivation, empty when the operation has none.
func (o timelineOps) derivation(ctx context.Context) (attention.Derivation, error) {
	if o.derived == nil {
		return attention.Derivation{}, nil
	}
	return o.derived.Reasons(ctx, attention.Query{})
}

// attentionRefs are the refs of the tasks that carry any attention reason.
func attentionRefs(d attention.Derivation) []string {
	refs := make([]string, 0, len(d.Tasks))
	for _, t := range d.Tasks {
		if len(t.Reasons) > 0 {
			refs = append(refs, t.Task)
		}
	}
	return refs
}

// document is the answer for a read, with each task's attention list from the
// attention derivation.
func document(got timeline.Result, reasons attention.Derivation) Timeline {
	out := Timeline{
		From: got.From, To: got.To, Initiatives: make([]TimelineInitiative, len(got.Initiatives)),
		Tasks: make([]TimelineTask, len(got.Tasks)), Leases: make([]TimelineLease, len(got.Leases)), Markers: make([]TimelineMarker, len(got.Markers)),
	}
	for i, v := range got.Initiatives {
		out.Initiatives[i] = TimelineInitiative{Slug: v.Slug, Title: v.Title, Tags: v.Tags, Closed: v.Closed}
	}
	for i, t := range got.Tasks {
		out.Tasks[i] = apiTimelineTask(t)
		out.Tasks[i].Attention, _ = apiTaskAttention(reasons.Of(t.Ref))
	}
	for i, l := range got.Leases {
		out.Leases[i] = TimelineLease{Ref: l.Ref, Task: l.Task, Holder: l.Holder, State: TimelineLeaseState(l.State),
			StartedAt: l.StartedAt, EndedAt: l.EndedAt, Intent: l.Intent}
	}
	for i, m := range got.Markers {
		out.Markers[i] = TimelineMarker{Task: m.Task, Type: TimelineMarkerType(m.Type), At: m.At, Label: m.Label}
	}
	return out
}

func apiTimelineTask(t timeline.Task) TimelineTask {
	return TimelineTask{
		Ref: t.Ref, Initiative: t.Initiative, Title: t.Title, Status: TimelineTaskStatus(t.Status), Kind: TimelineTaskKind(t.Kind),
		PlannedStart: t.PlannedStart, PlannedEnd: t.PlannedEnd, CreatedAt: t.CreatedAt, ClosedAt: t.ClosedAt, Deps: t.Deps,
		Tags: nonNil(t.Tags), TagMarks: apiTagMarks(t.TagMarks),
	}
}

// windowText is the text of the window parameter, empty when it is left out.
func windowText(w *NowWindow) string {
	if w == nil {
		return ""
	}
	return string(*w)
}

// rowsText is the text of the rows parameter, empty when it is left out.
func rowsText(r *GetTimelineParamsRows) string {
	if r == nil {
		return ""
	}
	return string(*r)
}

// refusal is the problem a request that cannot be read is answered with, or
// false when it can, and then the checked query, whose initiatives are resolved
// once for the read that follows. It is decided before anything is written: a rows value
// that was supplied and is neither window nor all (the empty value included), a
// window, closed_within or tag that does not read, or an initiative that names
// nothing. A request that leaves rows out is never refused for it.
func (o timelineOps) refusal(ctx context.Context, p GetTimelineParams) (timeline.Prepared, problemResponse, bool) {
	if p.Rows != nil {
		if r, bad := timelineFailure(timeline.ValidRows(string(*p.Rows))); bad {
			return timeline.Prepared{}, r, true
		}
	}
	prepared, err := o.timeline.Prepare(ctx, timelineQuery(p))
	if r, bad := timelineFailure(err); bad {
		return timeline.Prepared{}, r, true
	}
	return prepared, problemResponse{}, false
}
