package store

import (
	"context"
	"reflect"
	"testing"
)

func TestParseNotifyOn(t *testing.T) {
	all := []string{"stranded", "asking", "done"}
	for in, want := range map[string][]string{
		"":                     all,
		"  ":                   all,
		"done":                 {"done"},
		"asking,stranded":      {"asking", "stranded"},
		" stranded , done ":    {"stranded", "done"},
		"done,done":            {"done", "done"},
		"stranded,asking,done": all,
	} {
		got, err := ParseNotifyOn(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseNotifyOn(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
}

func TestParseNotifyOnRefusesAnyOtherWord(t *testing.T) {
	for _, in := range []string{"all", "Done", "done,", ",", "done stranded", "stranded,none", "alerts"} {
		if got, err := ParseNotifyOn(in); err == nil {
			t.Errorf("ParseNotifyOn(%q) = %q, want an error", in, got)
		}
	}
}

func TestParseNotifyOnReturnsACopyOfTheDefaults(t *testing.T) {
	got, _ := ParseNotifyOn("")
	got[0] = "changed"
	if again, _ := ParseNotifyOn(""); again[0] != "stranded" {
		t.Fatalf("the defaults were changed: %q", again)
	}
}

func TestNotifiesIsOffUntilSetNotify(t *testing.T) {
	var s Store
	if s.Notifies(NotifyDone) {
		t.Fatal("a store nobody configured notifies")
	}
	s.SetNotify([]string{NotifyAsking})
	if !s.Notifies(NotifyAsking) || s.Notifies(NotifyDone) || s.Notifies(NotifyStranded) {
		t.Fatal("SetNotify did not turn on exactly asking")
	}
	s.SetNotify(nil)
	if s.Notifies(NotifyAsking) {
		t.Fatal("SetNotify(nil) left asking on")
	}
}

func TestPublicURLIsEmptyUntilSet(t *testing.T) {
	var s Store
	if s.PublicURL() != "" {
		t.Fatalf("PublicURL = %q before SetPublicURL", s.PublicURL())
	}
	s.SetPublicURL("https://board.example")
	if s.PublicURL() != "https://board.example" {
		t.Fatalf("PublicURL = %q", s.PublicURL())
	}
}

func TestAfterTxCommitRunsOnCommitAndNeverOnRollback(t *testing.T) {
	s := unitStore(t)
	ctx := context.Background()
	for _, commit := range []bool{true, false} {
		tx, err := s.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ran := 0
		s.AfterTxCommit(ctx, tx.Tx, func() { ran++ })
		s.AfterTxCommit(ctx, tx.Tx, func() { ran++ })
		if ran != 0 {
			t.Fatalf("hooks ran before the end of the transaction: %d", ran)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 2, false: 0}[commit]; ran != want {
			t.Errorf("commit=%v: hooks ran %d times, want %d", commit, ran, want)
		}
		if _, left := s.txAfter.Load(tx.Tx); left {
			t.Errorf("commit=%v: the hooks of the ended transaction are still held", commit)
		}
	}
}

func TestAfterTxCommitUnderAUnitWaitsForTheUnit(t *testing.T) {
	s := unitStore(t)
	ctx, u := beginUnit(t, s)
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	s.AfterTxCommit(ctx, tx.Tx, func() { ran++ })
	if err := tx.Commit(); err != nil || ran != 0 {
		t.Fatalf("after the savepoint's release: err %v, ran %d, want nil and 0", err, ran)
	}
	if err := u.Commit(); err != nil || ran != 1 {
		t.Fatalf("after the unit's commit: err %v, ran %d, want nil and 1", err, ran)
	}
}

func TestWakeNotifyCallsWhatWasRegisteredAndNothingAfterItIsCleared(t *testing.T) {
	s := unitStore(t)
	s.WakeNotify() // nothing registered: no panic
	n := 0
	s.SetNotifyWake(func() { n++ })
	s.WakeNotify()
	s.SetNotifyWake(nil)
	s.WakeNotify()
	if n != 1 {
		t.Fatalf("the wake ran %d times, want 1", n)
	}
}
