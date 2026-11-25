package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/checkin"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/domain"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
)

func TestLeaseOperationsAnswer503WithoutAStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	start, _ := leaseOps{}.StartLease(ctx, StartLeaseRequestObject{})
	list, _ := leaseOps{}.ListLeases(ctx, ListLeasesRequestObject{})
	get, _ := leaseOps{}.GetLease(ctx, GetLeaseRequestObject{Ref: "L1"})
	renew, _ := leaseOps{}.RenewLease(ctx, RenewLeaseRequestObject{Ref: "L1"})
	end, _ := leaseOps{}.EndLease(ctx, EndLeaseRequestObject{Ref: "L1"})
	ack, _ := leaseOps{}.AckLease(ctx, AckLeaseRequestObject{Ref: "L1"})
	now, _ := nowOps{}.GetNow(ctx, GetNowRequestObject{})
	for name, got := range map[string]any{"start": start, "list": list, "get": get, "renew": renew, "end": end, "ack": ack, "now": now} {
		if p, ok := got.(problemResponse); !ok || p.status != http.StatusServiceUnavailable {
			t.Errorf("%s without a store = %#v, want a 503 problem", name, got)
		}
	}
}

func TestLeaseWritesWithoutAPrincipalAre401(t *testing.T) {
	t.Parallel()
	o := leaseOps{leases: &leases.Store{}}
	got, _ := o.StartLease(context.Background(), StartLeaseRequestObject{Body: &LeaseStart{Task: "aa-1", Intent: "x"}})
	if p, ok := got.(problemResponse); !ok || p.status != http.StatusUnauthorized {
		t.Errorf("start without a principal = %#v, want 401", got)
	}
}

func TestLeaseFailureMapsEachErrorToItsStatus(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 5, 12, 0, 0, 123000000, time.UTC)
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"invalid":  {leases.InvalidError{Msg: "bad", Next: "fix it"}, http.StatusBadRequest},
		"gone":     {leases.GoneError{Ref: "L1", ExpiredAt: at}, http.StatusGone},
		"conflict": {leases.ConflictError{Msg: "no", Next: "later"}, http.StatusConflict},
		"missing":  {errors.Join(leases.ErrNotFound), http.StatusNotFound},
	} {
		p, ok := leaseFailure(tc.err)
		if !ok || p.status != tc.status {
			t.Errorf("%s = %+v, %v; want status %d", name, p, ok, tc.status)
		}
	}
	if _, ok := leaseFailure(errors.New("disk")); ok {
		t.Error("a server error was mapped to a problem")
	}
	p, _ := leaseFailure(leases.GoneError{Ref: "L1", ExpiredAt: at})
	if p.extra["expired_at"] != "2026-11-05T12:00:00.123Z" {
		t.Errorf("expired_at = %v", p.extra["expired_at"])
	}
}

func TestLeaseResultKeepsAServerErrorAnError(t *testing.T) {
	t.Parallel()
	if _, err := leaseResult[GetLeaseResponseObject](errors.New("disk")); err == nil {
		t.Error("a server error was swallowed")
	}
	if r, err := leaseResult[GetLeaseResponseObject](leases.ConflictError{Msg: "x"}); err != nil || r == nil {
		t.Errorf("a conflict = %v, %v; want a problem response", r, err)
	}
}

func TestAConflictWithNoResourcesIsAnEmptyList(t *testing.T) {
	t.Parallel()
	c := apiConflict(domain.Holding{Lease: "L1", Holder: "claude@host", Task: "aa-1", Intent: "x"})
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), `"resources":[]`) {
		t.Errorf("conflict = %s", raw)
	}
}

func TestLeaseRoundTripThroughTheHandler(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	s.task(`{"initiative":"aa","title":"one"}`)
	rec := s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-1","intent":"work","resources":["db:x"]}`, "claude@host")
	doc := decode(t, rec)
	if rec.Code != http.StatusCreated || doc["conflicts"] == nil || doc["lease"].(map[string]any)["ref"] != "L1" {
		t.Fatalf("start = %d %s", rec.Code, rec.Body)
	}
	if rec := s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-1","intent":"again"}`, "claude@host"); rec.Code != http.StatusOK {
		t.Errorf("second start = %d %s, want 200", rec.Code, rec.Body)
	}
	for path, want := range map[string]int{
		"/api/v1/leases/L1":            http.StatusOK,
		"/api/v1/leases/L9":            http.StatusNotFound,
		"/api/v1/leases?state=bad":     http.StatusBadRequest,
		"/api/v1/leases?task=aa-9":     http.StatusNotFound,
		"/api/v1/leases?limit=0":       http.StatusBadRequest,
		"/api/v1/leases?resource=db:x": http.StatusOK,
	} {
		if rec := s.send(http.MethodGet, path, "", "claude@host"); rec.Code != want {
			t.Errorf("GET %s = %d %s, want %d", path, rec.Code, rec.Body, want)
		}
	}
	if rec := s.send(http.MethodPost, "/api/v1/leases/L1/end", `{"outcome":"done"}`, "codex@host"); rec.Code != http.StatusOK ||
		decode(t, rec)["ended_by"] != "codex@host" {
		t.Errorf("end = %d %s", rec.Code, rec.Body)
	}
}

func TestAListParameterSentTwiceIsRefused(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	for _, p := range []string{"state", "actor", "task", "resource", "limit", "after"} {
		if rec := s.send(http.MethodGet, "/api/v1/leases?"+p+"=1&"+p+"=2", "", "claude@host"); rec.Code != http.StatusBadRequest {
			t.Errorf("%s sent twice = %d %s, want 400", p, rec.Code, rec.Body)
		}
	}
}

func TestQueryBinderAnswersOnlyTheFirstFailure(t *testing.T) {
	t.Parallel()
	var seen []error
	siw := &ServerInterfaceWrapper{ErrorHandlerFunc: func(_ http.ResponseWriter, _ *http.Request, err error) { seen = append(seen, err) }}
	b := siw.queryBinder(nil, httptest.NewRequest(http.MethodGet, "/", nil))
	b.check("a", nil)
	if b.failed || len(seen) != 0 {
		t.Fatalf("a bound parameter failed the request: %v", seen)
	}
	b.check("limit", errors.New("not a number"))
	b.check("after", errors.New("second"))
	var invalid *InvalidParamFormatError
	if !b.failed || len(seen) != 1 || !errors.As(seen[0], &invalid) || invalid.ParamName != "limit" {
		t.Errorf("answers = %#v, want only the limit failure", seen)
	}
}

func TestRenewAndEndHandlersReadTheirOptionalBodies(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	s.task(`{"initiative":"aa","title":"one"}`)
	s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-1","intent":"work"}`, "claude@host")
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/api/v1/leases/L1/renew", `{"ttl_seconds":3600,"intent":"more","resources":["r"]}`, http.StatusOK},
		{"/api/v1/leases/L1/renew", ``, http.StatusOK},
		{"/api/v1/leases/L1/renew", `{"intent":""}`, http.StatusBadRequest},
		{"/api/v1/leases/L1/renew", `{broken`, http.StatusBadRequest},
		{"/api/v1/leases/L1/end", `{broken`, http.StatusBadRequest},
		{"/api/v1/leases", `{broken`, http.StatusBadRequest},
		{"/api/v1/leases/L1/end", ``, http.StatusOK},
		{"/api/v1/leases/L1/renew", ``, http.StatusConflict},
	} {
		if rec := s.send(http.MethodPost, tc.path, tc.body, "claude@host"); rec.Code != tc.want {
			t.Errorf("POST %s %q = %d %s, want %d", tc.path, tc.body, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestNowWithoutAPrincipalIs401(t *testing.T) {
	t.Parallel()
	full := nowOps{checkin: checkin.Store{Leases: &leases.Store{}, Asks: &asks.Store{}, Notes: &notes.Store{}, Cursors: &cursors.Store{}, Attention: &attention.Store{}}}
	got, _ := full.GetNow(context.Background(), GetNowRequestObject{})
	if p, ok := got.(problemResponse); !ok || p.status != http.StatusUnauthorized {
		t.Errorf("now without a principal = %#v, want 401", got)
	}
}

func TestQueriedResourcesAreSplitOnCommasAndValidated(t *testing.T) {
	t.Parallel()
	ptr := func(s string) *string { return &s }
	for raw, want := range map[string][]string{"a,b": {"a", "b"}, "a,a": {"a"}, "repo:x": {"repo:x"}} {
		if got, err := queriedResources(ptr(raw)); err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("queriedResources(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []*string{nil, ptr("")} {
		if got, err := queriedResources(raw); err != nil || len(got) != 0 {
			t.Errorf("queriedResources(%v) = %v, %v; want none", raw, got, err)
		}
	}
	for _, raw := range []string{"a,,b", "a,", strings.Repeat("x", 101)} {
		if _, err := queriedResources(ptr(raw)); err == nil {
			t.Errorf("queriedResources(%q) was accepted", raw)
		}
	}
}

func TestNowRoundTripThroughTheHandler(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	s.task(`{"initiative":"aa","title":"one"}`)
	s.task(`{"initiative":"aa","title":"two"}`)
	s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-1","intent":"work","resources":["db:x"]}`, "claude@host")
	s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-2","intent":"also","resources":["db:x","db:y"]}`, "codex@host")
	rec := s.send(http.MethodGet, "/api/v1/now?resources=db:y,db:z", "", "claude@host")
	doc := decode(t, rec)
	if rec.Code != http.StatusOK || len(doc) != 9 {
		t.Fatalf("now = %d %s, want exactly nine members (notices joined the eight, R9)", rec.Code, rec.Body)
	}
	for _, k := range []string{"actor", "my_leases", "others_leases", "conflicts", "my_asks", "notes", "notes_more", "attention"} {
		if doc[k] == nil {
			t.Errorf("now has no %s: %s", k, rec.Body)
		}
	}
	mine := doc["my_leases"].([]any)[0].(map[string]any)
	if doc["actor"] != "claude@host" || mine["title"] != "one" || mine["task"] != "aa-1" || mine["expires_at"] == nil {
		t.Errorf("now = %s", rec.Body)
	}
	if c := doc["conflicts"].([]any); len(c) != 1 || c[0].(map[string]any)["lease"] != "L2" {
		t.Errorf("conflicts = %v, want L2 once", c)
	}
	for path, want := range map[string]int{
		"/api/v1/now?resources=a,,b":          http.StatusBadRequest,
		"/api/v1/now?resources=a&resources=b": http.StatusBadRequest,
		"/api/v1/now?resources=":              http.StatusOK,
	} {
		if rec := s.send(http.MethodGet, path, "", "claude@host"); rec.Code != want {
			t.Errorf("GET %s = %d %s, want %d", path, rec.Code, rec.Body, want)
		}
	}
}
