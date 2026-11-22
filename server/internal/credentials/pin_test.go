package credentials

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
)

func pinService(t *testing.T) *Service {
	t.Helper()
	return open(t, Options{HTTP: &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}, Dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, Now: func() time.Time { return time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC) }})
}

// CR-15 to CR-19: an untrusted certificate is shown by Test, trusted
// only as seen again, accepted from then on, refused when it changes,
// and refused again once forgotten.
func TestTrustThisCertificate(t *testing.T) {
	cert := certpintest.SelfSigned(t)
	srv := certpintest.NewSwitchable(t, cert, nil)
	s := pinService(t)
	c := token(t, s, "self-signed", srv.URL, SendAs{}, "tok-1")
	fp := certpin.Fingerprint(cert.Leaf.Raw)

	res, err := s.RunTest(ctx, c.ID, "", "ann")
	if err != nil || res.OK || res.Certificate == nil || !res.Certificate.Trustable || res.Certificate.SHA256 != fp || !strings.Contains(res.Text, "not trusted") {
		t.Fatalf("an untrusted certificate: %+v %v", res, err)
	}
	if !strings.Contains(res.Certificate.Subject, "self-signed.test") || res.Certificate.NotAfter.IsZero() {
		t.Fatalf("the details: %+v", res.Certificate)
	}

	// Only the certificate the address presents now is pinned.
	if _, _, err := s.Trust(ctx, c.ID, "", "zz", "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not a fingerprint: %v", err)
	}
	other := certpintest.SelfSigned(t)
	if _, _, err := s.Trust(ctx, c.ID, "", certpin.Fingerprint(other.Leaf.Raw), "ann"); !errors.Is(err, ErrNotSeen) {
		t.Fatalf("a fingerprint not seen: %v", err)
	}
	if got, _ := s.Get(ctx, c.ID); got.PinnedCert != nil {
		t.Fatal("pinned what was not seen")
	}
	if _, _, err := s.Trust(ctx, c.ID, "https://elsewhere.example", fp, "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outside the endpoint: %v", err)
	}
	got, seen, err := s.Trust(ctx, c.ID, "", strings.ToLower(strings.ReplaceAll(fp, ":", "")), "ann")
	if err != nil || got.PinnedCert == nil || got.PinnedCert.SHA256 != fp || got.PinnedCert.By != "ann" || got.PinnedCert.At.IsZero() || seen.SHA256 != fp || !strings.Contains(got.PinnedCert.Subject, "self-signed.test") {
		t.Fatalf("trusted: %+v %v", got.PinnedCert, err)
	}

	res, _ = s.RunTest(ctx, c.ID, "", "ann")
	if !res.OK || res.Certificate == nil || !res.Certificate.Pinned {
		t.Fatalf("with the pin: %+v", res)
	}
	use, err := s.Resolve(ctx, c.ID, UseHTTP, srv.URL)
	if err != nil || use.Pin != fp || use.TLS() == nil {
		t.Fatalf("a use carries the pin: %+v %v", use, err)
	}

	// An edit keeps the pin on the same host; the page cannot set one.
	in := got
	in.PinnedCert = nil
	in.Description = "edited"
	if got, err = s.Update(ctx, c.ID, in, nil, "bob"); err != nil || got.PinnedCert.Fingerprint() != fp {
		t.Fatalf("kept on an edit: %+v %v", got.PinnedCert, err)
	}

	// A renewal fails, visibly.
	srv.Use(other)
	res, _ = s.RunTest(ctx, c.ID, "", "ann")
	want := "the certificate changed (was " + fp + ", now " + certpin.Fingerprint(other.Leaf.Raw) + ")"
	if res.OK || !strings.Contains(res.Text, want) || res.Certificate == nil || res.Certificate.PinnedWas != fp || !res.Certificate.Trustable {
		t.Fatalf("a changed certificate: %+v", res)
	}

	// Forgotten, the first certificate is refused again.
	srv.Use(cert)
	if old, err := s.Forget(ctx, c.ID); err != nil || old.PinnedCert.Fingerprint() != fp {
		t.Fatalf("forget: %+v %v", old.PinnedCert, err)
	}
	if _, err := s.Forget(ctx, c.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forget twice: %v", err)
	}
	res, _ = s.RunTest(ctx, c.ID, "", "ann")
	if res.OK || res.Certificate == nil || res.Certificate.Pinned {
		t.Fatalf("forgotten: %+v", res)
	}

	// Another host lets the pin go.
	if _, _, err := s.Trust(ctx, c.ID, "", fp, "ann"); err != nil {
		t.Fatal(err)
	}
	in.Endpoint = "https://elsewhere.example"
	if got, err = s.Update(ctx, c.ID, in, nil, "bob"); err != nil || got.PinnedCert != nil {
		t.Fatalf("another host: %+v %v", got.PinnedCert, err)
	}
}

// CR-17: the private CA works as before; a certificate it trusts, or one
// it trusts for another name, is not offered for a pin.
func TestThePrivateCAIsUnchangedByPinning(t *testing.T) {
	ca := certpintest.New(t, certpintest.Options{CN: "Test CA", IsCA: true})
	good := certpintest.Server(t, certpintest.New(t, certpintest.Options{Names: []string{"127.0.0.1"}, CA: &ca}), nil)
	wrong := certpintest.Server(t, certpintest.New(t, certpintest.Options{Names: []string{"other.example"}, CA: &ca}), nil)
	s := pinService(t)
	mk := func(name, endpoint string) Credential {
		c, err := s.Create(ctx, Credential{Name: name, Kind: KindToken, Endpoint: endpoint, CAPEM: ca.PEM}, Secret{Token: "tok-1"}, "ann")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := mk("good", good.URL)
	res, _ := s.RunTest(ctx, c.ID, "", "ann")
	if !res.OK || res.Certificate != nil {
		t.Fatalf("the private CA: %+v", res)
	}
	cert, _ := certpin.Probe(ctx, nil, good.URL, nil, certpin.Trust{CAPEM: ca.PEM})
	if _, _, err := s.Trust(ctx, c.ID, "", cert.SHA256, "ann"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "trusted already") {
		t.Fatalf("a trusted certificate: %v", err)
	}
	w := mk("wrong", wrong.URL)
	res, _ = s.RunTest(ctx, w.ID, "", "ann")
	if res.OK || res.Certificate == nil || res.Certificate.Trustable {
		t.Fatalf("another name: %+v", res)
	}
	if _, _, err := s.Trust(ctx, w.ID, "", res.Certificate.SHA256, "ann"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "another reason") {
		t.Fatalf("another name is not pinned: %v", err)
	}
}
