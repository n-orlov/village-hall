package leases_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/leases"
)

// startNotice starts a lease of the asked kind for me on the task.
func (e env) startNotice(t *testing.T, notice bool) (leases.Result, error) {
	t.Helper()
	e.Clock.add(time.Second)
	return e.S.Start(ctx, me, "key:claude@host", leases.Start{Task: "aa-1", Intent: "jenkins is down", Resources: []string{"svc:jenkins"}, Notice: notice})
}

// rowCounts is the number of lease rows and of events.
func (e env) rowCounts(t *testing.T) (leaseRows, eventRows int) {
	t.Helper()
	if err := e.S.Store.Reader().QueryRow(`SELECT COUNT(*) FROM leases`).Scan(&leaseRows); err != nil {
		t.Fatal(err)
	}
	if err := e.S.Store.Reader().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	return leaseRows, eventRows
}

// TestStartWithNoLiveLeaseOnTheTaskGivesANewLeaseOfTheAskedKind: start case 1,
// for each kind. The notice flag is stored, read back, and carried by the
// lease_started event for a notice only.
func TestStartWithNoLiveLeaseOnTheTaskGivesANewLeaseOfTheAskedKind(t *testing.T) {
	for _, notice := range []bool{true, false} {
		e := newEnv(t)
		r, err := e.startNotice(t, notice)
		if err != nil {
			t.Fatalf("notice=%v: %v", notice, err)
		}
		if r.Renewed || r.Lease.Ref != "L1" || r.Lease.Notice != notice {
			t.Errorf("notice=%v: result = %+v", notice, r)
		}
		var stored bool
		if err := e.S.Store.Reader().QueryRow(`SELECT notice FROM leases WHERE n = 1`).Scan(&stored); err != nil || stored != notice {
			t.Errorf("notice=%v: stored column = %v, %v", notice, stored, err)
		}
		got, err := e.S.LiveFor(ctx, []string{r.Lease.TaskID})
		live := got[r.Lease.TaskID]
		if err != nil || len(live) != 1 || live[0].Notice != notice {
			t.Errorf("notice=%v: a read gives %+v, %v", notice, live, err)
		}
		evs := e.events(t, leases.EventStarted)
		if len(evs) != 1 {
			t.Fatalf("notice=%v: events = %+v", notice, evs)
		}
		flag, present := evs[0].Data["notice"]
		if notice && flag != true || !notice && present {
			t.Errorf("notice=%v: lease_started data notice = %v (present %v)", notice, flag, present)
		}
	}
}

// TestStartWithALiveLeaseOfTheSameKindRenewsIt: start case 2, for each kind. The
// same lease is renewed, keeps its kind and writes no new lease row.
func TestStartWithALiveLeaseOfTheSameKindRenewsIt(t *testing.T) {
	for _, notice := range []bool{true, false} {
		e := newEnv(t)
		first, err := e.startNotice(t, notice)
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.startNotice(t, notice)
		if err != nil {
			t.Fatalf("notice=%v: %v", notice, err)
		}
		if !second.Renewed || second.Lease.ID != first.Lease.ID || second.Lease.Notice != notice {
			t.Errorf("notice=%v: second = %+v", notice, second)
		}
		if rows, _ := e.rowCounts(t); rows != 1 {
			t.Errorf("notice=%v: lease rows = %d, want 1", notice, rows)
		}
		if n := len(e.events(t, leases.EventRenewed)); n != 1 {
			t.Errorf("notice=%v: lease_renewed events = %d, want 1", notice, n)
		}
	}
}

// TestStartWithALiveLeaseOfTheOtherKindIsRefusedAndWritesNothing: start case 3,
// both ways round. The error names the held lease and says to end it first, and
// neither the lease rows nor the events change.
func TestStartWithALiveLeaseOfTheOtherKindIsRefusedAndWritesNothing(t *testing.T) {
	for _, heldNotice := range []bool{true, false} {
		e := newEnv(t)
		held, err := e.startNotice(t, heldNotice)
		if err != nil {
			t.Fatal(err)
		}
		leaseRows, eventRows := e.rowCounts(t)
		_, err = e.startNotice(t, !heldNotice)
		var ce leases.ConflictError
		if !errors.Is(err, leases.ErrConflict) || !errors.As(err, &ce) {
			t.Fatalf("held notice=%v: err = %v, want a ConflictError", heldNotice, err)
		}
		if !strings.Contains(ce.Msg, held.Lease.Ref) || !strings.Contains(ce.Msg, "aa-1") || !strings.Contains(ce.Next, "end "+held.Lease.Ref+" first") {
			t.Errorf("held notice=%v: msg %q next %q", heldNotice, ce.Msg, ce.Next)
		}
		if l, ev := e.rowCounts(t); l != leaseRows || ev != eventRows {
			t.Errorf("held notice=%v: rows %d/%d -> %d/%d, want no change", heldNotice, leaseRows, eventRows, l, ev)
		}
		got, err := e.S.LiveFor(ctx, []string{held.Lease.TaskID})
		live := got[held.Lease.TaskID]
		if err != nil || len(live) != 1 || live[0].Notice != heldNotice || !live[0].ExpiresAt.Equal(held.Lease.ExpiresAt) {
			t.Errorf("held notice=%v: the held lease changed: %+v, %v", heldNotice, live, err)
		}
	}
}

// TestAnotherActorsLeaseOfTheOtherKindDoesNotRefuseAStart: the refusal is about
// the actor's own live lease on the task, so another holder's lease of the other
// kind leaves a start a new lease.
func TestAnotherActorsLeaseOfTheOtherKindDoesNotRefuseAStart(t *testing.T) {
	e := newEnv(t)
	if _, err := e.startNotice(t, false); err != nil {
		t.Fatal(err)
	}
	e.Clock.add(time.Second)
	r, err := e.S.Start(ctx, bob, "key:b", leases.Start{Task: "aa-1", Intent: "freeze", Notice: true})
	if err != nil || r.Renewed || !r.Lease.Notice {
		t.Fatalf("start = %+v, %v", r, err)
	}
}
