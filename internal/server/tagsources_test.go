package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/leases"
)

func TestGetTagSourcesAnswers503WithoutAStore(t *testing.T) {
	t.Parallel()
	got, _ := taskOps{}.GetTagSources(context.Background(), GetTagSourcesRequestObject{})
	if p, ok := got.(problemResponse); !ok || p.status != http.StatusServiceUnavailable {
		t.Errorf("tag sources without a store = %#v, want a 503 problem", got)
	}
}

// sourcesOf reads the tag-sources endpoint and fails unless it answers 200 with a {tag: [sources]} object.
func (s *keyServer) sourcesOf(query string) map[string][]string {
	s.t.Helper()
	rec := s.send(http.MethodGet, "/api/v1/tasks/tag-sources"+query, "", "claude@host")
	var got map[string][]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got == nil {
		s.t.Fatalf("tag-sources%s = %d %s", query, rec.Code, rec.Body)
	}
	return got
}

func TestGetTagSourcesAnswersTheSourcesOfEveryTagInOneRead(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	if got := s.sourcesOf(""); len(got) != 0 {
		t.Fatalf("no tasks: %v, want {}", got)
	}
	s.create("aa", `,"tags":["shared","ini-only"]`)
	if got := s.sourcesOf(""); len(got) != 0 {
		t.Fatalf("an initiative with no task: %v, want {}", got)
	}
	s.task(`{"initiative":"aa","title":"one","tags":["Shared","own-only"]}`)
	s.task(`{"initiative":"aa","title":"two"}`)
	s.create("bb", `,"tags":["closed-only"]`)
	s.task(`{"initiative":"bb","title":"three","tags":["gone"]}`)
	ls := &leases.Store{Store: s.st}
	if _, err := ls.Start(context.Background(), audit.Actor{Handle: "ralph:loop"}, "key:caller", leases.Start{Task: "aa-2", Intent: "wire it"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.Start(context.Background(), audit.Actor{Handle: "codex@host"}, "key:caller", leases.Start{Task: "aa-1", Intent: "no kind"}); err != nil {
		t.Fatal(err)
	}
	s.send(http.MethodPost, "/api/v1/tasks/bb-1/done", "", "claude@host")
	s.send(http.MethodPost, "/api/v1/tasks/bb-1/close", `{"resolution":"dropped"}`, "claude@host")

	open := map[string][]string{
		"shared":   {"task", "initiative"},
		"ini-only": {"initiative"},
		"own-only": {"task"},
		"ralph":    {"actor"},
	}
	if got := s.sourcesOf(""); !reflect.DeepEqual(got, open) {
		t.Errorf("default (not_closed) = %v, want %v: closed bb-1 adds nothing", got, open)
	}
	if got := s.sourcesOf("?status=not_closed"); !reflect.DeepEqual(got, open) {
		t.Errorf("not_closed = %v, want %v", got, open)
	}
	all := map[string][]string{
		"shared": {"task", "initiative"}, "ini-only": {"initiative"}, "own-only": {"task"}, "ralph": {"actor"},
		"gone": {"task"}, "closed-only": {"initiative"},
	}
	if got := s.sourcesOf("?status=all"); !reflect.DeepEqual(got, all) {
		t.Errorf("status=all = %v, want %v", got, all)
	}
	if got, want := s.sourcesOf("?status=closed"), map[string][]string{"gone": {"task"}, "closed-only": {"initiative"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("status=closed = %v, want %v", got, want)
	}
	if got, want := s.sourcesOf("?status=all&tag=Shared&tag=gone&tag=nothing&tag=ralph"),
		map[string][]string{"shared": {"task", "initiative"}, "gone": {"task"}, "ralph": {"actor"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("tags asked for = %v, want %v: a tag nothing carries has no entry", got, want)
	}
}

func TestGetTagSourcesRefusesABadStatus(t *testing.T) {
	t.Parallel()
	s := newKeyServer(t)
	if rec := s.send(http.MethodGet, "/api/v1/tasks/tag-sources?status=bogus", "", "claude@host"); rec.Code != http.StatusBadRequest {
		t.Errorf("status=bogus = %d %s, want 400", rec.Code, rec.Body)
	}
}
