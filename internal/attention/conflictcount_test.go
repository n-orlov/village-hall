package attention

import (
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/store/storetest"
)

// TestConflictCountIsOneStatementAndCountsTheSharedResources pins conflictCount
// at one reader query whatever the leases, and checks its number in the cases
// that decide it: no live lease, two holders of a resource, one actor's two
// leases on different tasks sharing a resource, and an expired lease sharing a
// resource with a live one.
func TestConflictCountIsOneStatementAndCountsTheSharedResources(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e env)
		want  int
	}{
		{"no live lease", func(env) {}, 0},
		{"one holder", func(e env) { e.lease(t, me, "aa-1", "repo:board") }, 0},
		{"two holders of one resource", func(e env) {
			e.lease(t, me, "aa-1", "repo:board")
			e.lease(t, bob, "bb-1", "repo:board")
		}, 1},
		{"one actor's two leases on different tasks sharing a resource", func(e env) {
			e.lease(t, me, "aa-1", "repo:board")
			e.lease(t, me, "aa-2", "repo:board")
		}, 1},
		{"an expired lease sharing a resource with a live one", func(e env) {
			e.lease(t, me, "aa-1", "repo:board")
			e.tick(2 * time.Minute)
			e.lease(t, bob, "bb-1", "repo:board")
		}, 0},
		{"a notice lease sharing a resource with an ordinary lease", func(e env) {
			e.lease(t, me, "aa-1", "repo:board")
			e.tick(time.Second)
			must(e.Leases.Start(ctx, bob, "key:x", leases.Start{Task: "bb-1", Intent: "freeze", Resources: []string{"repo:board"}, Notice: true}))
		}, 0},
		{"two resources shared by the same two leases", func(e env) {
			e.lease(t, me, "aa-1", "repo:board", "host:x", "only:me")
			e.lease(t, bob, "bb-1", "repo:board", "host:x")
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, rec := storetest.OpenMigrated(t)
			e := newEnvOn(t, st)
			tc.setup(e)
			rec.Reset()
			got, err := e.A.conflictCount(ctx, scope{}, e.Clock.now)
			if err != nil || got != tc.want {
				t.Fatalf("conflictCount = %d, %v, want %d", got, err, tc.want)
			}
			if n := len(rec.All()); n != 1 || rec.Count(store.PoolReader, storetest.Query) != 1 {
				t.Errorf("conflictCount issued %+v, want one reader query", rec.All())
			}
		})
	}
}

// TestConflictCountKeepsTheConflictsOfTheScopeInOneStatement counts under an
// initiative scope and a tag scope: a conflict stays when one of its leases is on
// a task the scope keeps.
func TestConflictCountKeepsTheConflictsOfTheScopeInOneStatement(t *testing.T) {
	st, rec := storetest.OpenMigrated(t)
	e := newEnvOn(t, st)
	e.lease(t, me, "aa-1", "repo:board")
	e.lease(t, bob, "bb-1", "repo:board")
	e.lease(t, me, "bb-1", "host:x")
	e.lease(t, bob, "aa-2", "host:x")
	e.lease(t, me, "aa-2", "only:aa")
	e.lease(t, bob, "aa-1", "only:aa")
	for _, c := range []struct {
		name string
		q    Query
		want int
	}{
		{"no scope", Query{}, 3},
		{"initiative aa", Query{Initiatives: []string{"aa"}}, 3},
		{"initiative bb", Query{Initiatives: []string{"bb"}}, 2},
		{"tag ops", Query{Tags: []string{"ops"}}, 3},
		{"a tag nothing carries", Query{Tags: []string{"nope"}}, 0},
	} {
		sc, err := e.A.scope(ctx, c.q)
		if err != nil {
			t.Fatal(err)
		}
		rec.Reset()
		got, err := e.A.conflictCount(ctx, sc, e.Clock.now)
		if err != nil || got != c.want {
			t.Errorf("%s: conflictCount = %d, %v, want %d", c.name, got, err, c.want)
		}
		if n := len(rec.All()); n != 1 {
			t.Errorf("%s: conflictCount issued %+v, want one statement", c.name, rec.All())
		}
	}
}

// TestANoticeIsNeverAConflictInEitherDirection: a notice and an ordinary lease
// of different actors on one shared resource are no conflict in the section nor
// in its count, whichever was started first (SPEC 5.2). Two ordinary leases on
// the same resource still are, so the zero is the notice's doing.
func TestANoticeIsNeverAConflictInEitherDirection(t *testing.T) {
	start := func(e env, a audit.Actor, task string, notice bool) {
		e.tick(time.Second)
		ttl := int64(60)
		must(e.Leases.Start(ctx, a, "key:x", leases.Start{Task: task, Intent: "work on " + task, TTL: &ttl,
			Resources: []string{"svc:jenkins"}, Notice: notice}))
	}
	for _, d := range []struct {
		name                    string
		firstNotice, secondNote bool
		want                    int
	}{
		{"A: the ordinary lease exists, the notice is started", false, true, 0},
		{"B: the notice exists, the ordinary lease is started", true, false, 0},
		{"control: two ordinary leases", false, false, 1},
	} {
		t.Run(d.name, func(t *testing.T) {
			st, rec := storetest.OpenMigrated(t)
			e := newEnvOn(t, st)
			start(e, bob, "bb-1", d.firstNotice)
			start(e, me, "aa-1", d.secondNote)
			rec.Reset()
			got, err := e.A.conflictCount(ctx, scope{}, e.Clock.now)
			if err != nil || got != d.want {
				t.Errorf("conflictCount = %d, %v, want %d", got, err, d.want)
			}
			if n := len(rec.All()); n != 1 {
				t.Errorf("conflictCount issued %+v, want one statement", rec.All())
			}
			r := e.get(t, Query{})
			if len(r.Conflicts) != d.want || r.Counts.Conflicts != d.want {
				t.Errorf("section %+v, count %d, want %d", r.Conflicts, r.Counts.Conflicts, d.want)
			}
		})
	}
}
