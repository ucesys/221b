package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
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
