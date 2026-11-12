package awsv4

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Ambient is where the pod's identity comes from. Its zero value reads the
// usual environment, files and endpoints; tests point it elsewhere.
type Ambient struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	// STSURL is the AWS STS endpoint for web identities (IRSA).
	STSURL string
	HTTP   *http.Client
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

// Identity is the pod's own AWS identity: the environment's keys, a web
// identity (IRSA: AWS_ROLE_ARN and AWS_WEB_IDENTITY_TOKEN_FILE, exchanged
// at AWS STS) or EKS Pod Identity, in that order. Temporary credentials are
// kept until five minutes before they expire.
type Identity struct {
	Ambient *Ambient
	// Session names the role session in CloudTrail ("221b-backup").
	Session string
	Now     func() time.Time
	mu      sync.Mutex
	cached  Credentials
	expires time.Time
}

func (id *Identity) now() time.Time {
	if id.Now != nil {
		return id.Now()
	}
	return time.Now()
}

// Available says whether the pod has an identity to ask for: keys in the
// environment, a web identity or a Pod Identity agent.
func (id *Identity) Available() bool {
	a := id.Ambient
	return a.getenv("AWS_ACCESS_KEY_ID") != "" && a.getenv("AWS_SECRET_ACCESS_KEY") != "" ||
		a.getenv("AWS_ROLE_ARN") != "" && a.getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" ||
		a.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != ""
}

// Credentials returns the identity's credentials; region picks STS's
// regional endpoint when AWS_REGION does not.
func (id *Identity) Credentials(ctx context.Context, region string) (Credentials, error) {
	a := id.Ambient
	if k, secret := a.getenv("AWS_ACCESS_KEY_ID"), a.getenv("AWS_SECRET_ACCESS_KEY"); k != "" && secret != "" {
		return Credentials{AccessKeyID: k, SecretAccessKey: secret, SessionToken: a.getenv("AWS_SESSION_TOKEN")}, nil
	}
	id.mu.Lock()
	cached, expires := id.cached, id.expires
	id.mu.Unlock()
	if cached.AccessKeyID != "" && id.now().Add(5*time.Minute).Before(expires) {
		return cached, nil
	}
	var (
		creds Credentials
		err   error
	)
	switch {
	case a.getenv("AWS_ROLE_ARN") != "" && a.getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "":
		creds, expires, err = WebIdentity(ctx, a, region, id.Session)
	case a.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "":
		creds, expires, err = PodIdentity(ctx, a)
	default:
		return creds, errors.New("the pod has no AWS identity: give the console's ServiceAccount an IAM role (IRSA or EKS Pod Identity), or save an access key")
	}
	if err != nil {
		return creds, err
	}
	id.mu.Lock()
	id.cached, id.expires = creds, expires
	id.mu.Unlock()
	return creds, nil
}

// WebIdentity exchanges the projected ServiceAccount token for a role's
// credentials (IRSA): STS AssumeRoleWithWebIdentity, which needs no
// signature.
func WebIdentity(ctx context.Context, a *Ambient, region, session string) (Credentials, time.Time, error) {
	tok, err := a.readFile(a.getenv("AWS_WEB_IDENTITY_TOKEN_FILE"))
	if err != nil {
		return Credentials{}, time.Time{}, fmt.Errorf("reading the web identity token: %w", err)
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
	if session == "" {
		session = "221b"
	}
	q := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "RoleArn": {a.getenv("AWS_ROLE_ARN")}, "RoleSessionName": {session}, "WebIdentityToken": {strings.TrimSpace(string(tok))}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, sts, strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http().Do(req)
	if err != nil {
		return Credentials{}, time.Time{}, fmt.Errorf("asking AWS STS for the role's credentials: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return Credentials{}, time.Time{}, fmt.Errorf("AWS STS refused the web identity (%d): %s", res.StatusCode, strings.TrimSpace(string(b)))
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
		return Credentials{}, time.Time{}, errors.New("AWS STS answered without credentials")
	}
	k := r.Result.Credentials
	return Credentials{AccessKeyID: k.AccessKeyID, SecretAccessKey: k.SecretAccessKey, SessionToken: k.SessionToken}, k.Expiration, nil
}

// PodIdentity reads EKS Pod Identity's credentials from the agent.
func PodIdentity(ctx context.Context, a *Ambient) (Credentials, time.Time, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI"), nil)
	if f := a.getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); f != "" {
		tok, err := a.readFile(f)
		if err != nil {
			return Credentials{}, time.Time{}, fmt.Errorf("reading the pod identity token: %w", err)
		}
		req.Header.Set("Authorization", strings.TrimSpace(string(tok)))
	}
	res, err := a.http().Do(req)
	if err != nil {
		return Credentials{}, time.Time{}, fmt.Errorf("asking EKS Pod Identity for credentials: %w", err)
	}
	defer res.Body.Close()
	var r struct {
		AccessKeyID     string    `json:"AccessKeyId"`
		SecretAccessKey string    `json:"SecretAccessKey"`
		Token           string    `json:"Token"`
		Expiration      time.Time `json:"Expiration"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&r) != nil || r.AccessKeyID == "" {
		return Credentials{}, time.Time{}, fmt.Errorf("EKS Pod Identity answered %d without credentials", res.StatusCode)
	}
	return Credentials{AccessKeyID: r.AccessKeyID, SecretAccessKey: r.SecretAccessKey, SessionToken: r.Token}, r.Expiration, nil
}
