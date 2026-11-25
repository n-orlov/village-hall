package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// Cache policies of the embedded app. A file under /assets/ carries a content hash
// in its name, so it never changes under that name; the page that names those
// files must be asked again on every load.
const (
	immutableAssets = "public, max-age=31536000, immutable"
	revalidate      = "no-cache"
)

// compressible are the extensions `npm run build` precompresses (web/scripts/precompress):
// each has a `<name>.gz` beside it in the embedded files.
var compressible = map[string]bool{".js": true, ".css": true, ".html": true, ".svg": true, ".json": true}

// uiHandler serves the embedded app from ui. /assets/* answers an immutable
// Cache-Control (404 for a name that is not there, with no cache header); / and
// /index.html answer no-cache with an ETag over the page's bytes, and 304 to a
// matching If-None-Match. A text file is answered from its precompressed `.gz` twin
// to a client that accepts gzip, and as it is otherwise, with Vary: Accept-Encoding
// either way; the `.gz` files are not routes of their own. Every other path is
// looked up in ui as it is.
func uiHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		switch {
		case strings.HasSuffix(p, ".gz"):
			http.NotFound(w, r)
		case p == "/" || p == "/index.html":
			serveIndex(w, r, ui)
		case p == "/assets" || strings.HasPrefix(p, "/assets/"):
			if info, err := fs.Stat(ui, strings.TrimPrefix(p, "/")); err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", immutableAssets)
			serveFile(w, r, ui, files, p)
		default:
			serveFile(w, r, ui, files, p)
		}
	})
}

// serveFile answers the file at p: its gzip twin when the client accepts gzip and
// the build made one, else plain's own answer.
func serveFile(w http.ResponseWriter, r *http.Request, ui fs.FS, plain http.Handler, p string) {
	ext := path.Ext(p)
	if !compressible[ext] {
		plain.ServeHTTP(w, r)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if acceptsGzip(r) {
		if body, err := fs.ReadFile(ui, strings.TrimPrefix(p, "/")+".gz"); err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", mime.TypeByExtension(ext))
			http.ServeContent(w, r, p, time.Time{}, bytes.NewReader(body))
			return
		}
	}
	plain.ServeHTTP(w, r)
}

// acceptsGzip reports whether the request's Accept-Encoding allows gzip: the token
// gzip, or * when gzip is not named, each with a q other than 0.
func acceptsGzip(r *http.Request) bool {
	star, gz := false, false
	seen := false
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			name = strings.ToLower(strings.TrimSpace(name))
			ok := !zeroQ(params)
			switch name {
			case "gzip", "x-gzip":
				gz, seen = ok, true
			case "*":
				star = ok
			}
		}
	}
	if seen {
		return gz
	}
	return star
}

// zeroQ reports whether the parameters of one Accept-Encoding entry carry q=0.
func zeroQ(params string) bool {
	for _, param := range strings.Split(params, ";") {
		k, v, _ := strings.Cut(param, "=")
		if strings.EqualFold(strings.TrimSpace(k), "q") {
			q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return err == nil && q == 0
		}
	}
	return false
}

// serveIndex answers the app's page itself: http.FileServer would redirect
// /index.html to /, and it sets no ETag of its own. A gzip client gets the
// precompressed page under an ETag of its own (the validator names the bytes sent).
func serveIndex(w http.ResponseWriter, r *http.Request, ui fs.FS) {
	page, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(page)
	tag := hex.EncodeToString(sum[:16])
	w.Header().Add("Vary", "Accept-Encoding")
	if acceptsGzip(r) {
		if packed, err := fs.ReadFile(ui, "index.html.gz"); err == nil {
			page, tag = packed, tag+"-gzip"
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	w.Header().Set("ETag", `"`+tag+`"`)
	w.Header().Set("Cache-Control", revalidate)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
}
