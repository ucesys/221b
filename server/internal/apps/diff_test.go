package apps_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
)

// A diff masks every object as release values are masked, not only
// Secrets: a value under a credential-looking name, an environment
// variable named like one, and a value one of the manifest's Secrets
// holds, wherever it is copied, also in base64.
func TestADiffShowsNoCredentialWhateverKindHoldsIt(t *testing.T) {
	const from = `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  color: blue
`
	to := `---
# Source: x/templates/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: db
stringData:
  conn: kept-in-the-secret
---
# Source: x/templates/cm.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  color: green
  copied: "host=db conn=kept-in-the-secret"
  encoded: ` + base64.StdEncoding.EncodeToString([]byte("kept-in-the-secret")) + `
  password: under-a-credential-name
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: app
        env:
        - name: DB_PASSWORD
          value: in-an-environment-variable
        - name: MODE
          value: fast
      volumes:
      - name: certs
        secret:
          secretName: app-certs
`
	d := apps.Compare(from, to, "deployed", "source")
	for _, hidden := range []string{"kept-in-the-secret", base64.StdEncoding.EncodeToString([]byte("kept-in-the-secret")), "under-a-credential-name", "in-an-environment-variable"} {
		if strings.Contains(d.Text, hidden) {
			t.Errorf("the diff shows %q:\n%s", hidden, d.Text)
		}
	}
	for _, shown := range []string{"color: green", "fast", "hidden by 221B", "Secret db"} {
		if !strings.Contains(d.Text, shown) && !strings.Contains(strings.Join(d.Added, ","), shown) {
			t.Errorf("the diff lacks %q:\n%s", shown, d.Text)
		}
	}
	if len(d.Changed) != 1 || d.Changed[0] != "ConfigMap app" || len(d.Added) != 2 {
		t.Errorf("%+v", d)
	}
}
