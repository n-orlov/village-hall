package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/asks"
)

// An end whose COMMIT returned before until is answered ended in two cases. Its
// commit mark is at or before until but the first read comes after until. And
// its mark is read only after until, but a wait held from before until reads the
// end in time. Answers and withdrawals; both databases.
func TestAnEndWhoseCommitReturnedBeforeUntilIsAnsweredEnded(t *testing.T) {
	t.Parallel()
	forEachLeg(t, func(t *testing.T, _ string, dbs databases) {
		for _, withdraw := range []bool{false, true} {
			t.Run(fmt.Sprintf("withdraw=%v", withdraw), func(t *testing.T) {
				t.Parallel()
				want := map[bool]string{false: "answered", true: "withdrawn"}[withdraw]
				st := openStore(t, dbs.New(t))
				if err := st.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				r, as := askRig(t, st, newToken(t, st), newTestClock(), time.Hour)

				// the mark is at or before until; the first read comes after it
				ref := r.raiseAsk()
				until := time.Now().Add(300 * time.Millisecond)
				if err := endAsk(&asks.Store{Store: st}, withdraw, ref); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Until(until.Add(200 * time.Millisecond)))
				if k, _ := as.Get(context.Background(), ref); k.EndCommitAt == nil || k.EndCommitAt.After(until) {
					t.Fatalf("withdraw=%v: mark %v, want it at or before until", withdraw, k.EndCommitAt)
				}
				if status, doc := r.getUntil(ref, until); status != http.StatusOK || doc["state"] != want {
					t.Errorf("withdraw=%v: first read after until got %d %v, want 200 %s", withdraw, status, doc["state"], want)
				}

				// the mark is read after until; a wait held from before until reads the end in time.
				// until is 750 ms out, the end starts 50 ms in and its mark is read 150 ms
				// after until: the same hold : mark delay : until as 1500 : 100 : 300 ms, halved
				ref = r.raiseAsk()
				until = time.Now().Add(750 * time.Millisecond)
				delayed := &asks.Store{Store: st, MarkClock: func() time.Time {
					time.Sleep(time.Until(until.Add(150 * time.Millisecond)))
					return time.Now()
				}}
				w := r.startWait(ref, untilQuery("5", until))
				time.Sleep(50 * time.Millisecond)
				ended := make(chan error, 1)
				go func() { ended <- endAsk(delayed, withdraw, ref) }()
				w.ends(t)
				if err := <-ended; err != nil {
					t.Fatal(err)
				}
				if k, _ := as.Get(context.Background(), ref); k.EndCommitAt == nil || !k.EndCommitAt.After(until) {
					t.Fatalf("withdraw=%v: mark %v, want it after until", withdraw, k.EndCommitAt)
				}
				if w.status != http.StatusOK || w.state != want {
					t.Errorf("withdraw=%v: the wait held from before until got %d %q, want 200 %s", withdraw, w.status, w.state, want)
				}
			})
		}
	})
}
