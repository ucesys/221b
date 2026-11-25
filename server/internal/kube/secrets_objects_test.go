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

// The argument after a flag that names a credential is masked, in an
// object and in Helm values (kube#5).
func TestTheArgumentAfterASecretFlagIsMasked(t *testing.T) {
	obj := map[string]any{"kind": "Deployment", "spec": map[string]any{"args": []any{"--listen", ":8080", "--db-password", "fake-flag-pw-1", "-token", "fake-flag-pw-2", "--user", "admin"}}}
	b, _ := yaml.Marshal(MaskObjects([]map[string]any{obj}, "")[0])
	var masked []string
	v, _ := yaml.Marshal(MaskValues(map[string]any{"extraArgs": []any{"--password", "fake-flag-pw-3"}}, "", &masked))
	text := string(b) + string(v)
	for _, hidden := range []string{"fake-flag-pw-1", "fake-flag-pw-2", "fake-flag-pw-3"} {
		if strings.Contains(text, hidden) {
			t.Errorf("kept %q:\n%s", hidden, text)
		}
	}
	for _, kept := range []string{":8080", "admin", "--db-password"} {
		if !strings.Contains(text, kept) {
			t.Errorf("lost %q:\n%s", kept, text)
		}
	}
}
