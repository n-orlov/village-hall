package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/store"
)

// IdempotencyKeyHeader carries the key of a safe retry (SPEC 6.1), and
// ReplayedHeader marks an answer that is a stored replay.
const (
	IdempotencyKeyHeader = "Idempotency-Key"
	ReplayedHeader       = "Idempotent-Replayed"
)

// maxKeyLength is the longest Idempotency-Key, and maxIdempotentBody the largest
// request body the server reads to fingerprint a request that carries one.
const (
	maxKeyLength      = 255
	maxIdempotentBody = 1 << 20
)

// WithIdempotency serves the Idempotency-Key of every POST under /api/v1 from
// st; without it a POST that carries the header answers 503.
func WithIdempotency(st *idempotency.Store) Option {
	return func(o *operations) { o.idem = st }
}

// validKey reports whether the header values name one key of 1 to 255 printable
// ASCII characters, and returns it.
func validKey(values []string) (string, bool) {
	if len(values) != 1 || values[0] == "" || len(values[0]) > maxKeyLength {
		return "", false
	}
	for _, c := range []byte(values[0]) {
		if c < ' ' || c > '~' {
			return "", false
		}
	}
	return values[0], true
}

// writesUnderAPI reports whether r is a POST under /api/v1, the requests an
// Idempotency-Key applies to.
func writesUnderAPI(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/")
}

// idempotent wraps next, which must sit inside authentication so that the
// request has its identity. A POST under /api/v1 without an Idempotency-Key
// passes through unchanged. One with a key is answered from the stored answer
// when the same request (method, target, actor and body) came with the key
// before and was answered within 24 hours; the key of a different request is
// 422, the key of a request still running is 409, and an invalid key is 400.
// A new key runs the request and stores its answer in the same transaction as
// its writes, unless the server failed (5xx) or the answer could not be stored,
// when nothing is written and the key is released so the retry runs the
// request again.
func idempotent(next http.Handler, st *idempotency.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, present := r.Header[IdempotencyKeyHeader]
		who, authed := identityFrom(r.Context())
		if !present || !writesUnderAPI(r) || !authed {
			next.ServeHTTP(w, r)
			return
		}
		key, ok := validKey(values)
		switch {
		case !ok:
			writeProblemNext(w, http.StatusBadRequest, "the Idempotency-Key header is not one key of 1 to 255 printable ASCII characters",
				"Send one Idempotency-Key of 1 to 255 printable ASCII characters, such as a random UUID, or none.")
		case st == nil:
			writeProblem(w, http.StatusServiceUnavailable, "idempotency is not available")
		default:
			serveIdempotent(next, st, who.Principal, who.Actor, key, w, r)
		}
	})
}

// serveIdempotent claims the key for r and acts on what the claim found.
func serveIdempotent(next http.Handler, st *idempotency.Store, principal, actor, key string, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIdempotentBody))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeProblem(w, http.StatusRequestEntityTooLarge, "the request body is too large to send with an Idempotency-Key")
		return
	case err != nil:
		requestUnreadable(w, r, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	req := idempotency.Request{Principal: principal, Key: key, Fingerprint: idempotency.Fingerprint(r.Method, r.URL.RequestURI(), actor, body)}
	outcome, answer, err := st.Claim(r.Context(), req)
	switch {
	case err != nil:
		responseFailed(w, r, err)
	case outcome == idempotency.Mismatch:
		writeProblemNext(w, http.StatusUnprocessableEntity, "this Idempotency-Key was first used with a different request",
			"Send a new Idempotency-Key for a different request, or send the original request again.")
	case outcome == idempotency.InFlight:
		writeProblemNext(w, http.StatusConflict, "the request with this Idempotency-Key is still running",
			"Wait a moment, then send the same request with the same Idempotency-Key again.")
	case outcome == idempotency.Replay:
		w.Header().Set(ReplayedHeader, "true")
		deliver(w, answer.Status, answer.ContentType, answer.Body)
	default:
		runClaimed(next, st, req, w, r)
	}
}

// runClaimed runs the request that holds the key inside a unit of work (SPEC
// 6.1): everything the request writes and the answer stored for its key commit
// in one transaction, so a write is never committed without its answer. The
// response is held back until then, so a retry sent on receiving it finds the
// answer and not a running request. When the answer cannot be stored the unit
// rolls back, the request has written nothing, and the key is released: a
// success is then answered 503, so the client sends the same request again and
// it runs once. A 5xx or a panic rolls back and releases as well.
func runClaimed(next http.Handler, st *idempotency.Store, req idempotency.Request, w http.ResponseWriter, r *http.Request) {
	ctx := context.WithoutCancel(r.Context())
	unitCtx, unit, err := st.Store.BeginUnit(r.Context())
	if err != nil {
		_ = st.Release(ctx, req)
		responseFailed(w, r, err)
		return
	}
	defer func() {
		if err := unit.Rollback(); !errors.Is(err, sql.ErrTxDone) {
			// Still open: the handler panicked before the answer was settled.
			_ = st.Release(ctx, req)
		}
	}()
	held := &heldResponse{header: http.Header{}}
	next.ServeHTTP(held, r.WithContext(unitCtx))
	answer := idempotency.Answer{Status: held.code(), ContentType: held.header.Get("Content-Type"), Body: held.body.Bytes()}
	if answer.Status >= http.StatusInternalServerError || !settle(unitCtx, ctx, st, unit, req, answer) {
		_ = unit.Rollback()
		_ = st.Release(ctx, req)
		if answer.Status < http.StatusBadRequest {
			writeProblemNext(w, http.StatusServiceUnavailable, "the request was not carried out: its answer could not be stored",
				"Send the same request with the same Idempotency-Key again.")
			return
		}
	}
	deliver(w, answer.Status, answer.ContentType, answer.Body)
}

// settle stores the answer in the unit and commits the unit with it, and
// reports whether the answer is stored. A refusal writes nothing, so when it
// cannot commit with the unit (on Postgres a refused statement can leave the
// unit unable to commit) its answer is stored on its own instead.
func settle(unitCtx, ctx context.Context, st *idempotency.Store, unit *store.Unit, req idempotency.Request, answer idempotency.Answer) bool {
	err := st.Complete(unitCtx, req, answer)
	if err == nil {
		err = unit.Commit()
	}
	if err == nil {
		return true
	}
	_ = unit.Rollback()
	return answer.Status >= http.StatusBadRequest && st.Complete(ctx, req, answer) == nil
}

// deliver writes a response of the status, content type and body.
func deliver(w http.ResponseWriter, status int, contentType string, body []byte) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// heldResponse collects a response in memory.
type heldResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *heldResponse) Header() http.Header { return h.header }

func (h *heldResponse) WriteHeader(status int) {
	if h.status == 0 {
		h.status = status
	}
}

func (h *heldResponse) Write(b []byte) (int, error) {
	h.WriteHeader(http.StatusOK)
	return h.body.Write(b)
}

// code is the status the handler answered, 200 when it wrote nothing.
func (h *heldResponse) code() int {
	if h.status == 0 {
		return http.StatusOK
	}
	return h.status
}
