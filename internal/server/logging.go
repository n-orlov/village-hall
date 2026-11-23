package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// WithLogger has the server log to l (SPEC §8): the listen address once it
// listens, the start of a shutdown, every response with a status of 500 or more
// (method, path, status, duration and, when an operation failed, its error), a
// recovered panic, and the http.Server's own errors. It never logs a request
// line below 500, a query string, a header, a cookie or a body. Without it the
// server logs nothing.
func WithLogger(l *slog.Logger) Option {
	return func(o *operations) { o.logger = l }
}

// discardLogger is the logger of a handler built without WithLogger.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// failureKey is the context key of the request's failure, which responseFailed
// fills in for the log line.
type failureKey struct{}

// opError holds the error an operation returned for the request's 500.
type opError struct{ err error }

// noteOpError records err as the cause of r's failed response, when r passed
// through logged.
func noteOpError(r *http.Request, err error) {
	if f, ok := r.Context().Value(failureKey{}).(*opError); ok {
		f.err = err
	}
}

// logged wraps h so that a response with status 500 or more is logged, and a
// panic in h is recovered and logged with its stack. A panic before anything
// was written is answered 500; after a write it aborts the response
// (http.ErrAbortHandler), so a truncated answer never looks complete, and an
// http.ErrAbortHandler panic is passed on unlogged.
func logged(h http.Handler, l *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := &opError{}
		sw := &statusWriter{ResponseWriter: w, start: time.Now()}
		r = r.WithContext(context.WithValue(r.Context(), failureKey{}, f))
		defer func() { sw.done(l, r, f, recover()) }()
		h.ServeHTTP(sw, r)
	})
}

// statusWriter records the status of the response passing through it.
type statusWriter struct {
	http.ResponseWriter
	start  time.Time
	status int
}

func (s *statusWriter) WriteHeader(status int) {
	if s.status == 0 && status >= 200 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// done logs the request once it is answered: recovered is what recover()
// returned in the deferred call (nil without a panic). A panic is logged
// even when the client has gone.
func (s *statusWriter) done(l *slog.Logger, r *http.Request, f *opError, recovered any) {
	if recovered != nil {
		s.recovered(l, r, recovered)
	}
	// A request whose client went away (its context is canceled) fails with
	// "context canceled" and an undelivered 500: that is the client's doing,
	// not a server failure, so it logs nothing.
	if s.status < http.StatusInternalServerError || r.Context().Err() != nil {
		return
	}
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", s.status,
		"duration_ms", time.Since(s.start).Milliseconds()}
	if f.err != nil {
		attrs = append(attrs, "error", f.err.Error())
	}
	l.Error("request failed", attrs...)
}

// recovered logs a panic and answers 500, or aborts a response already under
// way. http.ErrAbortHandler is re-raised unlogged: it is how a handler asks
// net/http to drop the connection.
func (s *statusWriter) recovered(l *slog.Logger, r *http.Request, v any) {
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	l.Error("panic recovered", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	if s.status != 0 {
		panic(http.ErrAbortHandler)
	}
	s.WriteHeader(http.StatusInternalServerError)
}
