package certpin_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
)

func get(t *testing.T, hc *http.Client, url string) error {
	t.Helper()
	res, err := hc.Get(url)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return nil
}

func client(tr certpin.Trust) *http.Client {
	return &http.Client{Transport: certpin.Transport(&http.Transport{}, tr, nil), Timeout: 10 * time.Second}
}

func TestUntrustedIsShownAndTrustable(t *testing.T) {
	cert := certpintest.SelfSigned(t)
	srv := certpintest.Server(t, cert, nil)
	c, err := certpin.Probe(context.Background(), nil, srv.URL, nil, certpin.Trust{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Trusted || c.OK() || !c.Trustable || !c.SelfSigned || c.SHA256 != certpin.Fingerprint(cert.Leaf.Raw) || !strings.Contains(c.Subject, "self-signed.test") || c.Problem == "" {
		t.Fatalf("probe: %+v", c)
	}
	if err := get(t, client(certpin.Trust{}), srv.URL); err == nil {
		t.Fatal("an untrusted certificate was accepted without a pin")
	}
}

func TestPinAcceptsExactlyThatCertificate(t *testing.T) {
	cert := certpintest.SelfSigned(t)
	srv := certpintest.NewSwitchable(t, cert, nil)
	pin := certpin.Fingerprint(cert.Leaf.Raw)
	hc := client(certpin.Trust{Pin: pin})
	if err := get(t, hc, srv.URL); err != nil {
		t.Fatalf("the pinned certificate was refused: %v", err)
	}
	// The name is not checked against a pinned certificate.
	other := certpintest.SelfSigned(t, "elsewhere.example")
	srv.Use(other)
	hc.CloseIdleConnections()
	err := get(t, hc, srv.URL)
	var pe *certpin.Error
	if err == nil || !errors.As(err, &pe) {
		t.Fatalf("another certificate: %v", err)
	}
	want := "the certificate changed (was " + pin + ", now " + certpin.Fingerprint(other.Leaf.Raw) + ")"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
	c, err := certpin.Probe(context.Background(), nil, srv.URL, nil, certpin.Trust{Pin: pin})
	if err != nil || c.OK() || c.PinnedWas != pin || c.Changed() != want || !c.Trustable {
		t.Fatalf("probe after the change: %+v %v", c, err)
	}
	// Pinned without its name, it is accepted.
	srv.Use(other)
	if err := get(t, client(certpin.Trust{Pin: certpin.Fingerprint(other.Leaf.Raw)}), srv.URL); err != nil {
		t.Fatalf("a pinned certificate for another name: %v", err)
	}
}

func TestExpiredPinnedCertificateIsAcceptedAndShownExpired(t *testing.T) {
	cert := certpintest.New(t, certpintest.Options{Names: []string{"127.0.0.1"}, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
	srv := certpintest.Server(t, cert, nil)
	pin := certpin.Fingerprint(cert.Leaf.Raw)
	c, err := certpin.Probe(context.Background(), nil, srv.URL, nil, certpin.Trust{})
	if err != nil || !c.Trustable || !c.Expired {
		t.Fatalf("an expired self-signed certificate: %+v %v", c, err)
	}
	if err := get(t, client(certpin.Trust{Pin: pin}), srv.URL); err != nil {
		t.Fatalf("the expired pinned certificate was refused: %v", err)
	}
	c, _ = certpin.Probe(context.Background(), nil, srv.URL, nil, certpin.Trust{Pin: pin})
	if !c.Pinned || !c.Expired || !c.OK() {
		t.Fatalf("probe: %+v", c)
	}
}

func TestPrivateCAUnchangedAndNameMismatchNotTrustable(t *testing.T) {
	ca := certpintest.New(t, certpintest.Options{CN: "Test CA", IsCA: true})
	leaf := certpintest.New(t, certpintest.Options{CN: "server", Names: []string{"127.0.0.1"}, CA: &ca})
	srv := certpintest.Server(t, leaf, nil)
	if err := get(t, client(certpin.Trust{CAPEM: ca.PEM}), srv.URL); err != nil {
		t.Fatalf("the private CA: %v", err)
	}
	c, err := certpin.Probe(context.Background(), nil, srv.URL, nil, certpin.Trust{CAPEM: ca.PEM})
	if err != nil || !c.Trusted || c.Trustable {
		t.Fatalf("probe with the CA: %+v %v", c, err)
	}
	// A trusted CA's certificate for another name is not offered for a pin.
	wrong := certpintest.New(t, certpintest.Options{CN: "server", Names: []string{"other.example"}, CA: &ca})
	srv2 := certpintest.Server(t, wrong, nil)
	c, err = certpin.Probe(context.Background(), nil, srv2.URL, nil, certpin.Trust{CAPEM: ca.PEM})
	if err != nil || c.Trusted || c.Trustable || c.Problem == "" {
		t.Fatalf("a name mismatch: %+v %v", c, err)
	}
	if err := get(t, client(certpin.Trust{CAPEM: ca.PEM}), srv2.URL); err == nil {
		t.Fatal("a name mismatch was accepted")
	}
	// With a pin of something else, a trusted certificate is still
	// accepted when its name is right (by name, not by IP).
	if err := get(t, client(certpin.Trust{CAPEM: ca.PEM, Pin: strings.Repeat("AB:", 31) + "AB"}), strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)); err == nil {
		t.Fatal("localhost is not among the names: refused expected")
	}
}

func TestPinnedByNameStillVerifiesNormally(t *testing.T) {
	ca := certpintest.New(t, certpintest.Options{CN: "Test CA", IsCA: true})
	leaf := certpintest.New(t, certpintest.Options{CN: "server", Names: []string{"localhost"}, CA: &ca})
	srv := certpintest.Server(t, leaf, nil)
	byName := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	other := strings.Repeat("AB:", 31) + "AB"
	if err := get(t, client(certpin.Trust{CAPEM: ca.PEM, Pin: other}), byName); err != nil {
		t.Fatalf("a trusted certificate with a pin of another: %v", err)
	}
}

func TestContextTransport(t *testing.T) {
	cert := certpintest.SelfSigned(t)
	srv := certpintest.Server(t, cert, nil)
	ct := &certpin.ContextTransport{Base: &http.Transport{}}
	hc := &http.Client{Transport: ct}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := hc.Do(req); err == nil {
		t.Fatal("no trust in the context: refused expected")
	}
	ctx := certpin.With(context.Background(), certpin.Trust{Pin: certpin.Fingerprint(cert.Leaf.Raw)})
	res, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("pinned in the context: %v", err)
	}
	res.Body.Close()
	if got := certpin.From(certpin.With(ctx, certpin.Trust{CAPEM: "x"})); got.Pin == "" || got.CAPEM != "x" {
		t.Fatalf("With adds: %+v", got)
	}
}

func TestNormalize(t *testing.T) {
	fp := certpin.Fingerprint([]byte("x"))
	for _, in := range []string{fp, strings.ToLower(fp), strings.ReplaceAll(fp, ":", ""), " " + strings.ReplaceAll(fp, ":", " ") + " "} {
		if got := certpin.Normalize(in); got != fp {
			t.Fatalf("Normalize(%q) = %q", in, got)
		}
	}
	if certpin.Normalize("abc") != "" || certpin.Normalize("zz"+fp[2:]) != "" {
		t.Fatal("not a fingerprint")
	}
}

var _ = tls.VersionTLS12
