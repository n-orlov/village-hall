package tasks

import (
	"errors"
	"reflect"
	"testing"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
)

func TestTagSourcesIsEmptyWithNoTasksAndRefusesAStatusTheListRefuses(t *testing.T) {
	s := newStore(t)
	got, err := s.TagSources(ctx, "", nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no tasks: %v, %v, want an empty non-nil map", got, err)
	}
	if _, err := (initiatives.Store{Store: s.Store, Now: s.Now}).Create(ctx, who, "key:c", initiatives.New{Slug: "aa", Title: "I", Tags: []string{"home"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TagSources(ctx, "all", nil); err != nil || len(got) != 0 {
		t.Fatalf("an initiative with no task: %v, %v, want none: its tags have no task to be a source on", got, err)
	}
	var bad InvalidError
	if _, err := s.TagSources(ctx, "bogus", nil); !errors.As(err, &bad) {
		t.Fatalf("status bogus: %v, want an InvalidError", err)
	}
}

func TestTagSourcesNamesEachSourceOnceInOrderAndLeavesClosedTasksOutWhereTheListDoes(t *testing.T) {
	s := newStore(t)
	for _, in := range []initiatives.New{{Slug: "aa", Title: "I", Tags: []string{"home", "ralph"}}, {Slug: "bb", Title: "J", Tags: []string{"shut"}}} {
		if _, err := (initiatives.Store{Store: s.Store, Now: s.Now}).Create(ctx, who, "key:c", in); err != nil {
			t.Fatal(err)
		}
	}
	one, err := s.Create(ctx, who, "key:c", New{Initiative: "aa", Title: "t", Tags: []string{"work", "ralph", "home"}})
	if err != nil {
		t.Fatal(err)
	}
	mk(t, s, "aa", "plain")
	shut, err := s.Create(ctx, who, "key:c", New{Initiative: "bb", Title: "u", Tags: []string{"gone"}})
	if err != nil {
		t.Fatal(err)
	}
	ls := leases.Store{Store: s.Store, Now: s.Now}
	for _, h := range []string{"codex:a", "foo:x", "ralph", "claude:s", "codex:z"} {
		startLease(t, ls, audit.Actor{Handle: h}, one.Ref, "work")
	}
	if _, err := s.Close(ctx, who, "key:c", shut.Ref, "dropped"); err != nil {
		t.Fatal(err)
	}
	open := map[string][]string{
		"work": {"task"}, "ralph": {"task", "initiative"}, "home": {"task", "initiative"},
		"claude": {"actor"}, "codex": {"actor"},
	}
	for _, status := range []string{"", "not_closed", "open"} {
		if got, err := s.TagSources(ctx, status, nil); err != nil || !reflect.DeepEqual(got, open) {
			t.Errorf("status %q = %v, %v, want %v: the closed task adds no source", status, got, err, open)
		}
	}
	all := map[string][]string{"gone": {"task"}, "shut": {"initiative"}}
	for tag, sources := range open {
		all[tag] = sources
	}
	if got, err := s.TagSources(ctx, "all", nil); err != nil || !reflect.DeepEqual(got, all) {
		t.Errorf("status all = %v, %v, want %v", got, err, all)
	}
	if got, err := s.TagSources(ctx, "closed", nil); err != nil || !reflect.DeepEqual(got, map[string][]string{"gone": {"task"}, "shut": {"initiative"}}) {
		t.Errorf("status closed = %v, %v", got, err)
	}
	if got, err := s.TagSources(ctx, "done", nil); err != nil || len(got) != 0 {
		t.Errorf("status done = %v, %v, want none", got, err)
	}
	if got, err := s.TagSources(ctx, "all", []string{" Ralph ", "gone", "never", "foo"}); err != nil ||
		!reflect.DeepEqual(got, map[string][]string{"ralph": {"task", "initiative"}, "gone": {"task"}}) {
		t.Errorf("tags asked for = %v, %v: only those with a source, a handle of no kind is no tag", got, err)
	}
	// The answer is the sources of the marks the list gives, for the same tasks.
	page, err := s.List(ctx, Query{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	fromList := map[string]map[string]bool{}
	for _, item := range page.Items {
		for _, m := range item.TagMarks {
			if fromList[m.Tag] == nil {
				fromList[m.Tag] = map[string]bool{}
			}
			fromList[m.Tag][m.Source] = true
		}
	}
	for tag, sources := range all {
		if len(fromList[tag]) != len(sources) {
			t.Errorf("tag %q: the list's marks give %v, the endpoint %v", tag, fromList[tag], sources)
		}
		for _, source := range sources {
			if !fromList[tag][source] {
				t.Errorf("tag %q: source %s is not among the list's marks %v", tag, source, fromList[tag])
			}
		}
	}
	if len(fromList) != len(all) {
		t.Errorf("the list's marks name %d tags, the endpoint %d", len(fromList), len(all))
	}
}
