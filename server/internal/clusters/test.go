package clusters

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Step is one of the five checks of a connection test (P3-06): address,
// TLS, sign-in, Kubernetes, permissions.
type Step struct {
	Key  string `json:"k"`
	Tone string `json:"tone"` // ok, warn, err or muted (not reached)
	Text string `json:"t"`
}

// TestResult is a connection test's outcome.
type TestResult struct {
	Steps   []Step `json:"steps"`
	Tone    string `json:"tone"`
	Summary string `json:"summary"`
	// Error is the first failing step's text, or "".
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"`
	Nodes   int    `json:"nodes,omitempty"`
	// NodesMore is true when there are more nodes than were counted.
	NodesMore bool `json:"nodesMore,omitempty"`
	// Who the credential signs in as, from the API server.
	Who string `json:"who,omitempty"`
	// Impersonate is true when the account may impersonate people (P3-02).
	Impersonate bool `json:"impersonate"`
	// Server is the API server the test went to, for whoever ran it and
	// the audit; a kept test does not keep it.
	Server string `json:"server,omitempty"`
}

// TestOptions tune a test.
type TestOptions struct {
	// SecretsRead says whether the console will read Secrets here.
	SecretsRead bool
	// Timeout bounds each step.
	Timeout time.Duration
	Now     func() time.Time
	// Dialer and Resolver can be replaced by tests.
	Dialer   func(ctx context.Context, network, addr string) (net.Conn, error)
	Resolver func(ctx context.Context, host string) ([]string, error)
}

func (o *TestOptions) defaults() {
	if o.Timeout <= 0 {
		o.Timeout = 8 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dialer == nil {
		o.Dialer = (&net.Dialer{Timeout: o.Timeout}).DialContext
	}
	if o.Resolver == nil {
		o.Resolver = func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
}

type tester struct {
	opts  TestOptions
	steps []Step
	dead  bool
	// proxy is the proxy the connection reaches the API server through
	// (a kubeconfig's proxy-url, else HTTPS_PROXY and its kin), nil for
	// none: the checks go the same way.
	proxy *url.URL
}

// dial reaches the API server's addr as the connection does: directly,
// or through an HTTP(S) proxy's CONNECT tunnel.
func (t *tester) dial(ctx context.Context, addr string) (net.Conn, error) {
	if t.proxy == nil {
		return t.opts.Dialer(ctx, "tcp", addr)
	}
	return tunnel(ctx, t.opts.Dialer, t.proxy, addr)
}

// proxyOf is the proxy a connection with cfg reaches u through: cfg's
// own (a kubeconfig's proxy-url), else the environment's, as client-go
// takes them; nil for none.
func proxyOf(cfg *rest.Config, u *url.URL) *url.URL {
	proxy := http.ProxyFromEnvironment
	if cfg != nil && cfg.Proxy != nil {
		proxy = cfg.Proxy
	}
	p, err := proxy(&http.Request{URL: u})
	if err != nil || p == nil || p.Host == "" {
		return nil
	}
	return p
}

// proxyAddr is a proxy's host:port, its scheme's port filled in.
func proxyAddr(p *url.URL) string {
	if p.Port() != "" {
		return p.Host
	}
	port := map[string]string{"https": "443", "socks5": "1080", "socks5h": "1080"}[p.Scheme]
	if port == "" {
		port = "80"
	}
	return net.JoinHostPort(p.Hostname(), port)
}

// tunnel asks an HTTP or HTTPS proxy for a tunnel to addr (CONNECT), as
// client-go's transport does for an https:// server.
func tunnel(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), p *url.URL, addr string) (net.Conn, error) {
	if p.Scheme != "http" && p.Scheme != "https" {
		return nil, fmt.Errorf("a %s proxy is not tunnelled by the test", p.Scheme)
	}
	conn, err := dial(ctx, "tcp", proxyAddr(p))
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if p.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: p.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("the TLS handshake with the proxy failed: %w", err)
		}
		conn = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if u := p.User; u != nil {
		pw, _ := u.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)))
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("the proxy answered %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (t *tester) add(key, tone, text string) {
	t.steps = append(t.steps, Step{Key: key, Tone: tone, Text: text})
	if tone == "err" {
		t.dead = true
	}
}

func (t *tester) skip(key string) {
	t.steps = append(t.steps, Step{Key: key, Tone: "muted", Text: "Not reached."})
}

// Test runs the five steps against the API server and reports each.
func Test(ctx context.Context, s Spec, c Credential, opts TestOptions) TestResult {
	// Neither a backup's placeholder nor a credential that does not go
	// with the saved server (Spec.Unsettled) is ever sent: the sign-in
	// step says why, and the steps before it are checked as ever.
	switch {
	case s.Unsettled != "":
		return run(ctx, s, nil, errors.New(strings.TrimSuffix(s.Unsettled, ".")), c.CAPEM, opts)
	case c.HasPlaceholder():
		return run(ctx, s, nil, errors.New("its credential is not in the backup it was restored from: edit the connection and paste it again"), c.CAPEM, opts)
	}
	cfg, err := RestConfig(s, c)
	return run(ctx, s, cfg, err, c.CAPEM, opts)
}

// testWithConfig tests with a prepared client configuration: the local
// cluster's, whose credentials are the console's own.
func testWithConfig(ctx context.Context, s Spec, cfg *rest.Config, opts TestOptions) TestResult {
	caPEM := string(cfg.TLSClientConfig.CAData)
	if caPEM == "" && cfg.TLSClientConfig.CAFile != "" {
		if b, err := os.ReadFile(cfg.TLSClientConfig.CAFile); err == nil {
			caPEM = string(b)
		}
	}
	if cfg.TLSClientConfig.Insecure {
		s.CA = CASkip
	} else if caPEM != "" {
		s.CA = CAPEM
	} else {
		s.CA = CASystem
	}
	return run(ctx, s, cfg, nil, caPEM, opts)
}

func run(ctx context.Context, s Spec, cfg *rest.Config, cfgErr error, caPEM string, opts TestOptions) TestResult {
	opts.defaults()
	t := &tester{opts: opts}
	r := TestResult{Server: s.Server}
	c := Credential{CAPEM: caPEM}

	// 1. Address.
	u, err := ServerURL(s.Server)
	host, port := "", ""
	if err != nil {
		t.add("Address", "err", "Write the API server as https://host:port, the server: line of a kubeconfig.")
	} else {
		host = u.Hostname()
		port = u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		addr := net.JoinHostPort(host, port)
		where := host + " answers"
		t.proxy = proxyOf(cfg, u)
		switch {
		case t.proxy != nil:
			// The connection goes through the proxy, which resolves and
			// reaches the server: so does the check, as far as it can.
			t.proxyAddress(ctx, u, addr, port)
		case net.ParseIP(host) == nil:
			rctx, cancel := context.WithTimeout(ctx, opts.Timeout)
			ips, err := opts.Resolver(rctx, host)
			cancel()
			if err != nil || len(ips) == 0 {
				t.add("Address", "err", fmt.Sprintf("%s does not resolve: %s.", host, describeNetErr(err)))
			} else {
				where = fmt.Sprintf("%s resolves to %s, which answers", host, ips[0])
			}
		}
		if !t.dead && t.proxy == nil {
			dctx, cancel := context.WithTimeout(ctx, opts.Timeout)
			conn, err := opts.Dialer(dctx, "tcp", addr)
			cancel()
			if err != nil {
				t.add("Address", "err", fmt.Sprintf("Nothing answers on %s: %s.", addr, describeNetErr(err)))
			} else {
				_ = conn.Close()
				t.add("Address", "ok", fmt.Sprintf("%s on port %s.", where, port))
			}
		}
	}

	// 2. TLS.
	switch {
	case t.dead:
		t.skip("TLS")
	case u.Scheme == "http":
		t.add("TLS", "warn", "Plain HTTP: nothing between the console and the API server is encrypted. Use https:// for anything but a simulator.")
	case s.CA == CASkip:
		t.add("TLS", "warn", "Not verified: anything between the console and the API server could read the console’s credentials. Use the cluster’s CA certificate instead.")
	case t.proxy != nil && t.proxy.Scheme != "http" && t.proxy.Scheme != "https":
		t.add("TLS", "ok", fmt.Sprintf("Checked when signing in: the console reaches the API server through the %s proxy %s.", t.proxy.Scheme, t.proxy.Host))
	default:
		// The name the certificate is checked against is the connection's:
		// a kubeconfig's tls-server-name, else the address's host.
		name := host
		if cfg != nil && cfg.TLSClientConfig.ServerName != "" {
			name = cfg.TLSClientConfig.ServerName
		}
		t.tlsStep(ctx, s, c, host, port, name)
	}

	// 3. Sign-in.
	var cs *kubernetes.Clientset
	if t.dead {
		t.skip("Sign-in")
	} else if cfgErr != nil {
		t.add("Sign-in", "err", capitalize(cfgErr.Error())+".")
	} else {
		cfg = rest.CopyConfig(cfg)
		cfg.Timeout = opts.Timeout
		// Small requests: JSON suits every API server, simulators included.
		cfg.ContentType, cfg.AcceptContentTypes = "application/json", "application/json"
		var err error
		cs, err = kubernetes.NewForConfig(cfg)
		if err != nil {
			t.add("Sign-in", "err", "The connection could not be prepared: "+err.Error())
		}
	}
	var version string
	if cs != nil && !t.dead {
		// The client's timeout bounds the version read.
		info, err := cs.Discovery().ServerVersion()
		switch {
		case err == nil:
			version = info.GitVersion
			who := whoAmI(ctx, cs, opts.Timeout)
			// When the server does not say, the credential's own word for
			// its account stands in: a token's or a certificate's. A
			// kubeconfig's is the name of its user entry, which names no
			// account.
			if who == "" && s.Auth != AuthKubeconfig {
				who = s.Cred.Who
			}
			r.Who = who
			switch who {
			case "system:anonymous":
				t.add("Sign-in", "warn", "The API server took the console for nobody (system:anonymous): the connection sends no credentials it accepts. Give a token, a client certificate or a kubeconfig with one.")
			case "":
				t.add("Sign-in", "ok", "Signed in.")
			default:
				t.add("Sign-in", "ok", "Signed in as "+who+".")
			}
		case apierrors.IsUnauthorized(err):
			t.add("Sign-in", "err", "401 Unauthorized: the API server does not accept the credentials."+expiryNote(s, opts.Now()))
		case apierrors.IsForbidden(err):
			t.add("Sign-in", "err", "403 Forbidden: the account is accepted but may not even read the server version; check its RBAC.")
		default:
			t.add("Sign-in", "err", "Could not reach the API server: "+describeNetErr(err)+".")
		}
	}

	// 4. Kubernetes.
	if t.dead || cs == nil {
		t.skip("Kubernetes")
	} else {
		kctx, cancel := context.WithTimeout(ctx, opts.Timeout)
		nodes, err := cs.CoreV1().Nodes().List(kctx, metav1.ListOptions{Limit: 500})
		cancel()
		r.Version = version
		if err != nil {
			if apierrors.IsForbidden(err) {
				t.add("Kubernetes", "err", version+", but the account may not list nodes, which every tab needs. Bind it to the role below.")
			} else {
				t.add("Kubernetes", "err", "Listing nodes failed: "+describeNetErr(err)+".")
			}
		} else {
			r.Nodes = len(nodes.Items)
			r.NodesMore = nodes.Continue != ""
			n := fmt.Sprintf("%d", r.Nodes)
			if r.NodesMore {
				n += "+"
			}
			word := "nodes"
			if r.Nodes == 1 && !r.NodesMore {
				word = "node"
			}
			t.add("Kubernetes", "ok", fmt.Sprintf("%s, %s %s.", version, n, word))
		}
	}

	// 5. Permissions.
	if t.dead || cs == nil {
		t.skip("Permissions")
	} else {
		t.permissionsStep(ctx, cs, s, opts, &r)
	}

	r.Steps = t.steps
	for _, st := range t.steps {
		if st.Tone == "err" {
			r.Error = st.Text
			r.Tone = "err"
			r.Summary = "Can’t connect: the " + strings.ToLower(st.Key) + " step failed."
			return r
		}
	}
	r.Tone = "ok"
	warn := false
	for _, st := range t.steps {
		if st.Tone == "warn" {
			warn = true
		}
	}
	if warn {
		r.Tone = "warn"
	}
	nodes := fmt.Sprintf("%d nodes", r.Nodes)
	if r.NodesMore {
		nodes = fmt.Sprintf("%d+ nodes", r.Nodes)
	} else if r.Nodes == 1 {
		nodes = "1 node"
	}
	if warn {
		r.Summary = fmt.Sprintf("Connected, with a warning: %s, %s.", r.Version, nodes)
	} else {
		r.Summary = fmt.Sprintf("Connected: %s, %s.", r.Version, nodes)
	}
	return r
}

// proxyAddress is the address step through a proxy: an https:// server
// is reached through the proxy's tunnel, as the connection reaches it;
// for any other (a plain-HTTP simulator, a SOCKS proxy) the proxy itself
// is reached, and the server is when signing in.
func (t *tester) proxyAddress(ctx context.Context, u *url.URL, addr, port string) {
	via := t.proxy.Host
	dctx, cancel := context.WithTimeout(ctx, t.opts.Timeout)
	defer cancel()
	if u.Scheme == "https" && (t.proxy.Scheme == "http" || t.proxy.Scheme == "https") {
		conn, err := t.dial(dctx, addr)
		if err != nil {
			t.add("Address", "err", fmt.Sprintf("Through the proxy %s, %s is not reached: %s.", via, addr, describeNetErr(err)))
			return
		}
		_ = conn.Close()
		t.add("Address", "ok", fmt.Sprintf("%s answers on port %s, through the proxy %s.", u.Hostname(), port, via))
		return
	}
	conn, err := t.opts.Dialer(dctx, "tcp", proxyAddr(t.proxy))
	if err != nil {
		t.add("Address", "err", fmt.Sprintf("Nothing answers at the proxy %s: %s.", via, describeNetErr(err)))
		return
	}
	_ = conn.Close()
	t.add("Address", "ok", fmt.Sprintf("The proxy %s answers; the console reaches %s through it.", via, u.Hostname()))
}

// tlsStep shakes hands with the server at host:port and checks its
// certificate the way the connection will: against name.
func (t *tester) tlsStep(ctx context.Context, s Spec, c Credential, host, port, name string) {
	cfg := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	against := "the system trust store"
	if s.CA == CAPEM {
		if strings.TrimSpace(c.CAPEM) == "" {
			t.add("TLS", "err", "Paste the cluster’s CA certificate (PEM): /etc/kubernetes/pki/ca.crt on a control-plane node, or certificate-authority-data from a kubeconfig.")
			return
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.CAPEM)) {
			t.add("TLS", "err", "The CA certificate is not a certificate in PEM form.")
			return
		}
		cfg.RootCAs = pool
		against = "the CA certificate"
		if desc, err := CADescription([]byte(c.CAPEM)); err == nil {
			against += " (" + desc + ")"
		}
	}
	dctx, cancel := context.WithTimeout(ctx, t.opts.Timeout)
	defer cancel()
	raw, err := t.dial(dctx, net.JoinHostPort(host, port))
	if err != nil {
		t.add("TLS", "err", "Nothing answers: "+describeNetErr(err)+".")
		return
	}
	defer raw.Close()
	conn := tls.Client(raw, cfg)
	_ = conn.SetDeadline(time.Now().Add(t.opts.Timeout))
	if err := conn.HandshakeContext(dctx); err != nil {
		// Say who signed it, which is what the person needs to fix it.
		issuer := t.serverIssuer(ctx, host, port, name)
		var unknown x509.UnknownAuthorityError
		var invalid x509.CertificateInvalidError
		var hostErr x509.HostnameError
		switch {
		case errors.As(err, &unknown):
			if s.CA == CASystem {
				t.add("TLS", "err", fmt.Sprintf("The server’s certificate is signed by %s, which the system trust store doesn’t include. Choose Cluster CA certificate.", issuer))
			} else {
				t.add("TLS", "err", fmt.Sprintf("The server’s certificate is signed by %s, not by the CA you pasted.", issuer))
			}
		case errors.As(err, &hostErr):
			t.add("TLS", "err", fmt.Sprintf("The server’s certificate is not for %s: %s.", name, hostErr.Error()))
		case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
			t.add("TLS", "err", "The server’s certificate has expired.")
		default:
			t.add("TLS", "err", "The TLS handshake failed: "+describeNetErr(err)+".")
		}
		return
	}
	state := conn.ConnectionState()
	note := ""
	if len(state.PeerCertificates) > 0 {
		left := time.Until(state.PeerCertificates[0].NotAfter)
		if left < 30*24*time.Hour {
			note = fmt.Sprintf(" Its certificate expires on %s.", state.PeerCertificates[0].NotAfter.UTC().Format("2006-01-02"))
		}
	}
	t.add("TLS", "ok", fmt.Sprintf("The certificate for %s checks out against %s.%s", name, against, note))
}

// serverIssuer names who signed the server's certificate, without verifying it.
func (t *tester) serverIssuer(ctx context.Context, host, port, name string) string {
	opts := t.opts
	dctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	raw, err := t.dial(dctx, net.JoinHostPort(host, port))
	if err != nil {
		return "an unknown CA"
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: name, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // only to read the issuer's name
	_ = conn.SetDeadline(time.Now().Add(opts.Timeout))
	if err := conn.HandshakeContext(dctx); err != nil || len(conn.ConnectionState().PeerCertificates) == 0 {
		return "an unknown CA"
	}
	cert := conn.ConnectionState().PeerCertificates[0]
	issuer := cert.Issuer.CommonName
	if issuer == "" {
		issuer = cert.Issuer.String()
	}
	return "its own CA (" + issuer + ")"
}

// whoAmI asks the API server who the credentials are (SelfSubjectReview).
func whoAmI(ctx context.Context, cs *kubernetes.Clientset, timeout time.Duration) string {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rev, err := cs.AuthenticationV1().SelfSubjectReviews().Create(wctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return ""
	}
	return rev.Status.UserInfo.Username
}

// permission asks whether the account may do one thing.
func permission(ctx context.Context, cs *kubernetes.Clientset, timeout time.Duration, verb, group, resource, subresource string) (bool, error) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rev, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(pctx, &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authzv1.ResourceAttributes{Verb: verb, Group: group, Resource: resource, Subresource: subresource},
	}}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return rev.Status.Allowed, nil
}

type check struct {
	what                  string
	verb, group, resource string
	subresource           string
	need                  bool // required for reading at all
}

func (t *tester) permissionsStep(ctx context.Context, cs *kubernetes.Clientset, s Spec, opts TestOptions, r *TestResult) {
	checks := []check{
		{"list nodes", "list", "", "nodes", "", true},
		{"list pods", "list", "", "pods", "", true},
		{"watch pods", "watch", "", "pods", "", true},
		{"list Deployments", "list", "apps", "deployments", "", true},
		{"read pod logs", "get", "", "pods", "log", false},
		{"read metrics", "list", "metrics.k8s.io", "pods", "", false},
	}
	if opts.SecretsRead {
		checks = append(checks, check{"list Secrets", "list", "", "secrets", "", false})
	}
	var missing, optional []string
	for _, c := range checks {
		ok, err := permission(ctx, cs, opts.Timeout, c.verb, c.group, c.resource, c.subresource)
		if err != nil {
			t.add("Permissions", "err", "Asking what the account may do failed: "+describeNetErr(err)+".")
			return
		}
		if !ok {
			if c.need {
				missing = append(missing, c.what)
			} else {
				optional = append(optional, c.what)
			}
		}
	}
	if len(missing) > 0 {
		t.add("Permissions", "err", fmt.Sprintf("The account cannot %s, which the console needs. Bind it to the role below.", strings.Join(missing, ", ")))
		return
	}
	// Reading as people (P3-02) takes impersonating users and groups, and
	// creating the reviews that ask what each of them may list.
	imp := true
	for _, res := range []string{"users", "groups"} {
		ok, err := permission(ctx, cs, opts.Timeout, "impersonate", "", res, "")
		if err != nil || !ok {
			imp = false
		}
	}
	reviews := false
	if ok, err := permission(ctx, cs, opts.Timeout, "create", "authorization.k8s.io", "subjectaccessreviews", ""); err == nil && ok {
		reviews = true
	}
	r.Impersonate = imp && reviews
	var notes []string
	if len(optional) > 0 {
		notes = append(notes, fmt.Sprintf("it cannot %s, so those parts will be empty", strings.Join(optional, ", ")))
	}
	switch {
	case !imp && !reviews:
		notes = append(notes, "it cannot impersonate people or review their access, so nobody will see anything here until it can: bind it to the role below")
	case !imp:
		notes = append(notes, "it cannot impersonate people, so their logs and YAML will be refused: bind it to the role below")
	case !reviews:
		notes = append(notes, "it cannot create SubjectAccessReviews, so nobody will see anything here until it can: bind it to the role below")
	}
	if s.Mode == ModeWrite {
		var cannot []string
		for _, c := range []check{{"patch nodes", "patch", "", "nodes", "", false}, {"delete pods", "delete", "", "pods", "", false}, {"evict pods", "create", "", "pods", "eviction", false}, {"restart workloads", "patch", "apps", "deployments", "", false}} {
			if ok, err := permission(ctx, cs, opts.Timeout, c.verb, c.group, c.resource, c.subresource); err != nil || !ok {
				cannot = append(cannot, c.what)
			}
		}
		if len(cannot) > 0 {
			// Said first, with every other note after it: one warning that
			// carries all the step found.
			notes = append([]string{fmt.Sprintf("the account can read, but cannot %s, so the API server will refuse Watson’s changes: bind it to the read-and-change role below, or choose Read only", strings.Join(cannot, ", "))}, notes...)
		}
	}
	text := "Can read every resource the console shows"
	if r.Impersonate {
		text += " and read as each person, so Kubernetes RBAC decides what each of them sees"
	}
	if s.Mode == ModeWrite {
		text += ", and make the changes Watson runs"
	}
	text += "."
	if len(notes) > 0 {
		t.add("Permissions", "warn", capitalize(strings.Join(notes, "; "))+".")
		return
	}
	t.add("Permissions", "ok", text)
}

func expiryNote(s Spec, now time.Time) string {
	if s.Cred.Expires != nil && s.Cred.Expires.Before(now) {
		return fmt.Sprintf(" The saved credential expired on %s; replace it.", s.Cred.Expires.Format("2006-01-02"))
	}
	return ""
}

// describeNetErr says what went wrong in plain words.
func describeNetErr(err error) string {
	if err == nil {
		return "no answer"
	}
	msg := err.Error()
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsNotFound {
			return "no such host"
		}
		return "DNS lookup failed (" + dns.Err + ")"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded") {
		return "timed out"
	}
	if strings.Contains(msg, "connection refused") {
		return "connection refused"
	}
	if strings.Contains(msg, "no route to host") {
		return "no route to host"
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 && len(msg)-i < 80 {
		return strings.TrimSuffix(msg[i+2:], ".")
	}
	return strings.TrimSuffix(msg, ".")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
