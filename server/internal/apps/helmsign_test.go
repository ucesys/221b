package apps

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/apps/repotest"
)

// CR-04: a Helm repository's credential signs only the files under its
// endpoint; another path of the same host is asked without it.
func TestAHelmCredentialSignsOnlyUnderItsEndpoint(t *testing.T) {
	f := repotest.New()
	defer f.Close()
	r := newRepos(t, f)
	ctx := context.Background()
	repo, secret, err := r.Signed(ctx, Repository{Name: "shop-charts", Kind: KindHelm, URL: f.URL + "/charts", Credential: userPass(t, r, f.URL+"/charts", "reader", "s3cret")})
	if err != nil || secret != "s3cret" {
		t.Fatalf("%v", err)
	}
	for _, p := range []string{"/charts/index.yaml", "/elsewhere/x.tgz", "/charts/../elsewhere/y.tgz"} {
		_, _ = r.helmGet(ctx, repo, secret, f.URL+p, maxSmall)
	}
	if got := f.Auth["/charts/index.yaml"]; got != repotest.BasicAuth("reader", "s3cret") {
		t.Fatalf("under the endpoint: %q", got)
	}
	if _, asked := f.Auth["/elsewhere/x.tgz"]; !asked {
		t.Fatal("not asked")
	}
	for p, got := range f.Auth {
		if p != "/charts/index.yaml" && got != "" {
			t.Fatalf("%s was sent %q", p, got)
		}
	}
}
