package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/imagecheck"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/settings"
)

// The Helm tab's notice (HA-22): a release whose image has a newer tag in
// its registry is listed with it, after the registry was asked in the
// background; Settings turns the check off (HA-23).
func TestNewerImagesOfTheReleases(t *testing.T) {
	reg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/tags/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.2.3", "1.2.4", "1.3.0-rc.1", "latest"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(reg.Close)
	host := reg.Listener.Addr().String()

	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	rec := helmRecord("web", "shop", 1, "deployed", nil)
	rec.Manifest = "---\nkind: Deployment\nmetadata:\n  name: web\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: " + host + "/team/app:1.2.3\n        - name: side\n          image: " + host + "/team/other:2.0.0\n"
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.web.v1", Namespace: "shop", Labels: map[string]string{"owner": "helm", "name": "web", "version": "1", "status": "deployed"}}, Type: kube.HelmSecretType, Data: map[string][]byte{"release": kube.EncodeHelmRelease(rec)}})
	store := live.New(src, live.Options{})
	store.Notify(everything())
	store.Process()
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, slog.New(slog.NewTextHandler(io.Discard, nil)), func(settings.State) {})
	checks := imagecheck.New(imagecheck.Options{Client: &imagecheck.Client{HTTP: reg.Client()}})
	srv := httptest.NewServer(New(Options{Store: store, Cluster: src, Settings: shared, ImageChecks: checks, Ready: func() bool { return true }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)

	read := func() newerImagesView {
		t.Helper()
		r := get(t, srv.URL+"/api/v1/helm/newer-images")
		if r.StatusCode != 200 {
			t.Fatalf("status %d", r.StatusCode)
		}
		var v newerImagesView
		if err := json.Unmarshal([]byte(body(t, r)), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := read()
	if !v.Enabled || v.Pending != 2 || len(v.Releases) != 0 {
		t.Fatalf("first read: %+v", v)
	}
	deadline := time.Now().Add(5 * time.Second)
	for v.Pending > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		v = read()
	}
	got := v.Releases["shop/web"]
	if len(got) != 1 || got[0].Repository != "team/app" || got[0].Tag != "1.2.3" || got[0].Newer != "1.2.4" {
		t.Fatalf("newer images: %+v", v)
	}

	// Off in Settings: nothing is said, and nothing is asked.
	cur := shared.Current().Settings
	off := false
	cur.ImageChecks = &off
	if _, err := shared.Update(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if v := read(); v.Enabled || len(v.Releases) != 0 {
		t.Fatalf("off: %+v", v)
	}
}
