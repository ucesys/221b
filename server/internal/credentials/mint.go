package credentials

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// A GitHub App's installation token (CR-02): the console signs a JWT with
// the App's private key, which never leaves it, and asks GitHub's API for
// the installation's token, kept until five minutes before it expires. An
// OAuth2 client's bearer is fetched at its token URL with the client
// credentials grant and kept the same way.

// mintMargin is how long before it expires a minted token is fetched
// again.
const mintMargin = 5 * time.Minute

func (s *Service) mint(ctx context.Context, c Credential, sec Secret) (string, error) {
	s.mu.Lock()
	m, ok := s.minted[c.ID]
	s.mu.Unlock()
	now := s.o.Now()
	if ok && m.changed.Equal(c.Changed) && now.Add(mintMargin).Before(m.expires) {
		return m.token, nil
	}
	var (
		tok string
		exp time.Time
		err error
	)
	switch c.Kind {
	case KindGitHubApp:
		tok, exp, err = s.githubToken(ctx, c, sec)
	case KindOAuth2:
		tok, exp, err = s.oauth2Token(ctx, c, sec)
	default:
		return "", fmt.Errorf("%s mints no token", c.Kind)
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.minted[c.ID] = minted{token: tok, expires: exp, changed: c.Changed}
	s.mu.Unlock()
	return tok, nil
}

// GitHubAPI is the API of the GitHub an endpoint is on: api.github.com
// for github.com, api.SUBDOMAIN.ghe.com for GitHub Enterprise Cloud with
// data residency (GHE.com, credentials-unver-4), https://HOST/api/v3 for
// GitHub Enterprise Server, on its port. Tests set APIOverride.
func GitHubAPI(endpoint string) string {
	if APIOverride != "" {
		return APIOverride
	}
	ep, err := ParseEndpoint(endpoint)
	if err != nil {
		return "https://api.github.com"
	}
	switch {
	case ep.Host == "github.com" || ep.Host == "www.github.com" || ep.Host == "api.github.com":
		return "https://api.github.com"
	case strings.HasSuffix(ep.Host, ".ghe.com") && !strings.HasPrefix(ep.Host, "api."):
		return "https://api." + ep.Host
	case strings.HasSuffix(ep.Host, ".ghe.com"):
		return "https://" + ep.Host
	}
	return "https://" + ep.Host + "/api/v3"
}

// APIOverride replaces the GitHub API's address (tests).
var APIOverride string

func rsaKey(pemText string) (*rsa.PrivateKey, error) {
	b := pemBlock(pemText)
	if b == nil {
		return nil, invalid("the App's private key is not PEM (the .pem file GitHub gave)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, invalid("the App's private key cannot be read")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, invalid("the App's private key is not an RSA key")
	}
	return rk, nil
}

// appJWT is the App's JWT: RS256, issued a minute ago, for nine minutes.
func appJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (s *Service) githubToken(ctx context.Context, c Credential, sec Secret) (string, time.Time, error) {
	key, err := rsaKey(sec.PrivateKey)
	if err != nil {
		return "", time.Time{}, err
	}
	jwt, err := appJWT(c.AppID, key, s.o.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	api := strings.TrimRight(GitHubAPI(c.Endpoint), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/app/installations/"+c.InstallationID+"/access_tokens", nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := s.clientFor(c, req.URL.String()).Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("asking GitHub for %s's installation token: %w", c.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode/100 != 2 {
		return "", time.Time{}, fmt.Errorf("GitHub refused %s's installation token (%s): check the App's id, the installation's id and the private key", c.Name, res.Status)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(b, &out) != nil || out.Token == "" {
		return "", time.Time{}, errors.New("GitHub's answer held no installation token")
	}
	if out.ExpiresAt.IsZero() {
		out.ExpiresAt = s.o.Now().Add(time.Hour)
	}
	return out.Token, out.ExpiresAt, nil
}

func (s *Service) oauth2Token(ctx context.Context, c Credential, sec Secret) (string, time.Time, error) {
	cfg := clientcredentials.Config{ClientID: c.ClientID, ClientSecret: sec.ClientSecret, TokenURL: c.TokenURL}
	if c.Scope != "" {
		cfg.Scopes = strings.Fields(c.Scope)
	}
	// The client secret goes to the token URL alone: a redirect is not
	// followed (CR-04).
	hc := *s.clientFor(c, c.TokenURL)
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &hc)
	tok, err := cfg.Token(ctx)
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil {
			return "", time.Time{}, fmt.Errorf("the token URL refused %s's client credentials (%s)", c.Name, re.Response.Status)
		}
		return "", time.Time{}, fmt.Errorf("fetching %s's token: %w", c.Name, err)
	}
	exp := tok.Expiry
	if exp.IsZero() {
		exp = s.o.Now().Add(mintMargin + 5*time.Minute)
	}
	return tok.AccessToken, exp, nil
}

// clientFor is the service's client for an address with the credential's
// private CA and pinned certificate. The pin is the endpoint's: it is
// accepted under any name, so it is kept only for an address on the
// endpoint's host (credentials#10); an OAuth2 token URL or a GitHub API
// elsewhere is verified as any host is, with the private CA, which still
// checks names.
func (s *Service) clientFor(c Credential, target string) *http.Client {
	u := &Use{CAPEM: c.CAPEM, Pin: c.PinnedCert.Fingerprint()}
	if ep, err := ParseEndpoint(c.Endpoint); err != nil || !ep.sameHost(target) {
		u.Pin = ""
	}
	if u.Trust().Empty() {
		return s.o.HTTP
	}
	return withTLS(s.o.HTTP, u.TLS())
}

// withTLS is a client with its transport's TLS replaced, when the
// transport is an *http.Transport; the client as it is otherwise (tests).
func withTLS(hc *http.Client, cfg *tls.Config) *http.Client {
	if cfg == nil {
		return hc
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	tr, ok := base.(*http.Transport)
	if !ok {
		return hc
	}
	tr = tr.Clone()
	tr.TLSClientConfig = cfg
	out := *hc
	out.Transport = tr
	return &out
}

// ---------- where the console connects ----------

// metadataAddrs are the clouds' metadata services outside the link-local
// range.
var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("100.100.100.200"): true,
	netip.MustParseAddr("168.63.129.16"):   true,
	netip.MustParseAddr("192.0.0.192"):     true,
	netip.MustParseAddr("fd00:ec2::254"):   true,
	netip.MustParseAddr("fd20:ce::254"):    true,
}

// refused says whether the console does not connect to an address with a
// credential: its own pod and node, link-local, the metadata services.
func refused(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || metadataAddrs[ip]
}

func guardedControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if refused(ip) {
		return fmt.Errorf("the console does not connect to %s (its own pod or node, link-local or a metadata service)", ip)
	}
	return nil
}

// guardedClient connects anywhere but where guardedControl refuses,
// straight (no proxy), with cfg's TLS.
func guardedClient(cfg *tls.Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second, Control: guardedControl}).DialContext
	tr.TLSClientConfig = cfg
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
