package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
)

// noticeServer holds the board of the R9 listing criteria: notice N1 of
// codex@host (older), notice N2 of claude@host (the caller, newer), an ended
// notice, an expired notice and the ordinary lease L of codex@host.
func noticeServer(t *testing.T) *keyServer {
	t.Helper()
	s := newKeyServerOfBuild(t, keyServerBuild)
	s.create("aa", "")
	for _, title := range []string{"one", "two", "three", "four", "five"} {
		s.task(`{"initiative":"aa","title":"` + title + `"}`)
	}
	start := func(actor, task string, notice bool, res string) {
		t.Helper()
		body := `{"task":"` + task + `","intent":"work on ` + task + `","resources":["` + res + `"],"notice":` + map[bool]string{true: "true", false: "false"}[notice] + `}`
		if rec := s.send(http.MethodPost, "/api/v1/leases", body, actor); rec.Code != http.StatusCreated {
			t.Fatalf("start %s = %d %s", body, rec.Code, rec.Body)
		}
	}
	start("codex@host", "aa-1", true, "svc:one")   // L1: N1
	start("codex@host", "aa-2", true, "svc:two")   // L2: ended below
	start("codex@host", "aa-3", true, "svc:gone")  // L3: expired below
	start("codex@host", "aa-4", false, "svc:four") // L4: the ordinary lease L
	start("claude@host", "aa-5", true, "svc:five") // L5: N2
	if rec := s.send(http.MethodPost, "/api/v1/leases/L2/end", `{"outcome":"over"}`, "codex@host"); rec.Code != http.StatusOK {
		t.Fatalf("end L2 = %d %s", rec.Code, rec.Body)
	}
	past := store.EncodeBound(time.Now().Add(-time.Hour))
	if _, err := s.st.Writer().ExecContext(context.Background(), `UPDATE leases SET expires_at = ? WHERE n = 3`, past); err != nil {
		t.Fatal(err)
	}
	return s
}

func refsAt(t *testing.T, doc map[string]any, key string) string {
	t.Helper()
	items, ok := doc[key].([]any)
	if !ok {
		t.Fatalf("%s is not a list in %v", key, doc)
	}
	refs := make([]string, len(items))
	for i, it := range items {
		refs[i], _ = it.(map[string]any)["ref"].(string)
	}
	return strings.Join(refs, ",")
}

// TestNowListsTheLiveNoticesOfEveryoneOldestFirst: GET /now and the `now` of
// POST /start carry `notices` = [N1, N2] with the task title, without the ended
// and the expired notice, whatever resources are queried, and leave notices out
// of my_leases and others_leases (SPEC 6.5).
func TestNowListsTheLiveNoticesOfEveryoneOldestFirst(t *testing.T) {
	t.Parallel()
	s := noticeServer(t)
	check := func(label string, now map[string]any) {
		t.Helper()
		if got := refsAt(t, now, "notices"); got != "L1,L5" {
			t.Errorf("%s: notices = %s, want L1,L5", label, got)
		}
		notices := now["notices"].([]any)
		if first := notices[0].(map[string]any); first["title"] != "one" || first["notice"] != true || first["actor"] != "codex@host" {
			t.Errorf("%s: first notice = %v, want the codex notice with the title of aa-1", label, first)
		}
		if got := refsAt(t, now, "my_leases"); got != "" {
			t.Errorf("%s: my_leases = %q, want none", label, got)
		}
		if got := refsAt(t, now, "others_leases"); got != "L4" {
			t.Errorf("%s: others_leases = %q, want only L4", label, got)
		}
	}
	for _, q := range []string{"", "?resources=repo:none", "?resources=svc:five,svc:gone"} {
		rec := s.send(http.MethodGet, "/api/v1/now"+q, "", "claude@host")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /now%s = %d %s", q, rec.Code, rec.Body)
		}
		check("GET /now"+q, decode(t, rec))
	}
	rec := s.send(http.MethodPost, "/api/v1/start", `{"task":"aa-5","intent":"more","notice":true}`, "claude@host")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /start = %d %s", rec.Code, rec.Body)
	}
	check("POST /start", decode(t, rec)["now"].(map[string]any))
}

// TestListingLeasesByNotice: ?notice=true is exactly N1 and N2, ?notice=false
// exactly L, and no filter lists the three live leases.
func TestListingLeasesByNotice(t *testing.T) {
	t.Parallel()
	s := noticeServer(t)
	for q, want := range map[string]string{"?notice=true": "L1,L5", "?notice=false": "L4", "": "L1,L4,L5"} {
		rec := s.send(http.MethodGet, "/api/v1/leases"+q, "", "claude@host")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /leases%s = %d %s", q, rec.Code, rec.Body)
		}
		if got := refsAt(t, decode(t, rec), "items"); got != want {
			t.Errorf("GET /leases%s = %s, want %s", q, got, want)
		}
	}
}
