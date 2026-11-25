package leases_test

import (
	"testing"

	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/leases"
)

// startKind starts a lease of the kind asked for by a on the task, holding res.
func (e env) startKind(t *testing.T, a audit.Actor, task string, notice bool, res ...string) leases.Result {
	t.Helper()
	e.Clock.add(1)
	r, err := e.S.Start(ctx, a, "key:"+a.Handle, leases.Start{Task: task, Intent: "work on " + task, Resources: res, Notice: notice})
	if err != nil {
		t.Fatalf("start %s (notice %v): %v", task, notice, err)
	}
	return r
}

// TestANoticeIsNeverAConflictInEitherDirection: one notice and one ordinary
// lease of different actors on one shared resource give no conflict in the start
// answer, whichever was started first, nor in the check-in of either actor
// (SPEC 5.2). Two ordinary leases on the same resource still conflict, so the
// empty answers are the notice's doing.
func TestANoticeIsNeverAConflictInEitherDirection(t *testing.T) {
	const shared = "svc:jenkins"
	for _, d := range []struct {
		name         string
		firstNotice  bool // the first lease started is the notice
		secondNotice bool
	}{
		{"A: the ordinary lease exists, the notice is started", false, true},
		{"B: the notice exists, the ordinary lease is started", true, false},
		{"control: two ordinary leases", false, false},
	} {
		t.Run(d.name, func(t *testing.T) {
			e := newEnv(t)
			first := e.startKind(t, bob, "aa-2", d.firstNotice, shared, "host:x")
			second := e.startKind(t, me, "aa-1", d.secondNotice, shared)
			bothOrdinary := !d.firstNotice && !d.secondNotice
			if len(first.Conflicts) != 0 {
				t.Errorf("the first start answered conflicts %+v, want none", first.Conflicts)
			}
			if got := len(second.Conflicts) == 1; got != bothOrdinary || len(second.Conflicts) > 1 {
				t.Errorf("the second start answered conflicts %+v, want a conflict: %v", second.Conflicts, bothOrdinary)
			}
			// the check-in: with no query a conflict needs two ordinary leases; querying the
			// shared resource, an ordinary lease of the other actor is still a hit, a notice is not
			for _, c := range []struct {
				who       audit.Actor
				queried   []string
				otherKind bool // the lease of the other actor is a notice
			}{
				{me, nil, d.firstNotice}, {bob, nil, d.secondNotice},
				{me, []string{shared}, d.firstNotice}, {bob, []string{shared}, d.secondNotice},
			} {
				n, err := e.S.CheckIn(ctx, c.who.Handle, c.queried)
				if err != nil {
					t.Fatal(err)
				}
				want := bothOrdinary || (c.queried != nil && !c.otherKind)
				if got := len(n.Conflicts) > 0; got != want {
					t.Errorf("check-in of %s querying %v: conflicts %+v, want a conflict: %v", c.who.Handle, c.queried, n.Conflicts, want)
				}
			}
			// the renewal of the second lease answers the same way as its start
			renewed := e.startKind(t, me, "aa-1", d.secondNotice, shared)
			if !renewed.Renewed || (len(renewed.Conflicts) == 1) != bothOrdinary {
				t.Errorf("renewal = %+v, want renewed with a conflict: %v", renewed, bothOrdinary)
			}
		})
	}
}
