package knowledge

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/ucesys/221b/server/internal/scrub"
)

// Testing a source (KN-25, KN-26). Settings › Knowledge's source dialog
// tests the values as typed, before they are saved: a new source's, or a
// saved one's with what was changed in its dialog. A credential left blank
// on Edit is the saved one, read here by the source's id and never sent
// back. A test lists the source's items without reading them, changes
// nothing (no audit record) and never puts a credential in its answer.
// It first connects to the source's address within connectTimeout (the
// dial, then the TLS handshake), so an address that does not answer is
// said plainly, naming host:port, rather than as the client's error; the
// whole test takes at most testTimeout.

// connectTimeout bounds the dial, and then the TLS handshake, of a test;
// testTimeout bounds the whole test.
const (
	connectTimeout = 10 * time.Second
	testTimeout    = 30 * time.Second
)

// SourceTest is what a source's Test found: how many items it lists, the
// first few of them, and what it skips and why (KN-25).
type SourceTest struct {
	Items      int      `json:"items"`
	Sample     []string `json:"sample,omitempty"`
	Skipped    int      `json:"skipped,omitempty"`
	SkippedWhy string   `json:"skippedWhy,omitempty"`
}

// testSample is how many items a Test names.
const testSample = 5

// TestSource tests a source as saved, with its saved credential.
func (s *Service) TestSource(ctx context.Context, c Caller, id string) (SourceTest, error) {
	if !c.SuperAdmin {
		return SourceTest{}, wrapf(ErrDenied, "only a SuperAdmin tests Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return SourceTest{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return SourceTest{}, err
	}
	x, ok := set.source(id)
	if !ok {
		return SourceTest{}, wrapf(ErrNotFound, "no source %q", id)
	}
	keys, err := s.creds(ctx, sourceKey(x.ID))
	if err != nil {
		return SourceTest{}, err
	}
	return s.testSource(ctx, x, keys)
}

// TestSourceAsTyped tests a source's values as typed in its dialog, not
// saved (KN-26). saved is the id of the source being edited, "" for a new
// one: what ch leaves out (a blank field) is that source's saved
// credential.
func (s *Service) TestSourceAsTyped(ctx context.Context, c Caller, x Source, ch CredsChange, saved string) (SourceTest, error) {
	if !c.SuperAdmin {
		return SourceTest{}, wrapf(ErrDenied, "only a SuperAdmin tests Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return SourceTest{}, err
	}
	var have Creds
	if saved != "" {
		set, err := s.Settings(ctx)
		if err != nil {
			return SourceTest{}, err
		}
		old, ok := set.source(saved)
		if !ok {
			return SourceTest{}, wrapf(ErrNotFound, "no source %q", saved)
		}
		if old.Kind != x.Kind {
			return SourceTest{}, invalid("a source keeps its kind; add a new one instead")
		}
		if have, err = s.creds(ctx, sourceKey(saved)); err != nil {
			return SourceTest{}, err
		}
	}
	x = x.normalized()
	// A test needs no id or name of its own; they are checked when saved.
	x.ID = cmp.Or(saved, "new-source")
	if x.Name == "" {
		x.Name = "the new source"
	}
	if err := x.Validate(); err != nil {
		return SourceTest{}, err
	}
	keys := ch.apply(have)
	switch {
	case x.Kind != SourceConfluence && x.Kind != SourceGit && x.Kind != SourceS3:
		return SourceTest{}, invalid("only a Confluence space, a git repository or an S3 bucket is tested")
	case x.Kind == SourceConfluence && keys.Token == "":
		return SourceTest{}, invalid("type the API token to test")
	case x.Kind == SourceS3 && x.Credentials == CredentialsKeys && (keys.AccessKeyID == "" || keys.SecretAccessKey == ""):
		return SourceTest{}, invalid("give the access key's ID and secret to test, or use the pod's identity")
	}
	return s.testSource(ctx, x, keys)
}

// testSource connects to a source, lists its items and says what it
// found, or what is wrong without a credential in it.
func (s *Service) testSource(ctx context.Context, x Source, keys Creds) (SourceTest, error) {
	out, err := s.listForTest(ctx, x, keys)
	if err != nil {
		var secrets []string
		for _, v := range []string{keys.Token, keys.AccessKeyID, keys.SecretAccessKey} {
			if v != "" {
				secrets = append(secrets, v)
			}
		}
		if msg := scrub.Known(err.Error(), secrets); msg != err.Error() {
			return SourceTest{}, errors.New(msg)
		}
		return SourceTest{}, err
	}
	return out, nil
}

func (s *Service) listForTest(ctx context.Context, x Source, keys Creds) (SourceTest, error) {
	tctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	if err := s.reach(tctx, x); err != nil {
		return SourceTest{}, err
	}
	rd, err := s.readerWith(x, keys)
	if err != nil {
		return SourceTest{}, err
	}
	items, _, err := rd.List(tctx, time.Time{})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || tctx.Err() != nil {
			where := "The source"
			if u := sourceAddr(x); u != nil {
				where = u.Host
			}
			return SourceTest{}, fmt.Errorf("%s took the connection but did not list its items within %d s: %v", where, int(testTimeout/time.Second), err)
		}
		return SourceTest{}, err
	}
	out := SourceTest{Items: len(items)}
	for _, it := range items[:min(len(items), testSample)] {
		// A file's path says more than its name; a page's title more than
		// its number.
		if x.Kind == SourceS3 || x.Kind == SourceGit {
			out.Sample = append(out.Sample, it.ID)
		} else {
			out.Sample = append(out.Sample, it.Title)
		}
	}
	if sk, ok := rd.(skipper); ok {
		out.Skipped, out.SkippedWhy = sk.Skipped()
	}
	return out, nil
}

// sourceAddr is the address a source is read from, its port always
// named: a Confluence site, a repository, or a bucket's endpoint (with
// the bucket in the host name unless path-style). Nil when it has none
// to connect to.
func sourceAddr(x Source) *url.URL {
	var raw string
	switch x.Kind {
	case SourceConfluence:
		raw = x.BaseURL
	case SourceGit:
		raw = x.URL
	case SourceS3:
		raw = x.Endpoint
		if raw == "" {
			raw = "https://s3." + cmp.Or(x.Region, "us-east-1") + ".amazonaws.com"
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	host := u.Hostname()
	if x.Kind == SourceS3 && !x.PathStyle {
		host = x.Bucket + "." + host
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	u.Host = net.JoinHostPort(host, port)
	return u
}

// reach connects to a source's address and, for https, finishes the TLS
// handshake, each within connectTimeout. Through a proxy the console
// does not connect to the source itself, and nothing is checked here.
func (s *Service) reach(ctx context.Context, x Source) error {
	u := sourceAddr(x)
	if u == nil {
		return nil
	}
	if p, err := http.ProxyFromEnvironment(&http.Request{URL: u}); err != nil || p != nil {
		return nil
	}
	dial := s.dial
	if dial == nil {
		d := &net.Dialer{Timeout: connectTimeout}
		dial = d.DialContext
	}
	dctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, err := dial(dctx, "tcp", u.Host)
	if err != nil {
		return connectError(x, u, err)
	}
	defer conn.Close()
	if u.Scheme != "https" {
		return nil
	}
	tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), RootCAs: s.rootsFor(x), MinVersion: tls.VersionTLS12})
	hctx, cancelTLS := context.WithTimeout(ctx, connectTimeout)
	defer cancelTLS()
	if err := tc.HandshakeContext(hctx); err != nil {
		return tlsError(u.Host, err)
	}
	return nil
}

// rootsFor are the certificates the source's reader trusts: the system's
// with the source's private CA, or those of the console's client (nil,
// the system's, but for tests).
func (s *Service) rootsFor(x Source) *x509.CertPool {
	if strings.TrimSpace(x.CAPEM) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(x.CAPEM))
		return pool
	}
	if x.Kind != SourceGit && s.o.HTTP != nil {
		if tr, ok := s.o.HTTP.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
			return tr.TLSClientConfig.RootCAs
		}
	}
	return nil
}

// connectError says why a source's address could not be reached, naming
// host:port and what to check.
func connectError(x Source, u *url.URL, err error) error {
	check := "Check the address and port"
	if x.Kind == SourceS3 && x.Endpoint != "" {
		check += " (for MinIO, the S3 API is not the 9001 web console)"
	}
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.As(err, &dns):
		return fmt.Errorf("Could not find %s: the name does not resolve. Check the address.", u.Hostname())
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("Could not connect to %s: nothing listens there (connection refused). %s.", u.Host, check)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("Could not connect to %s within %d s. %s.", u.Host, int(connectTimeout/time.Second), check)
	}
	return fmt.Errorf("Could not connect to %s: %v. %s.", u.Host, err, check)
}

// tlsError says why the TLS handshake with a source failed.
func tlsError(addr string, err error) error {
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var ne net.Error
	switch {
	case errors.As(err, &verify), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalidCert):
		return fmt.Errorf("%s has a TLS certificate the console does not trust: set the private CA in this dialog, or check the address (%v).", addr, err)
	case errors.As(err, &record):
		return fmt.Errorf("%s does not answer in TLS: if it serves plain HTTP, the address starts with http://; otherwise check the port.", addr)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("%s took the connection but did not finish the TLS handshake within %d s. Check that this port serves HTTPS.", addr, int(connectTimeout/time.Second))
	}
	return fmt.Errorf("The TLS handshake with %s failed: %v. If it uses a private CA, set it in this dialog.", addr, err)
}
