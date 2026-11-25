package server

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n-orlov/village-hall/api"
	"github.com/n-orlov/village-hall/internal/idempotency"
	"github.com/n-orlov/village-hall/internal/swaggerui"
)

// readyTimeout bounds one readiness check.
const readyTimeout = 3 * time.Second

// strictOperations adapts ssi to the generated ServerInterface. A request the
// generated code cannot read, and an operation that fails, answer with an RFC
// 9457 problem document, like every other error.
func strictOperations(ssi StrictServerInterface, middlewares ...StrictMiddlewareFunc) ServerInterface {
	return NewStrictHandlerWithOptions(ssi, append([]StrictMiddlewareFunc{withRequest, bindBeforeEncoding}, middlewares...), StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestUnreadable,
		ResponseErrorHandlerFunc: responseFailed,
	})
}

func requestUnreadable(w http.ResponseWriter, _ *http.Request, _ error) {
	writeProblem(w, http.StatusBadRequest, "the request could not be read")
}

// parameterUnreadable answers a path or query parameter the generated binder
// could not read, such as limit=abc.
func parameterUnreadable(w http.ResponseWriter, _ *http.Request, err error) {
	writeProblem(w, http.StatusBadRequest, err.Error())
}

func responseFailed(w http.ResponseWriter, r *http.Request, err error) {
	noteOpError(r, err)
	writeProblem(w, http.StatusInternalServerError, "the request could not be answered")
}

// operations implements the generated strict server interface.
type operations struct {
	loginOps
	keyOps
	initiativeOps
	taskOps
	eventOps
	notifyOps
	timelineOps
	actorOps
	leaseOps
	nowOps
	startOps
	finishOps
	pingOps
	catchupOps
	askOps
	noteOps
	attentionOps
	cursorOps
	metaOps
	streamOps
	// idem stores the answers of requests that carry an Idempotency-Key; nil means
	// none are stored.
	idem *idempotency.Store
	// writeTimeout is the http.Server write timeout; 0 means none.
	writeTimeout time.Duration
	ready        Readiness
	// ui is the app the server serves at /; nil means the embedded build.
	ui fs.FS
	// logger is where the server logs (WithLogger); it discards by default.
	logger *slog.Logger
}

var _ StrictServerInterface = operations{}

func (operations) GetHealthz(context.Context, GetHealthzRequestObject) (GetHealthzResponseObject, error) {
	return GetHealthz200TextResponse("ok\n"), nil
}

func (o operations) GetReadyz(ctx context.Context, _ GetReadyzRequestObject) (GetReadyzResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	if err := o.ready(ctx); err != nil {
		return GetReadyz503TextResponse("not ready\n"), nil
	}
	return GetReadyz200TextResponse("ok\n"), nil
}

func (operations) GetOpenapiYAML(context.Context, GetOpenapiYAMLRequestObject) (GetOpenapiYAMLResponseObject, error) {
	doc := api.YAML()
	return GetOpenapiYAML200ApplicationyamlResponse{Body: bytes.NewReader(doc), ContentLength: int64(len(doc))}, nil
}

func (operations) GetOpenapiJSON(context.Context, GetOpenapiJSONRequestObject) (GetOpenapiJSONResponseObject, error) {
	doc, err := api.JSON()
	if err != nil {
		return nil, err
	}
	return GetOpenapiJSON200JSONResponse(doc), nil
}

func (operations) GetDocs(context.Context, GetDocsRequestObject) (GetDocsResponseObject, error) {
	return GetDocs200TexthtmlResponse{Body: strings.NewReader(swaggerui.Page), ContentLength: int64(len(swaggerui.Page))}, nil
}

func (operations) GetDocsAsset(_ context.Context, req GetDocsAssetRequestObject) (GetDocsAssetResponseObject, error) {
	body, contentType, ok := swaggerui.Asset(req.Name)
	if !ok {
		return GetDocsAsset404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse{
			Type: problemType, Title: http.StatusText(http.StatusNotFound), Status: http.StatusNotFound,
			Detail: ptr("there is no such documentation asset"),
			Next:   ptr("Open /api/docs and use the asset names that page links."),
		}}, nil
	}
	r, n := bytes.NewReader(body), int64(len(body))
	if contentType == "text/css" {
		return GetDocsAsset200TextcssResponse{Body: r, ContentLength: n}, nil
	}
	return GetDocsAsset200TextjavascriptResponse{Body: r, ContentLength: n}, nil
}

func ptr[T any](v T) *T { return &v }
