package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/leases"
)

func (p problemResponse) VisitStartLeaseResponse(w http.ResponseWriter) error { return p.write(w) }
func (p problemResponse) VisitListLeasesResponse(w http.ResponseWriter) error { return p.write(w) }
func (p problemResponse) VisitGetLeaseResponse(w http.ResponseWriter) error   { return p.write(w) }
func (p problemResponse) VisitRenewLeaseResponse(w http.ResponseWriter) error { return p.write(w) }
func (p problemResponse) VisitEndLeaseResponse(w http.ResponseWriter) error   { return p.write(w) }
func (p problemResponse) VisitAckLeaseResponse(w http.ResponseWriter) error   { return p.write(w) }

// leaseOps is the /api/v1/leases operations (SPEC 6.5). Any authenticated
// principal may use them: v1 has no ACL.
type leaseOps struct{ leases *leases.Store }

// ready answers 503 when the operations have no store behind them.
func (o leaseOps) ready() (problemResponse, bool) {
	return failure(http.StatusServiceUnavailable, "leases are not available"), o.leases == nil
}

// writer is the caller of a write, or the problem to answer with.
func (o leaseOps) writer(ctx context.Context) (auth.Identity, *problemResponse) {
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
	nextLeaseBody = "Send a JSON body such as {\"task\": \"<ref>\", \"intent\": \"what you are doing\"}."
	nextLeaseRead = "Read the leases with GET /api/v1/leases?state=all, or the task with GET /api/v1/tasks/<ref>."
)

// leaseFailure is the problem a failed lease operation is answered with, or
// false when the failure is the server's.
func leaseFailure(err error) (problemResponse, bool) {
	var bad leases.InvalidError
	var conflict leases.ConflictError
	var gone leases.GoneError
	switch {
	case errors.As(err, &bad):
		return failure(http.StatusBadRequest, bad.Msg).withNext(bad.Next), true
	case errors.As(err, &gone):
		p := failure(http.StatusGone, gone.Error()).
			withNext("Start a new lease with POST /api/v1/leases; the gap stays on the task's timeline.")
		p.extra = map[string]any{"expired_at": gone.ExpiredAt.UTC().Format(time.RFC3339Nano)}
		return p, true
	case errors.As(err, &conflict):
		return failure(http.StatusConflict, conflict.Msg).withNext(conflict.Next), true
	case errors.Is(err, leases.ErrNotFound):
		return failure(http.StatusNotFound, "there is no "+afterColon(err)).withNext(nextLeaseRead), true
	}
	return problemResponse{}, false
}

// leaseResult answers a failed lease operation: the problem it maps to, or the
// error itself when it is the server's.
func leaseResult[R any](err error) (R, error) {
	var zero R
	if p, ok := leaseFailure(err); ok {
		// problemResponse implements every Visit method of the lease operations
		if r, is := any(p).(R); is {
			return r, nil
		}
	}
	return zero, err
}

func apiLeaseResult(r leases.Result) LeaseResult {
	out := LeaseResult{Lease: apiLease(r.Lease), Conflicts: make([]LeaseConflict, len(r.Conflicts))}
	for i, h := range r.Conflicts {
		out.Conflicts[i] = apiConflict(h)
	}
	return out
}

func apiConflict(h domain.Holding) LeaseConflict {
	res := h.Resources
	if res == nil {
		res = []string{}
	}
	return LeaseConflict{Lease: h.Lease, Holder: h.Holder, Task: h.Task, Intent: h.Intent, ExpiresAt: h.ExpiresAt, Resources: res}
}

// isSet is an optional boolean, false when it was not sent.
func isSet(b *bool) bool {
	return b != nil && *b
}

func resourcesOf(r *[]string) []string {
	if r == nil {
		return nil
	}
	return *r
}

// StartLease starts a lease, or renews the caller's live lease on the task.
func (o leaseOps) StartLease(ctx context.Context, req StartLeaseRequestObject) (StartLeaseResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	if req.Body == nil {
		return failure(http.StatusBadRequest, "the request needs a JSON body with a task and an intent").withNext(nextLeaseBody), nil
	}
	b := req.Body
	res, err := o.leases.Start(ctx, who.AuditActor(), who.Principal,
		leases.Start{Task: b.Task, Intent: b.Intent, TTL: b.TtlSeconds, Resources: resourcesOf(b.Resources), Notice: isSet(b.Notice)})
	if err != nil {
		return leaseResult[StartLeaseResponseObject](err)
	}
	if res.Renewed {
		return StartLease200JSONResponse(apiLeaseResult(res)), nil
	}
	return StartLease201JSONResponse(apiLeaseResult(res)), nil
}

// RenewLease pushes the expiry of a live lease.
func (o leaseOps) RenewLease(ctx context.Context, req RenewLeaseRequestObject) (RenewLeaseResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	var in leases.Renew
	if b := req.Body; b != nil {
		in = leases.Renew{TTL: b.TtlSeconds, Intent: b.Intent, Resources: b.Resources}
	}
	res, err := o.leases.Renew(ctx, who.AuditActor(), who.Principal, req.Ref, in)
	if err != nil {
		return leaseResult[RenewLeaseResponseObject](err)
	}
	return RenewLease200JSONResponse(apiLeaseResult(res)), nil
}

// EndLease ends a live lease.
func (o leaseOps) EndLease(ctx context.Context, req EndLeaseRequestObject) (EndLeaseResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	outcome := ""
	if req.Body != nil {
		outcome = text(req.Body.Outcome)
	}
	l, err := o.leases.End(ctx, who.AuditActor(), who.Principal, req.Ref, outcome)
	if err != nil {
		return leaseResult[EndLeaseResponseObject](err)
	}
	return EndLease200JSONResponse(apiLease(l)), nil
}

// AckLease acknowledges an expired lease.
func (o leaseOps) AckLease(ctx context.Context, req AckLeaseRequestObject) (AckLeaseResponseObject, error) {
	who, bad := o.writer(ctx)
	if bad != nil {
		return *bad, nil
	}
	l, err := o.leases.Ack(ctx, who.AuditActor(), who.Principal, req.Ref)
	if err != nil {
		return leaseResult[AckLeaseResponseObject](err)
	}
	return AckLease200JSONResponse(apiLease(l)), nil
}

// GetLease reads one lease.
func (o leaseOps) GetLease(ctx context.Context, req GetLeaseRequestObject) (GetLeaseResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	l, err := o.leases.Current(ctx, req.Ref)
	if err != nil {
		return leaseResult[GetLeaseResponseObject](err)
	}
	return GetLease200JSONResponse(apiLease(l)), nil
}

// ListLeases lists a page of leases.
func (o leaseOps) ListLeases(ctx context.Context, req ListLeasesRequestObject) (ListLeasesResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	p := req.Params
	page, err := o.leases.List(ctx, leases.Query{
		State: text(p.State), Actor: text(p.Actor), Task: text(p.Task), Resource: text(p.Resource),
		After: text(p.After), Limit: pageLimit(p.Limit),
	})
	if err != nil {
		return leaseResult[ListLeasesResponseObject](err)
	}
	items := make([]Lease, len(page.Items))
	for i, l := range page.Items {
		items[i] = apiLease(l)
	}
	return ListLeases200JSONResponse(LeaseList{Items: items, NextAfter: optionalText(page.NextAfter)}), nil
}
