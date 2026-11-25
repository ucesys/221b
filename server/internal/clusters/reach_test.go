package clusters_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sim"
)

// tlsSim is a simulated API server over TLS whose certificate, signed by
// a CA of the test's own (caPEM), is for the names given and no address:
// it is not valid for 127.0.0.1, where it listens.
func tlsSim(t *testing.T, names ...string) (srv *httptest.Server, caPEM string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-cluster-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	must(t, err)
	ca, err := x509.ParseCertificate(caDER)
	must(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	must(t, err)
	srv = httptest.NewUnstartedServer(sim.New(sim.Options{Nodes: 3, Pods: 20, Seed: 7}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}

// steps is a test's steps as "Key:tone", in order.
func steps(r clusters.TestResult) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Key+":"+s.Tone)
	}
	return strings.Join(out, " ")
}

// Finding 10: the test checked the server's certificate against the
// address's host, while the connection checks it against the kubeconfig's
// tls-server-name, so a connection that works failed its TLS step.
func TestTheConnectionTestChecksTheTLSServerName(t *testing.T) {
	srv, ca := tlsSim(t, "kubernetes")
	m, ctx := started(t, clusters.NewMemoryStore())
	kc := kubeconfigFor(srv.URL, saToken("prod"), caData(ca), "tls-server-name: kubernetes")
	if _, err := m.Add(ctx, clusters.Draft{Spec: clusters.Spec{Name: "prod", Env: "production", Server: srv.URL, CA: "pem", Auth: "kubeconfig", Mode: "read"}, Credential: clusters.Credential{Kubeconfig: kc}}); err != nil {
		t.Fatal(err)
	}
	connected(t, m, "prod")
	r, err := m.TestSaved(ctx, "prod", "ann")
	must(t, err)
	if r.Tone == "err" || !strings.HasPrefix(steps(r), "Address:ok TLS:ok Sign-in:ok Kubernetes:ok") {
		t.Fatalf("the test of a connection with tls-server-name: %s, %q", steps(r), r.Error)
	}
	if !strings.Contains(r.Steps[1].Text, "kubernetes") {
		t.Errorf("the TLS step does not name the name it checked: %q", r.Steps[1].Text)
	}
	// Without the name, the certificate is not for the address: the step
	// says so, as the connection would fail.
	r, err = m.TestDraft(ctx, "", clusters.Draft{Spec: clusters.Spec{Name: "bare", Env: "development", Server: srv.URL, CA: "pem", Auth: "kubeconfig", Mode: "read"},
		Credential: clusters.Credential{Kubeconfig: kubeconfigFor(srv.URL, saToken("prod"), caData(ca))}})
	must(t, err)
	if r.Steps[1].Tone != "err" || !strings.Contains(r.Steps[1].Text, "not for 127.0.0.1") {
		t.Errorf("a certificate that is not for the address: %+v", r.Steps[1])
	}
}
