package credentials

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
)

// --- Trust this certificate (0.12.1, CR-15 to CR-19) ---
//
// A Test of an https:// address whose certificate is not trusted shows
// it (CR-15); a SuperAdmin may trust exactly that certificate (CR-16):
// Trust looks at the address again and pins the certificate only when it
// is the one the Test showed and fails only for its CA. The pin is kept
// with the credential; every use of it accepts that certificate from
// then on, and no other (CR-17, CR-18). Forget lets it go (CR-19).

// baseRoots are the CAs the service's client trusts: nil (the system's)
// but in tests.
func baseRoots(hc *http.Client) *x509.CertPool {
	if hc == nil {
		return nil
	}
	if tr, ok := hc.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		return tr.TLSClientConfig.RootCAs
	}
	return nil
}

// probe looks at the certificate an https:// address (an oci:// one's
// registry) presents, with the credential's private CA and pin. A client
// certificate's credential shows its certificate when the server asks
// for one: a server capped at TLS 1.2 that aborts on an empty one
// (Apache's SSLVerifyClient require, HAProxy, Java's clientAuth) shows
// nothing otherwise (credentials-cut-1).
func (s *Service) probe(ctx context.Context, target string, t certpin.Trust, client *tls.Certificate) (certpin.Cert, error) {
	if rest, ok := strings.CutPrefix(target, "oci://"); ok {
		host, _, _ := strings.Cut(rest, "/")
		target = "https://" + host
	}
	if client == nil {
		return certpin.Probe(ctx, s.o.Dial, target, baseRoots(s.o.HTTP), t)
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return certpin.Cert{}, certpin.ErrNotHTTPS
	}
	hostport := u.Host
	if u.Port() == "" {
		hostport = net.JoinHostPort(u.Hostname(), "443")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := s.o.Dial(ctx, "tcp", hostport)
	if err != nil {
		return certpin.Cert{}, fmt.Errorf("could not connect to %s: %w", hostport, err)
	}
	defer conn.Close()
	sni := u.Hostname()
	if net.ParseIP(sni) != nil {
		sni = ""
	}
	// Only to read the certificate, as certpin.Probe does: Check verifies
	// it below.
	tc := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, //nolint:gosec // verified by Check
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return client, nil }})
	if err := tc.HandshakeContext(ctx); err != nil {
		return certpin.Cert{}, fmt.Errorf("the TLS handshake with %s failed: %w", hostport, err)
	}
	return certpin.Check(tc.ConnectionState().PeerCertificates, u.Hostname(), certpin.Roots(baseRoots(s.o.HTTP), t.CAPEM), t.Pin, time.Now()), nil
}

// noteCertificate adds to a test the certificate its address presented,
// when it is not trusted or is the pinned one (CR-15): the person sees
// what Trust would pin, or that the pinned one has expired.
func (s *Service) noteCertificate(ctx context.Context, use *Use, target string, t *Test) {
	if u, err := url.Parse(target); err != nil || u.Scheme != "https" && u.Scheme != "oci" {
		return
	}
	if t.OK && use.Pin == "" {
		return
	}
	c, err := s.probe(ctx, target, use.Trust(), use.Certificate())
	if err != nil || c.Trusted && !c.Pinned {
		return
	}
	t.Certificate = &c
	switch {
	case c.Pinned && c.Expired:
		t.Text += fmt.Sprintf(" The pinned certificate expired on %s; it is still accepted, as pinned.", c.NotAfter.Format(time.DateOnly))
	case c.Changed() != "":
		if !strings.Contains(t.Text, "the certificate changed") {
			t.Text += " " + upper(c.Changed()) + "."
		}
	case !c.OK() && c.Trustable:
		t.Text += " Its certificate is not trusted: check it below and trust it, or set the private CA."
	}
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Trust pins the certificate an address under the credential's endpoint
// presents (CR-16): typed is the address the Test asked, sha256 the
// fingerprint it showed. The address is looked at again: the certificate
// is pinned only when it is that one, and fails only because its CA is
// not trusted (an unknown CA, or self-signed). It returns the credential
// as changed and the certificate pinned.
func (s *Service) Trust(ctx context.Context, id, typed, sha256, by string) (Credential, certpin.Cert, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Credential{}, certpin.Cert{}, err
	}
	if c.Kind == KindSSH {
		return Credential{}, certpin.Cert{}, invalid("an SSH key trusts a host key, not a certificate")
	}
	target := TestTarget(c, typed)
	if !c.Covers(target) {
		return Credential{}, certpin.Cert{}, invalid("%s is not under the endpoint %s", target, c.Endpoint)
	}
	var client *tls.Certificate
	if c.Kind == KindClientCert {
		if sec, err := s.secret(ctx, id); err == nil {
			if kp, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(sec.KeyPEM)); err == nil {
				client = &kp
			}
		}
	}
	cert, err := CheckTrust(ctx, func(ctx context.Context) (certpin.Cert, error) {
		return s.probe(ctx, target, certpin.Trust{CAPEM: c.CAPEM}, client)
	}, hostOf(target), sha256)
	if err != nil {
		return Credential{}, certpin.Cert{}, err
	}
	pin := certpin.PinOf(cert, by, s.o.Now())
	c, err = s.o.Repo.Update(ctx, id, func(cur *Credential) error {
		// Changed while its certificate was looked at: the pin is for the
		// address as it was, not one of the endpoint now.
		if !cur.Covers(target) {
			return invalid("%s changed meanwhile, and %s is not under its endpoint now: test it again", cur.Name, target)
		}
		cur.PinnedCert, cur.LastTest = pin, nil
		return nil
	})
	return c, cert, err
}

// Forget lets a credential's pinned certificate go (CR-19): its endpoint
// is verified normally again. It returns the credential as it was.
func (s *Service) Forget(ctx context.Context, id string) (Credential, error) {
	var old Credential
	_, err := s.o.Repo.Update(ctx, id, func(cur *Credential) error {
		old = *cur
		if old.PinnedCert == nil {
			return invalid("%s has no pinned certificate", old.Name)
		}
		cur.PinnedCert, cur.LastTest = nil, nil
		return nil
	})
	return old, err
}

// ErrNotSeen is a fingerprint to trust that is not the certificate the
// address presents now.
var ErrNotSeen = errors.New("not the certificate the address presents")

// CheckTrust looks at an address's certificate with look and returns it
// when it may be pinned for the fingerprint given (CR-16): the one the
// address presents now, failing only because its CA is not trusted. Every
// consumer's Trust goes through it.
func CheckTrust(ctx context.Context, look func(context.Context) (certpin.Cert, error), host, sha256 string) (certpin.Cert, error) {
	want := certpin.Normalize(sha256)
	if want == "" {
		return certpin.Cert{}, invalid("give the certificate's SHA-256 fingerprint, as Test showed it")
	}
	cert, err := look(ctx)
	if errors.Is(err, certpin.ErrNotHTTPS) {
		return certpin.Cert{}, invalid("%s", err.Error())
	}
	if err != nil {
		return certpin.Cert{}, err
	}
	switch {
	case cert.SHA256 != want:
		return certpin.Cert{}, fmt.Errorf("%w: %s presents %s now, not %s: test again, and check the certificate before trusting it", ErrNotSeen, host, cert.SHA256, want)
	case cert.Trusted:
		return certpin.Cert{}, invalid("%s's certificate is trusted already: nothing to pin", host)
	case !cert.Trustable:
		return certpin.Cert{}, invalid("%s's certificate fails for another reason than its CA (%s): trusting it is not offered", host, cert.Problem)
	}
	return cert, nil
}
