package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/n-orlov/village-hall/internal/webui"
)

func gunzipped(t *testing.T, name string, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: body is not gzip: %v", name, err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("%s: gunzip: %v", name, err)
	}
	return out
}

func builtFile(t *testing.T, glob string) string {
	t.Helper()
	if !webui.Built() {
		t.Fatal("the web build is absent: run `npm run build` in web/ first")
	}
	files, err := fs.Glob(webui.Files(), glob)
	if err != nil || len(files) != 1 {
		t.Fatalf("want exactly one built file matching %s: %v %v", glob, files, err)
	}
	return "/" + files[0]
}

func TestTextAssetsAreGzipEncodedToGzipClientsAndIdentityOtherwise(t *testing.T) {
	t.Parallel()
	paths := []string{
		builtFile(t, "assets/*.js"), builtFile(t, "assets/*.css"),
		"/", "/index.html", "/favicon.svg", "/icon.svg", "/manifest.json",
	}
	for _, p := range paths {
		plain := uiGet(t, p)
		if plain.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, plain.Code)
		}
		if got := plain.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("GET %s without Accept-Encoding: Content-Encoding = %q, want none", p, got)
		}
		if got := plain.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("GET %s without Accept-Encoding: Vary = %q, want Accept-Encoding", p, got)
		}
		zipped := uiGet(t, p, "Accept-Encoding", "gzip")
		if zipped.Code != http.StatusOK {
			t.Fatalf("GET %s with gzip = %d, want 200", p, zipped.Code)
		}
		if got := zipped.Header().Get("Content-Encoding"); got != "gzip" {
			t.Errorf("GET %s with gzip: Content-Encoding = %q, want gzip", p, got)
		}
		if got := zipped.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("GET %s with gzip: Vary = %q, want Accept-Encoding", p, got)
		}
		if got, want := zipped.Header().Get("Content-Type"), plain.Header().Get("Content-Type"); got != want || got == "" {
			t.Errorf("GET %s: Content-Type with gzip %q, without %q", p, got, want)
		}
		if !bytes.Equal(gunzipped(t, p, zipped.Body.Bytes()), plain.Body.Bytes()) {
			t.Errorf("GET %s: the gunzipped body is not the identity body", p)
		}
		if zipped.Body.Len() >= plain.Body.Len() && plain.Body.Len() > 1000 {
			t.Errorf("GET %s: gzip body %d bytes, identity %d", p, zipped.Body.Len(), plain.Body.Len())
		}
	}
}

func TestTheAPIIsNotGzipEncoded(t *testing.T) {
	t.Parallel()
	rec := uiGet(t, "/api/v1/meta", "Accept-Encoding", "gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/meta = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("GET /api/v1/meta Content-Encoding = %q, want none", got)
	}
}

func TestGzipCaching(t *testing.T) {
	t.Parallel()
	js := builtFile(t, "assets/*.js")
	if got := uiGet(t, js, "Accept-Encoding", "gzip").Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("the gzip asset answer lost its Cache-Control: %q", got)
	}
	plain, zipped := uiGet(t, "/"), uiGet(t, "/", "Accept-Encoding", "gzip")
	if plain.Header().Get("ETag") == zipped.Header().Get("ETag") {
		t.Errorf("the identity and gzip pages share the ETag %q", plain.Header().Get("ETag"))
	}
	etag := zipped.Header().Get("ETag")
	if m := uiGet(t, "/", "Accept-Encoding", "gzip", "If-None-Match", etag); m.Code != http.StatusNotModified || m.Body.Len() != 0 {
		t.Errorf("the gzip page with its own ETag = %d (%d bytes), want 304 and no body", m.Code, m.Body.Len())
	}
	if m := uiGet(t, "/", "If-None-Match", etag); m.Code != http.StatusOK {
		t.Errorf("the identity page with the gzip ETag = %d, want 200", m.Code)
	}
}

func TestAGzipFileIsNotARouteOfItsOwn(t *testing.T) {
	t.Parallel()
	for _, p := range []string{builtFile(t, "assets/*.js") + ".gz", "/index.html.gz", "/manifest.json.gz"} {
		if rec := uiGet(t, p); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
}

func TestAcceptEncodingIsReadByItsTokens(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		header []string
		want   bool
	}{
		{nil, false},
		{[]string{"gzip"}, true},
		{[]string{"GZIP"}, true},
		{[]string{"deflate, gzip;q=0.5, br"}, true},
		{[]string{"deflate", "gzip"}, true},
		{[]string{"identity"}, false},
		{[]string{"br, deflate"}, false},
		{[]string{"gzip;q=0"}, false},
		{[]string{"gzip; q=0.0"}, false},
		{[]string{"gzip;q=0.001"}, true},
		{[]string{"*"}, true},
		{[]string{"*;q=0"}, false},
		{[]string{"*, gzip;q=0"}, false},
		{[]string{"x-gzip"}, true},
		{[]string{"gzip;q=bogus"}, true},
		{[]string{"gzipx"}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, h := range c.header {
			r.Header.Add("Accept-Encoding", h)
		}
		if got := acceptsGzip(r); got != c.want {
			t.Errorf("Accept-Encoding %q: acceptsGzip = %v, want %v", c.header, got, c.want)
		}
	}
}

// A tree with no precompressed twins (an unbuilt checkout, a build run without the
// precompress step) is served as it is, still naming the Vary.
func TestAFileWithoutAGzipTwinIsServedIdentity(t *testing.T) {
	t.Parallel()
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<html>page</html>")},
		"assets/a.js":   {Data: []byte("one()")},
		"logo.png":      {Data: []byte("png")},
		"manifest.json": {Data: []byte("{}")},
	}
	h := uiHandler(ui)
	for _, p := range []string{"/", "/assets/a.js", "/manifest.json"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" || rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("GET %s: %d, Content-Encoding %q, Vary %q", p, rec.Code, rec.Header().Get("Content-Encoding"), rec.Header().Get("Vary"))
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logo.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Vary") != "" || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("a PNG is never encoded: Vary %q, Content-Encoding %q", rec.Header().Get("Vary"), rec.Header().Get("Content-Encoding"))
	}
}
