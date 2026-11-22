package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// logRecords decodes the JSON lines a slog JSON handler wrote to buf.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// serveLogged runs one request through logged(h) and returns the response and
// the log records.
func serveLogged(t *testing.T, h http.HandlerFunc, target string) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("secret body"))
	req.Header.Set("Authorization", "Bearer brd_secret")
	logged(h, slog.New(slog.NewJSONHandler(&buf, nil))).ServeHTTP(rec, req)
	return rec, logRecords(t, &buf)
}

func TestLoggedLogsAFailedResponseWithoutQueryHeadersOrBody(t *testing.T) {
	rec, logs := serveLogged(t, func(w http.ResponseWriter, r *http.Request) {
		noteOpError(r, errors.New("disk full"))
		w.WriteHeader(http.StatusServiceUnavailable)
	}, "/api/v1/tasks?token=abc")
	if rec.Code != http.StatusServiceUnavailable || len(logs) != 1 {
		t.Fatalf("code %d, logs %v", rec.Code, logs)
	}
	l := logs[0]
	if l["msg"] != "request failed" || l["level"] != "ERROR" || l["method"] != "POST" || l["path"] != "/api/v1/tasks" ||
		l["status"] != float64(503) || l["error"] != "disk full" || l["duration_ms"] == nil {
		t.Errorf("log = %v", l)
	}
	for _, leak := range []string{"token=abc", "brd_secret", "secret body"} {
		if b, _ := json.Marshal(l); strings.Contains(string(b), leak) {
			t.Errorf("log carries %q: %s", leak, b)
		}
	}
}

func TestLoggedLogsNothingBelow500(t *testing.T) {
	for _, h := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusContinue)
			w.WriteHeader(http.StatusOK)
		},
		func(http.ResponseWriter, *http.Request) {},
	} {
		if _, logs := serveLogged(t, h, "/x"); len(logs) != 0 {
			t.Errorf("logs = %v", logs)
		}
	}
}

func TestLoggedOmitsTheErrorWhenNoOperationFailed(t *testing.T) {
	_, logs := serveLogged(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, "/x")
	if len(logs) != 1 || logs[0]["error"] != nil {
		t.Fatalf("logs = %v", logs)
	}
}

func TestLoggedAnswers500ToAPanicBeforeAnyWrite(t *testing.T) {
	rec, logs := serveLogged(t, func(http.ResponseWriter, *http.Request) { panic("boom") }, "/x")
	if rec.Code != http.StatusInternalServerError || len(logs) != 2 {
		t.Fatalf("code %d, logs %v", rec.Code, logs)
	}
	if logs[0]["msg"] != "panic recovered" || logs[0]["panic"] != "boom" || !strings.Contains(logs[0]["stack"].(string), "goroutine") {
		t.Errorf("panic log = %v", logs[0])
	}
	if logs[1]["msg"] != "request failed" || logs[1]["status"] != float64(500) {
		t.Errorf("request log = %v", logs[1])
	}
}

// recoverPanic runs f and returns what it panicked with.
func recoverPanic(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

func TestLoggedAbortsAResponseAlreadyUnderWay(t *testing.T) {
	var buf bytes.Buffer
	h := logged(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}), slog.New(slog.NewJSONHandler(&buf, nil)))
	got := recoverPanic(func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil)) })
	if got != http.ErrAbortHandler {
		t.Fatalf("panic = %v, want http.ErrAbortHandler", got)
	}
	if logs := logRecords(t, &buf); len(logs) != 1 || logs[0]["msg"] != "panic recovered" {
		t.Errorf("logs = %v", logs)
	}
}

func TestLoggedPassesErrAbortHandlerOnUnlogged(t *testing.T) {
	var buf bytes.Buffer
	h := logged(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }),
		slog.New(slog.NewJSONHandler(&buf, nil)))
	got := recoverPanic(func() { h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil)) })
	if got != http.ErrAbortHandler || buf.Len() != 0 {
		t.Fatalf("panic = %v, log %q", got, buf.String())
	}
}

func TestStatusWriterUnwrapsAndNoteOpErrorIgnoresAnUnloggedRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	if (&statusWriter{ResponseWriter: rec}).Unwrap() != rec {
		t.Error("Unwrap did not return the wrapped writer")
	}
	noteOpError(httptest.NewRequest(http.MethodGet, "/x", nil), errors.New("x"))
}

func TestStatusWriterRecordsTheFinalStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		write func(w http.ResponseWriter)
		want  int
	}{
		"informational then 200": {func(w http.ResponseWriter) { w.WriteHeader(http.StatusContinue); w.WriteHeader(http.StatusOK) }, http.StatusOK},
		"first header wins": {func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.WriteHeader(http.StatusOK)
		}, http.StatusServiceUnavailable},
		"write without header": {func(w http.ResponseWriter) { _, _ = w.Write([]byte("x")) }, http.StatusOK},
		"header then write": {func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("x"))
		}, http.StatusInternalServerError},
		"nothing": {func(http.ResponseWriter) {}, 0},
	} {
		sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
		tc.write(sw)
		if sw.status != tc.want {
			t.Errorf("%s: status %d, want %d", name, sw.status, tc.want)
		}
	}
}

func TestLoggedLogsFrom500NotBelow(t *testing.T) {
	for status, want := range map[int]int{499: 0, 500: 1} {
		_, logs := serveLogged(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }, "/x")
		if len(logs) != want {
			t.Errorf("status %d: %d log lines, want %d", status, len(logs), want)
		}
	}
}
