package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func serve(t *testing.T, h http.Handler, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	resp := rec.Result()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestServesTheApp(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>221B</title>")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	})
	resp, body := serve(t, h, "/")
	if body != "<!doctype html><title>221B</title>" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("index: %q %v", body, resp.Header)
	}
	resp, _ = serve(t, h, "/assets/app-abc123.js")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = serve(t, h, "/favicon.svg")
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("favicon cache %q", resp.Header.Get("Cache-Control"))
	}
	// Paths inside the app get the page; missing files get a 404.
	if _, body := serve(t, h, "/nodes/worker-1"); body != "<!doctype html><title>221B</title>" {
		t.Fatalf("deep link got %q", body)
	}
	if resp, _ := serve(t, h, "/assets/missing.js"); resp.StatusCode != 404 {
		t.Fatalf("missing asset %d", resp.StatusCode)
	}
	if resp, _ := serve(t, h, "/../../etc/passwd"); resp.StatusCode != 200 {
		t.Fatalf("traversal attempt should get the page, got %d", resp.StatusCode)
	}
}

func TestWithoutABuild(t *testing.T) {
	h := handler(fstest.MapFS{".gitkeep": {Data: nil}})
	resp, body := serve(t, h, "/")
	if resp.StatusCode != http.StatusServiceUnavailable || body == "" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}
