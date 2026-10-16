package clusters_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sim"
)

// token makes an unsigned JWT with the claims, as a service account token
// looks to the console (the API server verifies; the console only reads).
func token(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "x"})
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc(h) + "." + enc(b) + "." + enc([]byte("signature"))
}

// selfSigned makes a certificate and key in PEM, for the certificate method.
func selfSigned(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn, Organization: []string{"system:masters"}}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

const kubeconfig = `apiVersion: v1
kind: Config
current-context: staging
clusters:
- name: staging
  cluster:
    server: https://10.21.0.10:6443
    certificate-authority-data: %s
- name: dev
  cluster:
    server: https://10.30.0.10:6443
contexts:
- name: staging
  context: {cluster: staging, user: console-staging}
- name: dev
  context: {cluster: dev, user: cloud}
users:
- name: console-staging
  user:
    token: %s
- name: cloud
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: aws
`

func TestCredentialsSayWhatTheyAre(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	exp := now.Add(300 * 24 * time.Hour)
	sa := token(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console", "exp": exp.Unix(), "kubernetes.io": map[string]any{"namespace": "kube-system", "serviceaccount": map[string]any{"name": "cluster-console"}}})
	check, server, err := clusters.CheckCredential(clusters.AuthToken, clusters.Credential{Token: sa}, "https://k8s.example:6443", now)
	if err != nil || check.Tone != "ok" || check.Info.Who != "system:serviceaccount:kube-system:cluster-console" || check.Info.Expires == nil || !check.Info.Expires.Equal(exp) || server != "https://k8s.example:6443" {
		t.Fatalf("token: %v %+v", err, check)
	}
	if !strings.Contains(check.Text, "valid until 2027-08-10") {
		t.Errorf("token text: %s", check.Text)
	}
	expired := token(map[string]any{"sub": "u", "exp": now.Add(-24 * time.Hour).Unix()})
	if _, _, err := clusters.CheckCredential(clusters.AuthToken, clusters.Credential{Token: expired}, "", now); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired token accepted: %v", err)
	}
	if _, _, err := clusters.CheckCredential(clusters.AuthToken, clusters.Credential{Token: "not a token"}, "", now); err == nil {
		t.Error("garbage token accepted")
	}

	// A kubeconfig: the current context, its server lifted, an exec plugin refused.
	caPEM, _ := selfSigned(t, "kubernetes", now.Add(10*365*24*time.Hour))
	kc := strings.ReplaceAll(kubeconfig, "%s", "X")
	kc = strings.Replace(kc, "certificate-authority-data: X", "certificate-authority-data: "+base64.StdEncoding.EncodeToString([]byte(caPEM)), 1)
	kc = strings.Replace(kc, "token: X", "token: "+sa, 1)
	check, server, err = clusters.CheckCredential(clusters.AuthKubeconfig, clusters.Credential{Kubeconfig: kc}, "", now)
	if err != nil || check.Tone != "ok" || check.Info.Context != "staging" || check.Info.Who != "console-staging" || server != "https://10.21.0.10:6443" || check.Info.Expires == nil {
		t.Fatalf("kubeconfig: %v %+v %s", err, check, server)
	}
	check, _, err = clusters.CheckCredential(clusters.AuthKubeconfig, clusters.Credential{Kubeconfig: kc}, "https://other.example:6443", now)
	if err != nil || check.Tone != "warn" || !strings.Contains(check.Text, "Its server is https://10.21.0.10:6443") {
		t.Errorf("kubeconfig with another server: %v %+v", err, check)
	}
	if _, _, err := clusters.CheckCredential(clusters.AuthKubeconfig, clusters.Credential{Kubeconfig: kc, Context: "dev"}, "", now); err == nil || !strings.Contains(err.Error(), "exec plugin (aws)") {
		t.Errorf("exec plugin accepted: %v", err)
	}
	if _, _, err := clusters.CheckCredential(clusters.AuthKubeconfig, clusters.Credential{Kubeconfig: kc, Context: "nope"}, "", now); err == nil || !strings.Contains(err.Error(), "no context") {
		t.Errorf("unknown context: %v", err)
	}
	names, current := clusters.Contexts(kc)
	if strings.Join(names, ",") != "dev,staging" || current != "staging" {
		t.Errorf("contexts: %v %s", names, current)
	}

	// A client certificate, with its subject and expiry.
	certPEM, keyPEM := selfSigned(t, "console", now.Add(90*24*time.Hour))
	check, _, err = clusters.CheckCredential(clusters.AuthCert, clusters.Credential{CertPEM: certPEM, KeyPEM: keyPEM}, "", now)
	if err != nil || check.Info.Who != "console (system:masters)" || check.Info.Expires == nil {
		t.Fatalf("cert: %v %+v", err, check)
	}
	if _, _, err := clusters.CheckCredential(clusters.AuthCert, clusters.Credential{CertPEM: certPEM}, "", now); err == nil {
		t.Error("cert without a key accepted")
	}
	if desc, err := clusters.CADescription([]byte(caPEM)); err != nil || !strings.HasPrefix(desc, "CN=kubernetes, valid until 2036-10-") {
		t.Errorf("CA description: %v %q", err, desc)
	}
}

func TestSpecValidation(t *testing.T) {
	good := clusters.Spec{Name: "lakehouse-qa", Env: "staging", Server: "https://k8s.example:6443", CA: "pem", Auth: "token", Mode: "read"}
	if err := clusters.ValidateSpec(good); err != nil {
		t.Fatal(err)
	}
	bad := []clusters.Spec{
		{Name: "", Env: "staging", Server: "https://x", CA: "pem", Auth: "token", Mode: "read"},
		{Name: "-no", Env: "staging", Server: "https://x", CA: "pem", Auth: "token", Mode: "read"},
		{Name: "ok", Env: "moon", Server: "https://x", CA: "pem", Auth: "token", Mode: "read"},
		{Name: "ok", Env: "staging", Server: "k8s.example:6443", CA: "pem", Auth: "token", Mode: "read"},
		{Name: "ok", Env: "staging", Server: "https://x", CA: "trust-me", Auth: "token", Mode: "read"},
		{Name: "ok", Env: "staging", Server: "https://x", CA: "pem", Auth: "magic", Mode: "read"},
		{Name: "ok", Env: "staging", Server: "https://x", CA: "pem", Auth: "token", Mode: "maybe"},
	}
	for i, s := range bad {
		if err := clusters.ValidateSpec(s); err == nil {
			t.Errorf("spec %d accepted: %+v", i, s)
		}
	}
	if clusters.Slug("Lakehouse Prod (EU)") != "lakehouse-prod-eu" || clusters.Slug("---") != "cluster" {
		t.Errorf("slug: %q %q", clusters.Slug("Lakehouse Prod (EU)"), clusters.Slug("---"))
	}
}

// simServer runs a simulated API server for the tests.
func simServer(t *testing.T) (*sim.Sim, *httptest.Server) {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 3, Pods: 20, Seed: 7})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func admin(t *testing.T, srv *httptest.Server, path, body string) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("%s: %s", path, resp.Status)
	}
}

func TestConnectionTestSteps(t *testing.T) {
	_, srv := simServer(t)
	now := time.Now()
	sa := token(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console", "exp": now.Add(24 * time.Hour).Unix()})
	spec := clusters.Spec{Name: "sim", Env: "development", Server: srv.URL, CA: "system", Auth: "token", Mode: "read"}
	opts := clusters.TestOptions{SecretsRead: true, Timeout: 5 * time.Second}

	r := clusters.Test(context.Background(), spec, clusters.Credential{Token: sa}, opts)
	keys := func(r clusters.TestResult) string {
		var out []string
		for _, s := range r.Steps {
			out = append(out, s.Key+":"+s.Tone)
		}
		return strings.Join(out, " ")
	}
	// Plain HTTP is a warning; everything else passes against the simulator.
	if keys(r) != "Address:ok TLS:warn Sign-in:ok Kubernetes:ok Permissions:ok" || r.Tone != "warn" || r.Nodes != 3 || !strings.HasPrefix(r.Version, "v1.37") || !r.Impersonate {
		t.Fatalf("steps: %s %+v", keys(r), r)
	}
	if r.Who != "system:serviceaccount:221b:k8s-console-221b" || !strings.Contains(r.Summary, "3 nodes") {
		t.Errorf("who/summary: %q %q", r.Who, r.Summary)
	}

	// A required token the console does not have: 401 at the sign-in step.
	admin(t, srv, "/sim/token", `{"token":"`+sa+`"}`)
	other := token(map[string]any{"sub": "someone-else", "exp": now.Add(24 * time.Hour).Unix()})
	r = clusters.Test(context.Background(), spec, clusters.Credential{Token: other}, opts)
	if keys(r) != "Address:ok TLS:warn Sign-in:err Kubernetes:muted Permissions:muted" || !strings.Contains(r.Error, "401") {
		t.Fatalf("wrong token: %s %q", keys(r), r.Error)
	}
	r = clusters.Test(context.Background(), spec, clusters.Credential{Token: sa}, opts)
	if r.Tone != "warn" || r.Nodes != 3 {
		t.Fatalf("right token: %s %+v", keys(r), r)
	}
	admin(t, srv, "/sim/token", `{"token":""}`)

	// No right to impersonate: a warning that everyone will see the same.
	admin(t, srv, "/sim/deny", `{"verbs":["impersonate"]}`)
	r = clusters.Test(context.Background(), spec, clusters.Credential{Token: sa}, opts)
	if r.Steps[4].Tone != "warn" || !strings.Contains(r.Steps[4].Text, "cannot impersonate") || r.Impersonate {
		t.Fatalf("impersonate denied: %+v", r.Steps[4])
	}
	// No right to list nodes: the console cannot work at all.
	admin(t, srv, "/sim/deny", `{"verbs":["list"]}`)
	r = clusters.Test(context.Background(), spec, clusters.Credential{Token: sa}, opts)
	if r.Tone != "err" || !strings.Contains(r.Error, "cannot list nodes") {
		t.Fatalf("list denied: %+v", r)
	}
	admin(t, srv, "/sim/deny", `{}`)

	// Nothing at the address.
	dead := spec
	dead.Server = "http://127.0.0.1:1"
	r = clusters.Test(context.Background(), dead, clusters.Credential{Token: sa}, opts)
	if keys(r) != "Address:err TLS:muted Sign-in:muted Kubernetes:muted Permissions:muted" || !strings.Contains(r.Error, "Nothing answers") {
		t.Fatalf("dead address: %s %q", keys(r), r.Error)
	}
	// A name that does not resolve.
	dead.Server = "https://no-such-host.invalid:6443"
	r = clusters.Test(context.Background(), dead, clusters.Credential{Token: sa}, clusters.TestOptions{Timeout: 3 * time.Second, Resolver: func(context.Context, string) ([]string, error) { return nil, &dnsErr{} }})
	if r.Steps[0].Tone != "err" || !strings.Contains(r.Steps[0].Text, "does not resolve") {
		t.Fatalf("no such host: %+v", r.Steps[0])
	}
	// Skipping verification is a warning, not a failure.
	skip := spec
	skip.CA = "skip"
	r = clusters.Test(context.Background(), skip, clusters.Credential{Token: sa}, opts)
	if r.Steps[1].Tone != "warn" {
		t.Fatalf("skip: %+v", r.Steps[1])
	}
}

type dnsErr struct{}

func (*dnsErr) Error() string { return "lookup no-such-host.invalid: no such host" }

func TestSecretStoreRoundTrip(t *testing.T) {
	_, srv := simServer(t)
	store, err := clusters.NewSecretStore(&rest.Config{Host: srv.URL}, "221b", "k8s-console-221b", map[string]string{"app.kubernetes.io/instance": "k8s-console"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if list, err := store.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v %v", err, list)
	}
	spec := clusters.Spec{ID: "staging", Name: "staging", Env: "staging", Server: "https://10.21.0.10:6443", CA: "pem", CAInfo: "CN=kubernetes", Auth: "token", Mode: "read", Added: time.Now().UTC()}
	cred := clusters.Credential{Token: "secret-token", CAPEM: "-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----"}
	if err := store.Save(ctx, spec, &cred); err != nil {
		t.Fatal(err)
	}
	got, gotCred, err := store.Get(ctx, "staging")
	if err != nil || got.Name != "staging" || gotCred.Token != "secret-token" || gotCred.CAPEM != cred.CAPEM {
		t.Fatalf("get: %v %+v %+v", err, got, gotCred)
	}
	// A spec change keeps the credential; a credential change replaces it.
	spec.Description = "pre-production"
	if err := store.Save(ctx, spec, nil); err != nil {
		t.Fatal(err)
	}
	got, gotCred, _ = store.Get(ctx, "staging")
	if got.Description != "pre-production" || gotCred.Token != "secret-token" {
		t.Fatalf("after spec change: %+v %+v", got, gotCred)
	}
	if err := store.Save(ctx, spec, &clusters.Credential{Token: "new-token"}); err != nil {
		t.Fatal(err)
	}
	_, gotCred, _ = store.Get(ctx, "staging")
	if gotCred.Token != "new-token" || gotCred.CAPEM != "" {
		t.Fatalf("after credential change: %+v", gotCred)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "staging" {
		t.Fatalf("list: %v %v", err, list)
	}
	// The Secret carries the labels and never the credential in spec.json.
	resp, err := http.Get(srv.URL + "/api/v1/namespaces/221b/secrets/k8s-console-221b-cluster-staging")
	if err != nil {
		t.Fatal(err)
	}
	var sec struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sec)
	resp.Body.Close()
	if sec.Metadata.Labels["app.kubernetes.io/component"] != "cluster-connection" || sec.Metadata.Labels["221b.ucesys.com/cluster"] != "staging" || sec.Metadata.Labels["app.kubernetes.io/instance"] != "k8s-console" {
		t.Errorf("labels: %v", sec.Metadata.Labels)
	}
	specJSON, _ := base64.StdEncoding.DecodeString(sec.Data["spec.json"])
	if strings.Contains(string(specJSON), "new-token") {
		t.Error("the credential leaked into spec.json")
	}
	if err := store.Delete(ctx, "staging"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "staging"); err != clusters.ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	if err := store.Delete(ctx, "staging"); err != clusters.ErrNotFound {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestManagerAddsUpdatesAndRemoves(t *testing.T) {
	_, srv := simServer(t)
	_, other := simServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := clusters.New(clusters.NewMemoryStore(), clusters.Options{Refresh: time.Second, HealthInterval: time.Second, SecretsRead: true})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sa := token(map[string]any{"sub": "system:serviceaccount:kube-system:cluster-console", "exp": now.Add(24 * time.Hour).Unix()})
	draft := clusters.Draft{Spec: clusters.Spec{Name: "Analytics-Dev", Env: "development", Description: "sandbox", Server: other.URL, CA: "system", Auth: "token", Mode: "read"}, Credential: clusters.Credential{Token: sa}}
	sum, err := m.Add(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ID != "analytics-dev" || sum.Name != "Analytics-Dev" || sum.Cred.Who == "" || sum.Cred.Expires == nil {
		t.Fatalf("added: %+v", sum)
	}
	// A second cluster with the same name, or a name that slugs the same, is refused.
	if _, err := m.Add(ctx, draft); err == nil || !strings.Contains(err.Error(), "already called") {
		t.Errorf("duplicate name: %v", err)
	}
	same := draft
	same.Name = "Analytics_Dev"
	if _, err := m.Add(ctx, same); err == nil {
		t.Error("a name that slugs the same was accepted")
	}
	// Its watches come up.
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, _ := m.Summary("analytics-dev")
		if s.State == "connected" && s.Synced && s.Nodes == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not connected: %+v", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// A rename keeps the credential and the watches.
	upd, err := m.Update(ctx, "analytics-dev", clusters.Draft{Spec: clusters.Spec{Name: "analytics-dev-2", Env: "staging", Server: other.URL, CA: "system", Auth: "token", Mode: "write"}})
	if err != nil || upd.Name != "analytics-dev-2" || upd.Env != "staging" || upd.Mode != "write" || upd.ID != "analytics-dev" || upd.Cred.Who == "" {
		t.Fatalf("update: %v %+v", err, upd)
	}
	// Changing the method without credentials is refused.
	if _, err := m.Update(ctx, "analytics-dev", clusters.Draft{Spec: clusters.Spec{Name: "analytics-dev-2", Env: "staging", Server: other.URL, CA: "system", Auth: "cert", Mode: "read"}}); err == nil {
		t.Error("method change without credentials accepted")
	}
	// A test of the draft, and of the saved connection.
	r, err := m.TestDraft(ctx, "analytics-dev", clusters.Draft{Spec: upd.Spec})
	if err != nil || r.Tone != "warn" || r.Nodes != 3 {
		t.Fatalf("test draft: %v %+v", err, r)
	}
	r, err = m.TestSaved(ctx, "analytics-dev")
	if err != nil || r.Nodes != 3 {
		t.Fatalf("test saved: %v %+v", err, r)
	}
	if s, _ := m.Summary("analytics-dev"); s.LastTest == nil || s.LastTested == nil {
		t.Errorf("the test was not kept: %+v", s)
	}
	if list := m.List(); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := m.Remove(ctx, "analytics-dev"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("analytics-dev"); ok {
		t.Error("still there after removal")
	}
	if _, _, err := m.Store().Get(ctx, "analytics-dev"); err != clusters.ErrNotFound {
		t.Errorf("the credential was not deleted: %v", err)
	}
	if err := m.Remove(ctx, "analytics-dev"); err != clusters.ErrNotFound {
		t.Errorf("remove twice: %v", err)
	}
	// A kubeconfig with no address given: the picked context supplies it,
	// and the connection comes up through it (the simulator is plain HTTP,
	// where client-go would keep the token back).
	kc := "apiVersion: v1\nkind: Config\nclusters:\n- name: a\n  cluster:\n    server: " + other.URL + "\n- name: b\n  cluster:\n    server: https://nowhere.invalid:6443\ncontexts:\n- name: here\n  context:\n    cluster: a\n    user: u\n- name: there\n  context:\n    cluster: b\n    user: u\ncurrent-context: there\nusers:\n- name: u\n  user:\n    token: " + sa + "\n"
	sum, err = m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "from-file", Env: "staging", CA: "system", Auth: "kubeconfig", Mode: "read"}, Credential: clusters.Credential{Kubeconfig: kc, Context: "here"}})
	if err != nil || sum.Server != other.URL || sum.Cred.Context != "here" || sum.Cred.Cluster != "a" || sum.Cred.Who != "u" {
		t.Fatalf("from a kubeconfig: %v %+v", err, sum)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		s, _ := m.Summary("from-file")
		if s.State == "connected" && s.Synced && s.Nodes == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not connected through the kubeconfig: %+v", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// A context whose cluster has no address is refused for want of one.
	noServer := strings.Replace(kc, "    server: https://nowhere.invalid:6443\n", "", 1)
	if _, err := m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "no-address", Env: "staging", CA: "system", Auth: "kubeconfig", Mode: "read"}, Credential: clusters.Credential{Kubeconfig: noServer, Context: "there"}}); err == nil || !strings.Contains(err.Error(), "address") {
		t.Errorf("a kubeconfig without an address: %v", err)
	}
	_ = srv
}
