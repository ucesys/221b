package certpin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// DialFunc connects; nil is a plain dialer.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// probeTimeout bounds a probe's dial and handshake.
const probeTimeout = 10 * time.Second

// ErrNotHTTPS is a probe of an address that is not https://.
var ErrNotHTTPS = errors.New("only an https:// address presents a certificate")

// Probe connects to an https:// address and checks the certificate it
// presents against base (nil is the system's CAs), t's private CA and
// pin, as a connection would, without sending anything: what a Test
// shows, and what Trust compares with the fingerprint it is given.
func Probe(ctx context.Context, dial DialFunc, address string, base *x509.CertPool, t Trust) (Cert, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return Cert{}, ErrNotHTTPS
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	return ProbeHost(ctx, dial, host, u.Hostname(), base, t)
}

// ProbeHost is Probe of host:port, asking for name.
func ProbeHost(ctx context.Context, dial DialFunc, hostport, name string, base *x509.CertPool, t Trust) (Cert, error) {
	if dial == nil {
		d := &net.Dialer{Timeout: probeTimeout}
		dial = d.DialContext
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", hostport)
	if err != nil {
		return Cert{}, fmt.Errorf("could not connect to %s: %w", hostport, err)
	}
	defer conn.Close()
	sni := name
	if net.ParseIP(name) != nil {
		sni = ""
	}
	// Only to read the certificate: Check verifies it below.
	tc := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // verified by Check
	if err := tc.HandshakeContext(ctx); err != nil {
		return Cert{}, fmt.Errorf("the TLS handshake with %s failed: %w", hostport, err)
	}
	return Check(tc.ConnectionState().PeerCertificates, name, Roots(base, t.CAPEM), t.Pin, time.Now()), nil
}
