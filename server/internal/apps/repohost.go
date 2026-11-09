package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
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

// CheckRepoHost refuses a repository whose host resolves to an address the
// console does not connect to, when a source is saved. A name that does
// not resolve now is left to the connection, which checks again.
func CheckRepoHost(ctx context.Context, repo string) error {
	u, err := url.Parse(repo)
	if err != nil || u.Hostname() == "" {
		return nil
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
	c := githttp.NewClient(&http.Client{Transport: cappedTransport{base}})
	client.InstallProtocol("https", c)
	client.InstallProtocol("http", c)
}

// CheckSourceHost is CheckRepoHost for any source's address: a git
// repository's, a Helm repository's or an OCI registry's (Phase 12, wave
// 5). The tests' repositories on the loopback pass with AllowLocalRepos.
func CheckSourceHost(ctx context.Context, address string) error {
	if AllowLocalRepos {
		return nil
	}
	return CheckRepoHost(ctx, strings.Replace(address, "oci://", "https://", 1))
}
