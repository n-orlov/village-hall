package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/leases"
)

func TestCountTasksAnswers503WithoutAStore(t *testing.T) {
	t.Parallel()
	got, _ := taskOps{}.CountTasks(context.Background(), CountTasksRequestObject{})
	if p, ok := got.(problemResponse); !ok || p.status != http.StatusServiceUnavailable {
		t.Errorf("count without a store = %#v, want a 503 problem", got)
	}
}

// counted reads the count form and fails unless it answers 200 with a body of the count alone.
func (s *keyServer) counted(query string) int {
	s.t.Helper()
	rec := s.send(http.MethodGet, "/api/v1/tasks/count"+query, "", "claude@host")
	doc := decode(s.t, rec)
	n, ok := doc["count"].(float64)
	if rec.Code != http.StatusOK || !ok || len(doc) != 1 {
		s.t.Fatalf("count%s = %d %s", query, rec.Code, rec.Body)
	}
	return int(n)
}

func TestCountTasksCountsWhatTheListKeepsAndNoRows(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	if n := s.counted(""); n != 0 {
		t.Fatalf("no tasks: count = %d, want 0", n)
	}
	s.create("aa", "")
	s.create("bb", "")
	if n := s.counted("?initiative=aa"); n != 0 {
		t.Errorf("an initiative with no tasks: count = %d, want 0", n)
	}
	s.task(`{"initiative":"aa","title":"one"}`)
	s.task(`{"initiative":"aa","title":"two"}`)
	s.task(`{"initiative":"aa","title":"three"}`)
	s.task(`{"initiative":"bb","title":"four","kind":"watch","cadence_seconds":60}`)
	ls := &leases.Store{Store: s.st}
	if _, err := ls.Start(context.Background(), audit.Actor{Handle: "codex@host"}, "key:caller", leases.Start{Task: "aa-2", Intent: "wire it"}); err != nil {
		t.Fatal(err)
	}
	s.send(http.MethodPost, "/api/v1/tasks/aa-3/done", "", "claude@host")
	s.send(http.MethodPost, "/api/v1/tasks/aa-3/close", `{"resolution":"dropped"}`, "claude@host")
	for query, want := range map[string]int{
		"":                             3, // not_closed is the default: aa-3 is closed and left out
		"?status=not_closed":           3,
		"?status=all":                  4,
		"?status=closed":               1,
		"?status=open":                 3,
		"?status=done":                 0,
		"?status=open&active=true":     1,
		"?status=open&active=false":    2,
		"?initiative=aa":               2,
		"?initiative=aa&initiative=bb": 3,
		"?initiative=aa&status=all":    3,
		"?kind=watch":                  1,
		"?ref=aa-1&ref=aa-3":           1,
		"?ref=zz-1":                    0,
		"?status=all&closed_within=7d": 4,
		"?status=open&attention=true":  0,
		"?q=one&limit=1&after=aa-1":    3, // the list's text, page size and cursor are not read
		"?status=open&blocked=false&kind=work&overdue=false": 2,
	} {
		if got := s.counted(query); got != want {
			t.Errorf("count%s = %d, want %d", query, got, want)
		}
		if strings.Contains(query, "limit") {
			continue
		}
		list := decode(t, s.send(http.MethodGet, "/api/v1/tasks"+query, "", "claude@host"))
		if items, _ := list["items"].([]any); len(items) != want {
			t.Errorf("list%s keeps %d tasks, the count says %d", query, len(items), want)
		}
	}
}

func TestCountTasksRefusesWhatTheListRefuses(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	s.create("aa", "")
	for query, want := range map[string]int{
		"?status=bogus":     http.StatusBadRequest,
		"?closed_within=7d": http.StatusBadRequest,
		"?initiative=zz":    http.StatusNotFound,
		"?tag=" + "%20":     http.StatusBadRequest,
	} {
		if rec := s.send(http.MethodGet, "/api/v1/tasks/count"+query, "", "claude@host"); rec.Code != want {
			t.Errorf("count%s = %d %s, want %d", query, rec.Code, rec.Body, want)
		}
	}
}
