package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
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

// uiHandler serves the embedded app from ui. /assets/* answers an immutable
// Cache-Control (404 for a name that is not there, with no cache header); / and
// /index.html answer no-cache with an ETag over the page's bytes, and 304 to a
// matching If-None-Match. Every other path is looked up in ui as it is.
func uiHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := path.Clean(r.URL.Path); {
		case p == "/" || p == "/index.html":
			serveIndex(w, r, ui)
		case p == "/assets" || strings.HasPrefix(p, "/assets/"):
			if info, err := fs.Stat(ui, strings.TrimPrefix(p, "/")); err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", immutableAssets)
			files.ServeHTTP(w, r)
		default:
			files.ServeHTTP(w, r)
		}
	})
}

// serveIndex answers the app's page itself: http.FileServer would redirect
// /index.html to /, and it sets no ETag of its own.
func serveIndex(w http.ResponseWriter, r *http.Request, ui fs.FS) {
	page, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(page)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	w.Header().Set("Cache-Control", revalidate)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(page))
}
