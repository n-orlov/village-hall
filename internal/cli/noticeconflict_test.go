package cli

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/actors"
	"github.com/n-orlov/village-hall/internal/asks"
	"github.com/n-orlov/village-hall/internal/attention"
	"github.com/n-orlov/village-hall/internal/audit"
	"github.com/n-orlov/village-hall/internal/cursors"
	"github.com/n-orlov/village-hall/internal/events"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/initiatives"
	"github.com/n-orlov/village-hall/internal/leases"
	"github.com/n-orlov/village-hall/internal/notes"
	"github.com/n-orlov/village-hall/internal/roster"
	"github.com/n-orlov/village-hall/internal/server"
	"github.com/n-orlov/village-hall/internal/store"
	"github.com/n-orlov/village-hall/internal/tasks"
)

// noticeBoard is the real server over a real SQLite store with an initiative
// "aa" and tasks aa-1 and aa-2, and the vh CLI of two actors run against it.
type noticeBoard struct {
	srv *httptest.Server
}

func newNoticeBoard(t *testing.T) noticeBoard {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	seed := audit.Actor{Handle: "seed@host"}
	if _, err := (&initiatives.Store{Store: st}).Create(ctx, seed, "key:x", initiatives.New{Slug: "aa", Title: "I"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := (&tasks.Store{Store: st}).Create(ctx, seed, "key:x", tasks.New{Initiative: "aa", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	ls := &leases.Store{Store: st}
	verify := func(context.Context, string) (string, error) { return "key:x", nil }
	h := server.NewHandler(func(context.Context) error { return nil }, verify, nil, nil,
		server.WithInitiatives(&initiatives.Store{Store: st}), server.WithTasks(&tasks.Store{Store: st}, ls),
		server.WithEvents(&events.Store{Store: st}), server.WithLeases(ls), server.WithAsks(&asks.Store{Store: st}),
		server.WithNotes(&notes.Store{Store: st}), server.WithAttention(&attention.Store{Store: st}),
		server.WithActors(&roster.Store{Store: st, Leases: ls, Asks: &asks.Store{Store: st}}), server.WithCursors(&cursors.Store{Store: st}),
		server.WithMeta(server.Build{}, &actors.Store{Store: st}), server.WithIdempotency(&idempotency.Store{Store: st}))
	b := noticeBoard{srv: httptest.NewServer(h)}
	t.Cleanup(b.srv.Close)
	return b
}

// vh runs the CLI as actor with args.
func (b noticeBoard) vh(actor string, args ...string) (int, string, string) {
	return runEnv(envWith(map[string]string{"BOARD_URL": b.srv.URL, "BOARD_API_KEY": "brd_abcdefgh" + strings.Repeat("0", 44),
		"BOARD_ACTOR": actor}), args...)
}

// TestLeaseStartWithANoticeOnASharedResourceExitsZeroInBothDirections: a
// notice and an ordinary lease of another actor on one shared resource, the
// ordinary one first (A) or the notice first (B), exit 0 on the start, not 7, and
// `vh now --resources` prints no conflict line for the ordinary lease's holder.
// A control with two ordinary leases exits 7, so the zero is the notice's doing.
func TestLeaseStartWithANoticeOnASharedResourceExitsZeroInBothDirections(t *testing.T) {
	const shared = "svc:jenkins"
	for _, d := range []struct {
		name     string
		notice   bool // the second start is the notice
		reversed bool // the notice exists first
		code     int
	}{
		{"A: the ordinary lease exists, the notice is started", true, false, ExitOK},
		{"B: the notice exists, the ordinary lease is started", false, true, ExitOK},
		{"control: two ordinary leases", false, false, ExitWarning},
	} {
		t.Run(d.name, func(t *testing.T) {
			b := newNoticeBoard(t)
			firstArgs := []string{"lease", "start", "aa-2", "first", "--res", shared}
			secondArgs := []string{"lease", "start", "aa-1", "second", "--res", shared}
			if d.reversed {
				firstArgs = append(firstArgs, "--notice")
			}
			if d.notice {
				secondArgs = append(secondArgs, "--notice")
			}
			if code, out, errs := b.vh("codex@host", firstArgs...); code != ExitOK {
				t.Fatalf("first start exits %d: %q %q", code, out, errs)
			}
			code, out, errs := b.vh("claude@host", secondArgs...)
			if code != d.code {
				t.Errorf("second start exits %d, want %d: %q %q", code, d.code, out, errs)
			}
			if has := strings.Contains(out, "CONFLICT"); has != (d.code == ExitWarning) {
				t.Errorf("second start prints a conflict line: %v, out %q", has, out)
			}
			// the holder of the ordinary lease asks about the shared resource
			holder, other := "claude@host", "codex@host"
			if d.notice {
				holder, other = other, holder
			}
			code, out, errs = b.vh(holder, "now", "--resources", shared)
			if want := d.code; code != want || strings.Contains(out, "CONFLICT") != (want == ExitWarning) {
				t.Errorf("vh now --resources as %s exits %d, want %d; out %q %q (other holder %s)", holder, code, want, out, errs, other)
			}
			if d.code == ExitOK && !strings.Contains(out, "conflicts: none") {
				t.Errorf("vh now --resources as %s has no `conflicts: none` section: %q", holder, out)
			}
		})
	}
}
