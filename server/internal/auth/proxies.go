package auth

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
)

// Proxies are the addresses whose X-Forwarded-For the console believes
// (auth.trustedProxies). Nobody else's is: any client can send the header.
type Proxies []netip.Prefix

// ParseProxies reads a list of CIDRs; a lone address counts as one.
func ParseProxies(list []string) (Proxies, error) {
	var out Proxies
	for _, s := range list {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not a CIDR such as 10.0.0.0/8", s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func (p Proxies) has(a netip.Addr) bool {
	for _, n := range p {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// hop reads one entry of X-Forwarded-For; some proxies add the port.
func hop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	a, err := netip.ParseAddr(s)
	if err != nil {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			return netip.Addr{}, false
		}
		a = ap.Addr()
	}
	return a.Unmap().WithZone(""), true
}

// Resolve works out the address a request that reached this replica from
// outside came from, and leaves it in RemoteAddr, where the audit log and
// the sign-in limits read it. X-Forwarded-For counts only from a trusted
// proxy, read from the right: the first entry no trusted proxy added is
// the client. Everything to its left is the client's to write.
//
// Anyone else's header is ignored: the request keeps the socket peer's
// address, and meets that address's limits like any other.
func (p Proxies) Resolve(r *http.Request) *http.Request {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	addr := peer.Addr().Unmap().WithZone("")
	if !p.has(addr) {
		return r
	}
	client := addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := hop(hops[i])
		if !ok {
			break
		}
		client = a
		if !p.has(a) {
			break
		}
	}
	r.RemoteAddr = net.JoinHostPort(client.String(), "0")
	return r
}

// forwardingHeaders are the headers a proxy names the client in, as
// http.Header keys them.
var forwardingHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"}

// proxyWarnEvery is how often the log repeats that a proxy is not trusted.
const proxyWarnEvery = time.Hour

// ProxyWatch notices requests that carry a forwarding header from a peer
// auth.trustedProxies does not list. The header is not believed, so
// everyone behind that peer shares its address: one limit per address on
// sign-in, and one From in the audit log. The log says so once, then at
// most once an hour, and Administration shows the peer to SuperAdmins.
// A nil watch notices nothing.
type ProxyWatch struct {
	peer   atomic.Pointer[netip.Addr]
	warned atomic.Int64 // when the log last said so, in Unix nanoseconds
	log    *slog.Logger
	now    func() time.Time
}

// NewProxyWatch makes a watch that warns on log.
func NewProxyWatch(log *slog.Logger, now func() time.Time) *ProxyWatch {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &ProxyWatch{log: log, now: now}
}

// Check notes r when it carries a forwarding header and its socket peer
// is not one of p. It writes nothing for a request like any before it.
func (w *ProxyWatch) Check(p Proxies, r *http.Request) {
	if w == nil {
		return
	}
	carries := false
	for _, h := range forwardingHeaders {
		if len(r.Header[h]) > 0 {
			carries = true
			break
		}
	}
	if !carries {
		return
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return
	}
	addr := peer.Addr().Unmap().WithZone("")
	if p.has(addr) {
		return
	}
	if old := w.peer.Load(); old == nil || *old != addr {
		w.peer.Store(&addr)
	}
	now := w.now().UnixNano()
	last := w.warned.Load()
	if last != 0 && now-last < int64(proxyWarnEvery) {
		return
	}
	if w.warned.CompareAndSwap(last, now) {
		w.log.Warn("requests reach the console through a proxy it does not trust: the sign-in limit per address and the audit log see the proxy's address; list the proxy under auth.trustedProxies", "peer", addr.String())
	}
}

// Peer is the last untrusted peer that sent a forwarding header, or "".
func (w *ProxyWatch) Peer() string {
	if w == nil {
		return ""
	}
	if a := w.peer.Load(); a != nil {
		return a.String()
	}
	return ""
}
