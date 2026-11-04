package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/settings"
)

// Every API route is the leader's unless listed as local (ADR-70): the
// mux's own patterns are walked, so a new route cannot stay on a follower
// by mistake. The sign-in service's routes, registered by main, are
// checked by path.
func TestEveryRouteIsForwardedUnlessLocal(t *testing.T) {
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	s := New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Logger: quietLog()})
	if len(s.patterns) < 20 {
		t.Fatalf("only %d routes registered", len(s.patterns))
	}
	placeholder := regexp.MustCompile(`\{[^}]*\}`)
	for _, pat := range s.patterns {
		_, path, ok := strings.Cut(pat, " ")
		if !ok {
			path = pat
		}
		path = placeholder.ReplaceAllString(path, "x")
		want := strings.HasPrefix(path, "/api/v1/")
		for _, p := range localPaths {
			if path == p || strings.HasPrefix(path, p+"/") {
				want = false
			}
		}
		if got := leaderOnly(path); got != want {
			t.Errorf("%s: forwarded %v, want %v", pat, got, want)
		}
	}
	for path, want := range map[string]bool{
		"/api/v1/me":                       false,
		"/api/v1/admin/users":              false,
		"/api/v1/admin/users/pat":          false,
		"/api/v1/admin/users/pat/sessions": false,
		"/api/v1/admin/ldap/refresh":       false,
		"/api/v1/admin/roles/manifest":     true,
		"/api/v1/meta":                     true,
		"/signin":                          false,
		"/signin/callback":                 false,
		"/setup":                           false,
		"/healthz":                         false,
		"/readyz":                          false,
		"/metrics":                         false,
		"/":                                false,
	} {
		if got := leaderOnly(path); got != want {
			t.Errorf("%s: forwarded %v, want %v", path, got, want)
		}
	}
}

// A cold standby holds nothing of its own: it is ready as soon as it knows
// the leader to forward to; the leader, and a warm standby, must have
// listed their local cluster, as before (ADR-70).
func TestAColdStandbyIsReadyOnceItKnowsTheLeader(t *testing.T) {
	lead := &testLeader{}
	standby := true
	srv := httptest.NewServer(New(Options{Leader: lead, Ready: func() bool { return false }, Standby: func() bool { return standby }, Logger: quietLog()}))
	defer srv.Close()
	if r := get(t, srv.URL+"/readyz"); r.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body(t, r), "leader") {
		t.Fatalf("a cold standby with no leader: %d", r.StatusCode)
	}
	lead.set("10.0.0.2:8080")
	if r := get(t, srv.URL+"/readyz"); r.StatusCode != http.StatusOK {
		t.Fatalf("a cold standby that knows the leader: %d", r.StatusCode)
	}
	standby = false
	if r := get(t, srv.URL+"/readyz"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a replica that reads, before its first list: %d", r.StatusCode)
	}
}

// The flicker (ADR-70): a page's refetch of a release through the replica
// without its stream found no usage there and showed —. A follower now
// forwards every data request to the leader, which reads the usage for
// everyone: twenty fetches through the follower all carry it, and the
// metrics say which replica is which.
func TestReleaseDetailsThroughAFollowerAlwaysCarryUsage(t *testing.T) {
	leaderSrv, store, src := setup(t, true)
	pod := samplePod("shop", "web", "n1")
	pod.Labels["app.kubernetes.io/managed-by"], pod.Labels["app.kubernetes.io/instance"] = "Helm", "web"
	src.Put(pod)
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.web.v1", Namespace: "shop", Labels: map[string]string{"owner": "helm", "name": "web", "version": "1", "status": "deployed"}}, Type: kube.HelmSecretType, Data: map[string][]byte{"release": kube.EncodeHelmRelease(helmRecord("web", "shop", 1, "deployed", map[string]any{"replicas": 2}))}})
	src.SetPodUsage(map[string]nodes.Usage{"shop/web": {CPUMilli: 7, MemoryBytes: 1 << 20}})
	store.Notify(everything())
	store.Process()

	lead := &testLeader{addr: strings.TrimPrefix(leaderSrv.URL, "http://")}
	empty := &source{Source: fake.New(), logs: map[string][]string{}}
	follower := httptest.NewServer(New(Options{Store: live.New(empty, live.Options{}), Cluster: empty, Leader: lead, ProxyToken: "s3cret", Logger: quietLog()}))
	defer follower.Close()
	for i := range 20 {
		r := get(t, follower.URL+"/api/v1/helm/shop/web")
		text := body(t, r)
		if r.StatusCode != http.StatusOK || !strings.Contains(text, `"cpuMilli":7`) {
			t.Fatalf("fetch %d through the follower: %d %s", i, r.StatusCode, text)
		}
	}
	if m := body(t, get(t, follower.URL+"/metrics")); !strings.Contains(m, "b221_leader 0\n") {
		t.Fatalf("the follower's metrics lack b221_leader 0:\n%s", m)
	}
	if m := body(t, get(t, leaderSrv.URL+"/metrics")); !strings.Contains(m, "b221_leader 1\n") {
		t.Fatalf("the leader's metrics lack b221_leader 1:\n%s", m)
	}
}
