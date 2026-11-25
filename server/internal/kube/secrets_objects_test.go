package kube

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// What only names a Secret is kept, and a short number masked under a
// credential-looking name is not masked anywhere else (kube#4); a value
// under one of those names that is no reference is still masked.
func TestMaskObjectsKeepsReferencesAndShortNumbers(t *testing.T) {
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(`
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
        - containerPort: 2048
        envFrom:
        - secretRef:
            name: web-env
        env:
        - name: TOKEN
          valueFrom:
            secretKeyRef:
              name: web-token
              key: token
      volumes:
      - name: tls
        secret:
          secretName: web-tls
          defaultMode: 420
  privateKey:
    size: 2048
  secret: fake-plain-secret-value
  credentials:
    secretRef:
      name: web-creds
    password: fake-nested-password
`), &obj); err != nil {
		t.Fatal(err)
	}
	out := MaskObjects([]map[string]any{obj}, "")
	b, _ := yaml.Marshal(out[0])
	text := string(b)
	for _, kept := range []string{"ghcr.io/acme/web:420", "containerPort: 2048", "name: web-env", "name: web-token", "secretName: web-tls", "defaultMode: 420", "name: web-creds"} {
		if !strings.Contains(text, kept) {
			t.Errorf("lost %q:\n%s", kept, text)
		}
	}
	for _, hidden := range []string{"fake-plain-secret-value", "fake-nested-password", "size: 2048"} {
		if strings.Contains(text, hidden) {
			t.Errorf("kept %q:\n%s", hidden, text)
		}
	}
}
