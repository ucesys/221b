// Package repotest fakes a Helm repository and an OCI registry (Phase
// 12, wave 5, HR-11) for the tests of the packages that fetch charts
// from them (apps, api, watson). Nothing here reaches a third party:
// each fake is an httptest server on the loopback, which
// apps.AllowLocalRepos lets the console reach.
package repotest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// ChartArchive builds a chart's .tgz, as helm package does: the files
// under a directory of the chart's name. files maps a path in the chart
// (templates/cm.yaml) to its text; Chart.yaml is written when missing.
func ChartArchive(name, version string, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	all := map[string]string{"Chart.yaml": fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\nappVersion: \"1.0\"\n", name, version)}
	for k, v := range files {
		all[k] = v
	}
	for p, text := range all {
		_ = tw.WriteHeader(&tar.Header{Name: name + "/" + p, Mode: 0o644, Size: int64(len(text)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(text))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

// The media types of a Helm chart in an OCI registry.
const (
	ociManifest  = "application/vnd.oci.image.manifest.v1+json"
	ociChartType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// Registry serves charts as a Helm repository (index.yaml and the
// archives) and as an OCI registry (/v2/, with a token service), over
// TLS; with User and Password set, both ask for them.
type Registry struct {
	*httptest.Server
	// User and Password, when set, are what a request must sign in with:
	// basic for the Helm repository, a token from /token for the
	// registry.
	User, Password string

	mu     sync.Mutex
	charts map[string]map[string][]byte
	// Requests counts what was asked, by path.
	Requests map[string]int
	// Auth are the Authorization headers seen, by path.
	Auth map[string]string
}

// New starts a fake; close it when done.
func New() *Registry {
	f := &Registry{charts: map[string]map[string][]byte{}, Requests: map[string]int{}, Auth: map[string]string{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	return f
}

// Add serves a chart's version.
func (f *Registry) Add(name, version string, files map[string]string) []byte {
	b := ChartArchive(name, version, files)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.charts[name] == nil {
		f.charts[name] = map[string][]byte{}
	}
	f.charts[name][version] = b
	return b
}

// Host is the fake's host:port, as oci:// addresses name it.
func (f *Registry) Host() string { return strings.TrimPrefix(f.URL, "https://") }

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *Registry) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.Requests[r.URL.Path]++
	f.Auth[r.URL.Path] = r.Header.Get("Authorization")
	f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/token":
		u, pw, ok := r.BasicAuth()
		if f.User != "" && (!ok || u != f.User || pw != f.Password) {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "registry-token-" + r.URL.Query().Get("scope")})
		return
	case strings.HasPrefix(p, "/v2/"):
		if f.User != "" && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer registry-token-") {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake"`, f.URL))
			http.Error(w, "sign in", http.StatusUnauthorized)
			return
		}
		f.serveOCI(w, p)
		return
	}
	if f.User != "" {
		u, pw, ok := r.BasicAuth()
		if !ok || u != f.User || pw != f.Password {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == "/charts/index.yaml" {
		var b strings.Builder
		b.WriteString("apiVersion: v1\nentries:\n")
		for name, versions := range f.charts {
			fmt.Fprintf(&b, "  %s:\n", name)
			for v, arc := range versions {
				fmt.Fprintf(&b, "  - name: %s\n    version: %s\n    appVersion: \"1.0\"\n    description: The %s chart\n    created: \"2026-10-01T10:00:00Z\"\n    digest: %s\n    urls:\n    - archives/%s-%s.tgz\n", name, v, name, strings.TrimPrefix(digestOf(arc), "sha256:"), name, v)
			}
		}
		_, _ = w.Write([]byte(b.String()))
		return
	}
	for name, versions := range f.charts {
		for v, arc := range versions {
			if p == "/charts/archives/"+name+"-"+v+".tgz" {
				_, _ = w.Write(arc)
				return
			}
		}
	}
	http.NotFound(w, nil)
}

// serveOCI answers /v2/: the ping, a chart's tags, its manifest and its
// layer, under any path whose last segment is the chart.
func (f *Registry) serveOCI(w http.ResponseWriter, p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == "/v2/" {
		_, _ = w.Write([]byte("{}"))
		return
	}
	rest := strings.TrimPrefix(p, "/v2/")
	for name, versions := range f.charts {
		for _, kind := range []string{"/tags/list", "/manifests/", "/blobs/"} {
			i := strings.Index(rest, kind)
			if i < 0 || !strings.HasSuffix(rest[:i], name) {
				continue
			}
			arg := rest[i+len(kind):]
			switch kind {
			case "/tags/list":
				var tags []string
				for v := range versions {
					tags = append(tags, strings.ReplaceAll(v, "+", "_"))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"name": rest[:i], "tags": tags})
				return
			case "/manifests/":
				arc, ok := versions[strings.ReplaceAll(arg, "_", "+")]
				if !ok {
					http.NotFound(w, nil)
					return
				}
				cfg := []byte(`{"name":"` + name + `"}`)
				m := map[string]any{"schemaVersion": 2, "mediaType": ociManifest,
					"config": map[string]any{"mediaType": "application/vnd.cncf.helm.config.v1+json", "digest": digestOf(cfg), "size": len(cfg)},
					"layers": []map[string]any{{"mediaType": ociChartType, "digest": digestOf(arc), "size": len(arc)}}}
				w.Header().Set("Content-Type", ociManifest)
				_ = json.NewEncoder(w).Encode(m)
				return
			case "/blobs/":
				for _, arc := range versions {
					if digestOf(arc) == arg {
						_, _ = w.Write(arc)
						return
					}
				}
			}
		}
	}
	http.NotFound(w, nil)
}

// BasicAuth is a header's basic credentials, for checks.
func BasicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}
