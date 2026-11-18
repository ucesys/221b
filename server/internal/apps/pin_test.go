package apps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/ucesys/221b/server/internal/apps/repotest"
	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
	"github.com/ucesys/221b/server/internal/credentials"
)

// pinRepos are repositories fetched with the console's own kind of
// client, trusting nothing but the system's CAs and the pins.
func pinRepos(t *testing.T) *Repos {
	t.Helper()
	allowLocal(t)
	creds := credentials.New(credentials.Options{HTTP: &http.Client{Transport: &http.Transport{}}, Dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext})
	return NewRepos(RepoOptions{Client: &http.Client{Transport: &certpin.ContextTransport{Base: &http.Transport{}}, Timeout: 10 * time.Second}, Credentials: creds})
}

// CR-15 to CR-19: a Helm repository with a certificate of no known CA.
func TestTrustingARepositorysCertificate(t *testing.T) {
	ctx := context.Background()
	f := repotest.New()
	t.Cleanup(f.Close)
	f.Add("app", "1.0.0", map[string]string{"templates/cm.yaml": cmTemplate})
	r := pinRepos(t)
	fp := certpin.Fingerprint(f.Certificate().Raw)
	at := f.URL + "/charts"
	if _, _, err := r.Save(ctx, "charts", RepositoryInput{Kind: KindHelm, URL: at}, "ann"); err != nil {
		t.Fatal(err)
	}
	repo, secret, _ := r.Get(ctx, "charts")
	res := r.Test(ctx, repo, secret)
	if res.OK || res.Certificate == nil || !res.Certificate.Trustable || res.Certificate.SHA256 != fp {
		t.Fatalf("untrusted: %+v", res)
	}
	other := certpintest.SelfSigned(t)
	if _, _, err := r.Trust(ctx, "charts", certpin.Fingerprint(other.Leaf.Raw), "ann"); !errors.Is(err, credentials.ErrNotSeen) {
		t.Fatalf("a fingerprint not seen: %v", err)
	}
	got, p, err := r.Trust(ctx, "charts", fp, "ann")
	if err != nil || p.With != "repository" || got.PinnedCert.Fingerprint() != fp {
		t.Fatalf("trust: %+v %+v %v", got.PinnedCert, p, err)
	}
	repo, secret, _ = r.Get(ctx, "charts")
	if res = r.Test(ctx, repo, secret); !res.OK || res.Certificate == nil || !res.Certificate.Pinned {
		t.Fatalf("pinned: %+v", res)
	}
	if charts, err := r.Charts(ctx, "charts"); err != nil || len(charts) != 1 {
		t.Fatalf("charts: %v %v", charts, err)
	}
	if _, _, err := r.Save(ctx, "charts", RepositoryInput{Kind: KindHelm, URL: at}, "bob"); err != nil {
		t.Fatal(err)
	}
	if x, _ := r.Raw(ctx, "charts"); x.PinnedCert.Fingerprint() != fp {
		t.Fatal("saved again, the pin went")
	}
	if pin := r.PinOf(ctx, repo); pin == nil || pin.With != "repository" {
		t.Fatalf("PinOf: %+v", pin)
	}
	if _, p, err := r.Forget(ctx, "charts"); err != nil || p.Pin.Fingerprint() != fp {
		t.Fatalf("forget: %+v %v", p, err)
	}
	r.forget("charts")
	repo, secret, _ = r.Get(ctx, "charts")
	if res = r.Test(ctx, repo, secret); res.OK {
		t.Fatal("forgotten, the certificate was accepted")
	}
	if _, _, err := r.Forget(ctx, "charts"); !errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("nothing to forget: %v", err)
	}

	// With a credential, the credential keeps the pin.
	c, err := r.o.Credentials.Create(ctx, credentials.Credential{Name: "registry", Kind: credentials.KindUserPass, Endpoint: at, Username: "u"}, credentials.Secret{Password: "p-123456"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Save(ctx, "charts", RepositoryInput{Kind: KindHelm, URL: at, Credential: c.ID}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, p, err = r.Trust(ctx, "charts", fp, "ann"); err != nil || p.With != "credential" {
		t.Fatalf("with the credential: %+v %v", p, err)
	}
	repo, secret, _ = r.Get(ctx, "charts")
	if res = r.Test(ctx, repo, secret); !res.OK {
		t.Fatalf("the credential's pin: %+v", res)
	}
}

// gitServer answers a git client's list of refs over TLS with cert.
func gitServer(t *testing.T, cert certpintest.Cert) string {
	t.Helper()
	head := strings.Repeat("ab", 20)
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	srv := certpintest.Server(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/info/refs") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte(pkt("# service=git-upload-pack\n") + "0000" + pkt(head+" HEAD\x00symref=HEAD:refs/heads/main agent=test\n") + pkt(head+" refs/heads/main\n") + "0000"))
	}))
	return srv.URL + "/ops/app.git"
}

// CR-17: a git fetch carries the pin in its context to the console's git
// client: the credential's, or its repository's.
func TestAGitFetchCarriesThePin(t *testing.T) {
	ctx := context.Background()
	was := client.Protocols["https"]
	client.InstallProtocol("https", githttp.NewClient(&http.Client{Transport: &certpin.ContextTransport{Base: &http.Transport{}}}))
	t.Cleanup(func() { client.InstallProtocol("https", was) })
	cert := certpintest.SelfSigned(t)
	url := gitServer(t, cert)
	fp := certpin.Fingerprint(cert.Leaf.Raw)
	if _, err := ListRefs(ctx, url, GitAuth{}); err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	refs, err := ListRefs(ctx, url, GitAuth{Trust: certpin.Trust{Pin: fp}})
	if err != nil || len(refs.Branches) != 1 {
		t.Fatalf("pinned: %+v %v", refs, err)
	}
	r := pinRepos(t)
	// On the loopback, which a saved address may not be: kept as is.
	if err := r.put(ctx, Repository{Name: "app", Kind: KindGit, URL: url, Auth: AuthNone, CredMigrated: true}); err != nil {
		t.Fatal(err)
	}
	repo, _ := r.Raw(ctx, "app")
	if res := r.Test(ctx, repo, ""); res.OK || res.Certificate == nil {
		t.Fatalf("untrusted: %+v", res)
	}
	if _, _, err := r.Trust(ctx, "app", fp, "ann"); err != nil {
		t.Fatal(err)
	}
	repo, _ = r.Raw(ctx, "app")
	if res := r.Test(ctx, repo, ""); !res.OK {
		t.Fatalf("pinned: %+v", res)
	}
	if refs, err := r.Refs(ctx, Source{Repo: repo.URL, Repository: "app", Ref: "main"}, GitAuth{}); err != nil || len(refs.Branches) != 1 {
		t.Fatalf("a source naming the repository: %+v %v", refs, err)
	}
}
