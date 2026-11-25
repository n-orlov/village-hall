package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oapi-codegen/runtime"

	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/initiatives"
)

func (s *keyServer) create(slug, extra string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.send(http.MethodPost, "/api/v1/initiatives", fmt.Sprintf(`{"slug":%q,"title":"Title of %s"%s}`, slug, slug, extra), "claude@host")
}

func TestCreateInitiativeReturnsItAndAppendsAnEventWithPrincipalAndActor(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	rec := s.create("phone-onboard", `,"description":"# Plan","tags":["Work"," homelab","work"],"target_start":"2026-03-01","target_end":"2026-04-01"`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	doc := decode(t, rec)
	for k, want := range map[string]any{
		"slug": "phone-onboard", "ref": "phone-onboard", "title": "Title of phone-onboard", "description": "# Plan",
		"status": "open", "created_by": "claude@host", "target_start": "2026-03-01", "target_end": "2026-04-01",
	} {
		if doc[k] != want {
			t.Errorf("%s = %v, want %v", k, doc[k], want)
		}
	}
	if fmt.Sprint(doc["tags"]) != "[homelab work]" || len(fmt.Sprint(doc["id"])) != 26 {
		t.Errorf("tags %v id %v", doc["tags"], doc["id"])
	}
	if _, has := doc["closed_at"]; has {
		t.Error("an open initiative carries closed_at")
	}
	ev := s.events()
	if len(ev) != 1 || ev[0].typ != "initiative_created" || ev[0].principal != "key:caller" || ev[0].actor != "claude@host" || ev[0].ref != doc["id"] {
		t.Fatalf("events = %+v", ev)
	}
}

func TestCreateInitiativeRefusesBadFieldsWithAProblemAndNext(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	cases := map[string]string{
		"x":                     "",
		"Has-Upper":             "",
		strings.Repeat("a", 41): "",
		"ok-slug":               `,"tags":["no way"]`,
		"ok-slug2":              `,"target_start":"2026-04-01","target_end":"2026-03-01"`,
		"ok-slug3":              `,"description":"` + strings.Repeat("x", 20001) + `"`,
	}
	for slug, extra := range cases {
		rec := s.create(slug, extra)
		doc := decode(t, rec)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/problem+json" || doc["next"] == "" || doc["next"] == nil || doc["detail"] == nil {
			t.Errorf("%q%s: %d %v", slug, extra[:min(len(extra), 30)], rec.Code, doc)
		}
	}
	if rec := s.send(http.MethodPost, "/api/v1/initiatives", `{"slug":"abc","title":"  "}`, "a"); rec.Code != 400 {
		t.Errorf("blank title = %d", rec.Code)
	}
	if rec := s.send(http.MethodPost, "/api/v1/initiatives", `null`, "a"); rec.Code != 400 {
		t.Errorf("null body = %d", rec.Code)
	}
	if rec := s.send(http.MethodPost, "/api/v1/initiatives", `{"slug":"abc","title":"t","target_start":"soon"}`, "a"); rec.Code != 400 {
		t.Errorf("bad date = %d", rec.Code)
	}
	if n := len(s.events()); n != 0 {
		t.Errorf("refused creations left %d events", n)
	}
}

func TestCreateInitiativeDuplicateIsConflictWithNext(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("phone-onboard", "")
	rec := s.create("phone-onboard", "")
	doc := decode(t, rec)
	if rec.Code != http.StatusConflict || !strings.Contains(fmt.Sprint(doc["next"]), "GET /api/v1/initiatives/phone-onboard") {
		t.Fatalf("duplicate = %d %v", rec.Code, doc)
	}
	if n := len(s.events()); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

func TestGetInitiativeBySlugAndUlidAndNotFound(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	id := decode(t, s.create("phone-onboard", `,"tags":["work"]`))["id"].(string)
	for _, ref := range []string{"phone-onboard", id} {
		rec := s.send(http.MethodGet, "/api/v1/initiatives/"+ref, "", "a")
		doc := decode(t, rec)
		if rec.Code != 200 || doc["id"] != id || fmt.Sprint(doc["tags"]) != "[work]" {
			t.Errorf("GET %s = %d %v", ref, rec.Code, doc)
		}
	}
	rec := s.send(http.MethodGet, "/api/v1/initiatives/nope", "", "a")
	if doc := decode(t, rec); rec.Code != 404 || !strings.Contains(fmt.Sprint(doc["next"]), "GET /api/v1/initiatives") {
		t.Errorf("missing = %d %v", rec.Code, doc)
	}
}

func TestListInitiativesPagesFiltersAndRefusesBadParameters(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa-one", `,"tags":["work","x"]`)
	s.create("bb-two", `,"tags":["work"]`)
	s.create("cc-three", `,"tags":["home"]`)
	rec := s.send(http.MethodGet, "/api/v1/initiatives?limit=2", "", "a")
	doc := decode(t, rec)
	items := doc["items"].([]any)
	if rec.Code != 200 || len(items) != 2 || doc["next_after"] != items[1].(map[string]any)["id"] {
		t.Fatalf("page 1 = %d %v", rec.Code, doc)
	}
	rec = s.send(http.MethodGet, "/api/v1/initiatives?limit=2&after="+doc["next_after"].(string), "", "a")
	doc = decode(t, rec)
	items = doc["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["slug"] != "cc-three" || doc["next_after"] != nil {
		t.Fatalf("page 2 = %v", doc)
	}
	rec = s.send(http.MethodGet, "/api/v1/initiatives?after=aa-one&tag=work", "", "a")
	if items := decode(t, rec)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["slug"] != "bb-two" {
		t.Errorf("after slug + tag = %v", items)
	}
	rec = s.send(http.MethodGet, "/api/v1/initiatives?tag=work&tag=x", "", "a")
	if items := decode(t, rec)["items"].([]any); len(items) != 1 {
		t.Errorf("two tags = %v", items)
	}
	for _, q := range []string{"limit=abc", "limit=0", "limit=1001", "limit=-3", "after=Bad!", "after=zz-nope", "tag=no%20way"} {
		rec := s.send(http.MethodGet, "/api/v1/initiatives?"+q, "", "a")
		doc := decode(t, rec)
		if rec.Code != 400 || rec.Header().Get("Content-Type") != "application/problem+json" || doc["next"] == nil {
			t.Errorf("?%s = %d %v", q, rec.Code, doc)
		}
	}
}

func TestInitiativesAreUnavailableWithoutAStore(t *testing.T) {
	t.Parallel()
	h := NewHandler(ok, acceptKey, nil, nil)
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/initiatives"}, {"POST", "/api/v1/initiatives"}, {"GET", "/api/v1/initiatives/abc"}, {"PATCH", "/api/v1/initiatives/abc"}} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"slug":"abc","title":"t"}`))
		r.Header.Set("Authorization", "Bearer "+testKey)
		r.Header.Set(ActorHeader, "tester")
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d", c.method, c.path, rec.Code)
		}
	}
}

func TestInitiativeStatusEnumAcceptsOnlyTheDeclaredStates(t *testing.T) {
	t.Parallel()
	if !InitiativeStatusOpen.Valid() || !InitiativeStatusClosed.Valid() || InitiativeStatus("gone").Valid() {
		t.Error("InitiativeStatus.Valid is wrong")
	}
}

func TestProblemDocumentsCarryTheDefaultNextOfTheirStatus(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]string{401: "Bearer", 404: "Check the path", 409: "Read what exists", 503: "Try again", 400: "Fix the request"} {
		rec := httptest.NewRecorder()
		writeProblem(rec, status, "d")
		if got := decode(t, rec)["next"]; !strings.Contains(fmt.Sprint(got), want) {
			t.Errorf("%d: next = %v, want it to contain %q", status, got, want)
		}
	}
}

func TestBindFailedAnswersRequiredAndInvalidParameters(t *testing.T) {
	t.Parallel()
	var seen []error
	siw := &ServerInterfaceWrapper{ErrorHandlerFunc: func(_ http.ResponseWriter, _ *http.Request, err error) { seen = append(seen, err) }}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	siw.bindFailed(nil, r, "limit", &runtime.RequiredParameterError{ParamName: "limit"})
	siw.bindFailed(nil, r, "limit", errors.New("not a number"))
	var required *RequiredParamError
	var invalid *InvalidParamFormatError
	if len(seen) != 2 || !errors.As(seen[0], &required) || required.ParamName != "limit" || !errors.As(seen[1], &invalid) || invalid.ParamName != "limit" {
		t.Errorf("answers = %#v", seen)
	}
}

func (s *keyServer) patch(ref, body string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.send(http.MethodPatch, "/api/v1/initiatives/"+ref, body, "editor@host")
}

func TestPatchInitiativeChangesFieldsAndWritesOneEvent(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", `,"tags":["old"],"target_start":"2026-03-01"`)
	rec := s.patch("aa", `{"title":"New","tags":["B","a"],"target_start":null,"target_end":"2026-05-01"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	doc := decode(t, rec)
	if doc["title"] != "New" || fmt.Sprint(doc["tags"]) != "[a b]" || doc["target_end"] != "2026-05-01" {
		t.Errorf("document = %v", doc)
	}
	if _, has := doc["target_start"]; has {
		t.Errorf("a cleared date is still there: %v", doc)
	}
	ev := s.events()
	last := ev[len(ev)-1]
	if len(ev) != 2 || last.typ != "initiative_updated" || last.actor != "editor@host" || last.principal != "key:caller" ||
		last.data != `{"changed":["title","tags","target_start","target_end"],"slug":"aa"}` {
		t.Fatalf("events = %+v", ev)
	}
}

func TestPatchInitiativeRefusesTheSlugANullTitleAndAMissingBody(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	for name, body := range map[string]string{
		"slug": `{"slug":"aa"}`, "other slug": `{"slug":"bb","title":"x"}`, "null slug": `{"slug":null}`,
		"null title": `{"title":null}`, "bad tag": `{"tags":["no way"]}`, "bad dates": `{"target_start":"2026-02-01","target_end":"2026-01-01"}`,
		"unreadable date": `{"target_end":"soon"}`,
	} {
		rec := s.patch("aa", body)
		doc := decode(t, rec)
		if rec.Code != http.StatusBadRequest || doc["next"] == nil || doc["detail"] == nil {
			t.Errorf("%s: %d %v", name, rec.Code, doc)
		}
	}
	if n := len(s.events()); n != 1 {
		t.Errorf("refusals wrote events: %d", n)
	}
	if rec := s.patch("aa", ``); rec.Code != http.StatusBadRequest {
		t.Errorf("zero-byte body = %d", rec.Code)
	}
	if r := initiativePatchRefusal(nil); r == nil || r.status != http.StatusBadRequest || r.next == "" {
		t.Errorf("no body: %+v", r)
	}
}

func TestPatchInitiativeUnknownIsNotFoundAndNoOpIsOK(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	if rec := s.patch("zz", `{"title":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown = %d", rec.Code)
	}
	if rec := s.patch("aa", `{}`); rec.Code != http.StatusOK {
		t.Errorf("no-op = %d", rec.Code)
	}
	if n := len(s.events()); n != 1 {
		t.Errorf("events = %d", n)
	}
}

func TestCloseAndReopenInitiativeAnswerTheInitiativeAndWriteEvents(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	s.task(`{"initiative":"aa","title":"one"}`)
	rec := s.send(http.MethodPost, "/api/v1/initiatives/aa/close", "", "op@host")
	doc := decode(t, rec)
	if rec.Code != http.StatusConflict || !strings.Contains(fmt.Sprint(doc["next"]), "force") {
		t.Fatalf("close without force = %d %v", rec.Code, doc)
	}
	rec = s.send(http.MethodPost, "/api/v1/initiatives/aa/close", `{"force":true}`, "op@host")
	doc = decode(t, rec)
	if rec.Code != http.StatusOK || doc["status"] != "closed" || doc["closed_by"] != "op@host" || doc["closed_at"] == nil {
		t.Fatalf("close = %d %v", rec.Code, doc)
	}
	if rec = s.send(http.MethodPost, "/api/v1/initiatives/aa/close", "", "op@host"); rec.Code != http.StatusConflict {
		t.Errorf("close closed = %d", rec.Code)
	}
	rec = s.send(http.MethodPost, "/api/v1/initiatives/aa/reopen", "", "op@host")
	doc = decode(t, rec)
	if rec.Code != http.StatusOK || doc["status"] != "open" || doc["closed_at"] != nil || doc["closed_by"] != nil {
		t.Fatalf("reopen = %d %v", rec.Code, doc)
	}
	if rec = s.send(http.MethodPost, "/api/v1/initiatives/aa/reopen", "", "op@host"); rec.Code != http.StatusConflict {
		t.Errorf("reopen open = %d", rec.Code)
	}
	var types []string
	for _, e := range s.events() {
		types = append(types, e.typ)
	}
	if got := strings.Join(types, ","); got != "initiative_created,task_created,task_closed,initiative_closed,initiative_reopened" {
		t.Errorf("events = %s", got)
	}
}

func TestCloseAndReopenUnknownInitiativeAreNotFound(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	for _, verb := range []string{"close", "reopen"} {
		rec := s.send(http.MethodPost, "/api/v1/initiatives/zz/"+verb, "", "op@host")
		if rec.Code != http.StatusNotFound || decode(t, rec)["next"] == nil {
			t.Errorf("%s = %d %s", verb, rec.Code, rec.Body)
		}
	}
}

func TestCloseInitiativeNeedsTheTaskStoreAndReopenNeedsTheInitiativeStore(t *testing.T) {
	t.Parallel()
	h := NewHandler(ok, acceptKey, nil, nil)
	for _, verb := range []string{"close", "reopen"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedWith("POST", "/api/v1/initiatives/aa/"+verb, ""))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d", verb, rec.Code)
		}
	}
}

func TestCloseInitiativeRefusesABodyThatIsNotJSON(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	rec := s.send(http.MethodPost, "/api/v1/initiatives/aa/close", `{"force":`, "op@host")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("close = %d %s", rec.Code, rec.Body)
	}
	if got := decode(t, s.send(http.MethodGet, "/api/v1/initiatives/aa", "", "op@host")); got["status"] != "open" {
		t.Errorf("status = %v", got["status"])
	}
}

func TestListInitiativesFiltersByStatusAndCarriesCountsAndLiveLeases(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	s.create("bb", "")
	if rec := s.send(http.MethodPost, "/api/v1/tasks", `{"initiative":"aa","title":"t"}`, "a"); rec.Code != 201 {
		t.Fatalf("task = %d %s", rec.Code, rec.Body)
	}
	if rec := s.send(http.MethodPost, "/api/v1/leases", `{"task":"aa-1","intent":"work"}`, "a"); rec.Code != 201 {
		t.Fatalf("lease = %d %s", rec.Code, rec.Body)
	}
	if rec := s.send(http.MethodPost, "/api/v1/initiatives/bb/close", "", "a"); rec.Code != 200 {
		t.Fatalf("close = %d %s", rec.Code, rec.Body)
	}
	doc := decode(t, s.send(http.MethodGet, "/api/v1/initiatives", "", "a"))
	items := doc["items"].([]any)
	first := items[0].(map[string]any)
	if len(items) != 1 || first["slug"] != "aa" || first["live_leases"] != float64(1) ||
		fmt.Sprint(first["counts"]) != "map[closed:0 done:0 open:1]" {
		t.Fatalf("open list = %v", doc)
	}
	doc = decode(t, s.send(http.MethodGet, "/api/v1/initiatives?status=closed&closed_within=7d", "", "a"))
	if items := doc["items"].([]any); len(items) != 1 || items[0].(map[string]any)["slug"] != "bb" {
		t.Errorf("closed list = %v", doc)
	}
	for _, q := range []string{"status=gone", "closed_within=7d", "status=closed&closed_within=x"} {
		if rec := s.send(http.MethodGet, "/api/v1/initiatives?"+q, "", "a"); rec.Code != 400 {
			t.Errorf("?%s = %d", q, rec.Code)
		}
	}
}

func TestListInitiativesNeedsTheTaskAndLeaseStoresToCount(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	h := NewHandler(ok, (&auth.Verifier{Store: s.st}).Verify, nil, s.keys, WithInitiatives(&initiatives.Store{Store: s.st}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/initiatives", nil)
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set(ActorHeader, "a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "counts") {
		t.Errorf("list without the task store = %d %s", rec.Code, rec.Body)
	}
}

func TestListEnumsAcceptOnlyTheDeclaredValues(t *testing.T) {
	t.Parallel()
	valid := []interface{ Valid() bool }{
		InitiativeListItemStatusOpen, InitiativeListItemStatusClosed,
		ListInitiativesParamsStatusOpen, ListInitiativesParamsStatusClosed, ListInitiativesParamsStatusAll,
		TaskListItemKindWork, TaskListItemKindWatch, TaskListItemStatusOpen, TaskListItemStatusDone, TaskListItemStatusClosed,
		TaskListItemResolutionCompleted, TaskListItemResolutionDropped, TaskListItemResolutionSuperseded, TaskListItemResolutionDuplicate,
		ListTasksParamsKindWork, ListTasksParamsKindWatch,
		ListTasksParamsStatusOpen, ListTasksParamsStatusDone, ListTasksParamsStatusClosed, ListTasksParamsStatusNotClosed, ListTasksParamsStatusAll,
		CountTasksParamsKindWork, CountTasksParamsKindWatch,
		CountTasksParamsStatusOpen, CountTasksParamsStatusDone, CountTasksParamsStatusClosed, CountTasksParamsStatusNotClosed, CountTasksParamsStatusAll,
		GetTagSourcesParamsStatusOpen, GetTagSourcesParamsStatusDone, GetTagSourcesParamsStatusClosed, GetTagSourcesParamsStatusNotClosed, GetTagSourcesParamsStatusAll,
		NowLeaseStateLive, NowLeaseStateEnded, NowLeaseStateExpired,
		InitiativeDetailStatusOpen, InitiativeDetailStatusClosed,
		TaskAttentionReasonStranded, TaskAttentionReasonAsking, TaskAttentionReasonDone,
	}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("%v is declared but reported invalid", v)
		}
	}
	for _, v := range []interface{ Valid() bool }{
		InitiativeListItemStatus("x"), ListInitiativesParamsStatus("x"), TaskListItemKind("x"), TaskListItemStatus("x"),
		TaskListItemResolution("x"), ListTasksParamsKind("x"), ListTasksParamsStatus("x"), CountTasksParamsKind("x"), CountTasksParamsStatus("x"), GetTagSourcesParamsStatus("x"), NowLeaseState("x"),
		InitiativeDetailStatus("x"), TaskAttentionReason("x"),
	} {
		if v.Valid() {
			t.Errorf("%T: an undeclared value is valid", v)
		}
	}
}
