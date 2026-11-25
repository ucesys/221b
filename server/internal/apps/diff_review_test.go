package apps_test

import (
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
)

// web is a Deployment that mounts a Secret, with an image tag and a port
// that look like its defaultMode.
func web(secretName string) string {
	return `---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
      - name: web
        image: ghcr.io/acme/web:420
        ports:
        - containerPort: 420
      volumes:
      - name: tls
        secret:
          secretName: ` + secretName + `
          defaultMode: 420
          items:
          - key: tls.crt
            path: tls.crt
      - name: projected
        projected:
          sources:
          - secret:
              name: ` + secretName + `
      - name: csi
        csi:
          driver: secrets-store.csi.k8s.io
          nodePublishSecretRef:
            name: ` + secretName + `-creds
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: web
spec:
  secretName: ` + secretName + `
  privateKey:
    algorithm: RSA
    size: 2048
  duration: 2048h
`
}

// A change to the name of a mounted Secret is a change, and what only
// names a Secret, or a number under a credential-looking name, is not
// masked elsewhere in the diff (kube#4).
func TestADiffSeesAMountedSecretsNewName(t *testing.T) {
	d := apps.Compare(web("web-tls-2025"), web("web-tls-2026"), "deployed", "source")
	if d.Same || len(d.Changed) != 2 {
		t.Fatalf("the change of the Secret's name was not seen: %+v", d)
	}
	for _, shown := range []string{"secretName: web-tls-2026", "name: web-tls-2026", "defaultMode: 420", "web-tls-2026-creds"} {
		if !strings.Contains(d.Text, shown) {
			t.Errorf("the diff lacks %q:\n%s", shown, d.Text)
		}
	}
}

// A change that only masked text shows still counts, and the diff says a
// hidden value changed without showing it (kube#4).
func TestADiffSeesAChangeOfAHiddenValue(t *testing.T) {
	obj := func(pw string) string {
		return "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\ndata:\n  password: " + pw + "\n"
	}
	d := apps.Compare(obj("fake-old-pw"), obj("fake-new-pw"), "deployed", "source")
	if d.Same || len(d.Changed) != 1 {
		t.Fatalf("a changed hidden value was not seen: %+v", d)
	}
	if strings.Contains(d.Text, "fake-old-pw") || strings.Contains(d.Text, "fake-new-pw") || !strings.Contains(d.Text, "hidden by 221B") {
		t.Errorf("diff:\n%s", d.Text)
	}
}
