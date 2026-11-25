package tasks

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func refs(p Page) string {
	out := make([]string, len(p.Items))
	for i, t := range p.Items {
		out[i] = t.Ref
	}
	return strings.Join(out, ",")
}

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

func TestListFiltersByInitiativeStatusKindAndText(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	initiative(t, s, "bb")
	mk(t, s, "aa", "Water plants")
	mk(t, s, "bb", "other")
	done := mk(t, s, "aa", "done one")
	if _, err := s.Done(ctx, who, "key:c", done.Ref, DoneOptions{}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"all by default but closed", Query{}, "aa-1,bb-1,aa-2"},
		{"one initiative", Query{Initiatives: []string{"bb"}}, "bb-1"},
		{"two initiatives", Query{Initiatives: []string{"bb", "AA"}}, "aa-1,bb-1,aa-2"},
		{"open", Query{Status: StatusOpen}, "aa-1,bb-1"},
		{"done", Query{Status: StatusDone}, "aa-2"},
		{"closed", Query{Status: StatusClosed}, ""},
		{"text", Query{Q: "PLANT"}, "aa-1"},
		{"text with a wildcard", Query{Q: "%"}, ""},
		{"kind", Query{Kind: "watch"}, ""},
	}
	for _, c := range cases {
		got, err := s.List(ctx, c.q)
		if err != nil || refs(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, refs(got), err, c.want)
		}
	}
}

func TestListPagesByIdAndRefusesWhatItCannotRead(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	for _, title := range []string{"one", "two", "three"} {
		mk(t, s, "aa", title)
	}
	first, err := s.List(ctx, Query{Limit: 2})
	if err != nil || refs(first) != "aa-1,aa-2" || first.NextAfter != first.Items[1].ID {
		t.Fatalf("first page = %q %q, %v", refs(first), first.NextAfter, err)
	}
	last, err := s.List(ctx, Query{Limit: 2, After: first.NextAfter})
	if err != nil || refs(last) != "aa-3" || last.NextAfter != "" {
		t.Fatalf("last page = %q %q, %v", refs(last), last.NextAfter, err)
	}
	if got, err := s.List(ctx, Query{After: "AA-1"}); err != nil || refs(got) != "aa-2,aa-3" {
		t.Errorf("after a ref in capitals = %q, %v", refs(got), err)
	}
	if got, err := s.List(ctx, Query{After: "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"}); err != nil || len(got.Items) != 0 {
		t.Errorf("after an id past every task = %q, %v", refs(got), err)
	}
	if _, err := s.List(ctx, Query{After: "aa-77"}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("after an unknown ref: %v, want ErrBadCursor", err)
	}
	for name, q := range map[string]Query{
		"limit":         {Limit: -1},
		"status":        {Status: "nope"},
		"kind":          {Kind: "nope"},
		"tag":           {Tags: []string{"Bad Tag!"}},
		"closed_within": {Status: StatusClosed, ClosedWithin: "soon"},
		"window status": {ClosedWithin: "7d"},
	} {
		if _, err := s.List(ctx, q); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.List(ctx, Query{Initiatives: []string{"zz"}}); !errors.Is(err, ErrNoInitiative) {
		t.Errorf("unknown initiative: %v, want ErrNoInitiative", err)
	}
}

func TestListKeepsTheDerivedStatesApart(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	blocker := mk(t, s, "aa", "blocker")
	waiting := mk(t, s, "aa", "waiting")
	late := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cadence := int64(60)
	if _, err := s.Create(ctx, who, "key:c", New{Initiative: "aa", Title: "watch", Kind: "watch", CadenceSeconds: &cadence, NextCheckAt: &late}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDep(ctx, who, "key:c", waiting.Ref, blocker.Ref); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		q    Query
		want string
	}{
		"blocked":        {Query{Blocked: yes()}, "aa-2"},
		"not blocked":    {Query{Blocked: no()}, "aa-1,aa-3"},
		"overdue":        {Query{Overdue: yes()}, "aa-3"},
		"not overdue":    {Query{Overdue: no()}, "aa-1,aa-2"},
		"active":         {Query{Active: yes()}, ""},
		"not active":     {Query{Active: no()}, "aa-1,aa-2,aa-3"},
		"watch overdue":  {Query{Kind: "watch", Overdue: yes()}, "aa-3"},
		"closed in time": {Query{Status: StatusAll, ClosedWithin: "7d"}, "aa-1,aa-2,aa-3"},
	} {
		got, err := s.List(ctx, c.q)
		if err != nil || refs(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", name, refs(got), err, c.want)
		}
	}
	got, _ := s.List(ctx, Query{Overdue: yes()})
	if len(got.Items) != 1 || !got.Items[0].Overdue {
		t.Errorf("overdue item = %+v", got.Items)
	}
}

func TestListTextFoldsNonASCIICaseAndKeepsPatternCharactersLiteral(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	mk(t, s, "aa", "Äpfel ernten")
	mk(t, s, "aa", "ΟΔΥΣΣΕΑΣ lesen")
	mk(t, s, "aa", "C:\\dir und 50% fertig_")
	mk(t, s, "aa", "C:xdir und 500 fertigX")
	if _, err := s.Create(ctx, who, "key:d", New{Initiative: "aa", Title: "Other", Description: "Купить ХЛЕБ"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, q, want string }{
		{"umlaut", "äPFEL", "aa-1"},
		{"capital umlaut in the text", "ÄPFEL", "aa-1"},
		{"final sigma", "οδυσσεας", "aa-2"},
		{"cyrillic description", "хлеб", "aa-5"},
		{"cyrillic capitals", "ХЛЕБ", "aa-5"},
		{"backslash", "c:\\", "aa-3"},
		{"percent", "50% f", "aa-3"},
		{"underscore", "g_", "aa-3"},
		{"wrong letter", "üpfel", ""},
	}
	for _, c := range cases {
		got, err := s.List(ctx, Query{Q: c.q})
		if err != nil || refs(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, refs(got), err, c.want)
		}
	}
	first, err := s.List(ctx, Query{Q: "DIR", Limit: 1})
	if err != nil || refs(first) != "aa-3" || first.NextAfter == "" {
		t.Fatalf("first page %q, %q, %v", refs(first), first.NextAfter, err)
	}
	next, err := s.List(ctx, Query{Q: "DIR", Limit: 1, After: first.NextAfter})
	if err != nil || refs(next) != "aa-4" || next.NextAfter != "" {
		t.Fatalf("next page %q, %q, %v", refs(next), next.NextAfter, err)
	}
}

func TestListKeepsTheTasksWhoseRefIsInTheAttentionRefs(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	initiative(t, s, "bb")
	mk(t, s, "aa", "one")
	mk(t, s, "bb", "two")
	mk(t, s, "aa", "three")
	needs := []string{"aa-1", "bb-1"}
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"no filter keeps every task", Query{AttentionRefs: needs}, "aa-1,bb-1,aa-2"},
		{"true keeps the listed refs", Query{Attention: yes(), AttentionRefs: needs}, "aa-1,bb-1"},
		{"false keeps the others", Query{Attention: no(), AttentionRefs: needs}, "aa-2"},
		{"true with no refs keeps nothing", Query{Attention: yes()}, ""},
		{"false with no refs keeps every task", Query{Attention: no()}, "aa-1,bb-1,aa-2"},
		{"true with another filter", Query{Attention: yes(), AttentionRefs: needs, Initiatives: []string{"bb"}}, "bb-1"},
		{"a ref is matched whole", Query{Attention: yes(), AttentionRefs: []string{"aa-10", "a-1"}}, ""},
	}
	for _, c := range cases {
		got, err := s.List(ctx, c.q)
		if err != nil || refs(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, refs(got), err, c.want)
		}
	}
}

func TestListKeepsOnlyTheTasksWhoseRefIsNamed(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	initiative(t, s, "bb")
	mk(t, s, "aa", "one")
	mk(t, s, "bb", "two")
	mk(t, s, "aa", "three")
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"no refs keeps every task", Query{}, "aa-1,bb-1,aa-2"},
		{"one ref", Query{Refs: []string{"bb-1"}}, "bb-1"},
		{"two refs in any letter case", Query{Refs: []string{"AA-2", "bb-1"}}, "bb-1,aa-2"},
		{"a ref that names no task keeps nothing", Query{Refs: []string{"aa-9"}}, ""},
		{"a ref is matched whole", Query{Refs: []string{"aa-10", "a-1"}}, ""},
		{"with another filter", Query{Refs: []string{"aa-1", "bb-1"}, Initiatives: []string{"bb"}}, "bb-1"},
	}
	for _, c := range cases {
		got, err := s.List(ctx, c.q)
		if err != nil || refs(got) != c.want {
			t.Errorf("%s: %q, %v, want %q", c.name, refs(got), err, c.want)
		}
	}
}

func TestCountIsTheLengthOfTheListWithoutAPageOrText(t *testing.T) {
	s := newStore(t)
	initiative(t, s, "aa")
	if n, err := s.Count(ctx, Query{}); err != nil || n != 0 {
		t.Fatalf("no tasks: %d, %v, want 0", n, err)
	}
	one := mk(t, s, "aa", "one")
	mk(t, s, "aa", "two")
	mk(t, s, "aa", "three")
	if _, err := s.Done(ctx, who, "key:d", one.Ref, DoneOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Close(ctx, who, "key:c", one.Ref, "dropped"); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		q    Query
		want int
	}{
		"default leaves the closed task out": {Query{}, 2},
		"not closed":                         {Query{Status: StatusNotClosed}, 2},
		"closed":                             {Query{Status: StatusClosed}, 1},
		"all":                                {Query{Status: StatusAll}, 3},
		"filter matching some":               {Query{Refs: []string{"aa-2"}}, 1},
		"filter matching none":               {Query{Refs: []string{"aa-9"}}, 0},
		"page and text are not read":         {Query{Limit: 1, Q: "one", After: "aa-1"}, 2},
	} {
		if got, err := s.Count(ctx, c.q); err != nil || got != c.want {
			t.Errorf("%s: %d, %v, want %d", name, got, err, c.want)
		}
	}
	if _, err := s.Count(ctx, Query{Status: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad status: %v, want ErrInvalid", err)
	}
	if _, err := s.Count(ctx, Query{Initiatives: []string{"zz"}}); !errors.Is(err, ErrNoInitiative) {
		t.Errorf("unknown initiative: %v, want ErrNoInitiative", err)
	}
}
