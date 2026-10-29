package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestThePageCarriesAStyleNonceTheCSPAllows(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("<html><head><title>x</title></head><body></body></html>")}}
	h := handler(files)
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self'")
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	i := strings.Index(body, `<meta name="csp-nonce" content="`)
	if i < 0 {
		t.Fatalf("no nonce meta in %q", body)
	}
	nonce := body[i+len(`<meta name="csp-nonce" content="`):]
	nonce = nonce[:strings.Index(nonce, `"`)]
	if len(nonce) < 20 {
		t.Errorf("short nonce %q", nonce)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self' 'nonce-"+nonce+"'") || !strings.Contains(csp, "img-src 'self'") {
		t.Errorf("CSP %q lacks the nonce", csp)
	}
	// Two loads, two nonces.
	rec2 := httptest.NewRecorder()
	rec2.Header().Set("Content-Security-Policy", "style-src 'self'")
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/", nil))
	if rec2.Body.String() == body {
		t.Error("the same nonce twice")
	}
	// Without a CSP to extend, the page is served as built.
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rec3.Body.String(), "csp-nonce") {
		t.Error("a nonce without a CSP")
	}
}
