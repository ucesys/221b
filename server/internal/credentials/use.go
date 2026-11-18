package credentials

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Use is a credential resolved for one use at one address (CR-04): what
// a request carries. It is sent only to addresses under the credential's
// endpoint: Apply refuses any other, and Resolve refuses a target outside
// it.
type Use struct {
	ID, Name, Kind string
	// Method is how it goes: SendBearer, SendBasic, SendHeader, SendQuery
	// or SendCookie for a token (a GitHub App's or an OAuth2 client's
	// minted one too), SendBasic for a user and password, and "sigv4",
	// "ssh", "mtls" or "none" for the others.
	Method   string
	SendAs   SendAs
	Username string
	// Token is what an HTTP method sends: the token or the password.
	Token string
	// An access key's parts and region.
	AccessKeyID, SecretAccessKey, SessionToken, Region string
	// An SSH key, its passphrase and the host key it trusts.
	PrivateKey, Passphrase, HostKey string
	CAPEM                           string
	cert                            *tls.Certificate
	ep                              Endpoint
}

// Methods other than the HTTP ones.
const (
	MethodSigV4 = "sigv4"
	MethodSSH   = "ssh"
	MethodMTLS  = "mtls"
	MethodNone  = "none"
)

// Resolve makes a credential's use for an address (CR-04): the address
// must be under its endpoint, and the credential must serve the use. A
// GitHub App's installation token and an OAuth2 client's bearer are
// fetched here, and kept until shortly before they expire.
func (s *Service) Resolve(ctx context.Context, id, use, target string) (*Use, error) {
	c, err := s.CheckChoice(ctx, id, use, target)
	if err != nil {
		return nil, err
	}
	sec, err := s.secret(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.resolve(ctx, c, sec)
}

func (s *Service) resolve(ctx context.Context, c Credential, sec Secret) (*Use, error) {
	// A credential restored without its secret, or one that no longer
	// reads, is not sent half-made.
	cp := c
	if err := cp.Check(sec); err != nil {
		return nil, fmt.Errorf("the credential %s cannot be used (%s): give its secret again in Settings › Credentials", c.Name, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	}
	ep, err := ParseEndpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	u := &Use{ID: c.ID, Name: c.Name, Kind: c.Kind, SendAs: c.SendAs, Method: c.SendAs.Method, CAPEM: c.CAPEM, ep: ep}
	if u.Method == "" {
		u.Method = SendBearer
	}
	if u.Method == SendBasic {
		u.Username = c.SendAs.Username
	}
	switch c.Kind {
	case KindToken:
		u.Token = sec.Token
	case KindUserPass:
		u.Method, u.Username, u.Token = SendBasic, c.Username, sec.Password
	case KindAccessKey:
		u.Method, u.AccessKeyID, u.SecretAccessKey, u.SessionToken, u.Region = MethodSigV4, sec.AccessKeyID, sec.SecretAccessKey, sec.SessionToken, c.Region
	case KindSSH:
		u.Method, u.PrivateKey, u.Passphrase, u.HostKey = MethodSSH, sec.PrivateKey, sec.Passphrase, c.HostKey
	case KindClientCert:
		cert, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(sec.KeyPEM))
		if err != nil {
			return nil, fmt.Errorf("the client certificate of %s cannot be used: %w", c.Name, err)
		}
		u.Method, u.cert = MethodMTLS, &cert
	case KindGitHubApp, KindOAuth2:
		tok, err := s.mint(ctx, c, sec)
		if err != nil {
			return nil, err
		}
		u.Token = tok
	case KindNone:
		u.Method = MethodNone
	}
	if !c.Expires.IsZero() && s.o.Now().After(c.Expires) {
		s.o.Logger.Warn("a credential past its expiry is used", "credential", c.Name, "expired", c.Expires.Format(time.DateOnly))
	}
	return u, nil
}

// Covers says whether the use may go to an address.
func (u *Use) Covers(target string) bool { return u != nil && u.ep.Covers(target) }

// Apply puts the credential on a request, which must be to an address
// under the endpoint (CR-04).
func (u *Use) Apply(req *http.Request) error {
	if u == nil {
		return nil
	}
	if !u.ep.Covers(req.URL.String()) {
		return fmt.Errorf("%w: %s is for %s, not %s", ErrOutside, u.Name, u.ep, req.URL.Host)
	}
	switch u.Method {
	case SendBearer:
		req.Header.Set("Authorization", "Bearer "+u.Token)
	case SendBasic:
		req.SetBasicAuth(u.Username, u.Token)
	case SendHeader:
		req.Header.Set(u.SendAs.Header, u.SendAs.Prefix+u.Token)
	case SendQuery:
		q := req.URL.Query()
		q.Set(u.SendAs.Param, u.Token)
		req.URL.RawQuery = q.Encode()
	case SendCookie:
		req.AddCookie(&http.Cookie{Name: u.SendAs.Cookie, Value: u.Token})
	}
	return nil
}

// Signer is Apply for a request that may leave the endpoint (a crawl's
// links, a redirect): it signs the requests under it and leaves the
// others as they are.
func (u *Use) Signer() func(*http.Request) {
	if u == nil {
		return nil
	}
	return func(r *http.Request) {
		if u.ep.Covers(r.URL.String()) {
			_ = u.Apply(r)
		}
	}
}

// Basic is a user and a password or token sent as Basic.
func (u *Use) Basic() (user, password string, ok bool) {
	if u == nil || u.Method != SendBasic {
		return "", "", false
	}
	return u.Username, u.Token, true
}

// Bearer is a token sent as Bearer.
func (u *Use) Bearer() (string, bool) {
	if u == nil || u.Method != SendBearer {
		return "", false
	}
	return u.Token, true
}

// TLS is the TLS a request to the endpoint needs: the private CA, the
// client certificate; nil when neither.
func (u *Use) TLS() *tls.Config {
	if u == nil || u.cert == nil && strings.TrimSpace(u.CAPEM) == "" {
		return nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(u.CAPEM) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(u.CAPEM))
		cfg.RootCAs = pool
	}
	if u.cert != nil {
		cfg.Certificates = []tls.Certificate{*u.cert}
	}
	return cfg
}

// Certificate is the client certificate, nil for none.
func (u *Use) Certificate() *tls.Certificate {
	if u == nil {
		return nil
	}
	return u.cert
}

// Values are the secret values the use holds, for scrubbing.
func (u *Use) Values() []string {
	if u == nil {
		return nil
	}
	var out []string
	for _, v := range []string{u.Token, u.SecretAccessKey, u.AccessKeyID, u.SessionToken, u.PrivateKey, u.Passphrase} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Text says how the use signs in, never with a value: "Bearer", "Basic as
// x-access-token", "the header X-API-Key".
func (u *Use) Text() string {
	if u == nil {
		return "nothing"
	}
	switch u.Method {
	case SendBearer:
		return "an Authorization header with Bearer"
	case SendBasic:
		return "an Authorization header with Basic for " + u.Username
	case SendHeader:
		return "the header " + u.SendAs.Header
	case SendQuery:
		return "the query parameter " + u.SendAs.Param
	case SendCookie:
		return "the cookie " + u.SendAs.Cookie
	case MethodSigV4:
		return "an AWS SigV4 signature"
	case MethodSSH:
		return "an SSH key"
	case MethodMTLS:
		return "a client certificate"
	}
	return "nothing (the pod's identity)"
}

// hostOf is an address's host, for messages.
func hostOf(target string) string {
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return u.Host
	}
	return target
}
