package watson

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
)

// What an upgrade would apply, for the Apps diff, renders on the console:
// a template that copies a Secret through lookup into a ConfigMap finds
// nothing, so neither the render nor the diff carries the value; the
// release's revision is the next one.
func TestAnUpgradesRenderReadsNoSecretThroughLookup(t *testing.T) {
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	tg, _ := simTarget(t)
	tg.Cluster = "local"
	tg.Apps = apps.NewMemoryStore()
	asJSON := rest.CopyConfig(tg.Config)
	asJSON.ContentType = "application/json"
	k := kubernetes.NewForConfigOrDie(asJSON)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "web"}, Data: map[string][]byte{"conn": []byte("planted-in-the-cluster")}}
	if _, err := k.CoreV1().Secrets("web").Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	repo := gitChart(t, map[string]string{
		"charts/web/templates/copy.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-copy\ndata:\n  found: {{ printf \"%s\" (" + `(lookup "v1" "Secret" "web" "db").data | toJson` + ") | quote }}\n  revision: {{ .Release.Revision | quote }}\n",
	})
	c, err := Parse("helm install hello --git " + repo + " --path charts/web --ref v1.0.0 --tag -n web")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runHelm(context.Background(), c, tg, &out, &out); code != 0 {
		t.Fatalf("install: %s", out.String())
	}
	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "v1.0.0", RefKind: apps.RefTag}
	co, err := apps.Fetch(context.Background(), src, apps.GitAuth{})
	if err != nil {
		t.Fatal(err)
	}
	ch, vals, err := apps.Compose(co, src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{})
	manifest, err := s.RenderUpgrade(context.Background(), Cluster{ID: "local", Config: tg.Config}, Who{}, "web", "hello", ch, vals)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(secret.Data["conn"])
	if !strings.Contains(manifest, "hello-copy") || !strings.Contains(manifest, `revision: "2"`) || strings.Contains(manifest, encoded) || strings.Contains(manifest, "planted-in-the-cluster") {
		t.Fatalf("%s", manifest)
	}
	d := apps.Compare("", manifest, "deployed", "source")
	if strings.Contains(d.Text, encoded) || strings.Contains(d.Text, "planted-in-the-cluster") {
		t.Fatalf("%s", d.Text)
	}
}
