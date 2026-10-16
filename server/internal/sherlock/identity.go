package sherlock

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Credentials for Amazon Bedrock and Vertex AI: a stored one, or the pod's
// own identity, so a console on EKS or GKE needs no key at all.

// Ambient is where the pod's identity comes from. Its zero value reads the
// usual environment, files and endpoints; tests point it elsewhere.
type Ambient struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	// STSURL is the AWS STS endpoint for web identities (IRSA).
	STSURL string
	// GoogleMetadataURL is the GKE metadata server's token address.
	GoogleMetadataURL string
	HTTP              *http.Client
}

func (a *Ambient) getenv(k string) string {
	if a != nil && a.Getenv != nil {
		return a.Getenv(k)
	}
	return os.Getenv(k)
}

func (a *Ambient) readFile(p string) ([]byte, error) {
	if a != nil && a.ReadFile != nil {
		return a.ReadFile(p)
	}
	return os.ReadFile(p)
}

func (a *Ambient) http() *http.Client {
	if a != nil && a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// ---------- AWS ----------

// AWSCredentials are what a SigV4 signature needs.
type AWSCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
}

type awsCreds struct {
	creds   AWSCredentials
	expires time.Time
}

// awsCredentials are the stored credentials (JSON), the environment's, a
// web identity's (IRSA) or EKS Pod Identity's, in that order. Temporary
// ones are kept until five minutes before they expire.
func (c *Client) awsCredentials(ctx context.Context, stored, region string) (AWSCredentials, error) {
	if strings.TrimSpace(stored) != "" {
		var k AWSCredentials
		if err := json.Unmarshal([]byte(stored), &k); err != nil || k.AccessKeyID == "" || k.SecretAccessKey == "" {
			return k, errors.New("the stored AWS credentials are not valid; save them again in Settings")
		}
		return k, nil
	}
	a := c.Ambient
	if id, secret := a.getenv("AWS_ACCESS_KEY_ID"), a.getenv("AWS_SECRET_ACCESS_KEY"); id != "" && secret != "" {
		return AWSCredentials{AccessKeyID: id, SecretAccessKey: secret, SessionToken: a.getenv("AWS_SESSION_TOKEN")}, nil
	}
	c.mu.Lock()
	cached := c.aws
	c.mu.Unlock()
	if cached != nil && c.now().Add(5*time.Minute).Before(cached.expires) {
		return cached.creds, nil
	}
	var (
		creds   AWSCredentials
		expires time.Time
		err     error
	)
	switch {
	case a.getenv("AWS_ROLE_ARN") != "" && a.getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "":
		creds, expires, err = c.webIdentity(ctx, region)
	case a.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "":
		creds, expires, err = c.podIdentity(ctx)
	default:
		return creds, errors.New("no AWS credentials: save an access key in Settings, or give the console's ServiceAccount an IAM role (IRSA or EKS Pod Identity)")
	}
	if err != nil {
		return creds, err
	}
	c.mu.Lock()
	c.aws = &awsCreds{creds: creds, expires: expires}
	c.mu.Unlock()
	return creds, nil
}

// webIdentity exchanges the projected ServiceAccount token for a role's
// credentials (IRSA): STS AssumeRoleWithWebIdentity, which needs no
// signature.
func (c *Client) webIdentity(ctx context.Context, region string) (AWSCredentials, time.Time, error) {
	a := c.Ambient
	tok, err := a.readFile(a.getenv("AWS_WEB_IDENTITY_TOKEN_FILE"))
	if err != nil {
		return AWSCredentials{}, time.Time{}, fmt.Errorf("reading the web identity token: %w", err)
	}
	sts := ""
	if a != nil {
		sts = a.STSURL
	}
	if sts == "" {
		r := a.getenv("AWS_REGION")
		if r == "" {
			r = region
		}
		if r == "" {
			r = "us-east-1"
		}
		sts = "https://sts." + r + ".amazonaws.com/"
	}
	q := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {a.getenv("AWS_ROLE_ARN")}, "RoleSessionName": {"221b-sherlock"}, "WebIdentityToken": {strings.TrimSpace(string(tok))}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, sts, strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http().Do(req)
	if err != nil {
		return AWSCredentials{}, time.Time{}, fmt.Errorf("asking AWS STS for the role's credentials: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return AWSCredentials{}, time.Time{}, fmt.Errorf("AWS STS refused the web identity (%d): %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var r struct {
		Result struct {
			Credentials struct {
				AccessKeyID     string    `xml:"AccessKeyId"`
				SecretAccessKey string    `xml:"SecretAccessKey"`
				SessionToken    string    `xml:"SessionToken"`
				Expiration      time.Time `xml:"Expiration"`
			} `xml:"Credentials"`
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if err := xml.Unmarshal(b, &r); err != nil || r.Result.Credentials.AccessKeyID == "" {
		return AWSCredentials{}, time.Time{}, errors.New("AWS STS answered without credentials")
	}
	k := r.Result.Credentials
	return AWSCredentials{AccessKeyID: k.AccessKeyID, SecretAccessKey: k.SecretAccessKey, SessionToken: k.SessionToken}, k.Expiration, nil
}

// podIdentity reads EKS Pod Identity's credentials from the agent.
func (c *Client) podIdentity(ctx context.Context) (AWSCredentials, time.Time, error) {
	a := c.Ambient
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI"), nil)
	if f := a.getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); f != "" {
		tok, err := a.readFile(f)
		if err != nil {
			return AWSCredentials{}, time.Time{}, fmt.Errorf("reading the pod identity token: %w", err)
		}
		req.Header.Set("Authorization", strings.TrimSpace(string(tok)))
	}
	res, err := a.http().Do(req)
	if err != nil {
		return AWSCredentials{}, time.Time{}, fmt.Errorf("asking EKS Pod Identity for credentials: %w", err)
	}
	defer res.Body.Close()
	var r struct {
		AccessKeyID     string    `json:"AccessKeyId"`
		SecretAccessKey string    `json:"SecretAccessKey"`
		Token           string    `json:"Token"`
		Expiration      time.Time `json:"Expiration"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&r) != nil || r.AccessKeyID == "" {
		return AWSCredentials{}, time.Time{}, fmt.Errorf("EKS Pod Identity answered %d without credentials", res.StatusCode)
	}
	return AWSCredentials{AccessKeyID: r.AccessKeyID, SecretAccessKey: r.SecretAccessKey, SessionToken: r.Token}, r.Expiration, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// awsEscape escapes a path or query value as SigV4 wants: everything but
// unreserved characters.
func awsEscape(s string, path bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || (path && c == '/') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// signV4 signs a request with AWS Signature Version 4, over its method,
// path, query, the host, content type, date and security token headers
// and the body.
func signV4(r *http.Request, body []byte, k AWSCredentials, region, service string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	r.Header.Set("X-Amz-Date", amzDate)
	if k.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", k.SessionToken)
	}
	payload := sha256Hex(body)
	if service == "s3" {
		r.Header.Set("X-Amz-Content-Sha256", payload)
	}
	host := r.URL.Host
	if r.Host != "" {
		host = r.Host
	}
	// Canonical path: each segment escaped once more (except for S3).
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if service != "s3" {
		segs := strings.Split(path, "/")
		for i, s := range segs {
			u, err := url.PathUnescape(s)
			if err != nil {
				u = s
			}
			segs[i] = awsEscape(awsEscape(u, false), false)
		}
		path = strings.Join(segs, "/")
	}
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for key := range q {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var qs []string
	for _, key := range keys {
		vals := append([]string(nil), q[key]...)
		sort.Strings(vals)
		for _, v := range vals {
			qs = append(qs, awsEscape(key, false)+"="+awsEscape(v, false))
		}
	}
	headers := map[string]string{"host": host, "x-amz-date": amzDate}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		headers["content-type"] = ct
	}
	if k.SessionToken != "" {
		headers["x-amz-security-token"] = k.SessionToken
	}
	if service == "s3" {
		headers["x-amz-content-sha256"] = payload
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.Join(strings.Fields(headers[n]), " ") + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{r.Method, path, strings.Join(qs, "&"), canonHeaders.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+k.SecretAccessKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+k.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

// ---------- Google ----------

// googleKey is a service account key file's fields that matter here.
type googleKey struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// ParseGoogleKey checks a service account key file.
func ParseGoogleKey(s string) (googleKey, *rsa.PrivateKey, error) {
	var k googleKey
	if err := json.Unmarshal([]byte(s), &k); err != nil || k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return k, nil, errors.New("that is not a Google service account key file (JSON, type service_account)")
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return k, nil, errors.New("the service account key's private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if pk, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
			parsed = pk
		} else {
			return k, nil, errors.New("the service account key's private key cannot be read")
		}
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return k, nil, errors.New("the service account key is not an RSA key")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return k, rk, nil
}

type googleToken struct {
	token   string
	expires time.Time
	from    string
}

const googleScope = "https://www.googleapis.com/auth/cloud-platform"

// googleAccessToken is an access token: from the stored service account
// key (a signed JWT exchanged at its token address), from the file
// GOOGLE_APPLICATION_CREDENTIALS names, or from the GKE metadata server
// (Workload Identity). It is kept until a minute before it expires.
func (c *Client) googleAccessToken(ctx context.Context, stored string) (string, error) {
	src := stored
	if strings.TrimSpace(src) == "" {
		if f := c.Ambient.getenv("GOOGLE_APPLICATION_CREDENTIALS"); f != "" {
			b, err := c.Ambient.readFile(f)
			if err != nil {
				return "", fmt.Errorf("reading GOOGLE_APPLICATION_CREDENTIALS: %w", err)
			}
			src = string(b)
		}
	}
	from := "metadata"
	if src != "" {
		from = sha256Hex([]byte(src))
	}
	c.mu.Lock()
	cached := c.google
	c.mu.Unlock()
	if cached != nil && cached.from == from && c.now().Add(time.Minute).Before(cached.expires) {
		return cached.token, nil
	}
	var (
		tok string
		ttl time.Duration
		err error
	)
	if src != "" {
		tok, ttl, err = c.googleJWT(ctx, src)
	} else {
		tok, ttl, err = c.googleMetadata(ctx)
	}
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.google = &googleToken{token: tok, expires: c.now().Add(ttl), from: from}
	c.mu.Unlock()
	return tok, nil
}

func (c *Client) googleJWT(ctx context.Context, src string) (string, time.Duration, error) {
	k, pk, err := ParseGoogleKey(src)
	if err != nil {
		return "", 0, err
	}
	now := c.now()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := map[string]string{"alg": "RS256", "typ": "JWT"}
	if k.PrivateKeyID != "" {
		head["kid"] = k.PrivateKeyID
	}
	unsigned := enc(head) + "." + enc(map[string]any{"iss": k.ClientEmail, "scope": googleScope, "aud": k.TokenURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, pk, crypto.SHA256, sum[:])
	if err != nil {
		return "", 0, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, k.TokenURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return googleTokenFrom(c.Ambient.http(), req)
}

func (c *Client) googleMetadata(ctx context.Context) (string, time.Duration, error) {
	addr := "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	if c.Ambient != nil && c.Ambient.GoogleMetadataURL != "" {
		addr = c.Ambient.GoogleMetadataURL
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, addr+"?scopes="+url.QueryEscape(googleScope), nil)
	req.Header.Set("Metadata-Flavor", "Google")
	tok, ttl, err := googleTokenFrom(c.Ambient.http(), req)
	if err != nil {
		return "", 0, fmt.Errorf("no Google identity: save a service account key in Settings, or run the console with Workload Identity (%w)", err)
	}
	return tok, ttl, nil
}

func googleTokenFrom(hc *http.Client, req *http.Request) (string, time.Duration, error) {
	res, err := hc.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("asking Google for an access token: %w", err)
	}
	defer res.Body.Close()
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&r)
	if res.StatusCode != http.StatusOK || r.AccessToken == "" {
		return "", 0, fmt.Errorf("Google refused the token request (%d) %s", res.StatusCode, r.Error)
	}
	if r.ExpiresIn <= 0 {
		r.ExpiresIn = 3600
	}
	return r.AccessToken, time.Duration(r.ExpiresIn) * time.Second, nil
}
