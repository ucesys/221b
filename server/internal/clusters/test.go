package clusters

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
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
	r := TestResult{}
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
		if net.ParseIP(host) == nil {
			rctx, cancel := context.WithTimeout(ctx, opts.Timeout)
			ips, err := opts.Resolver(rctx, host)
			cancel()
			if err != nil || len(ips) == 0 {
				t.add("Address", "err", fmt.Sprintf("%s does not resolve: %s.", host, describeNetErr(err)))
			} else {
				where = fmt.Sprintf("%s resolves to %s, which answers", host, ips[0])
			}
		}
		if !t.dead {
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
	default:
		t.tlsStep(ctx, s, c, host, port)
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
			if who == "" {
				who = s.Cred.Who
			}
			r.Who = who
			if who != "" {
				t.add("Sign-in", "ok", "Signed in as "+who+".")
			} else {
				t.add("Sign-in", "ok", "Signed in.")
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

// tlsStep shakes hands with the server and checks its certificate the way
// the connection will.
func (t *tester) tlsStep(ctx context.Context, s Spec, c Credential, host, port string) {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
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
	raw, err := t.opts.Dialer(dctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		t.add("TLS", "err", "Nothing answers: "+describeNetErr(err)+".")
		return
	}
	defer raw.Close()
	conn := tls.Client(raw, cfg)
	_ = conn.SetDeadline(time.Now().Add(t.opts.Timeout))
	if err := conn.HandshakeContext(dctx); err != nil {
		// Say who signed it, which is what the person needs to fix it.
		issuer := serverIssuer(ctx, t.opts, host, port)
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
			t.add("TLS", "err", fmt.Sprintf("The server’s certificate is not for %s: %s.", host, hostErr.Error()))
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
	t.add("TLS", "ok", fmt.Sprintf("The certificate for %s checks out against %s.%s", host, against, note))
}

// serverIssuer names who signed the server's certificate, without verifying it.
func serverIssuer(ctx context.Context, opts TestOptions, host, port string) string {
	dctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	raw, err := opts.Dialer(dctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return "an unknown CA"
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // only to read the issuer's name
	_ = conn.SetDeadline(time.Now().Add(opts.Timeout))
	if err := conn.HandshakeContext(dctx); err != nil || len(conn.ConnectionState().PeerCertificates) == 0 {
		return "an unknown CA"
	}
	cert := conn.ConnectionState().PeerCertificates[0]
	name := cert.Issuer.CommonName
	if name == "" {
		name = cert.Issuer.String()
	}
	return "its own CA (" + name + ")"
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
			t.add("Permissions", "warn", fmt.Sprintf("The account can read, but cannot %s, so the API server will refuse Watson’s changes. Bind it to the read-and-change role below, or choose Read only.", strings.Join(cannot, ", ")))
			return
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
