package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// adoptSetup is a console on the simulator, whose release web/shop
// (revision 3) has replicas, image.tag, ingress.host, db.host and a
// db.password; the console runs as that release.
func adoptSetup(t *testing.T) (*httptest.Server, *http.Client, *apps.MemoryStore) {
	t.Helper()
	store := apps.NewMemoryStore()
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, _, local, liveStore := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps = store
		o.SelfRelease = apps.SelfRelease{Cluster: clusters.LocalID, Namespace: "web", Name: "shop"}
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond})
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	return srv, c, store
}

func savedValues(t *testing.T, store *apps.MemoryStore) (apps.Record, map[string]any) {
	t.Helper()
	rec, _, err := store.Get(context.Background(), clusters.LocalID, "web", "shop")
	if err != nil {
		t.Fatal(err)
	}
	vals, err := apps.Values(rec.Values, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rec, vals
}

// A release adopted with a source alone keeps every value it has: the
// record starts from helm get values, and the page sees the credentials
// masked.
func TestAnAdoptedReleaseStartsFromItsOwnValues(t *testing.T) {
	srv, c, store := adoptSetup(t)
	resp, body := send(t, c, http.MethodPut, srv.URL+"/api/v1/apps/web/shop", `{"source":{"repo":"https://git.invalid/web/shop","ref":"main"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if strings.Contains(body, sim.PlantedHelmPassword) || !strings.Contains(body, `"valuesMasked":["db.password"]`) || !strings.Contains(body, `"adoptedRevision":3`) || !strings.Contains(body, `"self":true`) {
		t.Fatalf("the answer: %s", body)
	}
	rec, vals := savedValues(t, store)
	if rec.AdoptedRevision != 3 {
		t.Fatalf("adopted revision %d", rec.AdoptedRevision)
	}
	db, _ := vals["db"].(map[string]any)
	img, _ := vals["image"].(map[string]any)
	if db["password"] != sim.PlantedHelmPassword || db["host"] != "db.internal" || img["tag"] != "3.2.1" || vals["replicas"] == nil {
		t.Fatalf("the record's values: %v", vals)
	}
	resp, body = send(t, c, http.MethodGet, srv.URL+"/api/v1/apps/web/shop", "")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, sim.PlantedHelmPassword) || !strings.Contains(body, `"self":true`) {
		t.Fatalf("%s %s", resp.Status, body)
	}
}

// Values given with the first save are laid over the release's, theirs
// winning, not in their place.
func TestValuesGivenWhenAdoptingAreMergedOverTheReleases(t *testing.T) {
	srv, c, store := adoptSetup(t)
	resp, body := send(t, c, http.MethodPut, srv.URL+"/api/v1/apps/web/shop", `{"source":{"repo":"https://git.invalid/web/shop","ref":"main"},"values":"replicas: 5\nldap:\n  enabled: true\ndb:\n  host: db.other\n"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s", resp.Status, body)
	}
	_, vals := savedValues(t, store)
	db, _ := vals["db"].(map[string]any)
	ldap, _ := vals["ldap"].(map[string]any)
	ing, _ := vals["ingress"].(map[string]any)
	if vals["replicas"] != float64(5) && vals["replicas"] != int64(5) && vals["replicas"] != 5 {
		t.Errorf("replicas %#v", vals["replicas"])
	}
	if db["host"] != "db.other" || db["password"] != sim.PlantedHelmPassword || ldap["enabled"] != true || ing["host"] != "shop.example.test" {
		t.Fatalf("the merged values: %v", vals)
	}
	// A later save replaces the values: the merge is only the adoption's.
	resp, body = send(t, c, http.MethodPut, srv.URL+"/api/v1/apps/web/shop", `{"values":"replicas: 2\n"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s", resp.Status, body)
	}
	if _, vals = savedValues(t, store); len(vals) != 1 {
		t.Fatalf("a later save merged: %v", vals)
	}
}

// What the page was shown masked and sends back keeps the value it hid;
// a masked placeholder with nothing behind it is refused.
func TestAMaskedValueSentBackKeepsTheSavedOne(t *testing.T) {
	srv, c, store := adoptSetup(t)
	url := srv.URL + "/api/v1/apps/web/shop"
	if resp, body := send(t, c, http.MethodPut, url, `{"source":{"repo":"https://git.invalid/web/shop","ref":"main"}}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s", resp.Status, body)
	}
	shown, _ := maskRecordValues(func() string { r, _ := savedValues(t, store); return r.Values }())
	if !strings.Contains(shown, kube.MaskedValue) {
		t.Fatalf("not masked: %s", shown)
	}
	edited := strings.Replace(shown, "db.internal", "db.moved", 1)
	resp, body := send(t, c, http.MethodPut, url, `{"values":`+quoteJSON(edited)+`}`)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, sim.PlantedHelmPassword) {
		t.Fatalf("%s %s", resp.Status, body)
	}
	rec, vals := savedValues(t, store)
	db, _ := vals["db"].(map[string]any)
	if db["password"] != sim.PlantedHelmPassword || db["host"] != "db.moved" || strings.Contains(rec.Values, kube.MaskedValue) {
		t.Fatalf("the saved values: %s", rec.Values)
	}
	resp, body = send(t, c, http.MethodPut, url, `{"values":`+quoteJSON(edited+"token: "+quoteJSON(kube.MaskedValue)+"\n")+`}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "token") {
		t.Fatalf("a placeholder with nothing behind it: %s %s", resp.Status, body)
	}
	if rec2, _ := savedValues(t, store); rec2.Values != rec.Values {
		t.Fatal("the refused save changed the record")
	}
}

func TestRestoreMaskedFollowsListsAndMaps(t *testing.T) {
	kept := "env:\n  - name: DB_PASSWORD\n    value: s3cret-value\n  - name: MODE\n    value: fast\nauth:\n  passwords: [one-secret, two-secret]\n"
	shown, masked := maskRecordValues(kept)
	if len(masked) == 0 || strings.Contains(shown, "s3cret") || strings.Contains(shown, "one-secret") {
		t.Fatalf("%v %s", masked, shown)
	}
	got, err := restoreMasked(shown, kept)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := apps.Values(got, nil)
	b, _ := apps.Values(kept, nil)
	ra, ca := apps.ValueChanges(b, a)
	if len(ra)+len(ca) != 0 {
		t.Fatalf("restored differs: %v %v\n%s", ra, ca, got)
	}
	if plain, _ := maskRecordValues("replicas: 2 # two\n"); plain != "replicas: 2 # two\n" {
		t.Fatalf("a file without credentials changed: %q", plain)
	}
}

func quoteJSON(s string) string { return quoteDetail(s) }
