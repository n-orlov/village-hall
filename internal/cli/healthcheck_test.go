package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n-orlov/village-hall/internal/store"
)

func TestHealthcheckExitsZeroOnA200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	code, out, errs := run("healthcheck", "--url", srv.URL+"/healthz")
	if code != ExitOK || out != "ok healthz 200\n" || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	var stdout, stderr bytes.Buffer
	addr := strings.TrimPrefix(srv.URL, "http://")
	if code := Run([]string{"healthcheck"}, &stdout, &stderr, envWith(map[string]string{"VH_LISTEN": addr})); code != ExitOK {
		t.Fatalf("from VH_LISTEN: code=%d err=%q", code, stderr.String())
	}
}

func TestHealthcheckExitsOneWithOneLineOtherwise(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String() + "/healthz"
	_ = ln.Close()
	for name, url := range map[string]string{"503": srv.URL + "/healthz", "refused": closed, "bad url": "::nope"} {
		code, out, errs := run("healthcheck", "--url", url)
		if code != ExitError || out != "" || !strings.HasPrefix(errs, "vh: healthcheck: ") || strings.Count(errs, "\n") != 1 {
			t.Errorf("%s: code=%d out=%q err=%q", name, code, out, errs)
		}
	}
}

func TestHealthcheckTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	if err := probe(srv.URL, 50*time.Millisecond); err == nil || time.Since(start) > HealthcheckTimeout {
		t.Fatalf("probe of a hung server = %v after %v", err, time.Since(start))
	}
}

func TestHealthcheckUsage(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--url"}, {"--url", "a", "--url", "b"}, {"--bogus"}} {
		code, out, errs := run(append([]string{"healthcheck"}, args...)...)
		if code != ExitUsage || out != "" || !strings.Contains(errs, HealthcheckUsage) {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}

func TestHealthzURL(t *testing.T) {
	for addr, want := range map[string]string{
		"":               "http://127.0.0.1:8080/healthz",
		":9000":          "http://127.0.0.1:9000/healthz",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/healthz",
		"[::]:9000":      "http://127.0.0.1:9000/healthz",
		"10.0.0.5:9000":  "http://10.0.0.5:9000/healthz",
		"[fd00::1]:9000": "http://[fd00::1]:9000/healthz",
	} {
		if got := healthzURL(addr); got != want {
			t.Errorf("healthzURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

// jsonLogger returns a JSON slog logger writing to buf.
func jsonLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

func TestAnnounceLogsTheBuildListenAndSchemaButNotTheDatabaseURL(t *testing.T) {
	url := sqliteURL(t)
	st, err := store.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var buf bytes.Buffer
	if err := prepareSchema(context.Background(), st, true, jsonLogger(&buf)); err != nil {
		t.Fatal(err)
	}
	migrations := strings.Count(buf.String(), `"msg":"migration applied"`)
	buf.Reset()
	announce(context.Background(), jsonLogger(&buf), st, ":8080")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["msg"] != "starting" || rec["listen"] != ":8080" || rec["db"] != "sqlite" || rec["schema"] != float64(migrations) ||
		rec["protocol"] != float64(ProtocolVersion()) || rec["version"] == nil || rec["schema_error"] != nil || migrations == 0 {
		t.Errorf("start line = %v after %d migrations", rec, migrations)
	}
	if strings.Contains(buf.String(), strings.TrimPrefix(url, "sqlite://")) {
		t.Errorf("start line names the database: %s", buf.String())
	}
	_ = st.Close()
	buf.Reset()
	announce(context.Background(), jsonLogger(&buf), st, ":8080")
	if !strings.Contains(buf.String(), `"schema":-1`) || !strings.Contains(buf.String(), `"schema_error":`) {
		t.Errorf("start line on a closed store = %s", buf.String())
	}
}

func TestPrepareSchemaLogsNothingWithoutMigrate(t *testing.T) {
	st, err := store.Open(sqliteURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var buf bytes.Buffer
	if err := prepareSchema(context.Background(), st, false, jsonLogger(&buf)); err != nil || buf.Len() != 0 {
		t.Fatalf("prepareSchema = %v, log %q", err, buf.String())
	}
}

func TestServeLogsMigrationsStartListenAndStop(t *testing.T) {
	code, errs := serveReadyLog(t, sqliteURL(t), true)
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	var msgs []string
	for _, line := range strings.Split(strings.TrimSpace(errs), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if m := rec["msg"].(string); len(msgs) == 0 || msgs[len(msgs)-1] != m {
			msgs = append(msgs, m)
		}
	}
	if got := strings.Join(msgs, ","); got != "migration applied,starting,listening,shutting down,stopped" {
		t.Errorf("log messages = %s", got)
	}
}
