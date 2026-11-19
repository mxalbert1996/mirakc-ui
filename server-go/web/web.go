// Package web serves the built client (client/dist) from an embedded FS,
// mirroring main.ts's serveStatic + SPA-fallback pair: real files with
// correct content types, and index.html for any unknown GET path so the
// client router takes over.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:static
var staticFS embed.FS

// contentTypes pins the extensions Vite emits so responses do not depend
// on the host's mime tables (the production image has none).
var contentTypes = map[string]string{
	".css":         "text/css; charset=utf-8",
	".html":        "text/html; charset=utf-8",
	".ico":         "image/x-icon",
	".js":          "text/javascript; charset=utf-8",
	".json":        "application/json",
	".mjs":         "text/javascript; charset=utf-8",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
	".webp":        "image/webp",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
}

// zeroTime suppresses Last-Modified in http.ServeContent (the Unix epoch is
// net/http's "unknown modtime" sentinel); the embedded files share no
// meaningful modification time.
var zeroTime = time.Unix(0, 0)

// Handler serves the embedded client.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embedded static dir: " + err.Error())
	}
	return HandlerFS(sub)
}

// HandlerFS serves a client tree (tests inject fstest.MapFS). GET/HEAD
// fall back to index.html for unknown paths (SPA routing); other methods
// 404, like the Deno app whose fallback route was GET-only.
func HandlerFS(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if serveFile(w, r, fsys, strings.TrimPrefix(clean, "/")) {
			return
		}
		if !serveFile(w, r, fsys, "index.html") {
			// No embedded client (static/ carries only .gitkeep): same 404
			// the Deno server gives without a built client/dist.
			http.NotFound(w, r)
		}
	})
}

// serveFile writes one file from fsys; ok is false when it does not
// exist. ".gitkeep" only exists to keep the embed pattern valid in a
// fresh clone — it must never be served.
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	if name == ".gitkeep" {
		return false
	}
	content, err := fs.ReadFile(fsys, name)
	if err != nil {
		return false
	}
	if ct, ok := contentTypes[path.Ext(name)]; ok {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, zeroTime, strings.NewReader(string(content)))
	return true
}
