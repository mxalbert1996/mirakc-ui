package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func clientFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>ui</title>")},
		"assets/app.js":        {Data: []byte("console.log(1)")},
		"assets/style.css":     {Data: []byte("body{}")},
		"assets/font.woff2":    {Data: []byte{0x77, 0x4f, 0x32}},
		"assets/logo.webp":     {Data: []byte{0x00}},
		"data.json":            {Data: []byte(`{"a":1}`)},
		"manifest.webmanifest": {Data: []byte("{}")},
		".gitkeep":             {Data: []byte("")},
	}
}

func serve(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	h := HandlerFS(clientFS())
	req := httptest.NewRequest(method, target, nil)
	// httptest.NewRequest keeps raw paths; simulate the router-cleaned form
	// only where the client would actually send it.
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func TestContentTypes(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/assets/app.js", "text/javascript; charset=utf-8"},
		{"/assets/style.css", "text/css; charset=utf-8"},
		{"/assets/font.woff2", "font/woff2"},
		{"/assets/logo.webp", "image/webp"},
		{"/data.json", "application/json"},
		{"/manifest.webmanifest", "application/manifest+json"},
		{"/index.html", "text/html; charset=utf-8"},
	} {
		res := serve(t, "GET", tc.path)
		if res.Code != 200 {
			t.Errorf("GET %s = %d", tc.path, res.Code)
			continue
		}
		if got := res.Header().Get("Content-Type"); got != tc.want {
			t.Errorf("GET %s content-type = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSPAFallback(t *testing.T) {
	for _, p := range []string{"/", "/settings/live-comments", "/no/such/file.js", "/assets"} {
		res := serve(t, "GET", p)
		if res.Code != 200 || !strings.Contains(res.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s = %d %q, want SPA index", p, res.Code, res.Header().Get("Content-Type"))
		}
	}
}

func TestNonGetHeadNotFound(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		res := serve(t, m, "/anything")
		if res.Code != 404 {
			t.Errorf("%s /anything = %d, want 404", m, res.Code)
		}
	}
	res := serve(t, "HEAD", "/")
	if res.Code != 200 {
		t.Errorf("HEAD / = %d, want 200", res.Code)
	}
}

func TestGitkeepNeverServed(t *testing.T) {
	res := serve(t, "GET", "/.gitkeep")
	if res.Code != 200 || !strings.Contains(res.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET /.gitkeep = %d %q, want the SPA fallback", res.Code, res.Header().Get("Content-Type"))
	}
}

func TestTraversalProbesStayInsideFS(t *testing.T) {
	for _, p := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/..%2f..%2fetc%2fpasswd", "/assets/../../secret"} {
		res := serve(t, "GET", p)
		if res.Code != 200 || !strings.Contains(res.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %q = %d %q — escaped the tree or errored", p, res.Code, res.Header().Get("Content-Type"))
		}
		if strings.Contains(res.Body.String(), "secret") {
			t.Errorf("GET %q leaked file content", p)
		}
	}
}

func TestNoLastModified(t *testing.T) {
	res := serve(t, "GET", "/assets/app.js")
	if lm := res.Header().Get("Last-Modified"); lm != "" {
		t.Errorf("asset response carries Last-Modified %q", lm)
	}
}

func TestEmptyEmbedFourOhFours(t *testing.T) {
	// A fresh clone embeds only .gitkeep: every path 404s, exactly like
	// the Deno server without a built client/dist.
	h := HandlerFS(fstest.MapFS{".gitkeep": {Data: nil}})
	for _, p := range []string{"/", "/index.html", "/deep/route"} {
		res := httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest("GET", p, nil))
		if res.Code != 404 {
			t.Errorf("GET %s on empty embed = %d, want 404", p, res.Code)
		}
	}
}
