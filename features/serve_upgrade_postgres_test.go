package features

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
)

// TestTwoServersStartedTogetherOnOnePostgresReopenATaskOnce is the two-server
// case of the start-up upgrade (SPEC 4.3): two `vh serve` processes start at
// the same moment on a database holding a done task with a live lease, and the
// conditional update lets exactly one of them reopen it and write its one
// task_reopened event.
func TestTwoServersStartedTogetherOnOnePostgresReopenATaskOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pg, err := startPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.terminate(context.Background()) })
	url, err := pg.newEmptyDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedDoneTaskWithLease(ctx, url, "claude@laptop", false); err != nil {
		t.Fatal(err)
	}
	h := &harness{binary: buildBoard(t), leg: "postgres", dbURL: url}
	var servers []*exec.Cmd
	var addrs []string
	logs := make([]*bytes.Buffer, 2)
	for i := range logs {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := "127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()
		logs[i] = &bytes.Buffer{}
		cmd := exec.Command(h.binary, "serve")
		cmd.Env = h.env("BOARD_LISTEN="+addr, "BOARD_DB_URL="+url)
		cmd.Stdout, cmd.Stderr = logs[i], logs[i]
		servers, addrs = append(servers, cmd), append(addrs, addr)
	}
	for _, cmd := range servers {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, cmd := range servers {
			endServer(cmd)
		}
	})
	for i, addr := range addrs {
		waitListening(t, addr, logs[i])
	}
	h.dbURL = url
	got, err := h.taskReopenedEvents(ctx, "aa-1")
	if err != nil || len(got) != 1 || got[0][1] != "system:upgrade" || !jsonHas(got[0][2], "reason", "live lease at upgrade") {
		t.Fatalf("task_reopened events of aa-1 = %v, %v; want exactly one by system:upgrade\nserver logs:\n%s\n%s", got, err, logs[0], logs[1])
	}
	if err := h.taskStatusIs(ctx, "aa-1", "open"); err != nil {
		t.Fatal(err)
	}
}

func waitListening(t *testing.T, addr string, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("tcp", addr); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server never listened on %s: %s", addr, logs.String())
}

// TestTwoServersStartedTogetherOnOnePostgresCreateOneInbox is the two-server
// case of the inbox bootstrap (SPEC 4.2): two `vh serve` processes start at
// the same moment on a migrated database with no inbox, and the unique slug
// leaves one inbox initiative and one initiative_created event.
func TestTwoServersStartedTogetherOnOnePostgresCreateOneInbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pg, err := startPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.terminate(context.Background()) })
	url, err := pg.newEmptyDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := &harness{binary: buildBoard(t), leg: "postgres", dbURL: url}
	var servers []*exec.Cmd
	var addrs []string
	logs := make([]*bytes.Buffer, 2)
	for i := range logs {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := "127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		_ = ln.Close()
		logs[i] = &bytes.Buffer{}
		cmd := exec.Command(h.binary, "serve")
		cmd.Env = h.env("BOARD_LISTEN="+addr, "BOARD_DB_URL="+url, "BOARD_MIGRATE=off")
		cmd.Stdout, cmd.Stderr = logs[i], logs[i]
		servers, addrs = append(servers, cmd), append(addrs, addr)
	}
	for _, cmd := range servers {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, cmd := range servers {
			endServer(cmd)
		}
	})
	for i, addr := range addrs {
		waitListening(t, addr, logs[i])
	}
	if err := checkCreationEvents(ctx, st, "inbox", 1, "operator", "operator", "system:bootstrap"); err != nil {
		t.Fatalf("%v\nserver logs:\n%s\n%s", err, logs[0], logs[1])
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM initiatives`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d initiatives, err %v; want the one inbox", n, err)
	}
}
