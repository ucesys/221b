package api

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/repotest"
	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Trust this certificate (0.12.1, CR-15 to CR-19) ---

// A SuperAdmin trusts the certificate a credential's Test showed, and
// forgets it; others may not; the page cannot set a pin itself; both are
// audited with the fingerprint.
func TestTrustingACertificateThroughTheAPI(t *testing.T) {
	creds := credentials.New(credentials.Options{HTTP: &http.Client{Transport: &http.Transport{}}, Dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext})
	srv, c, _, auditOut, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { o.Credentials = creds })
	cert := certpintest.SelfSigned(t)
	site := certpintest.Server(t, cert, nil)
	fp := certpin.Fingerprint(cert.Leaf.Raw)
	base := srv.URL + "/api/v1/credentials"
	// A pin sent with the credential is not taken.
	resp, got := send(t, c, http.MethodPost, base, `{"name":"intranet","kind":"token","endpoint":"`+site.URL+`","pinnedCert":{"sha256":"`+fp+`"},"secret":{"token":"tok-123456"}}`)
	if resp.StatusCode != http.StatusCreated || strings.Contains(got, "pinnedCert") {
		t.Fatalf("create: %s %s", resp.Status, got)
	}
	id := between(got, `"id":"`, `"`)
	resp, got = send(t, c, http.MethodPost, base+"/"+id+"/test", `{}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(got, `"ok":false`) || !strings.Contains(got, `"sha256":"`+fp+`"`) || !strings.Contains(got, `"trustable":true`) {
		t.Fatalf("test: %s %s", resp.Status, got)
	}

	v := signIn(t, srv, "viewer@corp.example")
	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		if resp, got := send(t, v, m, base+"/"+id+"/trust", `{"sha256":"`+fp+`"}`); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a viewer's %s: %s %s", m, resp.Status, got)
		}
	}
	other := certpin.Fingerprint(certpintest.SelfSigned(t).Leaf.Raw)
	if resp, got := send(t, c, http.MethodPost, base+"/"+id+"/trust", `{"sha256":"`+other+`"}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a fingerprint not seen: %s %s", resp.Status, got)
	}
	if resp, got := send(t, c, http.MethodPost, base+"/"+id+"/trust", `{"sha256":"`+fp+`"}`); resp.StatusCode != http.StatusOK || !strings.Contains(got, `"pinnedCert":{"sha256":"`+fp+`"`) {
		t.Fatalf("trust: %s %s", resp.Status, got)
	}
	if _, got := send(t, c, http.MethodGet, base, ""); !strings.Contains(got, `"pinnedCert":{"sha256":"`+fp+`"`) {
		t.Fatalf("listed: %s", got)
	}
	if resp, got := send(t, c, http.MethodPost, base+"/"+id+"/test", `{}`); resp.StatusCode != http.StatusOK || !strings.Contains(got, `"ok":true`) || !strings.Contains(got, `"pinned":true`) {
		t.Fatalf("test pinned: %s %s", resp.Status, got)
	}
	if resp, got := send(t, c, http.MethodDelete, base+"/"+id+"/trust", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget: %s %s", resp.Status, got)
	}
	if resp, _ := send(t, c, http.MethodDelete, base+"/"+id+"/trust", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forget twice: %s", resp.Status)
	}
	log := auditOut.String()
	for _, want := range []string{ActionCredentialTrust, ActionCredentialForget, fp} {
		if !strings.Contains(log, want) {
			t.Errorf("the audit log lacks %q", want)
		}
	}
	if strings.Contains(log, "tok-123456") {
		t.Error("the audit log holds the secret")
	}
}

// The same for a repository of Administration › Settings › Repositories:
// the list shows the pin to a SuperAdmin.
func TestTrustingARepositorysCertificateThroughTheAPI(t *testing.T) {
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	f := repotest.New()
	t.Cleanup(f.Close)
	f.Add("shop", "1.0.0", map[string]string{"templates/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"})
	creds := credentials.New(credentials.Options{HTTP: &http.Client{Transport: &http.Transport{}}, Dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext})
	repos := apps.NewRepos(apps.RepoOptions{Client: &http.Client{Transport: &certpin.ContextTransport{Base: &http.Transport{}}}, Credentials: creds})
	srv, c, _, auditOut, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		store := apps.NewMemoryStore()
		o.Apps, o.Repos, o.Credentials = store, repos, creds
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), Apps: store, Repos: repos, Credentials: creds})
	})
	fp := certpin.Fingerprint(f.Certificate().Raw)
	base := srv.URL + "/api/v1/helm-repositories"
	if resp, got := send(t, c, http.MethodPut, base+"/shop", `{"kind":"helm","url":"`+f.URL+`/charts"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("save: %s %s", resp.Status, got)
	}
	if resp, got := send(t, c, http.MethodPost, base+"/shop/test", ""); resp.StatusCode != http.StatusOK || !strings.Contains(got, `"sha256":"`+fp+`"`) {
		t.Fatalf("test: %s %s", resp.Status, got)
	}
	v := signIn(t, srv, "viewer@corp.example")
	if resp, _ := send(t, v, http.MethodPost, base+"/shop/trust", `{"sha256":"`+fp+`"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer: %s", resp.Status)
	}
	if resp, got := send(t, c, http.MethodPost, base+"/shop/trust", `{"sha256":"`+fp+`"}`); resp.StatusCode != http.StatusOK || !strings.Contains(got, `"with":"repository"`) {
		t.Fatalf("trust: %s %s", resp.Status, got)
	}
	if _, got := send(t, c, http.MethodGet, base, ""); !strings.Contains(got, `"pinned":{"pin":{"sha256":"`+fp+`"`) {
		t.Fatalf("listed: %s", got)
	}
	if resp, got := send(t, c, http.MethodPost, base+"/test", `{"name":"shop","kind":"helm","url":"`+f.URL+`/charts"}`); resp.StatusCode != http.StatusOK || !strings.Contains(got, `"ok":true`) {
		t.Fatalf("a test as typed uses the saved pin: %s %s", resp.Status, got)
	}
	if resp, got := send(t, c, http.MethodDelete, base+"/shop/trust", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget: %s %s", resp.Status, got)
	}
	log := auditOut.String()
	for _, want := range []string{ActionHelmRepoTrust, ActionHelmRepoForget, fp} {
		if !strings.Contains(log, want) {
			t.Errorf("the audit log lacks %q", want)
		}
	}
}
