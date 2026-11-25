package server

import (
	"context"
	"net/http"

	"github.com/n-orlov/village-hall/internal/checkin"
	"github.com/n-orlov/village-hall/internal/domain"
)

func (p problemResponse) VisitStartResponse(w http.ResponseWriter) error { return p.write(w) }

// startOps is POST /api/v1/start (SPEC 8.4). It is built from the other
// operations' stores once every option has been applied; without all of them it
// answers 503.
type startOps struct {
	checkin  checkin.Store
	protocol int
}

func newStartOps(o operations) startOps {
	c := newNowOps(o).checkin
	c.Tasks = o.taskOps.tasks
	return startOps{checkin: c, protocol: o.build.Protocol}
}

func (o startOps) ready() (problemResponse, bool) {
	c := o.checkin
	off := c.Leases == nil || c.Asks == nil || c.Notes == nil || c.Cursors == nil || c.Attention == nil || c.Tasks == nil
	return failure(http.StatusServiceUnavailable, "the start is not available"), off
}

const nextStartBody = "Send a JSON body such as {\"task\": \"<ref>\", \"intent\": \"what you are doing\"}, or {\"new_task\": {\"initiative\": \"<slug>\", \"title\": \"...\"}, \"intent\": \"...\"}."

// startFailure is the problem a refused start is answered with: a lease problem,
// else a task problem, else false when the failure is the server's.
func startFailure(err error) (problemResponse, bool) {
	if p, ok := leaseFailure(err); ok {
		return p, true
	}
	return taskFailure(err, "there is no task ")
}

// Start starts the caller's lease on a task, or on one it creates, in one
// transaction, and answers it with the caller's check-in.
func (o startOps) Start(ctx context.Context, req StartRequestObject) (StartResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	who, r, ok := caller(ctx)
	if !ok {
		return r, nil
	}
	if req.Body == nil {
		return failure(http.StatusBadRequest, "the request needs a JSON body with an intent and a task or a new_task").withNext(nextStartBody), nil
	}
	res, err := o.checkin.Start(ctx, who.AuditActor(), who.Principal, startRequest(req.Body))
	if err != nil {
		if p, is := startFailure(err); is {
			return p, nil
		}
		return nil, err
	}
	out := StartResult{
		Lease: apiLease(res.Lease.Lease), Conflicts: apiConflicts(res.Lease.Conflicts), TaskCreated: res.TaskCreated,
		Now: apiNow(who.Actor, res.Now), Protocol: o.protocol, ProtocolSeen: res.ProtocolSeen,
	}
	if res.Lease.Renewed {
		return Start200JSONResponse(out), nil
	}
	return Start201JSONResponse(out), nil
}

func startRequest(b *StartRequest) checkin.StartRequest {
	in := checkin.StartRequest{Task: text(b.Task), Intent: b.Intent, TTL: b.TtlSeconds, Resources: resourcesOf(b.Resources), Notice: isSet(b.Notice)}
	if n := b.NewTask; n != nil {
		in.NewTask = &checkin.NewTask{Initiative: n.Initiative, Title: n.Title, Tags: orEmpty(n.Tags)}
	}
	return in
}

// apiConflicts is the conflicts as the API lists them, an empty list for none.
func apiConflicts(hs []domain.Holding) []LeaseConflict {
	out := make([]LeaseConflict, len(hs))
	for i, h := range hs {
		out[i] = apiConflict(h)
	}
	return out
}
