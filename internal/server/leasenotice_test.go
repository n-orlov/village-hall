package server

import (
	"net/http"
	"strings"
	"testing"
)

// noticeOf is the `notice` of the lease document in an answer, failing when
// the document does not carry the field at all.
func noticeOf(t *testing.T, doc map[string]any) bool {
	t.Helper()
	lease, _ := doc["lease"].(map[string]any)
	v, present := lease["notice"]
	if !present {
		t.Fatalf("the lease document has no notice field: %v", lease)
	}
	b, _ := v.(bool)
	return b
}

// TestStartingANoticeThroughTheAPI: POST /leases and POST /start take an
// optional notice (false when left out), the Lease document carries it, the
// same kind renews (200) and the other kind is a 409 naming the lease and
// saying to end it first, with nothing written.
func TestStartingANoticeThroughTheAPI(t *testing.T) {
	t.Parallel()
	for _, route := range []struct {
		name, path string
	}{{"leases", "/api/v1/leases"}, {"start", "/api/v1/start"}} {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			s := newKeyServerOfBuild(t, keyServerBuild)
			s.create("aa", "")
			s.task(`{"initiative":"aa","title":"one"}`)
			s.task(`{"initiative":"aa","title":"two"}`)
			post := func(body string) (int, map[string]any, string) {
				rec := s.send(http.MethodPost, route.path, body, "claude@host")
				if rec.Code >= 300 {
					return rec.Code, decode(t, rec), rec.Body.String()
				}
				return rec.Code, decode(t, rec), ""
			}

			// no notice field: an ordinary lease, notice false in the document
			code, doc, _ := post(`{"task":"aa-1","intent":"plain"}`)
			if code != http.StatusCreated || noticeOf(t, doc) {
				t.Fatalf("a start without notice = %d, notice %v", code, doc["lease"])
			}
			// notice true on another task: 201 with notice true
			code, doc, _ = post(`{"task":"aa-2","intent":"heads up","notice":true}`)
			if code != http.StatusCreated || !noticeOf(t, doc) || doc["lease"].(map[string]any)["ref"] != "L2" {
				t.Fatalf("a notice start = %d, %v", code, doc["lease"])
			}
			// the same kind renews: 200, same lease, notice kept
			code, doc, _ = post(`{"task":"aa-2","intent":"still","notice":true}`)
			if code != http.StatusOK || !noticeOf(t, doc) || doc["lease"].(map[string]any)["ref"] != "L2" {
				t.Fatalf("a notice renewed = %d, %v", code, doc["lease"])
			}
			code, doc, _ = post(`{"task":"aa-1","intent":"again","notice":false}`)
			if code != http.StatusOK || noticeOf(t, doc) || doc["lease"].(map[string]any)["ref"] != "L1" {
				t.Fatalf("an ordinary lease renewed = %d, %v", code, doc["lease"])
			}

			// the other kind: 409 naming the lease and `end L<n> first`
			for body, held := range map[string]string{
				`{"task":"aa-1","intent":"x","notice":true}`: "L1",
				`{"task":"aa-2","intent":"x"}`:               "L2",
			} {
				code, doc, raw := post(body)
				detail, _ := doc["detail"].(string)
				next, _ := doc["next"].(string)
				if code != http.StatusConflict || !strings.Contains(detail, held) || !strings.Contains(next, "end "+held+" first") {
					t.Errorf("%s = %d %s, want 409 naming %s and `end %s first`", body, code, raw, held, held)
				}
			}
			// nothing was written by the refusals: still the two leases
			rec := s.send(http.MethodGet, "/api/v1/leases?state=all", "", "claude@host")
			items := decode(t, rec)["items"].([]any)
			if len(items) != 2 {
				t.Errorf("leases after the refusals = %d, want 2: %s", len(items), rec.Body)
			}
			for _, it := range items {
				l := it.(map[string]any)
				if want := l["ref"] == "L2"; l["notice"] != want {
					t.Errorf("listed lease %v notice = %v, want %v", l["ref"], l["notice"], want)
				}
			}
		})
	}
}
