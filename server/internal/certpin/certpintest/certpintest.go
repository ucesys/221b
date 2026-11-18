// Package certpintest makes self-signed certificates and TLS servers for
// the tests of pinned certificates.
package certpintest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Cert is a certificate with its key, its PEM and the parsed leaf.
type Cert struct {
	TLS  tls.Certificate
	Leaf *x509.Certificate
	PEM  string
}

// Options shape a certificate: its common name, names, dates and the CA
// that signs it (nil: self-signed).
type Options struct {
	CN        string
	Names     []string
	NotBefore time.Time
	NotAfter  time.Time
	CA        *Cert
	IsCA      bool
}

// New makes a certificate.
func New(t testing.TB, o Options) Cert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if o.CN == "" {
		o.CN = "self-signed.test"
	}
	if o.NotBefore.IsZero() {
		o.NotBefore = time.Now().Add(-time.Hour)
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = time.Now().Add(24 * time.Hour)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: o.CN, Organization: []string{"221b tests"}},
		NotBefore:             o.NotBefore,
		NotAfter:              o.NotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  o.IsCA,
	}
	for _, n := range o.Names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	parent, signer := tmpl, any(key)
	if o.CA != nil {
		parent, signer = o.CA.Leaf, o.CA.TLS.PrivateKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Cert{
		TLS:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		Leaf: leaf,
		PEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// SelfSigned is a self-signed certificate for names (127.0.0.1 and
// localhost when none).
func SelfSigned(t testing.TB, names ...string) Cert {
	if len(names) == 0 {
		names = []string{"127.0.0.1", "localhost"}
	}
	return New(t, Options{Names: names})
}

// Server is a TLS server presenting cert, answering with h (200 "ok"
// when nil); closed with the test.
func Server(t testing.TB, cert Cert, h http.Handler) *httptest.Server {
	t.Helper()
	if h == nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert.TLS}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// Switchable is a TLS server whose certificate can be changed, as a
// renewal does.
type Switchable struct {
	*httptest.Server
	cur chan tls.Certificate
}

// NewSwitchable is a Switchable presenting cert first.
func NewSwitchable(t testing.TB, cert Cert, h http.Handler) *Switchable {
	t.Helper()
	if h == nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	}
	s := &Switchable{cur: make(chan tls.Certificate, 1)}
	s.cur <- cert.TLS
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		c := <-s.cur
		s.cur <- c
		return &c, nil
	}}
	srv.StartTLS()
	// httptest adds its own certificate, which a client without a server
	// name would be given instead.
	srv.TLS.Certificates = nil
	t.Cleanup(srv.Close)
	s.Server = srv
	return s
}

// Use presents cert from the next connection on; open connections are
// closed.
func (s *Switchable) Use(cert Cert) {
	<-s.cur
	s.cur <- cert.TLS
	s.CloseClientConnections()
}
