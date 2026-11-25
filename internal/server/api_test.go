package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/api"
	"go.yaml.in/yaml/v3"
)

func TestSpecDocumentsAreServedToAnAuthenticatedCaller(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, authed("/api/openapi.yaml"))
	if rec.Code != http.StatusOK || rec.Body.String() != string(api.YAML()) {
		t.Fatalf("GET /api/openapi.yaml = %d, body is not the checked-in document", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("GET /api/openapi.yaml Content-Type = %q, want application/yaml", ct)
	}
	var fromYAML map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &fromYAML); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, authed("/api/openapi.json"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/openapi.json = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET /api/openapi.json Content-Type = %q, want application/json", ct)
	}
	var fromJSON map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fromJSON); err != nil {
		t.Fatal(err)
	}
	// Both documents declare the same paths.
	yamlPaths, jsonPaths := fromYAML["paths"].(map[string]any), fromJSON["paths"].(map[string]any)
	if len(yamlPaths) == 0 || len(yamlPaths) != len(jsonPaths) {
		t.Fatalf("YAML declares %d paths, JSON %d", len(yamlPaths), len(jsonPaths))
	}
	for p := range yamlPaths {
		if _, ok := jsonPaths[p]; !ok {
			t.Errorf("JSON document lacks path %s", p)
		}
	}
}

var (
	externalURL = regexp.MustCompile(`https?://`)
	assetRef    = regexp.MustCompile(`(?:src|href)="([^"]+)"`)
)

func TestDocsPageIsSwaggerUIFromEmbeddedAssetsAndReferencesNoExternalHost(t *testing.T) {
	t.Parallel()
	h := NewHandler(ok, acceptKey, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed("/api/docs"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/docs = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html" {
		t.Errorf("GET /api/docs Content-Type = %q, want text/html", ct)
	}
	page := rec.Body.String()
	if externalURL.MatchString(page) {
		t.Errorf("GET /api/docs references an external host: %q", page)
	}
	refs := assetRef.FindAllStringSubmatch(page, -1)
	if len(refs) == 0 {
		t.Fatalf("GET /api/docs references no asset: %q", page)
	}
	served := ""
	for _, ref := range refs {
		url := ref[1]
		if !strings.HasPrefix(url, "/") || strings.HasPrefix(url, "//") {
			t.Errorf("asset URL %q is not a path on this host", url)
			continue
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authed(url))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d with %d bytes, want 200 with a body", url, rec.Code, rec.Body.Len())
		}
		served += rec.Body.String()[:min(rec.Body.Len(), 1<<20)]
		if want := map[string]string{".js": "text/javascript", ".css": "text/css"}[url[strings.LastIndex(url, "."):]]; rec.Header().Get("Content-Type") != want {
			t.Errorf("GET %s Content-Type = %q, want %q", url, rec.Header().Get("Content-Type"), want)
		}
	}
	if !strings.Contains(served, "SwaggerUIBundle") {
		t.Error("the served assets do not contain Swagger UI")
	}
}

func TestDocsInitScriptNamesNoExternalHostAndLoadsTheSpecFromThisOne(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, authed("/api/docs/assets/init.js"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET init.js = %d, want 200", rec.Code)
	}
	if externalURL.MatchString(rec.Body.String()) {
		t.Errorf("init.js references an external host: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "'/api/openapi.json'") {
		t.Errorf("init.js does not load /api/openapi.json: %q", rec.Body.String())
	}
}

func TestUnknownDocsAssetIs404AProblem(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, authed("/api/docs/assets/nope.js"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/docs/assets/nope.js = %d, want 404", rec.Code)
	}
	doc := problem(t, rec)
	if doc["status"].(float64) != 404 {
		t.Errorf("problem = %v", doc)
	}
	if next, _ := doc["next"].(string); !strings.Contains(next, "/api/docs") {
		t.Errorf("unknown asset problem next = %q, want advice naming /api/docs", next)
	}
}

// TestEveryUnknownDocsAssetNameAnswersAProblemWithNext holds the rule behind the
// unknown asset: any name the embedded set lacks, however spelled, is a 404
// problem document that keeps its fields and names a next action.
func TestEveryUnknownDocsAssetNameAnswersAProblemWithNext(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"nope.js", "INIT.JS", "init.js.map", "%2e%2e%2fetc", "swagger-ui.css.gz"} {
		rec := httptest.NewRecorder()
		NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, authed("/api/docs/assets/"+name))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET assets/%s = %d, want 404", name, rec.Code)
			continue
		}
		doc := problem(t, rec)
		if next, _ := doc["next"].(string); strings.TrimSpace(next) == "" {
			t.Errorf("assets/%s: problem %v has no next", name, doc)
		}
		for _, f := range []string{"type", "title", "detail"} {
			if s, _ := doc[f].(string); s == "" {
				t.Errorf("assets/%s: problem %v lost %s", name, doc, f)
			}
		}
	}
}

func TestDocsAssetsNeedACredential(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/docs", "/api/docs/assets/init.js", "/api/docs/assets/nope.js"} {
		if rec := request(path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no credential = %d, want 401", path, rec.Code)
		}
	}
}

func TestSpecDocumentsRefuseACallerWithoutCredentials(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/openapi.yaml", "/api/openapi.json", "/api/docs"} {
		rec := httptest.NewRecorder()
		NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without credentials = %d, want 401", path, rec.Code)
		}
	}
}

// stubOperations answers every operation with the configured behaviour.
type stubOperations struct {
	mode string
}

// brokenResponse is a response object of every operation whose body cannot be
// written.
type brokenResponse struct{}

var errBroken = errors.New("cannot write")

func (brokenResponse) VisitGetHealthzResponse(http.ResponseWriter) error     { return errBroken }
func (brokenResponse) VisitGetReadyzResponse(http.ResponseWriter) error      { return errBroken }
func (brokenResponse) VisitGetOpenapiYAMLResponse(http.ResponseWriter) error { return errBroken }
func (brokenResponse) VisitGetOpenapiJSONResponse(http.ResponseWriter) error { return errBroken }
func (brokenResponse) VisitGetDocsResponse(http.ResponseWriter) error        { return errBroken }
func (brokenResponse) VisitGetDocsAssetResponse(http.ResponseWriter) error   { return errBroken }
func (brokenResponse) VisitGetAuthLoginResponse(http.ResponseWriter) error   { return errBroken }
func (brokenResponse) VisitGetAuthOidcCallbackResponse(http.ResponseWriter) error {
	return errBroken
}
func (brokenResponse) VisitGetAuthSessionResponse(http.ResponseWriter) error { return errBroken }
func (brokenResponse) VisitPostAuthLogoutResponse(http.ResponseWriter) error { return errBroken }
func (brokenResponse) VisitListKeysResponse(http.ResponseWriter) error       { return errBroken }
func (brokenResponse) VisitCreateKeyResponse(http.ResponseWriter) error      { return errBroken }
func (brokenResponse) VisitRevokeKeyResponse(http.ResponseWriter) error      { return errBroken }
func (brokenResponse) VisitListInitiativesResponse(http.ResponseWriter) error {
	return errBroken
}
func (brokenResponse) VisitCreateInitiativeResponse(http.ResponseWriter) error {
	return errBroken
}
func (brokenResponse) VisitGetInitiativeResponse(http.ResponseWriter) error       { return errBroken }
func (brokenResponse) VisitPatchInitiativeResponse(http.ResponseWriter) error     { return errBroken }
func (brokenResponse) VisitCloseInitiativeResponse(http.ResponseWriter) error     { return errBroken }
func (brokenResponse) VisitReopenInitiativeResponse(http.ResponseWriter) error    { return errBroken }
func (brokenResponse) VisitCreateTaskResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitGetTaskResponse(http.ResponseWriter) error             { return errBroken }
func (brokenResponse) VisitPatchTaskResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitDoneTaskResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitReopenTaskResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitCloseTaskResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitAddTaskDepResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitRemoveTaskDepResponse(http.ResponseWriter) error       { return errBroken }
func (brokenResponse) VisitAddTaskTagResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitRemoveTaskTagResponse(http.ResponseWriter) error       { return errBroken }
func (brokenResponse) VisitRecordTaskCheckResponse(http.ResponseWriter) error     { return errBroken }
func (brokenResponse) VisitRecordTaskMilestoneResponse(http.ResponseWriter) error { return errBroken }
func (brokenResponse) VisitListTaskEventsResponse(http.ResponseWriter) error      { return errBroken }
func (brokenResponse) VisitListTasksResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitCountTasksResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitListEventsResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitListNotificationsResponse(http.ResponseWriter) error   { return errBroken }
func (brokenResponse) VisitGetTimelineResponse(http.ResponseWriter) error         { return errBroken }
func (brokenResponse) VisitListActorsResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitGetActorResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitStartLeaseResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitListLeasesResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitGetNowResponse(http.ResponseWriter) error              { return errBroken }
func (brokenResponse) VisitStartResponse(http.ResponseWriter) error               { return errBroken }
func (brokenResponse) VisitFinishResponse(http.ResponseWriter) error              { return errBroken }
func (brokenResponse) VisitPingResponse(http.ResponseWriter) error                { return errBroken }
func (brokenResponse) VisitCatchupResponse(http.ResponseWriter) error             { return errBroken }
func (brokenResponse) VisitGetLeaseResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitRenewLeaseResponse(http.ResponseWriter) error          { return errBroken }
func (brokenResponse) VisitEndLeaseResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitAckLeaseResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitRaiseAskResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitListAsksResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitGetAskResponse(http.ResponseWriter) error              { return errBroken }
func (brokenResponse) VisitAnswerAskResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitWithdrawAskResponse(http.ResponseWriter) error         { return errBroken }
func (brokenResponse) VisitPostNoteResponse(http.ResponseWriter) error            { return errBroken }
func (brokenResponse) VisitListNotesResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitGetAttentionResponse(http.ResponseWriter) error        { return errBroken }
func (brokenResponse) VisitGetAttentionNowResponse(http.ResponseWriter) error     { return errBroken }
func (brokenResponse) VisitGetStreamResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitGetNoteResponse(http.ResponseWriter) error             { return errBroken }
func (brokenResponse) VisitPutCursorResponse(http.ResponseWriter) error           { return errBroken }
func (brokenResponse) VisitGetMetaResponse(http.ResponseWriter) error             { return errBroken }
func (brokenResponse) VisitPutProtocolSeenResponse(http.ResponseWriter) error {
	return errBroken
}

// outcome is what every stub operation returns: an error or a response that
// fails to write.
func (s stubOperations) outcome() (any, error) {
	if s.mode == "fails" {
		return nil, errBroken
	}
	return brokenResponse{}, nil
}

func (s stubOperations) GetHealthz(context.Context, GetHealthzRequestObject) (GetHealthzResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetHealthzResponseObject)
	return resp, err
}

func (s stubOperations) GetReadyz(context.Context, GetReadyzRequestObject) (GetReadyzResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetReadyzResponseObject)
	return resp, err
}

func (s stubOperations) GetOpenapiYAML(context.Context, GetOpenapiYAMLRequestObject) (GetOpenapiYAMLResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetOpenapiYAMLResponseObject)
	return resp, err
}

func (s stubOperations) GetOpenapiJSON(context.Context, GetOpenapiJSONRequestObject) (GetOpenapiJSONResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetOpenapiJSONResponseObject)
	return resp, err
}

func (s stubOperations) GetDocsAsset(context.Context, GetDocsAssetRequestObject) (GetDocsAssetResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetDocsAssetResponseObject)
	return resp, err
}

func (s stubOperations) GetAuthLogin(context.Context, GetAuthLoginRequestObject) (GetAuthLoginResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetAuthLoginResponseObject)
	return resp, err
}

func (s stubOperations) GetAuthOidcCallback(context.Context, GetAuthOidcCallbackRequestObject) (GetAuthOidcCallbackResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetAuthOidcCallbackResponseObject)
	return resp, err
}

func (s stubOperations) GetAuthSession(context.Context, GetAuthSessionRequestObject) (GetAuthSessionResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetAuthSessionResponseObject)
	return resp, err
}

func (s stubOperations) PostAuthLogout(context.Context, PostAuthLogoutRequestObject) (PostAuthLogoutResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PostAuthLogoutResponseObject)
	return resp, err
}

func (s stubOperations) ListKeys(context.Context, ListKeysRequestObject) (ListKeysResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListKeysResponseObject)
	return resp, err
}

func (s stubOperations) CreateKey(context.Context, CreateKeyRequestObject) (CreateKeyResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CreateKeyResponseObject)
	return resp, err
}

func (s stubOperations) RevokeKey(context.Context, RevokeKeyRequestObject) (RevokeKeyResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RevokeKeyResponseObject)
	return resp, err
}

func (s stubOperations) ListInitiatives(context.Context, ListInitiativesRequestObject) (ListInitiativesResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListInitiativesResponseObject)
	return resp, err
}

func (s stubOperations) CreateInitiative(context.Context, CreateInitiativeRequestObject) (CreateInitiativeResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CreateInitiativeResponseObject)
	return resp, err
}

func (s stubOperations) GetInitiative(context.Context, GetInitiativeRequestObject) (GetInitiativeResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetInitiativeResponseObject)
	return resp, err
}

func (s stubOperations) PatchInitiative(context.Context, PatchInitiativeRequestObject) (PatchInitiativeResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PatchInitiativeResponseObject)
	return resp, err
}

func (s stubOperations) CloseInitiative(context.Context, CloseInitiativeRequestObject) (CloseInitiativeResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CloseInitiativeResponseObject)
	return resp, err
}

func (s stubOperations) ReopenInitiative(context.Context, ReopenInitiativeRequestObject) (ReopenInitiativeResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ReopenInitiativeResponseObject)
	return resp, err
}

func (s stubOperations) CreateTask(context.Context, CreateTaskRequestObject) (CreateTaskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CreateTaskResponseObject)
	return resp, err
}

func (s stubOperations) GetTask(context.Context, GetTaskRequestObject) (GetTaskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetTaskResponseObject)
	return resp, err
}

func (s stubOperations) PatchTask(context.Context, PatchTaskRequestObject) (PatchTaskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PatchTaskResponseObject)
	return resp, err
}

func (s stubOperations) DoneTask(context.Context, DoneTaskRequestObject) (DoneTaskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(DoneTaskResponseObject)
	return resp, err
}

func (s stubOperations) ReopenTask(context.Context, ReopenTaskRequestObject) (ReopenTaskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ReopenTaskResponseObject)
	return resp, err
}

func (s stubOperations) CloseTask(context.Context, CloseTaskRequestObject) (CloseTaskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CloseTaskResponseObject)
	return resp, err
}

func (s stubOperations) AddTaskDep(context.Context, AddTaskDepRequestObject) (AddTaskDepResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(AddTaskDepResponseObject)
	return resp, err
}

func (s stubOperations) RemoveTaskDep(context.Context, RemoveTaskDepRequestObject) (RemoveTaskDepResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RemoveTaskDepResponseObject)
	return resp, err
}

func (s stubOperations) AddTaskTag(context.Context, AddTaskTagRequestObject) (AddTaskTagResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(AddTaskTagResponseObject)
	return resp, err
}

func (s stubOperations) RemoveTaskTag(context.Context, RemoveTaskTagRequestObject) (RemoveTaskTagResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RemoveTaskTagResponseObject)
	return resp, err
}

func (s stubOperations) RecordTaskCheck(context.Context, RecordTaskCheckRequestObject) (RecordTaskCheckResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RecordTaskCheckResponseObject)
	return resp, err
}

func (s stubOperations) RecordTaskMilestone(context.Context, RecordTaskMilestoneRequestObject) (RecordTaskMilestoneResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RecordTaskMilestoneResponseObject)
	return resp, err
}

func (s stubOperations) ListTasks(context.Context, ListTasksRequestObject) (ListTasksResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListTasksResponseObject)
	return resp, err
}

func (s stubOperations) CountTasks(context.Context, CountTasksRequestObject) (CountTasksResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CountTasksResponseObject)
	return resp, err
}

func (s stubOperations) ListTaskEvents(context.Context, ListTaskEventsRequestObject) (ListTaskEventsResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListTaskEventsResponseObject)
	return resp, err
}

func (s stubOperations) ListEvents(context.Context, ListEventsRequestObject) (ListEventsResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListEventsResponseObject)
	return resp, err
}

func (s stubOperations) ListNotifications(context.Context, ListNotificationsRequestObject) (ListNotificationsResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListNotificationsResponseObject)
	return resp, err
}

func (s stubOperations) GetTimeline(context.Context, GetTimelineRequestObject) (GetTimelineResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetTimelineResponseObject)
	return resp, err
}

func (s stubOperations) ListActors(context.Context, ListActorsRequestObject) (ListActorsResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListActorsResponseObject)
	return resp, err
}

func (s stubOperations) GetActor(context.Context, GetActorRequestObject) (GetActorResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetActorResponseObject)
	return resp, err
}

func (s stubOperations) StartLease(context.Context, StartLeaseRequestObject) (StartLeaseResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(StartLeaseResponseObject)
	return resp, err
}

func (s stubOperations) GetAttention(context.Context, GetAttentionRequestObject) (GetAttentionResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetAttentionResponseObject)
	return resp, err
}

func (s stubOperations) GetAttentionNow(context.Context, GetAttentionNowRequestObject) (GetAttentionNowResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetAttentionNowResponseObject)
	return resp, err
}

func (s stubOperations) GetNow(context.Context, GetNowRequestObject) (GetNowResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetNowResponseObject)
	return resp, err
}

func (s stubOperations) Finish(context.Context, FinishRequestObject) (FinishResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(FinishResponseObject)
	return resp, err
}

func (s stubOperations) Ping(context.Context, PingRequestObject) (PingResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PingResponseObject)
	return resp, err
}

func (s stubOperations) Catchup(context.Context, CatchupRequestObject) (CatchupResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(CatchupResponseObject)
	return resp, err
}

func (s stubOperations) Start(context.Context, StartRequestObject) (StartResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(StartResponseObject)
	return resp, err
}

func (s stubOperations) ListLeases(context.Context, ListLeasesRequestObject) (ListLeasesResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListLeasesResponseObject)
	return resp, err
}

func (s stubOperations) GetLease(context.Context, GetLeaseRequestObject) (GetLeaseResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetLeaseResponseObject)
	return resp, err
}

func (s stubOperations) RenewLease(context.Context, RenewLeaseRequestObject) (RenewLeaseResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RenewLeaseResponseObject)
	return resp, err
}

func (s stubOperations) EndLease(context.Context, EndLeaseRequestObject) (EndLeaseResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(EndLeaseResponseObject)
	return resp, err
}

func (s stubOperations) AckLease(context.Context, AckLeaseRequestObject) (AckLeaseResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(AckLeaseResponseObject)
	return resp, err
}

func (s stubOperations) RaiseAsk(context.Context, RaiseAskRequestObject) (RaiseAskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(RaiseAskResponseObject)
	return resp, err
}

func (s stubOperations) ListAsks(context.Context, ListAsksRequestObject) (ListAsksResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListAsksResponseObject)
	return resp, err
}

func (s stubOperations) GetAsk(context.Context, GetAskRequestObject) (GetAskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetAskResponseObject)
	return resp, err
}

func (s stubOperations) PostNote(context.Context, PostNoteRequestObject) (PostNoteResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PostNoteResponseObject)
	return resp, err
}

func (s stubOperations) ListNotes(context.Context, ListNotesRequestObject) (ListNotesResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(ListNotesResponseObject)
	return resp, err
}

func (s stubOperations) PutCursor(context.Context, PutCursorRequestObject) (PutCursorResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PutCursorResponseObject)
	return resp, err
}

func (s stubOperations) GetNote(context.Context, GetNoteRequestObject) (GetNoteResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetNoteResponseObject)
	return resp, err
}

func (s stubOperations) GetStream(context.Context, GetStreamRequestObject) (GetStreamResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetStreamResponseObject)
	return resp, err
}

func (s stubOperations) AnswerAsk(context.Context, AnswerAskRequestObject) (AnswerAskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(AnswerAskResponseObject)
	return resp, err
}

func (s stubOperations) WithdrawAsk(context.Context, WithdrawAskRequestObject) (WithdrawAskResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(WithdrawAskResponseObject)
	return resp, err
}

func (s stubOperations) GetMeta(context.Context, GetMetaRequestObject) (GetMetaResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetMetaResponseObject)
	return resp, err
}

func (s stubOperations) PutProtocolSeen(context.Context, PutProtocolSeenRequestObject) (PutProtocolSeenResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(PutProtocolSeenResponseObject)
	return resp, err
}

func (s stubOperations) GetDocs(context.Context, GetDocsRequestObject) (GetDocsResponseObject, error) {
	r, err := s.outcome()
	resp, _ := r.(GetDocsResponseObject)
	return resp, err
}

// TestAFailingOperationAnswersAProblemDocument sends every operation through
// the generated handler with an operation that errors, one whose response
// cannot be written, and a middleware that swaps in a foreign response: each answers 500 as a
// problem document, and the strict middleware sees every call.
func TestAFailingOperationAnswersAProblemDocument(t *testing.T) {
	t.Parallel()
	paths := []string{
		"/healthz", "/readyz", "/api/openapi.yaml", "/api/openapi.json", "/api/docs", "/api/docs/assets/init.js",
		"/auth/login", "/auth/oidc/callback", "/auth/session", "POST /auth/logout",
		"/api/v1/keys", "POST /api/v1/keys", "POST /api/v1/keys/workstation/revoke",
		"/api/v1/initiatives", "POST /api/v1/initiatives", "/api/v1/initiatives/phone-onboard", "POST /api/v1/initiatives/phone-onboard/close", "POST /api/v1/initiatives/phone-onboard/reopen",
		"POST /api/v1/tasks", "/api/v1/tasks", "/api/v1/tasks/aa-1", "PATCH /api/v1/tasks/aa-1",
		"POST /api/v1/tasks/aa-1/done", "POST /api/v1/tasks/aa-1/reopen", "POST /api/v1/tasks/aa-1/close",
		"POST /api/v1/leases", "/api/v1/leases", "/api/v1/leases/L1", "POST /api/v1/leases/L1/renew", "POST /api/v1/leases/L1/end", "POST /api/v1/leases/L1/ack", "/api/v1/now",
		"POST /api/v1/asks", "/api/v1/asks", "/api/v1/asks/A1", "POST /api/v1/asks/A1/answer", "POST /api/v1/asks/A1/withdraw",
		"POST /api/v1/start", "POST /api/v1/finish", "POST /api/v1/ping", "POST /api/v1/catchup",
		"/api/v1/meta", "PUT /api/v1/actors/me/protocol-seen", "/api/v1/stream",
	}
	for _, mode := range []string{"fails", "wrong type", "unwritable"} {
		var seen []string
		record := func(next StrictHandlerFunc, operationID string) StrictHandlerFunc {
			return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
				seen = append(seen, operationID)
				if mode == "wrong type" {
					return struct{}{}, nil
				}
				return next(ctx, w, r, request)
			}
		}
		mux := http.NewServeMux()
		HandlerFromMux(strictOperations(stubOperations{mode: mode}, record), mux)
		for _, path := range paths {
			rec := httptest.NewRecorder()
			method, target := http.MethodGet, path
			if m, p, ok := strings.Cut(path, " "); ok {
				method, target = m, p
			}
			req := httptest.NewRequest(method, target, strings.NewReader(`{"name":"x"}`))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("%s: GET %s = %d, want 500", mode, path, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("%s: GET %s Content-Type = %q, want application/problem+json", mode, path, ct)
			}
		}
		if len(seen) != len(paths) {
			t.Errorf("%s: the middleware saw %v, want one call per operation", mode, seen)
		}
	}
}

// TestAFailingOperationThroughTheHandlerAnswersAProblemWithNext sends the same
// failing operations through the wrapper NewHandler installs: whatever the
// operation does wrong, the 500 problem document names a next action.
func TestAFailingOperationThroughTheHandlerAnswersAProblemWithNext(t *testing.T) {
	t.Parallel()
	paths := []string{"/healthz", "/api/docs/assets/init.js", "/api/v1/keys", "POST /api/v1/initiatives", "/api/v1/meta"}
	for _, mode := range []string{"fails", "wrong type", "unwritable"} {
		mux := http.NewServeMux()
		HandlerFromMux(strictOperations(stubOperations{mode: mode}, func(next StrictHandlerFunc, _ string) StrictHandlerFunc { return next }), mux)
		h := withProblemNext(mux)
		for _, path := range paths {
			method, target := http.MethodGet, path
			if m, p, ok := strings.Cut(path, " "); ok {
				method, target = m, p
			}
			req := httptest.NewRequest(method, target, strings.NewReader(`{"name":"x"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("%s: %s = %d, want 500", mode, path, rec.Code)
				continue
			}
			doc := problem(t, rec)
			if next, _ := doc["next"].(string); strings.TrimSpace(next) == "" {
				t.Errorf("%s: %s: problem %v has no next", mode, path, doc)
			}
		}
	}
}

// TestDeclaredProblemResponsesWriteAProblemDocument writes every problem
// response the document declares and checks status, content type and body.
func TestDeclaredProblemResponsesWriteAProblemDocument(t *testing.T) {
	t.Parallel()
	problem := Problem{Type: "about:blank", Title: "t", Status: 1}
	cases := []struct {
		name  string
		want  int
		write func(http.ResponseWriter) error
	}{
		{"docs 400", 400, GetDocs400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetDocsResponse},
		{"docs 401", 401, GetDocs401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetDocsResponse},
		{"asset 400", 400, GetDocsAsset400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetDocsAssetResponse},
		{"asset 401", 401, GetDocsAsset401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetDocsAssetResponse},
		{"asset 404", 404, GetDocsAsset404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetDocsAssetResponse},
		{"json 400", 400, GetOpenapiJSON400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetOpenapiJSONResponse},
		{"json 401", 401, GetOpenapiJSON401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetOpenapiJSONResponse},
		{"yaml 400", 400, GetOpenapiYAML400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetOpenapiYAMLResponse},
		{"yaml 401", 401, GetOpenapiYAML401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetOpenapiYAMLResponse},
		{"readyz 400", 400, GetReadyz400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetReadyzResponse},
		{"readyz 401", 401, GetReadyz401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetReadyzResponse},
		{"login 401", 401, GetAuthLogin401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthLoginResponse},
		{"login 404", 404, GetAuthLogin404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthLoginResponse},
		{"login 502", 502, GetAuthLogin502ApplicationProblemPlusJSONResponse{BadGatewayApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthLoginResponse},
		{"login 503", 503, GetAuthLogin503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthLoginResponse},
		{"login default", 418, GetAuthLogindefaultApplicationProblemPlusJSONResponse{Body: problem, StatusCode: 418}.VisitGetAuthLoginResponse},
		{"callback 400", 400, GetAuthOidcCallback400ApplicationProblemPlusJSONResponse(problem).VisitGetAuthOidcCallbackResponse},
		{"callback 401", 401, GetAuthOidcCallback401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthOidcCallbackResponse},
		{"callback 403", 403, GetAuthOidcCallback403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthOidcCallbackResponse},
		{"callback 404", 404, GetAuthOidcCallback404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthOidcCallbackResponse},
		{"callback 502", 502, GetAuthOidcCallback502ApplicationProblemPlusJSONResponse{BadGatewayApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthOidcCallbackResponse},
		{"callback default", 418, GetAuthOidcCallbackdefaultApplicationProblemPlusJSONResponse{Body: problem, StatusCode: 418}.VisitGetAuthOidcCallbackResponse},
		{"session 401", 401, GetAuthSession401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthSessionResponse},
		{"session 404", 404, GetAuthSession404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetAuthSessionResponse},
		{"logout 401", 401, PostAuthLogout401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPostAuthLogoutResponse},
		{"keys 400", 400, ListKeys400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListKeysResponse},
		{"keys 401", 401, ListKeys401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListKeysResponse},
		{"keys 503", 503, ListKeys503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListKeysResponse},
		{"create key 400", 400, CreateKey400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitCreateKeyResponse},
		{"create key 401", 401, CreateKey401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCreateKeyResponse},
		{"create key 409", 409, CreateKey409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitCreateKeyResponse},
		{"create key 503", 503, CreateKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCreateKeyResponse},
		{"revoke key 400", 400, RevokeKey400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRevokeKeyResponse},
		{"revoke key 401", 401, RevokeKey401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRevokeKeyResponse},
		{"revoke key 404", 404, RevokeKey404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRevokeKeyResponse},
		{"revoke key 503", 503, RevokeKey503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRevokeKeyResponse},
		{"list initiatives 400", 400, ListInitiatives400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListInitiativesResponse},
		{"list initiatives 401", 401, ListInitiatives401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListInitiativesResponse},
		{"list initiatives 503", 503, ListInitiatives503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListInitiativesResponse},
		{"create initiative 400", 400, CreateInitiative400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitCreateInitiativeResponse},
		{"create initiative 401", 401, CreateInitiative401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCreateInitiativeResponse},
		{"create initiative 409", 409, CreateInitiative409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitCreateInitiativeResponse},
		{"create initiative 503", 503, CreateInitiative503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCreateInitiativeResponse},
		{"get initiative 400", 400, GetInitiative400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetInitiativeResponse},
		{"get initiative 401", 401, GetInitiative401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetInitiativeResponse},
		{"get initiative 404", 404, GetInitiative404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetInitiativeResponse},
		{"get initiative 503", 503, GetInitiative503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetInitiativeResponse},
		{"patch initiative 400", 400, PatchInitiative400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitPatchInitiativeResponse},
		{"patch initiative 401", 401, PatchInitiative401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPatchInitiativeResponse},
		{"patch initiative 404", 404, PatchInitiative404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitPatchInitiativeResponse},
		{"patch initiative 503", 503, PatchInitiative503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitPatchInitiativeResponse},
		{"close initiative 400", 400, CloseInitiative400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitCloseInitiativeResponse},
		{"close initiative 401", 401, CloseInitiative401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCloseInitiativeResponse},
		{"close initiative 404", 404, CloseInitiative404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitCloseInitiativeResponse},
		{"close initiative 409", 409, CloseInitiative409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitCloseInitiativeResponse},
		{"close initiative 503", 503, CloseInitiative503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCloseInitiativeResponse},
		{"reopen initiative 401", 401, ReopenInitiative401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitReopenInitiativeResponse},
		{"reopen initiative 404", 404, ReopenInitiative404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitReopenInitiativeResponse},
		{"reopen initiative 409", 409, ReopenInitiative409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitReopenInitiativeResponse},
		{"reopen initiative 503", 503, ReopenInitiative503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitReopenInitiativeResponse},
		{"create task 400", 400, CreateTask400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitCreateTaskResponse},
		{"create task 401", 401, CreateTask401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCreateTaskResponse},
		{"create task 404", 404, CreateTask404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitCreateTaskResponse},
		{"create task 409", 409, CreateTask409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitCreateTaskResponse},
		{"create task 503", 503, CreateTask503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCreateTaskResponse},
		{"get task 400", 400, GetTask400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetTaskResponse},
		{"get task 401", 401, GetTask401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetTaskResponse},
		{"get task 404", 404, GetTask404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetTaskResponse},
		{"get task 503", 503, GetTask503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetTaskResponse},
		{"patch task 400", 400, PatchTask400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitPatchTaskResponse},
		{"patch task 401", 401, PatchTask401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPatchTaskResponse},
		{"patch task 404", 404, PatchTask404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitPatchTaskResponse},
		{"patch task 409", 409, PatchTask409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitPatchTaskResponse},
		{"patch task 503", 503, PatchTask503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitPatchTaskResponse},
		{"done task 400", 400, DoneTask400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitDoneTaskResponse},
		{"done task 401", 401, DoneTask401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitDoneTaskResponse},
		{"done task 404", 404, DoneTask404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitDoneTaskResponse},
		{"done task 409", 409, DoneTask409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitDoneTaskResponse},
		{"done task 503", 503, DoneTask503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitDoneTaskResponse},
		{"reopen task 400", 400, ReopenTask400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitReopenTaskResponse},
		{"reopen task 401", 401, ReopenTask401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitReopenTaskResponse},
		{"reopen task 404", 404, ReopenTask404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitReopenTaskResponse},
		{"reopen task 409", 409, ReopenTask409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitReopenTaskResponse},
		{"reopen task 503", 503, ReopenTask503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitReopenTaskResponse},
		{"close task 400", 400, CloseTask400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitCloseTaskResponse},
		{"close task 401", 401, CloseTask401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCloseTaskResponse},
		{"close task 404", 404, CloseTask404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitCloseTaskResponse},
		{"close task 409", 409, CloseTask409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitCloseTaskResponse},
		{"close task 503", 503, CloseTask503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCloseTaskResponse},
		{"add task dep 400", 400, AddTaskDep400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskDepResponse},
		{"add task dep 401", 401, AddTaskDep401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskDepResponse},
		{"add task dep 404", 404, AddTaskDep404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskDepResponse},
		{"add task dep 409", 409, AddTaskDep409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskDepResponse},
		{"add task dep 503", 503, AddTaskDep503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskDepResponse},
		{"remove task dep 400", 400, RemoveTaskDep400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskDepResponse},
		{"remove task dep 401", 401, RemoveTaskDep401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskDepResponse},
		{"remove task dep 404", 404, RemoveTaskDep404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskDepResponse},
		{"remove task dep 503", 503, RemoveTaskDep503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskDepResponse},
		{"add task tag 400", 400, AddTaskTag400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskTagResponse},
		{"add task tag 401", 401, AddTaskTag401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskTagResponse},
		{"add task tag 404", 404, AddTaskTag404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskTagResponse},
		{"add task tag 409", 409, AddTaskTag409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskTagResponse},
		{"add task tag 503", 503, AddTaskTag503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitAddTaskTagResponse},
		{"remove task tag 400", 400, RemoveTaskTag400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskTagResponse},
		{"remove task tag 401", 401, RemoveTaskTag401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskTagResponse},
		{"remove task tag 404", 404, RemoveTaskTag404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskTagResponse},
		{"remove task tag 409", 409, RemoveTaskTag409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskTagResponse},
		{"remove task tag 503", 503, RemoveTaskTag503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRemoveTaskTagResponse},
		{"record task check 400", 400, RecordTaskCheck400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskCheckResponse},
		{"record task check 401", 401, RecordTaskCheck401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskCheckResponse},
		{"record task check 404", 404, RecordTaskCheck404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskCheckResponse},
		{"record task check 409", 409, RecordTaskCheck409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskCheckResponse},
		{"record task check 503", 503, RecordTaskCheck503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskCheckResponse},
		{"record task milestone 400", 400, RecordTaskMilestone400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskMilestoneResponse},
		{"record task milestone 401", 401, RecordTaskMilestone401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskMilestoneResponse},
		{"record task milestone 404", 404, RecordTaskMilestone404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskMilestoneResponse},
		{"record task milestone 503", 503, RecordTaskMilestone503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRecordTaskMilestoneResponse},
		{"list tasks 400", 400, ListTasks400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListTasksResponse},
		{"list tasks 401", 401, ListTasks401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListTasksResponse},
		{"list tasks 404", 404, ListTasks404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitListTasksResponse},
		{"list tasks 503", 503, ListTasks503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListTasksResponse},
		{"count tasks 400", 400, CountTasks400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitCountTasksResponse},
		{"count tasks 401", 401, CountTasks401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCountTasksResponse},
		{"count tasks 404", 404, CountTasks404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitCountTasksResponse},
		{"count tasks 503", 503, CountTasks503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCountTasksResponse},
		{"list task events 400", 400, ListTaskEvents400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListTaskEventsResponse},
		{"list task events 401", 401, ListTaskEvents401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListTaskEventsResponse},
		{"list task events 404", 404, ListTaskEvents404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitListTaskEventsResponse},
		{"list task events 503", 503, ListTaskEvents503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListTaskEventsResponse},
		{"get stream 400", 400, GetStream400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetStreamResponse},
		{"get stream 401", 401, GetStream401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetStreamResponse},
		{"get stream 503", 503, GetStream503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetStreamResponse},
		{"list events 400", 400, ListEvents400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListEventsResponse},
		{"list events 401", 401, ListEvents401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListEventsResponse},
		{"list events 404", 404, ListEvents404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitListEventsResponse},
		{"list events 503", 503, ListEvents503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListEventsResponse},
		{"list notifications 400", 400, ListNotifications400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListNotificationsResponse},
		{"list notifications 401", 401, ListNotifications401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListNotificationsResponse},
		{"list notifications 503", 503, ListNotifications503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListNotificationsResponse},
		{"get timeline 400", 400, GetTimeline400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetTimelineResponse},
		{"get timeline 401", 401, GetTimeline401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetTimelineResponse},
		{"get timeline 404", 404, GetTimeline404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetTimelineResponse},
		{"get timeline 503", 503, GetTimeline503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetTimelineResponse},
		{"list actors 400", 400, ListActors400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListActorsResponse},
		{"list actors 401", 401, ListActors401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListActorsResponse},
		{"list actors 503", 503, ListActors503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListActorsResponse},
		{"get actor 401", 401, GetActor401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetActorResponse},
		{"get actor 404", 404, GetActor404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetActorResponse},
		{"get actor 503", 503, GetActor503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetActorResponse},
		{"start lease 400", 400, StartLease400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitStartLeaseResponse},
		{"start lease 401", 401, StartLease401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitStartLeaseResponse},
		{"start lease 404", 404, StartLease404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitStartLeaseResponse},
		{"start lease 409", 409, StartLease409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitStartLeaseResponse},
		{"start lease 503", 503, StartLease503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitStartLeaseResponse},
		{"list leases 400", 400, ListLeases400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListLeasesResponse},
		{"list leases 401", 401, ListLeases401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListLeasesResponse},
		{"list leases 404", 404, ListLeases404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitListLeasesResponse},
		{"raise ask 400", 400, RaiseAsk400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRaiseAskResponse},
		{"raise ask 401", 401, RaiseAsk401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRaiseAskResponse},
		{"raise ask 404", 404, RaiseAsk404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRaiseAskResponse},
		{"raise ask 409", 409, RaiseAsk409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitRaiseAskResponse},
		{"raise ask 503", 503, RaiseAsk503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRaiseAskResponse},
		{"list asks 400", 400, ListAsks400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListAsksResponse},
		{"list asks 401", 401, ListAsks401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListAsksResponse},
		{"list asks 404", 404, ListAsks404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitListAsksResponse},
		{"list asks 503", 503, ListAsks503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListAsksResponse},
		{"get ask 400", 400, GetAsk400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetAskResponse},
		{"get ask 401", 401, GetAsk401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetAskResponse},
		{"get ask 404", 404, GetAsk404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetAskResponse},
		{"get ask 503", 503, GetAsk503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetAskResponse},
		{"post note 400", 400, PostNote400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitPostNoteResponse},
		{"post note 401", 401, PostNote401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPostNoteResponse},
		{"post note 404", 404, PostNote404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitPostNoteResponse},
		{"post note 503", 503, PostNote503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitPostNoteResponse},
		{"list notes 400", 400, ListNotes400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitListNotesResponse},
		{"list notes 401", 401, ListNotes401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitListNotesResponse},
		{"list notes 404", 404, ListNotes404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitListNotesResponse},
		{"list notes 503", 503, ListNotes503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListNotesResponse},
		{"put cursor 400", 400, PutCursor400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitPutCursorResponse},
		{"put cursor 401", 401, PutCursor401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPutCursorResponse},
		{"put cursor 404", 404, PutCursor404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitPutCursorResponse},
		{"put cursor 503", 503, PutCursor503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitPutCursorResponse},
		{"get note 401", 401, GetNote401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetNoteResponse},
		{"get note 404", 404, GetNote404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetNoteResponse},
		{"get note 503", 503, GetNote503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetNoteResponse},
		{"answer ask 400", 400, AnswerAsk400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitAnswerAskResponse},
		{"answer ask 401", 401, AnswerAsk401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitAnswerAskResponse},
		{"answer ask 404", 404, AnswerAsk404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitAnswerAskResponse},
		{"answer ask 409", 409, AnswerAsk409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitAnswerAskResponse},
		{"answer ask 503", 503, AnswerAsk503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitAnswerAskResponse},
		{"withdraw ask 400", 400, WithdrawAsk400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitWithdrawAskResponse},
		{"withdraw ask 401", 401, WithdrawAsk401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitWithdrawAskResponse},
		{"withdraw ask 404", 404, WithdrawAsk404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitWithdrawAskResponse},
		{"withdraw ask 409", 409, WithdrawAsk409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitWithdrawAskResponse},
		{"withdraw ask 503", 503, WithdrawAsk503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitWithdrawAskResponse},
		{"get attention 400", 400, GetAttention400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionResponse},
		{"get attention 401", 401, GetAttention401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionResponse},
		{"get attention 404", 404, GetAttention404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionResponse},
		{"get attention 503", 503, GetAttention503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionResponse},
		{"get attention now 400", 400, GetAttentionNow400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionNowResponse},
		{"get attention now 401", 401, GetAttentionNow401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionNowResponse},
		{"get attention now 404", 404, GetAttentionNow404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionNowResponse},
		{"get attention now 503", 503, GetAttentionNow503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetAttentionNowResponse},
		{"get now 400", 400, GetNow400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetNowResponse},
		{"get now 401", 401, GetNow401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetNowResponse},
		{"get now 503", 503, GetNow503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetNowResponse},
		{"start 400", 400, Start400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitStartResponse},
		{"start 401", 401, Start401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitStartResponse},
		{"start 404", 404, Start404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitStartResponse},
		{"start 409", 409, Start409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitStartResponse},
		{"start 503", 503, Start503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitStartResponse},
		{"finish 400", 400, Finish400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitFinishResponse},
		{"finish 401", 401, Finish401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitFinishResponse},
		{"finish 404", 404, Finish404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitFinishResponse},
		{"finish 409", 409, Finish409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitFinishResponse},
		{"finish 503", 503, Finish503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitFinishResponse},
		{"ping 400", 400, Ping400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitPingResponse},
		{"ping 401", 401, Ping401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPingResponse},
		{"ping 409", 409, Ping409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitPingResponse},
		{"ping 503", 503, Ping503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitPingResponse},
		{"catchup 401", 401, Catchup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitCatchupResponse},
		{"catchup 503", 503, Catchup503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitCatchupResponse},
		{"list leases 503", 503, ListLeases503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitListLeasesResponse},
		{"get lease 400", 400, GetLease400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitGetLeaseResponse},
		{"get lease 401", 401, GetLease401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetLeaseResponse},
		{"get lease 404", 404, GetLease404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitGetLeaseResponse},
		{"get lease 503", 503, GetLease503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitGetLeaseResponse},
		{"renew lease 400", 400, RenewLease400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitRenewLeaseResponse},
		{"renew lease 401", 401, RenewLease401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitRenewLeaseResponse},
		{"renew lease 404", 404, RenewLease404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitRenewLeaseResponse},
		{"renew lease 409", 409, RenewLease409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitRenewLeaseResponse},
		{"renew lease 410", 410, RenewLease410ApplicationProblemPlusJSONResponse{GoneApplicationProblemPlusJSONResponse(problem)}.VisitRenewLeaseResponse},
		{"renew lease 503", 503, RenewLease503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitRenewLeaseResponse},
		{"end lease 400", 400, EndLease400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitEndLeaseResponse},
		{"end lease 401", 401, EndLease401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitEndLeaseResponse},
		{"end lease 404", 404, EndLease404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitEndLeaseResponse},
		{"end lease 409", 409, EndLease409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitEndLeaseResponse},
		{"end lease 503", 503, EndLease503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitEndLeaseResponse},
		{"ack lease 400", 400, AckLease400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitAckLeaseResponse},
		{"ack lease 401", 401, AckLease401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitAckLeaseResponse},
		{"ack lease 404", 404, AckLease404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitAckLeaseResponse},
		{"ack lease 409", 409, AckLease409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse(problem)}.VisitAckLeaseResponse},
		{"ack lease 503", 503, AckLease503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitAckLeaseResponse},
		{"meta 401", 401, GetMeta401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitGetMetaResponse},
		{"protocol seen 400", 400, PutProtocolSeen400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse(problem)}.VisitPutProtocolSeenResponse},
		{"protocol seen 401", 401, PutProtocolSeen401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse(problem)}.VisitPutProtocolSeenResponse},
		{"protocol seen 503", 503, PutProtocolSeen503ApplicationProblemPlusJSONResponse{UnavailableApplicationProblemPlusJSONResponse(problem)}.VisitPutProtocolSeenResponse},
		{"logout 404", 404, PostAuthLogout404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse(problem)}.VisitPostAuthLogoutResponse},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		if err := tc.write(rec); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got != problem {
			t.Errorf("%s: body %q does not decode to %+v (%v)", tc.name, rec.Body.String(), problem, err)
		}
		if rec.Code != tc.want || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: %d %q, want %d application/problem+json", tc.name, rec.Code, rec.Header().Get("Content-Type"), tc.want)
		}
		// The same typed response served through the handler's wrapper gains the
		// next its value lacked and keeps every other field.
		wrapped := httptest.NewRecorder()
		withProblemNext(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = tc.write(w) })).ServeHTTP(wrapped, httptest.NewRequest(http.MethodGet, "/", nil))
		var doc map[string]any
		if err := json.Unmarshal(wrapped.Body.Bytes(), &doc); err != nil {
			t.Errorf("%s: wrapped body %q: %v", tc.name, wrapped.Body.String(), err)
			continue
		}
		if next, _ := doc["next"].(string); strings.TrimSpace(next) == "" {
			t.Errorf("%s: wrapped problem %v has no next", tc.name, doc)
		}
		if doc["title"] != problem.Title || doc["type"] != problem.Type || wrapped.Code != tc.want || wrapped.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: wrapped response %d %q %v lost fields", tc.name, wrapped.Code, wrapped.Header().Get("Content-Type"), doc)
		}
	}
}

// TestReadyzNotReadyIsTextAndTheHandlerHonoursABaseURL covers the 503 body and
// the generated routers.
func TestReadyzNotReadyIsTextAndTheHandlerHonoursABaseURL(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if err := GetReadyz503TextResponse("not ready\n").VisitGetReadyzResponse(rec); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 || rec.Body.String() != "not ready\n" {
		t.Errorf("503 response = %d %q", rec.Code, rec.Body.String())
	}
	ssi := operations{ready: ok}
	for name, h := range map[string]http.Handler{
		"Handler":                   Handler(NewStrictHandler(ssi, nil)),
		"HandlerFromMuxWithBaseURL": HandlerFromMuxWithBaseURL(NewStrictHandler(ssi, nil), http.NewServeMux(), "/v9"),
	} {
		path := "/healthz"
		if name != "Handler" {
			path = "/v9/healthz"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
			t.Errorf("%s: GET %s = %d %q, want 200 ok", name, path, rec.Code, rec.Body.String())
		}
	}
}

// TestParameterErrorsNameTheParameter pins the messages the generated router
// reports for a bad parameter, and that a wrapped cause stays reachable.
func TestParameterErrorsNameTheParameter(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	cases := []struct {
		err    error
		want   string
		unwrap bool
	}{
		{&UnescapedCookieParamError{ParamName: "p", Err: cause}, "cookie parameter 'p'", true},
		{&UnmarshalingParamError{ParamName: "p", Err: cause}, "parameter p as JSON: boom", true},
		{&RequiredParamError{ParamName: "p"}, "Query argument p is required", false},
		{&RequiredHeaderError{ParamName: "p", Err: cause}, "Header parameter p is required", true},
		{&InvalidParamFormatError{ParamName: "p", Err: cause}, "Invalid format for parameter p: boom", true},
		{&TooManyValuesForParamError{ParamName: "p", Count: 2}, "Expected one value for p, got 2", false},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); !strings.Contains(got, tc.want) {
			t.Errorf("%T: %q does not contain %q", tc.err, got, tc.want)
		}
		if tc.unwrap && !errors.Is(tc.err, cause) {
			t.Errorf("%T does not wrap its cause", tc.err)
		}
	}
}

func TestAnUnreadableRequestAnswersAProblemDocument(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	requestUnreadable(rec, httptest.NewRequest(http.MethodGet, "/", nil), errBroken)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("unreadable request = %d %q, want 400 application/problem+json", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// TestPingRefusesABodyThatIsNotJSONAndAcceptsNone: the ping body is optional,
// so an empty one reaches the operation, but one that is not JSON is a 400
// problem and never reaches it.
func TestPingRefusesABodyThatIsNotJSONAndAcceptsNone(t *testing.T) {
	t.Parallel()
	called := 0
	count := func(next StrictHandlerFunc, _ string) StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			called++
			return next(ctx, w, r, request)
		}
	}
	mux := http.NewServeMux()
	HandlerFromMux(strictOperations(stubOperations{mode: "fails"}, count), mux)
	for _, tc := range []struct {
		name, body string
		status     int
		reached    int
	}{
		{"not JSON", `{"progress":`, http.StatusBadRequest, 0},
		{"no body", ``, http.StatusInternalServerError, 1},
	} {
		called = 0
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ping", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.status || called != tc.reached {
			t.Errorf("%s: status %d, operation called %d times; want %d and %d", tc.name, rec.Code, called, tc.status, tc.reached)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type = %q, want application/problem+json", tc.name, ct)
		}
	}
}
