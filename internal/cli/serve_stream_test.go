package cli

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/auth"
	"github.com/n-orlov/village-hall/internal/store"
)

func TestServeRejectsAnUnreadableStreamHeartbeat(t *testing.T) {
	for _, v := range []string{"soon", "0", "0s", "-2s", "off"} {
		if code, errs := runServe(t, envWith(map[string]string{"BOARD_STREAM_HEARTBEAT": v})); code != ExitError ||
			!strings.Contains(errs, "VH_STREAM_HEARTBEAT") || strings.Count(errs, "\n") != 1 {
			t.Errorf("BOARD_STREAM_HEARTBEAT=%q: code=%d err=%q", v, code, errs)
		}
	}
}

// TestServeServesTheStreamWithTheConfiguredHeartbeat runs serveUntil with a
// heartbeat of 50 ms and reads its first ping off /api/v1/stream, then stops it.
func TestServeServesTheStreamWithTheConfiguredHeartbeat(t *testing.T) {
	url := sqliteURL(t)
	st, err := store.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	token, _, err := (&auth.Keys{Store: st}).Create(context.Background(), "caller", "admin-cli", 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errb bytes.Buffer
	exit := make(chan int, 1)
	env := envWith(map[string]string{"BOARD_STREAM_HEARTBEAT": "50ms", "BOARD_STREAM_POLL": "100ms"})
	go func() { exit <- serveUntil(ctx, &errb, env, url, addr, false) }()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v1/stream?topics=events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Board-Actor", "tester@host")
	var resp *http.Response
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("the server did not come up: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %v", resp.StatusCode, resp.Header)
	}
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Scan()
		line <- sc.Text()
	}()
	select {
	case got := <-line:
		if got != ": ping" {
			t.Errorf("first line = %q, want \": ping\"", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ping within 2 s")
	}
	cancel()
	select {
	case code := <-exit:
		if code != ExitOK || withoutLogLines(t, errb.String()) != "" {
			t.Errorf("serve ended with code=%d err=%q", code, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}
