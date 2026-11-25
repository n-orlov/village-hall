package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n-orlov/village-hall/internal/webui"
)

func uiGet(t *testing.T, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := authed(path)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	NewHandler(ok, acceptKey, nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestAssetsAreImmutableAndAnUnknownAssetIs404(t *testing.T) {
	t.Parallel()
	if !webui.Built() {
		t.Fatal("the web build is absent: run `npm run build` in web/ first")
	}
	files, err := fs.Glob(webui.Files(), "assets/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no built js file under assets/: %v %v", files, err)
	}
	rec := uiGet(t, "/"+files[0])
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s = %d, want 200", files[0], rec.Code)
	}
	if got, want := rec.Header().Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Errorf("Cache-Control of /%s = %q, want %q", files[0], got, want)
	}
	for _, p := range []string{"/assets/x.js", "/assets/", "/assets"} {
		rec := uiGet(t, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("GET %s answered an immutable Cache-Control: %q", p, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestThePageIsNoCacheWithAnETagAnd304(t *testing.T) {
	t.Parallel()
	want, err := fs.ReadFile(webui.Files(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/index.html"} {
		rec := uiGet(t, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", p, got)
		}
		if rec.Body.String() != string(want) {
			t.Errorf("GET %s body is not the embedded index.html", p)
		}
		etag := rec.Header().Get("ETag")
		if !strings.HasPrefix(etag, `"`) || len(etag) < 8 {
			t.Fatalf("GET %s ETag = %q, want a quoted validator", p, etag)
		}
		if match := uiGet(t, p, "If-None-Match", etag); match.Code != http.StatusNotModified || match.Body.Len() != 0 {
			t.Errorf("GET %s with the matching ETag = %d (%d body bytes), want 304 and no body", p, match.Code, match.Body.Len())
		}
		if other := uiGet(t, p, "If-None-Match", `"not-it"`); other.Code != http.StatusOK {
			t.Errorf("GET %s with another ETag = %d, want 200", p, other.Code)
		}
		if head := uiGet(t, p, "If-None-Match", etag); head.Header().Get("Cache-Control") != "no-cache" || head.Header().Get("ETag") != etag {
			t.Errorf("the 304 of %s lost its Cache-Control or ETag: %v", p, head.Header())
		}
	}
}

func TestAPIAnswersAreNotImmutable(t *testing.T) {
	t.Parallel()
	rec := uiGet(t, "/api/v1/meta")
	if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("GET /api/v1/meta Cache-Control = %q, must not be immutable", rec.Header().Get("Cache-Control"))
	}
}
