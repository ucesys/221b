// Package credentials keeps Administration › Credentials (0.12.1, CR-01 to
// CR-14): who the console is when it signs in somewhere, and where that
// applies. A credential has a name, a kind, an endpoint (the address, or
// the prefix of the addresses, it is ever sent to), a description, when it
// expires and a private CA; its secret part is written once, kept in the
// Secret <release>-credentials, and never leaves the server: no page, log,
// prompt or backup in clear. Applications, Helm repositories and Mycroft's
// sources name a credential by its id; each use resolves it for one
// address and is refused anywhere outside the endpoint.
package credentials

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// The kinds of credential (CR-02).
const (
	KindToken      = "token"
	KindUserPass   = "userpass"
	KindAccessKey  = "accesskey"
	KindSSH        = "ssh"
	KindGitHubApp  = "githubapp"
	KindClientCert = "clientcert"
	KindOAuth2     = "oauth2"
	KindNone       = "none"
)

// Kinds are the kinds, in the order the page offers them.
var Kinds = []string{KindToken, KindUserPass, KindAccessKey, KindSSH, KindGitHubApp, KindClientCert, KindOAuth2, KindNone}

// How an HTTP credential is sent (CR-03).
const (
	SendBearer = "bearer"
	SendBasic  = "basic"
	SendHeader = "header"
	SendQuery  = "query"
	SendCookie = "cookie"
)

// What a credential is used for: each consumer takes only the kinds and
// the ways of sending it supports (CR-09).
const (
	// UseGit is a git repository over HTTPS: Basic or Bearer.
	UseGit = "git"
	// UseHelm is a Helm repository or an OCI registry: Basic, or a token
	// sent as Bearer and exchanged at the registry's token service.
	UseHelm = "helm"
	// UseS3 is a bucket: an access key (SigV4) or the pod's identity.
	UseS3 = "s3"
	// UseConfluenceCloud signs in with Basic (the account's email and an
	// API token), UseConfluenceDC with Bearer (a personal access token).
	UseConfluenceCloud = "confluence-cloud"
	UseConfluenceDC    = "confluence-dc"
	// UseHTTP is a web site or a live source: any way of sending.
	UseHTTP = "http"
)

// Uses are the uses, for checking what a request names.
var Uses = []string{UseGit, UseHelm, UseS3, UseConfluenceCloud, UseConfluenceDC, UseHTTP}

// SendAs says how an HTTP credential's token goes on a request.
type SendAs struct {
	// Method is SendBearer (the default), SendBasic, SendHeader,
	// SendQuery or SendCookie.
	Method string `json:"method,omitempty"`
	// Username goes with the token as Basic's user: x-access-token for
	// GitHub, oauth2 for GitLab.
	Username string `json:"username,omitempty"`
	// Header and Prefix are a custom header's name and what precedes the
	// token in it (X-API-Key; PRIVATE-TOKEN; Authorization with "token ").
	Header string `json:"header,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	// Param is the query parameter's name, Cookie the cookie's.
	Param  string `json:"param,omitempty"`
	Cookie string `json:"cookie,omitempty"`
}

// Test is a credential's last test (CR-07): when, against what, how it
// was sent (never its value) and what the far end answered.
type Test struct {
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Target string    `json:"target,omitempty"`
	Method string    `json:"method,omitempty"`
	Status int       `json:"status,omitempty"`
	Text   string    `json:"text"`
	// Suggest is a way of sending the server's WWW-Authenticate asks
	// for, after a 401.
	Suggest string `json:"suggest,omitempty"`
	By      string `json:"by,omitempty"`
}

// Credential is one credential without its secret part.
type Credential struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Endpoint    string    `json:"endpoint"`
	Description string    `json:"description,omitempty"`
	Expires     time.Time `json:"expires,omitzero"`
	// CAPEM is a private CA trusted for the endpoint's TLS.
	CAPEM  string `json:"caPEM,omitempty"`
	SendAs SendAs `json:"sendAs,omitzero"`
	// The parts of a kind that are not secret: the user of a user and
	// password, an access key's region, an SSH key's host key, a GitHub
	// App's ids, an OAuth2 client's token URL, id and scope, a client
	// certificate.
	Username       string `json:"username,omitempty"`
	Region         string `json:"region,omitempty"`
	HostKey        string `json:"hostKey,omitempty"`
	AppID          string `json:"appId,omitempty"`
	InstallationID string `json:"installationId,omitempty"`
	TokenURL       string `json:"tokenURL,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	Scope          string `json:"scope,omitempty"`
	CertPEM        string `json:"certPEM,omitempty"`
	// Set names the secret parts kept, never their values.
	Set      []string  `json:"set,omitempty"`
	LastTest *Test     `json:"lastTest,omitempty"`
	Created  time.Time `json:"created,omitzero"`
	// CreatedBy and ChangedBy name who; Changed is when the credential,
	// or its secret, last changed.
	CreatedBy string    `json:"createdBy,omitempty"`
	Changed   time.Time `json:"changed,omitzero"`
	ChangedBy string    `json:"changedBy,omitempty"`
	// From says what the upgrade made it from (CR-12).
	From []string `json:"from,omitempty"`
}

// Secret is a credential's secret part: what each kind holds.
type Secret struct {
	Token           string `json:"token,omitempty"`
	Password        string `json:"password,omitempty"`
	AccessKeyID     string `json:"accessKeyId,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	SessionToken    string `json:"sessionToken,omitempty"`
	PrivateKey      string `json:"privateKey,omitempty"`
	Passphrase      string `json:"passphrase,omitempty"`
	ClientSecret    string `json:"clientSecret,omitempty"`
	KeyPEM          string `json:"keyPEM,omitempty"`
}

// values are the secret's values, for scrubbing what is written.
func (s Secret) values() []string {
	var out []string
	for _, v := range []string{s.Token, s.Password, s.AccessKeyID, s.SecretAccessKey, s.SessionToken, s.PrivateKey, s.Passphrase, s.ClientSecret, s.KeyPEM} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parts names the parts set.
func (s Secret) parts() []string {
	var out []string
	for _, p := range []struct {
		v, n string
	}{{s.Token, "token"}, {s.Password, "password"}, {s.AccessKeyID, "accessKeyId"}, {s.SecretAccessKey, "secretAccessKey"}, {s.SessionToken, "sessionToken"}, {s.PrivateKey, "privateKey"}, {s.Passphrase, "passphrase"}, {s.ClientSecret, "clientSecret"}, {s.KeyPEM, "keyPEM"}} {
		if p.v != "" {
			out = append(out, p.n)
		}
	}
	return out
}

func (s Secret) empty() bool { return s == Secret{} }

// trimmed is the secret as typed, its one-line parts trimmed.
func (s Secret) trimmed() Secret {
	for _, p := range []*string{&s.Token, &s.Password, &s.AccessKeyID, &s.SecretAccessKey, &s.SessionToken, &s.Passphrase, &s.ClientSecret} {
		*p = strings.TrimSpace(*p)
	}
	s.PrivateKey, s.KeyPEM = strings.TrimSpace(s.PrivateKey), strings.TrimSpace(s.KeyPEM)
	return s
}

// ErrInvalid is a credential or a use that is not valid; ErrNotFound one
// that is not there; ErrInUse a delete of one that is used; ErrOutside a
// use outside its endpoint.
var (
	ErrInvalid  = errors.New("not valid")
	ErrNotFound = errors.New("no such credential")
	ErrInUse    = errors.New("the credential is in use")
	ErrOutside  = errors.New("the address is outside the credential's endpoint")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var (
	headerName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)
	paramName  = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)
	oneLine    = regexp.MustCompile(`^[^\r\n]*$`)
)

// checkSendAs checks how a token is sent and fills in the default.
func (c *Credential) checkSendAs() error {
	sa := &c.SendAs
	sa.Username, sa.Header, sa.Param, sa.Cookie = strings.TrimSpace(sa.Username), strings.TrimSpace(sa.Header), strings.TrimSpace(sa.Param), strings.TrimSpace(sa.Cookie)
	if sa.Method == "" {
		sa.Method = SendBearer
	}
	keep := SendAs{Method: sa.Method}
	switch sa.Method {
	case SendBearer:
	case SendBasic:
		if sa.Username == "" || len(sa.Username) > 200 || strings.ContainsAny(sa.Username, ":\r\n") {
			return invalid("Basic sends the token with a user name: give one without a colon (x-access-token for GitHub, oauth2 for GitLab)")
		}
		keep.Username = sa.Username
	case SendHeader:
		if !headerName.MatchString(sa.Header) {
			return invalid("give the header's name, such as X-API-Key or PRIVATE-TOKEN")
		}
		if len(sa.Prefix) > 40 || !oneLine.MatchString(sa.Prefix) {
			return invalid("the prefix is one line of up to 40 characters, such as \"token \"")
		}
		keep.Header, keep.Prefix = sa.Header, sa.Prefix
	case SendQuery:
		if !paramName.MatchString(sa.Param) {
			return invalid("give the query parameter's name, such as access_token")
		}
		keep.Param = sa.Param
	case SendCookie:
		if !paramName.MatchString(sa.Cookie) {
			return invalid("give the cookie's name")
		}
		keep.Cookie = sa.Cookie
	default:
		return invalid("a token is sent as bearer, basic, header, query or cookie")
	}
	c.SendAs = keep
	return nil
}

// sendsHTTP says whether the kind's token goes on HTTP requests as SendAs
// says.
func sendsHTTP(kind string) bool {
	return kind == KindToken || kind == KindGitHubApp || kind == KindOAuth2
}

// Check checks a credential as a SuperAdmin saves it, with the secret
// it will have (the kept one, or the one typed), and cleans it.
func (c *Credential) Check(sec Secret) error {
	c.Name, c.Endpoint, c.Description = strings.TrimSpace(c.Name), strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.Description)
	if c.Name == "" || len(c.Name) > 100 || !oneLine.MatchString(c.Name) {
		return invalid("give the credential a name of up to 100 characters")
	}
	if len(c.Description) > 500 {
		return invalid("the description is at most 500 characters")
	}
	ep, err := ParseEndpoint(c.Endpoint)
	if err != nil {
		return err
	}
	if strings.Contains(c.Endpoint, "?") {
		return invalid("an endpoint has no query")
	}
	c.Endpoint = ep.String()
	if strings.TrimSpace(c.CAPEM) != "" {
		if len(c.CAPEM) > 12<<10 || !strings.Contains(c.CAPEM, "-----BEGIN CERTIFICATE-----") {
			return invalid("the private CA is PEM certificates, at most 12 KiB")
		}
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CAPEM)) {
			return invalid("the private CA holds no certificate that can be read")
		}
	} else {
		c.CAPEM = ""
	}
	if sendsHTTP(c.Kind) {
		if err := c.checkSendAs(); err != nil {
			return err
		}
	} else {
		c.SendAs = SendAs{}
	}
	if c.Kind != KindUserPass {
		c.Username = ""
	}
	if c.Kind != KindAccessKey {
		c.Region = ""
	}
	if c.Kind != KindSSH {
		c.HostKey = ""
	}
	if c.Kind != KindGitHubApp {
		c.AppID, c.InstallationID = "", ""
	}
	if c.Kind != KindOAuth2 {
		c.TokenURL, c.ClientID, c.Scope = "", "", ""
	}
	if c.Kind != KindClientCert {
		c.CertPEM = ""
	}
	need := func(v, what string) error {
		if v == "" {
			return invalid("give the %s", what)
		}
		if len(v) > 16<<10 {
			return invalid("the %s is at most 16 KiB", what)
		}
		return nil
	}
	switch c.Kind {
	case KindToken:
		if err := need(sec.Token, "token"); err != nil {
			return err
		}
	case KindUserPass:
		c.Username = strings.TrimSpace(c.Username)
		if c.Username == "" || len(c.Username) > 200 || strings.ContainsAny(c.Username, ":\r\n") {
			return invalid("give the user name, without a colon")
		}
		if err := need(sec.Password, "password or token"); err != nil {
			return err
		}
	case KindAccessKey:
		c.Region = strings.TrimSpace(c.Region)
		if !regexp.MustCompile(`^[a-z0-9-]{0,40}$`).MatchString(c.Region) {
			return invalid("the region is a name such as eu-west-1")
		}
		if err := need(sec.AccessKeyID, "access key ID"); err != nil {
			return err
		}
		if err := need(sec.SecretAccessKey, "secret access key"); err != nil {
			return err
		}
	case KindSSH:
		c.HostKey = strings.TrimSpace(c.HostKey)
		if c.HostKey == "" {
			return invalid("an SSH key needs the server's host key (a known_hosts line or the key itself): the console never trusts a host on first use")
		}
		if _, err := ParseHostKey(c.HostKey); err != nil {
			return err
		}
		if err := need(sec.PrivateKey, "private key"); err != nil {
			return err
		}
		if _, err := sshSigner(sec); err != nil {
			return err
		}
	case KindGitHubApp:
		c.AppID, c.InstallationID = strings.TrimSpace(c.AppID), strings.TrimSpace(c.InstallationID)
		if !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(c.AppID) {
			return invalid("give the GitHub App's id, a number")
		}
		if !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(c.InstallationID) {
			return invalid("give the installation's id, a number (in the installation's settings address)")
		}
		if err := need(sec.PrivateKey, "App's private key"); err != nil {
			return err
		}
		if _, err := rsaKey(sec.PrivateKey); err != nil {
			return err
		}
	case KindClientCert:
		c.CertPEM = strings.TrimSpace(c.CertPEM)
		if err := need(c.CertPEM, "certificate"); err != nil {
			return err
		}
		if err := need(sec.KeyPEM, "certificate's private key"); err != nil {
			return err
		}
		if _, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(sec.KeyPEM)); err != nil {
			return invalid("the certificate and its key do not go together: %v", err)
		}
	case KindOAuth2:
		c.TokenURL, c.ClientID, c.Scope = strings.TrimSpace(c.TokenURL), strings.TrimSpace(c.ClientID), strings.TrimSpace(c.Scope)
		u, err := url.Parse(c.TokenURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return invalid("the token URL is an https:// address")
		}
		if c.ClientID == "" || len(c.ClientID) > 400 || !oneLine.MatchString(c.ClientID) {
			return invalid("give the client id")
		}
		if len(c.Scope) > 1000 || !oneLine.MatchString(c.Scope) {
			return invalid("the scope is one line")
		}
		if err := need(sec.ClientSecret, "client secret"); err != nil {
			return err
		}
	case KindNone:
	default:
		return invalid("a credential is token, userpass, accesskey, ssh, githubapp, clientcert, oauth2 or none")
	}
	return nil
}

// keep drops the parts of a secret its kind does not hold.
func keep(kind string, s Secret) Secret {
	switch kind {
	case KindToken:
		return Secret{Token: s.Token}
	case KindUserPass:
		return Secret{Password: s.Password}
	case KindAccessKey:
		return Secret{AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, SessionToken: s.SessionToken}
	case KindSSH:
		return Secret{PrivateKey: s.PrivateKey, Passphrase: s.Passphrase}
	case KindGitHubApp:
		return Secret{PrivateKey: s.PrivateKey}
	case KindClientCert:
		return Secret{KeyPEM: s.KeyPEM}
	case KindOAuth2:
		return Secret{ClientSecret: s.ClientSecret}
	}
	return Secret{}
}

// Supports says whether a credential serves a use, and why not.
func (c Credential) Supports(use string) error {
	method := c.SendAs.Method
	if method == "" {
		method = SendBearer
	}
	ok := false
	switch use {
	case UseGit:
		ok = c.Kind == KindUserPass || sendsHTTP(c.Kind) && (method == SendBasic || method == SendBearer)
	case UseHelm:
		ok = c.Kind == KindUserPass || (c.Kind == KindToken || c.Kind == KindGitHubApp) && (method == SendBasic || method == SendBearer)
	case UseS3:
		ok = c.Kind == KindAccessKey || c.Kind == KindNone
	case UseConfluenceCloud:
		ok = c.Kind == KindUserPass || c.Kind == KindToken && method == SendBasic
	case UseConfluenceDC:
		ok = (c.Kind == KindToken || c.Kind == KindOAuth2) && method == SendBearer
	case UseHTTP:
		ok = c.Kind == KindUserPass || c.Kind == KindClientCert || sendsHTTP(c.Kind)
	default:
		return invalid("no use %q", use)
	}
	if !ok {
		return invalid("%s (%s) cannot be used for %s: %s", c.Name, Describe(c), useText[use], useWants[use])
	}
	return nil
}

var useText = map[string]string{
	UseGit: "a git repository", UseHelm: "a Helm repository or OCI registry", UseS3: "a bucket",
	UseConfluenceCloud: "Confluence Cloud", UseConfluenceDC: "Confluence Data Center", UseHTTP: "a web site or live source",
}

var useWants = map[string]string{
	UseGit:             "it takes a token sent as Basic or Bearer, or a user and password",
	UseHelm:            "it takes a user and password, or a token sent as Basic or Bearer",
	UseS3:              "it takes an access key or the pod's identity",
	UseConfluenceCloud: "it takes the account's email and an API token, sent as Basic",
	UseConfluenceDC:    "it takes a personal access token sent as Bearer",
	UseHTTP:            "it takes a token, a user and password, a client certificate or an OAuth2 client",
}

// Describe says how a credential signs in, in words: never a value.
func Describe(c Credential) string {
	switch c.Kind {
	case KindToken, KindGitHubApp, KindOAuth2:
		what := map[string]string{KindToken: "a token", KindGitHubApp: "a GitHub App's installation token", KindOAuth2: "an OAuth2 client's token"}[c.Kind]
		return what + " " + MethodText(c.SendAs)
	case KindUserPass:
		return "user " + c.Username + " and a password, as Basic"
	case KindAccessKey:
		return "an access key (SigV4)"
	case KindSSH:
		return "an SSH key"
	case KindClientCert:
		return "a client certificate (mTLS)"
	case KindNone:
		return "no secret (the pod's identity)"
	}
	return c.Kind
}

// MethodText says how a token is sent: as Bearer, as Basic for user u, in
// the header H, in the query parameter p, in the cookie c.
func MethodText(sa SendAs) string {
	switch sa.Method {
	case SendBasic:
		return "as Basic for " + sa.Username
	case SendHeader:
		if sa.Prefix != "" {
			return fmt.Sprintf("in the header %s after %q", sa.Header, sa.Prefix)
		}
		return "in the header " + sa.Header
	case SendQuery:
		return "in the query parameter " + sa.Param
	case SendCookie:
		return "in the cookie " + sa.Cookie
	}
	return "as Bearer"
}

// Expiring says whether a credential expires within the warning's 14
// days (or has), at now.
func (c Credential) Expiring(now time.Time) bool {
	return !c.Expires.IsZero() && c.Expires.Sub(now) <= WarnBefore
}

// WarnBefore is how long before it expires a credential is warned about.
const WarnBefore = 14 * 24 * time.Hour

// ---------- endpoints ----------

// Endpoint is a credential's endpoint, parsed: a scheme or none, a host
// with its port when not the scheme's own, and a path prefix.
type Endpoint struct {
	Scheme string
	Host   string
	Path   string
}

func (e Endpoint) String() string {
	s := e.Host + e.Path
	if e.Scheme != "" {
		s = e.Scheme + "://" + s
	}
	return s
}

var defaultPort = map[string]string{"https": "443", "http": "80", "ssh": "22", "oci": "443", "s3": "443"}

var scpLike = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+):([^/][^:]*)$`)

// ParseEndpoint reads an address as the endpoint and the uses name them:
// https://github.com/org, github.com/org, oci://ghcr.io/org,
// ssh://git@host/org, git@host:org/repo.git.
func ParseEndpoint(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, invalid("give the endpoint: the address, or the start of the addresses, the credential is sent to (https://github.com/org)")
	}
	if len(raw) > 500 || strings.ContainsAny(raw, " \t\r\n") {
		return Endpoint{}, invalid("an endpoint is one address of at most 500 characters")
	}
	if !strings.Contains(raw, "://") {
		// git@host:org/repo.git, as git writes an SSH address.
		if m := scpLike.FindStringSubmatch(raw); m != nil && strings.Contains(raw, "@") {
			raw = "ssh://" + m[1] + "/" + m[2]
		}
	}
	scheme := ""
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = strings.ToLower(raw[:i])
	} else {
		raw = "none://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil && scheme != "ssh" {
		return Endpoint{}, invalid("%q is not an address such as https://github.com/org", strings.TrimPrefix(raw, "none://"))
	}
	switch scheme {
	case "", "https", "http", "ssh", "oci", "s3":
	default:
		return Endpoint{}, invalid("an endpoint is https://, http://, ssh://, oci:// or s3://, or a host without a scheme")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if host == "" {
		return Endpoint{}, invalid("the endpoint needs a host")
	}
	if port != "" && port == defaultPort[scheme] {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, ".git")
	return Endpoint{Scheme: scheme, Host: host, Path: p}, nil
}

// Covers says whether an address is under the endpoint: the same host
// and port, a scheme the endpoint allows (an endpoint without one allows
// any but plain http), and a path at or under its path.
func (e Endpoint) Covers(target string) bool {
	t, err := ParseEndpoint(target)
	if err != nil || t.Scheme == "" {
		return false
	}
	if t.Host != e.Host {
		return false
	}
	switch {
	case e.Scheme == "":
		if t.Scheme == "http" {
			return false
		}
	case e.Scheme == t.Scheme:
	case (e.Scheme == "oci" || e.Scheme == "https") && (t.Scheme == "oci" || t.Scheme == "https"):
		// An OCI registry is reached over https.
	default:
		return false
	}
	if e.Path == "" {
		return true
	}
	tp := strings.ReplaceAll(t.Path, ".git/", "/")
	return tp == e.Path || strings.HasPrefix(tp, e.Path+"/")
}

// Covers says whether a credential may be sent to an address.
func (c Credential) Covers(target string) bool {
	ep, err := ParseEndpoint(c.Endpoint)
	return err == nil && ep.Covers(target)
}

// ---------- keys ----------

// ParseHostKey reads a host key as a known_hosts line or an
// authorized_keys line (ssh-ed25519 AAAA…).
func ParseHostKey(s string) (ssh.PublicKey, error) {
	s = strings.TrimSpace(s)
	if _, _, k, _, _, err := ssh.ParseKnownHosts([]byte(s)); err == nil {
		return k, nil
	}
	if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s)); err == nil {
		return k, nil
	}
	return nil, invalid("the host key is not a known_hosts line or a public key (ssh-ed25519 AAAA…); ssh-keyscan HOST prints it")
}

func sshSigner(sec Secret) (ssh.Signer, error) {
	var (
		sg  ssh.Signer
		err error
	)
	if sec.Passphrase != "" {
		sg, err = ssh.ParsePrivateKeyWithPassphrase([]byte(sec.PrivateKey), []byte(sec.Passphrase))
	} else {
		sg, err = ssh.ParsePrivateKey([]byte(sec.PrivateKey))
	}
	if err != nil {
		var miss *ssh.PassphraseMissingError
		if errors.As(err, &miss) {
			return nil, invalid("the private key is encrypted: give its passphrase")
		}
		return nil, invalid("the private key cannot be read (OpenSSH or PEM)")
	}
	return sg, nil
}

// pemBlock is the first PEM block of a text.
func pemBlock(s string) *pem.Block {
	b, _ := pem.Decode([]byte(strings.TrimSpace(s)))
	return b
}
