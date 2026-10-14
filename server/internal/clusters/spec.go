// Package clusters keeps the console's cluster connections (P3-05 to
// P3-09): what each one is, how the console signs in to it, where the
// credentials live (Kubernetes Secrets, never the browser), and one
// running watch per connection.
package clusters

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Environments a cluster can be tagged with.
const (
	EnvProduction  = "production"
	EnvStaging     = "staging"
	EnvDevelopment = "development"
	EnvOther       = "other"
)

// How the API server's certificate is checked.
const (
	CAPEM    = "pem"    // against a pasted CA certificate
	CASystem = "system" // against the system trust store
	CASkip   = "skip"   // not at all
)

// How the console signs in to a cluster.
const (
	AuthToken      = "token"      // a service account token
	AuthKubeconfig = "kubeconfig" // a kubeconfig's context
	AuthCert       = "cert"       // a client certificate and key
	AuthOIDC       = "oidc"       // an OIDC refresh token the console renews
	AuthInCluster  = "incluster"  // the console's own ServiceAccount
	AuthNone       = "none"       // no credentials: the simulator, tests
)

// What the console may do in the cluster.
const (
	ModeRead  = "read"
	ModeWrite = "write"
)

// Spec is a connection as it is stored and shown, without credentials.
type Spec struct {
	// ID is the connection's slug, fixed at creation; "local" is the
	// cluster the console runs in.
	ID          string `json:"id"`
	Name        string `json:"name"`
	Env         string `json:"env"`
	Description string `json:"description,omitempty"`
	// Server is the API server's URL.
	Server string `json:"server"`
	// CA is how the server's certificate is checked (pem, system, skip).
	CA string `json:"ca"`
	// CAInfo describes the saved CA certificate: its subject and expiry.
	CAInfo string `json:"caInfo,omitempty"`
	// Auth is the sign-in method (token, kubeconfig, cert, oidc, incluster).
	Auth string `json:"auth"`
	// Mode is read or write (write is stored now, used from Phase 6).
	Mode string `json:"mode"`
	// Local is the cluster the console runs in (or was started against):
	// its credentials are the console's own and cannot be replaced.
	Local bool `json:"local,omitempty"`
	// Cred describes the saved credential without revealing it.
	Cred    CredInfo  `json:"cred"`
	Added   time.Time `json:"added"`
	Updated time.Time `json:"updated"`
}

// CredInfo is what a saved credential says about itself (X-04: the
// credential itself never returns to the browser).
type CredInfo struct {
	// Who the credential signs in as, when it says: a service account's
	// name from its token, a kubeconfig's user.
	Who string `json:"who,omitempty"`
	// Context and Cluster of a kubeconfig.
	Context string `json:"context,omitempty"`
	Cluster string `json:"cluster,omitempty"`
	// Issuer and ClientID of an OIDC sign-in.
	Issuer   string `json:"issuer,omitempty"`
	ClientID string `json:"clientID,omitempty"`
	// Expires is when a token or certificate stops working, when known.
	Expires *time.Time `json:"expires,omitempty"`
	// Set is when the credential was saved.
	Set time.Time `json:"set"`
}

// Credential is what the console signs in with. It lives in a Secret and
// is never serialized to the browser.
type Credential struct {
	Token      string
	Kubeconfig string
	Context    string
	CertPEM    string
	KeyPEM     string
	CAPEM      string
	// OIDC: the refresh token and the client's secret.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRefreshToken string
	OIDCScopes       string
}

// Empty says whether the credential carries nothing.
func (c Credential) Empty() bool {
	return c.Token == "" && c.Kubeconfig == "" && c.CertPEM == "" && c.KeyPEM == "" && c.OIDCRefreshToken == ""
}

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,61}[A-Za-z0-9])?$`)
	slugRE   = regexp.MustCompile(`[^a-z0-9]+`)
	jwtRE    = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`)
	certRE   = regexp.MustCompile(`-----BEGIN CERTIFICATE-----[\s\S]+?-----END CERTIFICATE-----`)
	keyRE    = regexp.MustCompile(`-----BEGIN (?:(?:RSA|EC) )?PRIVATE KEY-----[\s\S]+?-----END (?:(?:RSA|EC) )?PRIVATE KEY-----`)
	envs     = map[string]bool{EnvProduction: true, EnvStaging: true, EnvDevelopment: true, EnvOther: true}
	cas      = map[string]bool{CAPEM: true, CASystem: true, CASkip: true}
	auths    = map[string]bool{AuthToken: true, AuthKubeconfig: true, AuthCert: true, AuthOIDC: true}
	modes    = map[string]bool{ModeRead: true, ModeWrite: true}
	reserved = map[string]bool{"local": true, "new": true, "test": true}
)

// Slug makes a connection ID from its name: lowercase letters, digits and
// dashes.
func Slug(name string) string {
	s := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "cluster"
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// ValidationError says what is wrong with a draft, for the editor.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Msg }

func invalid(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }

// ValidateSpec checks the fields every connection has.
func ValidateSpec(s Spec) error {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return invalid("name", "Give the cluster a name.")
	}
	if !nameRE.MatchString(name) {
		return invalid("name", "Use letters, digits, dots, dashes or underscores, up to 63, starting and ending with a letter or digit.")
	}
	if !envs[s.Env] {
		return invalid("env", "Choose an environment: production, staging, development or other.")
	}
	if !s.Local {
		if _, err := ServerURL(s.Server); err != nil {
			return invalid("server", err.Error())
		}
		if !cas[s.CA] {
			return invalid("ca", "Choose how the API server's certificate is checked.")
		}
		if !auths[s.Auth] {
			return invalid("auth", "Choose how the console signs in: a token, a kubeconfig, a client certificate or OIDC.")
		}
	}
	if !modes[s.Mode] {
		return invalid("mode", "Choose read only or read and change.")
	}
	return nil
}

// ServerURL checks an API server address: https://host[:port][/path], or
// http:// for a simulator, which is not encrypted.
func ServerURL(server string) (*url.URL, error) {
	server = strings.TrimSpace(server)
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("The API server address starts with https://, for example https://k8s-api.example.internal:6443.")
	}
	return u, nil
}

// CredentialCheck is what a credential says about itself before it is
// used: a note for the editor and the metadata the saved connection keeps.
type CredentialCheck struct {
	Tone string // ok, warn or err
	Text string
	Info CredInfo
}

// CheckCredential inspects a credential for the method: a token's claims,
// a kubeconfig's contexts, a certificate's PEM. It makes no connection.
// The draft's server may be filled in from a kubeconfig; the returned
// server is the one to use.
func CheckCredential(auth string, c Credential, server string, now time.Time) (CredentialCheck, string, error) {
	info := CredInfo{Set: now}
	switch auth {
	case AuthToken:
		t := strings.TrimSpace(c.Token)
		if t == "" {
			return CredentialCheck{}, server, invalid("token", "Paste the service account token.")
		}
		if !jwtRE.MatchString(t) {
			return CredentialCheck{}, server, invalid("token", "That isn’t a service account token: those are three dot-separated parts that start with eyJ.")
		}
		claims := jwtClaims(t)
		info.Who = jwtWho(claims)
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			e := time.Unix(int64(exp), 0).UTC()
			info.Expires = &e
			if e.Before(now) {
				return CredentialCheck{}, server, invalid("token", fmt.Sprintf("This token expired on %s. Create a new one.", e.Format("2006-01-02")))
			}
		}
		who := info.Who
		if who == "" {
			who = "an account the token doesn’t name"
		}
		until := ", with no expiry"
		if info.Expires != nil {
			until = ", valid until " + info.Expires.Format("2006-01-02")
		}
		return CredentialCheck{Tone: "ok", Text: "Token for " + who + until + ".", Info: info}, server, nil
	case AuthKubeconfig:
		if strings.TrimSpace(c.Kubeconfig) == "" {
			return CredentialCheck{}, server, invalid("kubeconfig", "Paste a kubeconfig.")
		}
		kc, err := clientcmd.Load([]byte(c.Kubeconfig))
		if err != nil {
			return CredentialCheck{}, server, invalid("kubeconfig", "That isn’t a kubeconfig: "+err.Error())
		}
		if len(kc.Contexts) == 0 {
			return CredentialCheck{}, server, invalid("kubeconfig", "That kubeconfig has no contexts.")
		}
		name := strings.TrimSpace(c.Context)
		if name == "" {
			name = kc.CurrentContext
		}
		if name == "" {
			for n := range kc.Contexts {
				if name == "" || n < name {
					name = n
				}
			}
		}
		ctx, ok := kc.Contexts[name]
		if !ok {
			return CredentialCheck{}, server, invalid("context", fmt.Sprintf("The kubeconfig has no context %q. It has %s.", name, strings.Join(contextNames(kc), ", ")))
		}
		info.Context, info.Cluster, info.Who = name, ctx.Cluster, ctx.AuthInfo
		user := kc.AuthInfos[ctx.AuthInfo]
		if user != nil && user.Exec != nil {
			cmd := ""
			if user.Exec.Command != "" {
				cmd = " (" + user.Exec.Command + ")"
			}
			return CredentialCheck{}, server, invalid("kubeconfig", fmt.Sprintf("Context %s signs in through an exec plugin%s, which the console can’t run. Use a token or a client certificate for it instead.", name, cmd))
		}
		if user != nil && user.AuthProvider != nil {
			return CredentialCheck{}, server, invalid("kubeconfig", fmt.Sprintf("Context %s signs in through the %s auth provider, which the console can’t run. Use a token or a client certificate for it instead.", name, user.AuthProvider.Name))
		}
		if user != nil {
			if user.Token != "" {
				if exp, ok := jwtClaims(user.Token)["exp"].(float64); ok && exp > 0 {
					e := time.Unix(int64(exp), 0).UTC()
					info.Expires = &e
				}
			} else if len(user.ClientCertificateData) > 0 {
				if e, ok := certExpiry(user.ClientCertificateData); ok {
					info.Expires = &e
				}
			} else if user.ClientCertificate != "" || user.TokenFile != "" {
				return CredentialCheck{}, server, invalid("kubeconfig", fmt.Sprintf("Context %s refers to files on the machine it came from (%s); paste a kubeconfig with the certificate and key, or the token, inline (kubectl config view --flatten --minify).", name, firstNonEmpty(user.ClientCertificate, user.TokenFile)))
			}
		}
		cl := kc.Clusters[ctx.Cluster]
		tone, text := "ok", fmt.Sprintf("Context %s: user %s on cluster %s.", name, firstNonEmpty(ctx.AuthInfo, "?"), firstNonEmpty(ctx.Cluster, "?"))
		if cl != nil && cl.Server != "" {
			if strings.TrimSpace(server) == "" {
				server = cl.Server
			} else if strings.TrimSuffix(cl.Server, "/") != strings.TrimSuffix(strings.TrimSpace(server), "/") {
				tone, text = "warn", text+fmt.Sprintf(" Its server is %s, not %s.", cl.Server, strings.TrimSpace(server))
			}
		}
		if info.Expires != nil && info.Expires.Before(now) {
			return CredentialCheck{}, server, invalid("kubeconfig", fmt.Sprintf("The credentials of context %s expired on %s.", name, info.Expires.Format("2006-01-02")))
		}
		return CredentialCheck{Tone: tone, Text: text, Info: info}, server, nil
	case AuthCert:
		if !certRE.MatchString(c.CertPEM) {
			return CredentialCheck{}, server, invalid("certPem", "Paste the client certificate (PEM).")
		}
		if !keyRE.MatchString(c.KeyPEM) {
			return CredentialCheck{}, server, invalid("keyPem", "Paste the private key (PEM) that goes with the certificate.")
		}
		if e, ok := certExpiry([]byte(c.CertPEM)); ok {
			info.Expires = &e
			if e.Before(now) {
				return CredentialCheck{}, server, invalid("certPem", fmt.Sprintf("This certificate expired on %s.", e.Format("2006-01-02")))
			}
		}
		if who, ok := certSubject([]byte(c.CertPEM)); ok {
			info.Who = who
		}
		return CredentialCheck{Tone: "ok", Text: "Client certificate and key provided.", Info: info}, server, nil
	case AuthOIDC:
		iss := strings.TrimSpace(c.OIDCIssuer)
		if _, err := ServerURL(iss); err != nil {
			return CredentialCheck{}, server, invalid("issuer", "The issuer URL starts with https://, for example https://login.corp.example/realms/data.")
		}
		if strings.TrimSpace(c.OIDCClientID) == "" {
			return CredentialCheck{}, server, invalid("client", "Add the client ID the API server trusts (its --oidc-client-id).")
		}
		info.Issuer, info.ClientID = iss, strings.TrimSpace(c.OIDCClientID)
		return CredentialCheck{Tone: "ok", Text: fmt.Sprintf("The console will sign in at %s as client “%s” and keep the refresh token.", iss, info.ClientID), Info: info}, server, nil
	}
	return CredentialCheck{}, server, invalid("auth", "Choose how the console signs in.")
}

func contextNames(kc *clientcmdapi.Config) []string {
	var out []string
	for n := range kc.Contexts {
		out = append(out, n)
	}
	sortStrings(out)
	return out
}

// Contexts lists a kubeconfig's contexts, for the editor's picker.
func Contexts(kubeconfig string) (names []string, current string) {
	kc, err := clientcmd.Load([]byte(kubeconfig))
	if err != nil {
		return nil, ""
	}
	return contextNames(kc), kc.CurrentContext
}

// jwtClaims decodes a JWT's payload without verifying it: the API server
// verifies; the console only reads what the token says about itself.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(b, &claims) != nil {
		return nil
	}
	return claims
}

// jwtWho names a service account token's account, or the subject.
func jwtWho(claims map[string]any) string {
	if k, ok := claims["kubernetes.io"].(map[string]any); ok {
		ns, _ := k["namespace"].(string)
		if sa, ok := k["serviceaccount"].(map[string]any); ok {
			if name, _ := sa["name"].(string); name != "" && ns != "" {
				return "system:serviceaccount:" + ns + ":" + name
			}
		}
	}
	sub, _ := claims["sub"].(string)
	return sub
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// StorageVersion marks the spec.json format in the Secret.
const StorageVersion = 1

// stored is the Secret's spec.json.
type stored struct {
	Version int  `json:"version"`
	Spec    Spec `json:"spec"`
}

// MarshalSpec encodes a spec for the Secret.
func MarshalSpec(s Spec) ([]byte, error) {
	return json.Marshal(stored{Version: StorageVersion, Spec: s})
}

// UnmarshalSpec decodes a spec from the Secret.
func UnmarshalSpec(b []byte) (Spec, error) {
	var st stored
	if err := json.Unmarshal(b, &st); err != nil {
		return Spec{}, err
	}
	if st.Version > StorageVersion {
		return Spec{}, fmt.Errorf("the connection was saved by a newer console (format %d)", st.Version)
	}
	return st.Spec, nil
}
