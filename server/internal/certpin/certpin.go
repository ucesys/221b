// Package certpin is the TLS of the console's own HTTPS clients for what
// signs in somewhere (0.12.1, CR-15 to CR-19): the system's CAs, a
// private CA when one is set, and a pinned certificate. One helper builds
// it for the credentials, Mycroft's sources and wiki store, and the
// repositories.
//
// A pin is "Trust this certificate": the SHA-256 fingerprint of one leaf
// certificate a SuperAdmin saw in a Test and trusted. A connection is
// accepted when normal verification succeeds (the system's CAs and the
// private CA, the host's name, the dates); otherwise only when the leaf
// presented is exactly the pinned one. The fingerprint names that one
// certificate, so its chain, its names and its dates are not checked
// again: a self-signed certificate rarely names the host it serves, and an
// expired pinned certificate is accepted (Test warns of it). Any other
// certificate fails, saying "the certificate changed (was …, now …)".
// There is no switch that skips verification.
package certpin

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Pin is a pinned certificate as it is kept with a credential, a source,
// a repository or the wiki's store: its fingerprint, what it said, and
// who trusted it when.
type Pin struct {
	// SHA256 is the leaf certificate's fingerprint, as Fingerprint writes
	// it.
	SHA256   string    `json:"sha256"`
	Subject  string    `json:"subject,omitempty"`
	Issuer   string    `json:"issuer,omitempty"`
	NotAfter time.Time `json:"notAfter,omitzero"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at,omitzero"`
}

// Fingerprint is the pin's fingerprint, "" for none.
func (p *Pin) Fingerprint() string {
	if p == nil {
		return ""
	}
	return p.SHA256
}

// PinOf is a pin of a certificate a Test showed.
func PinOf(c Cert, by string, at time.Time) *Pin {
	return &Pin{SHA256: c.SHA256, Subject: c.Subject, Issuer: c.Issuer, NotAfter: c.NotAfter, By: by, At: at.UTC()}
}

// Trust is what a connection trusts besides the system's CAs: a private
// CA (PEM) and a pinned fingerprint. It is comparable, so a transport can
// keep one client per Trust.
type Trust struct {
	CAPEM string
	Pin   string
}

// Empty says it trusts nothing more than the system's CAs.
func (t Trust) Empty() bool { return strings.TrimSpace(t.CAPEM) == "" && t.Pin == "" }

// Or is t with what it leaves empty taken from o.
func (t Trust) Or(o Trust) Trust {
	if strings.TrimSpace(t.CAPEM) == "" {
		t.CAPEM = o.CAPEM
	}
	if t.Pin == "" {
		t.Pin = o.Pin
	}
	return t
}

// Fingerprint is a certificate's SHA-256 fingerprint: upper-case hex
// pairs joined by colons, as browsers and openssl show it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// Normalize reads a fingerprint as typed or copied (any case, with or
// without colons or spaces) and writes it as Fingerprint does; "" when it
// is not 32 bytes of hex.
func Normalize(s string) string {
	s = strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s))
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return ""
	}
	h := strings.ToUpper(hex.EncodeToString(b))
	var out strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			out.WriteByte(':')
		}
		out.WriteString(h[i : i+2])
	}
	return out.String()
}

// Roots are the CAs a connection verifies against: the system's with the
// private CA when one is set, base otherwise (nil is the system's; tests
// give their own).
func Roots(base *x509.CertPool, caPEM string) *x509.CertPool {
	if strings.TrimSpace(caPEM) == "" {
		return base
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM([]byte(caPEM))
	return pool
}

// Config is base (nil for a new one) with t's trust: the private CA in
// its roots and, with a pin, the verification of this package. Without
// a pin, verification is Go's own, as it was.
func Config(base *tls.Config, t Trust) *tls.Config {
	var cfg *tls.Config
	if base != nil {
		cfg = base.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.RootCAs = Roots(cfg.RootCAs, t.CAPEM)
	if t.Pin == "" {
		return cfg
	}
	roots, pin := cfg.RootCAs, t.Pin
	// VerifyConnection below verifies every connection: normally, then
	// against the pin.
	cfg.InsecureSkipVerify = true //nolint:gosec // verified in VerifyConnection
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		// An address by IP sends no server name: its certificate is not
		// verified by name, so only the pinned one is accepted there.
		c := Check(cs.PeerCertificates, cs.ServerName, roots, pin, time.Now())
		if cs.ServerName == "" {
			c.Trusted = false
		}
		if c.Trusted || c.Pinned {
			return nil
		}
		return &Error{Cert: c, Pin: pin}
	}
	return cfg
}

// Cert is a certificate a server presented, as a Test shows it: what it
// says, its fingerprint, and how it fared.
type Cert struct {
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	SHA256    string    `json:"sha256"`
	// Names are the host names and addresses it is for.
	Names      []string `json:"names,omitempty"`
	SelfSigned bool     `json:"selfSigned,omitempty"`
	Expired    bool     `json:"expired,omitempty"`
	// Trusted says normal verification succeeded: the system's CAs or
	// the private CA, the name, the dates.
	Trusted bool `json:"trusted"`
	// Pinned says it is the pinned certificate; PinnedWas is the pin when
	// it is another (the certificate changed).
	Pinned    bool   `json:"pinned,omitempty"`
	PinnedWas string `json:"pinnedWas,omitempty"`
	// Trustable says it may be pinned: it fails only because its chain is
	// not trusted (an unknown CA, or self-signed). A certificate of a
	// trusted CA for another name is not.
	Trustable bool `json:"trustable"`
	// Problem is why normal verification failed.
	Problem string `json:"problem,omitempty"`
}

// OK says a connection presenting it is accepted.
func (c Cert) OK() bool { return c.Trusted || c.Pinned }

// Check checks a presented chain (the leaf first) against roots (nil is
// the system's), the name the client asked for and a pin.
func Check(chain []*x509.Certificate, name string, roots *x509.CertPool, pin string, now time.Time) Cert {
	if len(chain) == 0 {
		return Cert{Problem: "the server presented no certificate"}
	}
	leaf := chain[0]
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	c := Cert{
		Subject:   leaf.Subject.String(),
		Issuer:    leaf.Issuer.String(),
		NotBefore: leaf.NotBefore.UTC(),
		NotAfter:  leaf.NotAfter.UTC(),
		SHA256:    Fingerprint(leaf.Raw),
		Names:     append([]string(nil), leaf.DNSNames...),
		Expired:   now.After(leaf.NotAfter) || now.Before(leaf.NotBefore),
	}
	for _, ip := range leaf.IPAddresses {
		c.Names = append(c.Names, ip.String())
	}
	c.SelfSigned = bytes.Equal(leaf.RawIssuer, leaf.RawSubject) && leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: inter, CurrentTime: now})
	c.Trusted = err == nil
	if err != nil {
		c.Problem = err.Error()
	}
	if pin != "" {
		if c.SHA256 == pin {
			c.Pinned = true
		} else {
			c.PinnedWas = pin
		}
	}
	if !c.Trusted {
		// Only an untrusted chain may be pinned: checked at a time the
		// leaf is valid and without the name, the chain still fails for
		// its authority.
		at := now
		if at.After(leaf.NotAfter) {
			at = leaf.NotAfter
		}
		if at.Before(leaf.NotBefore) {
			at = leaf.NotBefore
		}
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at})
		var ua x509.UnknownAuthorityError
		c.Trustable = errors.As(err, &ua)
	}
	return c
}

// Error is a connection refused by a pinned connection's verification:
// the certificate presented is neither trusted nor the pinned one.
type Error struct {
	Cert Cert
	Pin  string
}

func (e *Error) Error() string {
	if e.Pin != "" && e.Cert.Trustable {
		return fmt.Sprintf("the certificate changed (was %s, now %s)", e.Pin, e.Cert.SHA256)
	}
	if e.Cert.Problem != "" {
		return e.Cert.Problem
	}
	return "the certificate is not trusted"
}

// Changed is a pinned certificate's change, for a Test's text: "" when it
// did not change.
func (c Cert) Changed() string {
	if c.PinnedWas == "" || c.Trusted || !c.Trustable {
		return ""
	}
	return fmt.Sprintf("the certificate changed (was %s, now %s)", c.PinnedWas, c.SHA256)
}
