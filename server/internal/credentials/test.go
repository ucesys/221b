package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/s3"
)

// S3Address is a bucket's address, as an access key's endpoint covers
// it: https://s3.amazonaws.com/BUCKET on AWS, ENDPOINT/BUCKET elsewhere.
func S3Address(endpoint, bucket string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		endpoint = "https://s3.amazonaws.com"
	}
	return endpoint + "/" + bucket
}

// testTimeout bounds a test.
const testTimeout = 30 * time.Second

// TestTarget is what a test asks: the address typed, completed from the
// endpoint (a name alone is under it: 221b.git under
// https://github.com/org), or the endpoint itself.
func TestTarget(c Credential, typed string) string {
	typed = strings.TrimSpace(typed)
	ep, err := ParseEndpoint(c.Endpoint)
	if err != nil {
		return typed
	}
	base := ep
	if base.Scheme == "" {
		base.Scheme = "https"
		if c.Kind == KindSSH {
			base.Scheme = "ssh"
		}
	}
	if typed == "" {
		return base.String()
	}
	if strings.Contains(typed, "://") {
		return typed
	}
	first, _, _ := strings.Cut(typed, "/")
	if strings.Contains(first, ".") && !strings.HasSuffix(first, ".git") || strings.Contains(first, ":") {
		// A host: the endpoint's scheme goes with it.
		return base.Scheme + "://" + typed
	}
	return base.String() + "/" + strings.TrimLeft(typed, "/")
}

// RunTest tests a saved credential against its endpoint or an address
// under it (CR-07), and keeps the result. It says how the credential was
// sent, never its value, and what the far end answered; after a 401 it
// suggests the way of sending the server's WWW-Authenticate asks for.
func (s *Service) RunTest(ctx context.Context, id, typed, by string) (Test, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Test{}, err
	}
	sec, err := s.secret(ctx, id)
	if err != nil {
		return Test{}, err
	}
	t := s.test(ctx, c, sec, TestTarget(c, typed))
	t.By = by
	if err := s.noteTestOf(ctx, c, t); err != nil {
		s.o.Logger.Warn("a credential's test could not be kept", "credential", c.Name, "err", err)
	}
	return t, nil
}

// errChangedMeanwhile is a credential changed while it was tested.
var errChangedMeanwhile = errors.New("changed while it was tested")

// noteTestOf keeps a test of the credential c as it was read, unless an
// edit, a Trust or a Forget changed it while the test ran: the result is
// of the credential as it was, and is not kept over the change
// (credentials-cut-3).
func (s *Service) noteTestOf(ctx context.Context, c Credential, t Test) error {
	was := c
	was.LastTest = nil
	before, _ := json.Marshal(was)
	_, err := s.o.Repo.Update(ctx, c.ID, func(cur *Credential) error {
		now := *cur
		now.LastTest = nil
		if after, _ := json.Marshal(now); string(after) != string(before) {
			return errChangedMeanwhile
		}
		cur.LastTest = &t
		return nil
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, errChangedMeanwhile) {
		return nil
	}
	return err
}

func (s *Service) test(ctx context.Context, c Credential, sec Secret, target string) Test {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	t := Test{At: s.o.Now().UTC(), Target: target}
	if !c.Covers(target) {
		t.Text = fmt.Sprintf("%s is not under the endpoint %s: the credential is never sent there.", target, c.Endpoint)
		return t
	}
	use, err := s.resolve(ctx, c, sec)
	if err != nil {
		t.Text = err.Error()
		// A GitHub App's or an OAuth2 client's token is fetched over TLS
		// first: a certificate it failed on is shown, so it can be trusted
		// (credentials#14).
		if c.Kind == KindGitHubApp || c.Kind == KindOAuth2 {
			s.noteCertificate(ctx, &Use{CAPEM: c.CAPEM, Pin: c.PinnedCert.Fingerprint()}, target, &t)
		}
		t.Text = redact(t.Text, sec.values())
		return t
	}
	t.Method = use.Text()
	secrets := append(sec.values(), use.Values()...)
	switch c.Kind {
	case KindAccessKey:
		s.testBucket(ctx, c, use, target, &t)
	case KindSSH:
		s.testSSH(ctx, use, target, &t)
	default:
		if strings.HasPrefix(target, "oci://") {
			s.testRegistry(ctx, use, target, &t)
		} else {
			s.testHTTP(ctx, c, use, target, &t)
		}
	}
	if c.Kind != KindSSH {
		s.noteCertificate(ctx, use, target, &t)
	}
	t.Text = redact(t.Text, secrets)
	return t
}

// redact takes the secret values out of a test's text, in every form an
// address may carry them (escaped for a query or a path, and escaped
// again, as a login redirect's next= does); nothing else is changed, so
// the text can say how the credential was sent.
func redact(text string, secrets []string) string {
	var forms []string
	for _, v := range secrets {
		if len(v) < 4 {
			continue
		}
		q := url.QueryEscape(v)
		for _, f := range []string{v, q, url.PathEscape(v), url.QueryEscape(q), url.PathEscape(q)} {
			if !slices.Contains(forms, f) {
				forms = append(forms, f)
			}
		}
	}
	// The longest first: one form may hold another.
	slices.SortStableFunc(forms, func(a, b string) int { return len(b) - len(a) })
	for _, f := range forms {
		text = strings.ReplaceAll(text, f, "[REDACTED]")
	}
	return text
}

// redirectHost is the host a redirect goes to, a relative one resolved
// against the request: never its path or query, which may carry the
// request (a login's next=) and the credential with it.
func redirectHost(res *http.Response) string {
	if loc, err := res.Location(); err == nil && loc.Host != "" {
		return loc.Host
	}
	return "another address"
}

// testHTTP asks the address with the credential on, a git repository's
// refs when the address is one (…/repo.git).
func (s *Service) testHTTP(ctx context.Context, c Credential, use *Use, target string, t *Test) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		t.Text = "Only an http:// or https:// address can be asked with this credential."
		return
	}
	if strings.HasSuffix(u.Path, ".git") {
		u.Path += "/info/refs"
		u.RawQuery = "service=git-upload-pack"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		t.Text = err.Error()
		return
	}
	req.Header.Set("User-Agent", "221b-credential-test")
	if c.Kind != KindNone {
		if err := use.Apply(req); err != nil {
			t.Text = err.Error()
			return
		}
	} else {
		t.Method = "nothing (no secret)"
	}
	res, err := withTLS(s.o.HTTP, use.TLS()).Do(req)
	if err != nil {
		t.Text = "Sent " + t.Method + " to " + u.Host + ", which could not be reached: " + reachText(err)
		return
	}
	defer res.Body.Close()
	t.Status = res.StatusCode
	t.OK = res.StatusCode/100 == 2
	t.Text = fmt.Sprintf("Sent %s to %s; it answered %s.", t.Method, u.Host, res.Status)
	if res.StatusCode/100 == 3 {
		t.Text += " It redirects elsewhere (" + redirectHost(res) + "); the credential does not follow."
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		if sug, why := suggest(res.Header.Values("WWW-Authenticate"), use.Method); sug != "" {
			t.Suggest = sug
			t.Text += " " + why
		}
	}
}

// testRegistry asks an OCI registry's API (https://HOST/v2/) with the
// credential, as Helm does (credentials#15): a token as Bearer, then the
// registry's challenge answered, Basic with the user and password (or
// the token, as the user "token"), or the token service its Bearer
// challenge names, which the registry's credential goes to (CR-04).
func (s *Service) testRegistry(ctx context.Context, use *Use, target string, t *Test) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		t.Text = "Name the registry to test, as oci://HOST/ORG."
		return
	}
	api := "https://" + u.Host + "/v2/"
	user, secret := "", ""
	switch use.Method {
	case SendBasic:
		user, secret = use.Username, use.Token
	case SendBearer:
		user, secret = "token", use.Token
	case MethodNone:
	default:
		t.Text = "An OCI registry takes a user and password, or a token sent as Basic or Bearer."
		return
	}
	get := func(raw, auth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "221b-credential-test")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return s.useClient(use, raw).Do(req)
	}
	first := ""
	if use.Method == SendBearer {
		first = "Bearer " + secret
	}
	res, err := get(api, first)
	if err != nil {
		t.Text = "Sent " + t.Method + " to " + u.Host + "'s registry API, which could not be reached: " + reachText(err)
		return
	}
	res.Body.Close()
	via := ""
	if res.StatusCode == http.StatusUnauthorized && secret != "" {
		scheme, params := parseChallenge(res.Header.Get("WWW-Authenticate"))
		auth := ""
		switch scheme {
		case "basic":
			auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
		case "bearer":
			realm, err := url.Parse(params["realm"])
			if err != nil || realm.Scheme != "https" || realm.Host == "" {
				t.Status = res.StatusCode
				t.Text = "Sent " + t.Method + " to " + u.Host + "'s registry API; its token service is not an https:// address."
				return
			}
			q := realm.Query()
			for _, k := range []string{"service", "scope"} {
				if v := params[k]; v != "" {
					q.Set(k, v)
				}
			}
			realm.RawQuery = q.Encode()
			tres, err := get(realm.String(), "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+secret)))
			if err != nil {
				t.Text = "Sent the credential to " + realm.Host + ", the registry's token service, which could not be reached: " + reachText(err)
				return
			}
			b, _ := io.ReadAll(io.LimitReader(tres.Body, 64<<10))
			tres.Body.Close()
			var out struct {
				Token       string `json:"token"`
				AccessToken string `json:"access_token"`
			}
			if tres.StatusCode/100 != 2 || json.Unmarshal(b, &out) != nil || out.Token+out.AccessToken == "" {
				t.Status = tres.StatusCode
				t.Text = fmt.Sprintf("Sent the credential as Basic to %s, the registry's token service; it answered %s.", realm.Host, tres.Status)
				return
			}
			tok := out.Token
			if tok == "" {
				tok = out.AccessToken
			}
			auth, via = "Bearer "+tok, " through its token service at "+realm.Host
		}
		if auth != "" {
			if res, err = get(api, auth); err != nil {
				t.Text = "Sent " + t.Method + " to " + u.Host + "'s registry API, which could not be reached: " + reachText(err)
				return
			}
			res.Body.Close()
		}
	}
	t.Status = res.StatusCode
	t.OK = res.StatusCode/100 == 2
	t.Text = fmt.Sprintf("Sent %s to %s's registry API%s; it answered %s.", t.Method, u.Host, via, res.Status)
}

// useClient is the service's client for an address with the use's TLS:
// all of it on the endpoint's host, the private CA alone elsewhere (a
// registry's token service), since the pin is the endpoint's
// (credentials#10).
func (s *Service) useClient(use *Use, target string) *http.Client {
	if use.ep.sameHost(target) {
		return withTLS(s.o.HTTP, use.TLS())
	}
	return withTLS(s.o.HTTP, (&Use{CAPEM: use.CAPEM}).TLS())
}

// parseChallenge reads a WWW-Authenticate challenge: its scheme, in lower
// case, and its parameters (realm, service, scope).
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; {
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				params[k] = v[1:]
				break
			}
			params[k], rest = v[1:1+end], v[2+end:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			params[k], rest = strings.TrimSpace(val), after
		}
		rest = strings.TrimLeft(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return strings.ToLower(scheme), params
}

// suggest reads a 401's WWW-Authenticate: the way of sending it asks for
// when it is not the one sent.
func suggest(challenges []string, sent string) (string, string) {
	for _, h := range challenges {
		scheme, _, _ := strings.Cut(strings.TrimSpace(h), " ")
		switch strings.ToLower(scheme) {
		case "basic":
			if sent != SendBasic {
				return SendBasic, "The server asks for Basic: send the token as Basic with a user name (x-access-token for GitHub, oauth2 for GitLab)."
			}
			return "", ""
		case "bearer":
			if sent != SendBearer {
				return SendBearer, "The server asks for Bearer: send the token as Bearer."
			}
			return "", ""
		}
	}
	return "", ""
}

func reachText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// testBucket lists one key of the bucket the address names, signed with
// the access key (SigV4).
func (s *Service) testBucket(ctx context.Context, c Credential, use *Use, target string, t *Test) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		t.Text = "Name the bucket to test, as https://s3.amazonaws.com/BUCKET or ENDPOINT/BUCKET."
		return
	}
	bucket, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	if bucket == "" {
		t.Text = "Name the bucket to test: an access key is tested by listing a bucket (type its name)."
		return
	}
	endpoint := u.Scheme + "://" + u.Host
	if strings.EqualFold(u.Host, "s3.amazonaws.com") {
		endpoint = ""
	}
	creds := func(context.Context) (awsv4.Credentials, error) {
		return awsv4.Credentials{AccessKeyID: use.AccessKeyID, SecretAccessKey: use.SecretAccessKey, SessionToken: use.SessionToken}, nil
	}
	client, err := s3.New(s3.Config{Endpoint: endpoint, Region: use.Region, Bucket: bucket, PathStyle: endpoint != "", Credentials: creds, HTTP: s.clientFor(c, target), Now: s.o.Now})
	if err != nil {
		t.Text = err.Error()
		return
	}
	_, _, err = client.ListMax(ctx, "", 1)
	if err != nil {
		t.Text = "Signed a listing of " + bucket + " with the access key (SigV4); it failed: " + err.Error()
		return
	}
	t.OK = true
	t.Text = "Signed a listing of " + bucket + " with the access key (SigV4); the bucket answered."
}

// testSSH connects to the host, checks its key against the one saved, and
// signs in with the private key.
func (s *Service) testSSH(ctx context.Context, use *Use, target string, t *Test) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		t.Text = "Name the SSH host to test, as ssh://git@host."
		return
	}
	user := "git"
	if u.User != nil && u.User.Username() != "" {
		user = u.User.Username()
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "22")
	}
	signer, err := sshSigner(Secret{PrivateKey: use.PrivateKey, Passphrase: use.Passphrase})
	if err != nil {
		t.Text = err.Error()
		return
	}
	saved, err := ParseHostKeys(use.HostKey)
	if err != nil {
		t.Text = err.Error()
		return
	}
	// The server is asked for the types of key saved: one with several
	// host keys shows another first otherwise, which would be taken for a
	// mismatch (credentials#13); any key saved is accepted
	// (credentials-cut-4).
	var algos []string
	for _, k := range saved {
		for _, a := range algorithmsOf(k.Type()) {
			if !slices.Contains(algos, a) {
				algos = append(algos, a)
			}
		}
	}
	accept := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		for _, k := range saved {
			if bytes.Equal(k.Marshal(), key.Marshal()) {
				return nil
			}
		}
		return errors.New("ssh: host key mismatch")
	}
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: accept, HostKeyAlgorithms: algos, Timeout: 15 * time.Second}
	// The service's dialer: the guarded one, or a test's.
	conn, err := s.o.Dial(ctx, "tcp", host)
	if err != nil {
		t.Text = "Could not reach " + host + ": " + err.Error()
		return
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	sc, _, _, err := ssh.NewClientConn(conn, host, cfg)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "host key mismatch"):
			t.Text = host + " showed another host key than the one saved: the key is not sent."
		case strings.Contains(err.Error(), "unable to authenticate"):
			t.Text = "The host key of " + host + " matched; the server refused the key for " + user + "."
		default:
			t.Text = "SSH to " + host + " failed: " + err.Error()
		}
		return
	}
	sc.Close()
	t.OK = true
	t.Text = "The host key of " + host + " matched, and the server accepted the key for " + user + "."
}

// algorithmsOf names the signature algorithms a host key type is used
// with: an RSA key signs with SHA-2 today (as nodessh asks).
func algorithmsOf(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}
