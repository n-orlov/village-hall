package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/checkin"
	"github.com/n-orlov/village-hall/internal/domain"
)

func (p problemResponse) VisitRaiseAskResponse(w http.ResponseWriter) error    { return p.write(w) }
func (p problemResponse) VisitListAsksResponse(w http.ResponseWriter) error    { return p.write(w) }
func (p problemResponse) VisitGetAskResponse(w http.ResponseWriter) error      { return p.write(w) }
func (p problemResponse) VisitAnswerAskResponse(w http.ResponseWriter) error   { return p.write(w) }
func (p problemResponse) VisitWithdrawAskResponse(w http.ResponseWriter) error { return p.write(w) }

// askOps is the /api/v1/asks operations (SPEC 6.6). Any authenticated principal
// may use them: v1 has no ACL.
type askOps struct {
	asks  *asks.Store
	waits *streamHub    // the change feed that wakes a wait; nil: waits answer 503
	touch time.Duration // how often a held wait renews poll_seen_at; 0 is DefaultAskTouch
	clock AskClock      // the time a wait reads; nil is the real clock
	unit  time.Duration // how long one second of ?wait= lasts; 0 is a real second (a test rig shortens it)
	block checkin.Store // the stores a raise with lease_action stop ends the lease through; set once every option is applied
}

// waitUnit is how long one second of ?wait= lasts: a second, unless a test rig
// set it shorter with WithAskWaitUnit.
func (o askOps) waitUnit() time.Duration {
	if o.unit > 0 {
		return o.unit
	}
	return time.Second
}

// ready answers 503 when the operations have no store behind them.
func (o askOps) ready() (problemResponse, bool) {
	return failure(http.StatusServiceUnavailable, "asks are not available"), o.asks == nil
}

// writer is the caller of a write, or the problem to answer with.
func (o askOps) writer(ctx context.Context) (auth.Identity, *problemResponse) {
	if r, off := o.ready(); off {
		return auth.Identity{}, &r
	}
	who, r, ok := caller(ctx)
	if !ok {
		return auth.Identity{}, &r
	}
	return who, nil
}

const (
	nextAskBody = "Send a JSON body such as {\"question\": \"which one?\"}, with a task or an initiative if the ask belongs to one."
	nextAskRead = "Read the asks with GET /api/v1/asks?state=all."
)

// askFailure is the problem a failed ask operation is answered with, or false
// when the failure is the server's.
func askFailure(err error) (problemResponse, bool) {
	var bad asks.InvalidError
	var conflict asks.ConflictError
	switch {
	case errors.As(err, &bad):
		return failure(http.StatusBadRequest, bad.Msg).withNext(bad.Next), true
	case errors.As(err, &conflict):
		return failure(http.StatusConflict, conflict.Msg).withNext(conflict.Next), true
	case errors.Is(err, asks.ErrNotFound):
		return failure(http.StatusNotFound, "there is no "+afterColon(err)).withNext(nextAskRead), true
	}
	return problemResponse{}, false
}

// askResult answers a failed ask operation: the problem it maps to, or the error
// itself when it is the server's.
func askResult[R any](err error) (R, error) {
	var zero R
	if p, ok := askFailure(err); ok {
		// problemResponse implements every Visit method of the ask operations
		if r, is := any(p).(R); is {
			return r, nil
		}
	}
	return zero, err
}

func (o askOps) apiAsk(k asks.Ask) Ask {
	out := Ask{
		Id: k.ID, Ref: k.Ref, Task: optionalText(k.Task), Initiative: optionalText(k.Initiative), Asker: k.Actor,
		Question: k.Question, Details: k.Details, Options: k.Options, Mode: AskMode(k.Mode), Blocking: k.Blocking,
		State: AskState(k.State), Answer: optionalText(k.Answer), AnsweredBy: optionalText(k.AnsweredBy),
		OnBehalfOf: optionalText(k.OnBehalfOf), CreatedAt: k.CreatedAt, AnsweredAt: k.AnsweredAt, WithdrawnAt: k.WithdrawnAt,
		WithdrawReason: optionalText(k.WithdrawReason), EndedAt: k.EndedAt, AskerWaiting: o.asks.Waiting(k),
	}
	if k.Channel != "" {
		c := AskChannel(k.Channel)
		out.Channel = &c
	}
	if out.Options == nil {
		out.Options = []string{}
	}
	return out
}

func askRequest(b *AskRaise) asks.New {
	in := asks.New{Task: text(b.Task), Initiative: text(b.Initiative), Question: b.Question, Details: text(b.Details)}
	if b.Options != nil {
		in.Options = *b.Options
	}
	if b.Mode != nil {
		in.Mode = string(*b.Mode)
	}
	if b.Blocking != nil {
		in.Blocking = *b.Blocking
	}
	if b.LeaseAction != nil {
		in.LeaseAction = string(*b.LeaseAction)
	}
	return in
}

// RaiseAsk raises an open ask.
func (o askOps) RaiseAsk(ctx context.Context, req RaiseAskRequestObject) (RaiseAskResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	if req.Body == nil {
		return failure(http.StatusBadRequest, "the request needs a JSON body with a question").withNext(nextAskBody), nil
	}
	if acts(req.Body) {
		return o.raiseActing(ctx, who, askRequest(req.Body))
	}
	k, err := o.asks.Raise(ctx, who.AuditActor(), who.Principal, askRequest(req.Body))
	if err != nil {
		return askResult[RaiseAskResponseObject](err)
	}
	return RaiseAsk201JSONResponse(askRaised(o.apiAsk(k), nil)), nil
}

// GetAsk reads one ask.
func (o askOps) GetAsk(ctx context.Context, req GetAskRequestObject) (GetAskResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	if p := askWaitProblem(req.Params.Wait, req.Params.Until); p != nil {
		return *p, nil
	}
	k, err := o.asks.Get(ctx, req.Ref)
	if err != nil {
		return askResult[GetAskResponseObject](err)
	}
	readBack := o.clk().Now()
	if req.Params.Wait != nil {
		k = o.lateMark(ctx, k, readBack, req.Params.Until)
	}
	return o.read(ctx, k, readBack, req.Params.Wait, req.Params.Until), nil
}

// lateMark is k, an ask a wait with until found already ended by a read that
// came back after until, with the commit mark of its end taken in when that is
// at or before until and was still being written when k was read. The write of
// the poll queues behind any write in flight, as a wait's own end does, so a
// mark that was sampled in time but persisted late is not lost, and the end is
// counted; with no such mark k is unchanged and the end stays uncounted.
func (o askOps) lateMark(ctx context.Context, k asks.Ask, readBack time.Time, until *time.Time) asks.Ask {
	if until == nil || k.State == domain.AskOpen || k.EndCommitAt != nil || readProves(k.State, readBack, until) {
		return k
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := o.asks.Touch(ctx, k.ID); err != nil {
		return k
	}
	return adoptedMark(ctx, o.asks, k, *until)
}

// read answers with the ask k, whose read came back at readBack: at once, or
// after a wait when one was asked for and the ask is still open, ending at until
// if that comes first. With until, an ask found ended is answered as it stood at
// until (atUntil): ended when that read came back by until or the end's commit
// mark is at or before it, and else open, without a wait.
func (o askOps) read(ctx context.Context, k asks.Ask, readBack time.Time, wait *int, until *time.Time) GetAskResponseObject {
	switch {
	case wait == nil || k.State != domain.AskOpen:
		return GetAsk200JSONResponse(o.apiAsk(atUntil(k, until, readProves(k.State, readBack, until))))
	case o.waits == nil:
		return failure(http.StatusServiceUnavailable, "waiting for an ask is not available")
	}
	return askWaitResponse{ctx: ctx, ops: o, ask: k, max: time.Duration(*wait) * o.waitUnit(), until: until}
}

// ListAsks lists a page of asks.
func (o askOps) ListAsks(ctx context.Context, req ListAsksRequestObject) (ListAsksResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	p := req.Params
	page, err := o.asks.List(ctx, asks.Query{
		State: text(p.State), Actor: text(p.Actor), Task: text(p.Task), Initiative: text(p.Initiative),
		After: text(p.After), Limit: pageLimit(p.Limit),
	})
	if err != nil {
		return askResult[ListAsksResponseObject](err)
	}
	items := make([]Ask, len(page.Items))
	for i, k := range page.Items {
		items[i] = o.apiAsk(k)
	}
	return ListAsks200JSONResponse(AskList{Items: items, NextAfter: optionalText(page.NextAfter)}), nil
}

// AnswerAsk answers an open ask.
func (o askOps) AnswerAsk(ctx context.Context, req AnswerAskRequestObject) (AnswerAskResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	if req.Body == nil {
		return failure(http.StatusBadRequest, "the request needs a JSON body with an answer").
			withNext("Send a JSON body such as {\"answer\": \"the first option\"}."), nil
	}
	in := asks.Reply{Answer: req.Body.Answer, OnBehalfOf: text(req.Body.OnBehalfOf)}
	if req.Body.Channel != nil {
		in.Channel = string(*req.Body.Channel)
	}
	k, err := o.asks.Answer(ctx, who.AuditActor(), who.Principal, req.Ref, in)
	if err != nil {
		return askResult[AnswerAskResponseObject](err)
	}
	return AnswerAsk200JSONResponse(o.apiAsk(k)), nil
}

// WithdrawAsk withdraws an open ask.
func (o askOps) WithdrawAsk(ctx context.Context, req WithdrawAskRequestObject) (WithdrawAskResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	reason := ""
	if req.Body != nil {
		reason = text(req.Body.Reason)
	}
	k, err := o.asks.Withdraw(ctx, who.AuditActor(), who.Principal, req.Ref, reason)
	if err != nil {
		return askResult[WithdrawAskResponseObject](err)
	}
	return WithdrawAsk200JSONResponse(o.apiAsk(k)), nil
}
