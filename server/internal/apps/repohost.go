package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/ucesys/221b/server/internal/certpin"
)

// Which hosts the console fetches repositories from. Git servers on
// private addresses are ordinary on-premises and stay allowed; what is
// refused is the console's own pod and node (loopback, link-local) and the
// clouds' metadata services. The name is checked when a source is saved and
// the address at every connection, redirects included, so a name that
// later resolves elsewhere gets no further.

var errHostRefused = errors.New("the repository's host is a loopback, link-local or cloud metadata address, which the console does not fetch from")

// metadataAddrs are metadata services outside the link-local range.
var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"):   true, // Azure
	netip.MustParseAddr("192.0.0.192"):     true, // Oracle Cloud
	netip.MustParseAddr("fd00:ec2::254"):   true, // AWS
	netip.MustParseAddr("fd20:ce::254"):    true, // Google Cloud
}

// refusedAddr says whether the console does not connect to an address.
func refusedAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || metadataAddrs[ip]
}

// refusedName says whether a host is refused as it is written, without
// asking DNS: an address, or a name that means one.
func refusedName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		return refusedAddr(ip)
	}
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "metadata.google.internal"
}

// --- The hosts a console fetches from (apps.allowedHosts) ---

// errHostNotAllowed is a repository on a host the chart's apps.allowedHosts
// does not list.
var errHostNotAllowed = errors.New("the repository's host is not one this console fetches from: apps.allowedHosts in the chart's values lists them")

// hostPattern is one entry of apps.allowedHosts: a host name, or with
// suffix any name under it; with port, that port only.
type hostPattern struct {
	host   string
	suffix bool
	port   string
}

var (
	allowedMu    sync.RWMutex
	allowedHosts []hostPattern
)

// SetAllowedHosts sets the hosts the console fetches git repositories,
// Helm repositories and OCI registries from: a host name
// (git.example.com), or *.example.com for any name under example.com,
// either with :port for that port only. Empty allows any host the other
// checks do.
func SetAllowedHosts(list []string) error {
	var out []hostPattern
	for _, s := range list {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		p, err := parseHostPattern(s)
		if err != nil {
			return err
		}
		out = append(out, p)
	}
	allowedMu.Lock()
	allowedHosts = out
	allowedMu.Unlock()
	return nil
}

func parseHostPattern(s string) (hostPattern, error) {
	in := s
	s = strings.ToLower(s)
	var p hostPattern
	if h, port, err := net.SplitHostPort(s); err == nil {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return p, fmt.Errorf("allowed host %q: the port is a number from 1 to 65535", in)
		}
		s, p.port = h, port
	} else if _, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		s = strings.Trim(s, "[]")
	}
	s = strings.TrimSuffix(s, ".")
	if rest, ok := strings.CutPrefix(s, "*."); ok {
		s, p.suffix = rest, true
	}
	if s == "" || strings.ContainsAny(s, "*/@?#[] \t") || (strings.Contains(s, ":") && !isAddr(s)) {
		return p, fmt.Errorf("allowed host %q: give a host name, *.domain, or either with :port", in)
	}
	p.host = s
	return p, nil
}

func isAddr(s string) bool { _, err := netip.ParseAddr(s); return err == nil }

// hostAllowed says whether apps.allowedHosts lets the console connect to
// host on port.
func hostAllowed(host, port string) bool {
	allowedMu.RLock()
	defer allowedMu.RUnlock()
	if len(allowedHosts) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range allowedHosts {
		if p.port != "" && p.port != port {
			continue
		}
		if host == p.host && !p.suffix || p.suffix && strings.HasSuffix(host, "."+p.host) {
			return true
		}
	}
	return false
}

// checkAllowed refuses an address whose host apps.allowedHosts does not
// list; one that cannot be read is left to the other checks.
func checkAllowed(u *url.URL) error {
	if u == nil || u.Hostname() == "" {
		return nil
	}
	port := u.Port()
	if port == "" {
		port = map[bool]string{true: "80", false: "443"}[strings.EqualFold(u.Scheme, "http")]
	}
	if !hostAllowed(u.Hostname(), port) {
		return errHostNotAllowed
	}
	return nil
}

// allowedTransport refuses every request, each redirect's included, to a
// host apps.allowedHosts does not list.
type allowedTransport struct{ http.RoundTripper }

func (t allowedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := checkAllowed(r.URL); err != nil {
		return nil, err
	}
	return t.RoundTripper.RoundTrip(r)
}

// --- What a person is told of a fetch that failed ---

// ErrNotFetched is what a person is told of a fetch that failed on the
// way: a host that refused the connection or did not answer, a name that
// did not resolve, a TLS failure, an HTTP status other than a refused
// sign-in or a missing repository. Its cause goes to the server's log.
var ErrNotFetched = errors.New("the repository could not be fetched")

type notFetched struct{ cause error }

func (e notFetched) Error() string        { return ErrNotFetched.Error() }
func (e notFetched) Is(target error) bool { return target == ErrNotFetched }

// FetchCause is the cause of an ErrNotFetched, for a log, or "".
func FetchCause(err error) string {
	var nf notFetched
	if errors.As(err, &nf) && nf.cause != nil {
		return nf.cause.Error()
	}
	return ""
}

// notFetchedFor logs a fetch's cause and returns ErrNotFetched for it.
func notFetchedFor(cause error) error {
	slog.Default().Warn("a repository could not be fetched", "component", "apps", "cause", cause.Error())
	return notFetched{cause: cause}
}

// onTheWay says whether a fetch failed on the network, between the
// console and the repository, rather than in what the repository said.
func onTheWay(err error) bool {
	var ue *url.Error
	var ne net.Error
	var he *githttp.Err
	var ux *plumbing.UnexpectedError
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ue) || errors.As(err, &ne) || errors.As(err, &he) || errors.As(err, &ux) ||
		strings.HasPrefix(err.Error(), "http redirect:")
}

// CheckRepoHost refuses a repository whose host apps.allowedHosts does not
// list, or that resolves to an address the console does not connect to,
// when a source is saved. A name that does not resolve now is left to the
// connection, which checks again.
func CheckRepoHost(ctx context.Context, repo string) error {
	u, err := url.Parse(repo)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	if err := checkAllowed(u); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return nil
	}
	for _, ip := range addrs {
		if refusedAddr(ip) {
			return errHostRefused
		}
	}
	return nil
}

// checkConnect is the dialer's check: it sees the address each connection
// is about to use, after DNS.
func checkConnect(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("the address %q cannot be read: %w", address, err)
	}
	if refusedAddr(ip) {
		return errHostRefused
	}
	return nil
}

// cappedBody fails a response longer than a fetch may be, which also
// bounds a list of refs, which never reaches the pack's own check.
type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.left -= int64(n); b.left < 0 {
		return 0, fmt.Errorf("%w: it sends more than %d MB", ErrFetchLimit, FetchLimits.Pack>>20)
	}
	return n, err
}

type cappedTransport struct{ http.RoundTripper }

func (t cappedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.RoundTripper.RoundTrip(r)
	if err == nil {
		res.Body = &cappedBody{ReadCloser: res.Body, left: FetchLimits.Pack + 1<<20}
	}
	return res, err
}

func init() {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: checkConnect}).DialContext
	// Each fetch's context carries the trust of its repository's
	// credential or its own: a private CA, a pinned certificate (CR-17).
	c := githttp.NewClient(&http.Client{Transport: cappedTransport{allowedTransport{&certpin.ContextTransport{Base: base}}}})
	client.InstallProtocol("https", c)
	client.InstallProtocol("http", c)
}

// CheckSourceHost is CheckRepoHost for any source's address: a git
// repository's, a Helm repository's or an OCI registry's (Phase 12, wave
// 5). The tests' repositories on the loopback pass with AllowLocalRepos.
func CheckSourceHost(ctx context.Context, address string) error {
	address = strings.Replace(address, "oci://", "https://", 1)
	if u, err := url.Parse(address); err == nil {
		if err := checkAllowed(u); err != nil {
			return err
		}
	}
	if AllowLocalRepos {
		return nil
	}
	return CheckRepoHost(ctx, address)
}
