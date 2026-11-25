package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/tasks"
)

func (p problemResponse) VisitGetAttentionResponse(w http.ResponseWriter) error { return p.write(w) }

// attentionOps is the GET /api/v1/attention operation (SPEC 6.8). Any
// authenticated principal may use it: v1 has no ACL.
type attentionOps struct {
	attention *attention.Store
}

const (
	nextAttentionFields = "Send fields=counts for only the counts, or leave fields out (or send fields=full) for the sections too."
	nextAttentionInit   = "Send the slug of an initiative from GET /api/v1/initiatives."
)

// attentionFailure is the problem a failed attention read is answered with, or
// false when the failure is the server's.
func attentionFailure(err error) (problemResponse, bool) {
	var bad attention.InvalidError
	switch {
	case errors.As(err, &bad):
		return failure(http.StatusBadRequest, bad.Msg).withNext(bad.Next), true
	case errors.Is(err, attention.ErrNotFound):
		return failure(http.StatusNotFound, "there is no initiative "+afterColon(err)).withNext(nextAttentionInit), true
	}
	return problemResponse{}, false
}

func attentionQuery(p GetAttentionParams) attention.Query {
	return attention.Query{Initiatives: orEmpty(p.Initiative), Tags: orEmpty(p.Tag)}
}

// attentionCountsOnly reads the fields parameter: full (the default) or counts.
func attentionCountsOnly(fields *string) (bool, bool) {
	switch text(fields) {
	case "", "full":
		return false, true
	case "counts":
		return true, true
	}
	return false, false
}

func apiActor(a attention.Actor) AttentionActor {
	return AttentionActor{Handle: a.Handle, WayBack: optionalText(a.WayBack)}
}

func apiCounts(c attention.Counts) AttentionCounts {
	return AttentionCounts{OpenAsks: c.OpenAsks, DoneNotClosed: c.DoneNotClosed, ExpiredLeases: c.ExpiredLeases,
		OverdueWatches: c.OverdueWatches, Conflicts: c.Conflicts, Total: c.Total}
}

// mapped converts every item of a section; an empty section is an empty list.
func mapped[S, D any](in []S, f func(S) D) *[]D {
	out := make([]D, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return &out
}

func apiAttentionAsk(k attention.OpenAsk) AttentionAsk {
	return AttentionAsk{
		Ref: k.Ref, Question: k.Question, Task: optionalText(k.Task), Initiative: optionalText(k.Initiative), Asker: apiActor(k.Asker), Mode: AttentionAskMode(k.Mode),
		AskerWaiting: k.AskerWaiting, Blocking: k.Blocking, CreatedAt: k.CreatedAt, AgeSeconds: int(k.Age.Seconds()),
	}
}

func apiLastLease(l *attention.LastLease) *AttentionLastLease {
	if l == nil {
		return nil
	}
	return &AttentionLastLease{Lease: l.Lease, Holder: apiActor(l.Holder), Outcome: optionalText(l.Outcome), EndedAt: l.EndedAt}
}

func apiDoneTask(d attention.DoneTask) AttentionDoneTask {
	return AttentionDoneTask{Task: d.Task, Title: d.Title, DoneBy: apiActor(d.DoneBy), DoneAt: d.DoneAt, LastLease: apiLastLease(d.LastLease),
		Tags: nonNil(d.Tags), TagMarks: apiTagMarks(d.TagMarks)}
}

func apiExpiredLease(l attention.ExpiredLease) AttentionExpiredLease {
	return AttentionExpiredLease{Lease: l.Lease, Holder: apiActor(l.Holder), Task: l.Task, Title: l.Title, Intent: l.Intent,
		RenewedAt: l.RenewedAt, ExpiredAt: l.ExpiredAt, Notice: l.Notice, Tags: nonNil(l.Tags), TagMarks: apiTagMarks(l.TagMarks)}
}

func apiLastCheck(c *attention.LastCheck) *AttentionLastCheck {
	if c == nil {
		return nil
	}
	return &AttentionLastCheck{Summary: c.Summary, By: apiActor(c.By), At: c.At}
}

func apiWatch(w attention.OverdueWatch) AttentionWatch {
	return AttentionWatch{Task: w.Task, Title: w.Title, Owner: apiActor(w.Owner), NextCheckAt: w.NextCheckAt, LastCheck: apiLastCheck(w.LastCheck),
		Tags: nonNil(w.Tags), TagMarks: apiTagMarks(w.TagMarks)}
}

func apiHolder(h attention.Holder) AttentionHolder {
	return AttentionHolder{Lease: h.Lease, Actor: apiActor(h.Actor), Task: h.Task, Intent: h.Intent, ExpiresAt: h.ExpiresAt}
}

func apiConflictItem(c attention.Conflict) AttentionConflict {
	tasks := append([]string{}, c.Tasks...)
	return AttentionConflict{Resource: c.Resource, Holders: *mapped(c.Holders, apiHolder), Tasks: tasks, Since: c.Since}
}

func apiAttention(r attention.Result) Attention {
	return Attention{
		Counts: apiCounts(r.Counts), OpenAsks: mapped(r.OpenAsks, apiAttentionAsk), DoneNotClosed: mapped(r.DoneNotClosed, apiDoneTask),
		ExpiredLeases: mapped(r.ExpiredLeases, apiExpiredLease), OverdueWatches: mapped(r.OverdueWatches, apiWatch),
		Conflicts: mapped(r.Conflicts, apiConflictItem),
	}
}

// read is the attention counts or the whole result for a query.
func (o attentionOps) read(ctx context.Context, q attention.Query, countsOnly bool) (Attention, error) {
	if countsOnly {
		c, err := o.attention.Counts(ctx, q)
		return Attention{Counts: apiCounts(c)}, err
	}
	r, err := o.attention.Get(ctx, q)
	return apiAttention(r), err
}

// GetAttention answers the five attention sections and their counts, or only the
// counts with fields=counts. Reading expires the leases that ran out first.
func (o attentionOps) GetAttention(ctx context.Context, req GetAttentionRequestObject) (GetAttentionResponseObject, error) {
	if o.attention == nil {
		return failure(http.StatusServiceUnavailable, "attention is not available"), nil
	}
	countsOnly, ok := attentionCountsOnly(req.Params.Fields)
	if !ok {
		return failure(http.StatusBadRequest, "fields "+text(req.Params.Fields)+" is not full or counts").withNext(nextAttentionFields), nil
	}
	out, err := o.read(ctx, attentionQuery(req.Params), countsOnly)
	if p, is := attentionFailure(err); is {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return GetAttention200JSONResponse(out), nil
}

func (p problemResponse) VisitGetAttentionNowResponse(w http.ResponseWriter) error { return p.write(w) }

func apiNowTask(t attention.FlightTask) NowTask {
	return NowTask{Task: t.Ref, Title: t.Title, Initiative: t.Initiative, Status: NowTaskStatus(t.Status), Kind: NowTaskKind(t.Kind),
		Tags: nonNil(t.Tags), TagMarks: apiTagMarks(t.TagMarks)}
}

func apiFlightLease(l attention.FlightLease) NowFlightLease {
	return NowFlightLease{Lease: l.Lease, Holder: apiActor(l.Holder), Intent: l.Intent, StartedAt: l.StartedAt, RenewedAt: l.RenewedAt,
		ExpiresAt: l.ExpiresAt}
}

func apiInFlight(f attention.InFlight) NowInFlight {
	return NowInFlight{Task: apiNowTask(f.Task), Leases: *mapped(f.Leases, apiFlightLease)}
}

func apiRecent(r attention.Recent) NowRecent {
	e := r.LastEvent
	last := NowLastEvent{Type: e.Type, Summary: e.Summary, Actor: apiActor(e.Actor), At: e.At}
	if e.Origin != "" {
		last.Origin = &e.Origin
	}
	return NowRecent{Task: apiNowTask(r.Task), LastEvent: last}
}

func apiTaskMarks(in map[string]tasks.Tagged) map[string]TaskTagMarks {
	out := make(map[string]TaskTagMarks, len(in))
	for ref, t := range in {
		out[ref] = TaskTagMarks{Tags: nonNil(t.Tags), TagMarks: apiTagMarks(t.TagMarks)}
	}
	return out
}

func apiBoard(b attention.Board) AttentionNow {
	return AttentionNow{Window: NowWindow(b.Window), Attention: apiAttention(b.Attention),
		InFlight: *mapped(b.InFlight, apiInFlight), Recent: *mapped(b.Recent, apiRecent), TaskMarks: apiTaskMarks(b.TaskMarks)}
}

// nowWindow reads the window parameter: absent means the default, anything but
// the three declared values is not a window.
func nowWindow(w *NowWindow) (string, bool) {
	if w == nil {
		return attention.DefaultWindow, true
	}
	return string(*w), w.Valid()
}

// GetAttentionNow answers what the Now view shows in one read: the attention
// sections, the tasks in flight with their live leases and the recently touched
// tasks of a window. A bad window is 400 and nothing is written but the expiry of
// leases that ran out, as in every read.
func (o attentionOps) GetAttentionNow(ctx context.Context, req GetAttentionNowRequestObject) (GetAttentionNowResponseObject, error) {
	if o.attention == nil {
		return failure(http.StatusServiceUnavailable, "attention is not available"), nil
	}
	window, ok := nowWindow(req.Params.Window)
	if !ok {
		return failure(http.StatusBadRequest, "window "+window+" is not 8h, 24h or 3d").withNext(attention.NextWindow), nil
	}
	q := attention.Query{Initiatives: orEmpty(req.Params.Initiative), Tags: orEmpty(req.Params.Tag)}
	b, err := o.attention.Board(ctx, q, window)
	if p, is := attentionFailure(err); is {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	return GetAttentionNow200JSONResponse(apiBoard(b)), nil
}
