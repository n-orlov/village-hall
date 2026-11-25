package leases_test

import (
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// noticeBoard builds the store of the R9 listing criteria: notice N1 (another
// actor, older), notice N2 (the caller, newer), one ended notice, one expired
// notice and one ordinary lease L. It returns the refs of N1, N2 and L.
func noticeBoard(t *testing.T) (e env, n1, n2, l string) {
	t.Helper()
	e = newEnv(t)
	for i := 0; i < 2; i++ {
		e.Clock.add(time.Second)
		if _, err := e.Tasks.Create(ctx, me, "key:x", tasks.New{Initiative: "aa", Title: "extra"}); err != nil {
			t.Fatal(err)
		}
	}
	first := e.startKind(t, bob, "aa-1", true, "svc:one")
	ended := e.startKind(t, bob, "aa-2", true, "svc:two")
	e.Clock.add(time.Second)
	if _, err := e.S.End(ctx, bob, "key:b", ended.Lease.Ref, "over"); err != nil {
		t.Fatal(err)
	}
	e.Clock.add(time.Second)
	ttl := int64(60)
	gone, err := e.S.Start(ctx, bob, "key:b", leases.Start{Task: "aa-3", Intent: "short", TTL: &ttl, Resources: []string{"svc:three"}, Notice: true})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := e.startKind(t, bob, "aa-4", false, "svc:four")
	second := e.startKind(t, me, "aa-2", true, "svc:five")
	e.Clock.add(2 * time.Minute)
	if e.Clock.Now().Before(gone.Lease.ExpiresAt) {
		t.Fatalf("the short notice %s is still live", gone.Lease.Ref)
	}
	return e, first.Lease.Ref, second.Lease.Ref, ordinary.Lease.Ref
}

func joinRefs(ls []leases.Lease) string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Ref
	}
	return strings.Join(out, ",")
}

// TestCheckInListsTheLiveNoticesOfEveryoneOldestFirstAndKeepsThemOutOfTheLeaseLists:
// notices holds N1 then N2 (the caller's own included) whatever the queried
// resources, without the ended and the expired notice, and Mine and Others hold
// the ordinary lease only (SPEC 6.5).
func TestCheckInListsTheLiveNoticesOfEveryoneOldestFirstAndKeepsThemOutOfTheLeaseLists(t *testing.T) {
	e, n1, n2, l := noticeBoard(t)
	for name, queried := range map[string][]string{"no resources": nil, "an unrelated resource": {"svc:none"}, "a notice's resource": {"svc:one"}} {
		t.Run(name, func(t *testing.T) {
			n, err := e.S.CheckIn(ctx, me.Handle, queried)
			if err != nil {
				t.Fatal(err)
			}
			if got := joinRefs(n.Notices); got != n1+","+n2 {
				t.Errorf("notices = %s, want %s,%s", got, n1, n2)
			}
			if len(n.Mine) != 0 {
				t.Errorf("my leases = %s, want none (the caller's notice is in notices)", joinRefs(n.Mine))
			}
			if got := joinRefs(n.Others); got != l {
				t.Errorf("others = %s, want only %s", got, l)
			}
			if len(n.Conflicts) != 0 {
				t.Errorf("conflicts = %+v, want none", n.Conflicts)
			}
			for _, k := range n.Notices {
				if !k.Notice || k.Title == "" {
					t.Errorf("notice %+v lacks the notice flag or the task title", k)
				}
			}
		})
	}
}

// TestListFiltersByNotice: notice=true is exactly N1 and N2, notice=false is
// exactly L, and no filter keeps all three live leases.
func TestListFiltersByNotice(t *testing.T) {
	e, n1, n2, l := noticeBoard(t)
	yes, no := true, false
	for name, c := range map[string]struct {
		notice *bool
		want   string
	}{"true": {&yes, n1 + "," + n2}, "false": {&no, l}, "absent": {nil, n1 + "," + l + "," + n2}} {
		t.Run(name, func(t *testing.T) {
			p, err := e.S.List(ctx, leases.Query{Notice: c.notice})
			if err != nil {
				t.Fatal(err)
			}
			if got := joinRefs(p.Items); got != c.want {
				t.Errorf("list = %s, want %s", got, c.want)
			}
		})
	}
}
