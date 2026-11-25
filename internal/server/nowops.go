package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/checkin"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/leases"
)

func (p problemResponse) VisitGetNowResponse(w http.ResponseWriter) error { return p.write(w) }

const nextNowResources = "Send at most 20 comma-separated resources of 1 to 100 characters, such as ?resources=repo:board,host:build1."

// queriedResources reads the comma-separated resources query: none for an absent
// or empty value, otherwise each entry validated like a lease's resources.
func queriedResources(raw *string) ([]string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	return domain.NormalizeResources(strings.Split(*raw, ","))
}

func apiNowLease(l leases.Lease) NowLease {
	a := apiLease(l)
	return NowLease{
		Id: a.Id, Ref: a.Ref, Task: a.Task, Title: l.Title, Actor: a.Actor, Intent: a.Intent, TtlSeconds: a.TtlSeconds,
		StartedAt: a.StartedAt, RenewedAt: a.RenewedAt, ExpiresAt: a.ExpiresAt, State: NowLeaseState(a.State),
		Resources: a.Resources, EndedAt: a.EndedAt, EndedBy: a.EndedBy, Outcome: a.Outcome, AckedAt: a.AckedAt, AckedBy: a.AckedBy, Notice: a.Notice,
	}
}

// nowOps is GET /api/v1/now (SPEC 6.5). It is built from the other operations'
// stores once every option has been applied; without all of them it answers 503.
type nowOps struct{ checkin checkin.Store }

func newNowOps(o operations) nowOps {
	return nowOps{checkin: checkin.Store{
		Leases: o.leaseOps.leases, Asks: o.askOps.asks, Notes: o.notes, Cursors: o.cursorOps.cursors, Attention: o.attention,
	}}
}

func (o nowOps) ready() (problemResponse, bool) {
	c := o.checkin
	off := c.Leases == nil || c.Asks == nil || c.Notes == nil || c.Cursors == nil || c.Attention == nil
	return failure(http.StatusServiceUnavailable, "the check-in is not available"), off
}

func apiNow(actor string, r checkin.Result) Now {
	n := r.Leases
	out := Now{
		Actor: actor, MyLeases: make([]NowLease, len(n.Mine)), OthersLeases: make([]Lease, len(n.Others)),
		Conflicts: make([]LeaseConflict, len(n.Conflicts)), MyAsks: make([]Ask, len(r.MyAsks)),
		Notes: make([]Note, len(r.Notes)), NotesMore: r.NotesMore, Attention: apiCounts(r.Counts),
	}
	for i, l := range n.Mine {
		out.MyLeases[i] = apiNowLease(l)
	}
	for i, l := range n.Others {
		out.OthersLeases[i] = apiLease(l)
	}
	for i, h := range n.Conflicts {
		out.Conflicts[i] = apiConflict(h)
	}
	for i, k := range r.MyAsks {
		out.MyAsks[i] = askOps{asks: &asks.Store{}}.apiAsk(k)
	}
	for i, k := range r.Notes {
		out.Notes[i] = apiNote(k, false)
	}
	return out
}

// GetNow answers the caller's check-in (SPEC 6.5). It writes nothing but the
// lease_expired events of the leases it expires, and moves no cursor.
func (o nowOps) GetNow(ctx context.Context, req GetNowRequestObject) (GetNowResponseObject, error) {
	if r, off := o.ready(); off {
		return r, nil
	}
	who, r, ok := caller(ctx)
	if !ok {
		return r, nil
	}
	queried, err := queriedResources(req.Params.Resources)
	if err != nil {
		return failure(http.StatusBadRequest, err.Error()).withNext(nextNowResources), nil
	}
	res, err := o.checkin.Check(ctx, who.Actor, queried)
	if err != nil {
		return nil, err
	}
	return GetNow200JSONResponse(apiNow(who.Actor, res)), nil
}
