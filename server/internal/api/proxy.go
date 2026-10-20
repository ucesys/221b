package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Several replicas (P7-02): one, the leader, runs Sherlock and Watson,
// whose conversations and cards live in its memory; the others forward
// those requests to it. Every replica checks the session first, so only
// signed-in requests travel between them, and tells the leader whose
// request it is, so the audit log and the sign-in limits see the person's
// address rather than the replica's.

// Leader says whether this replica runs Sherlock and Watson, and where the
// one that does is; *leader.Elector implements it.
type Leader interface {
	IsLeader() bool
	// LeaderAddr is the leader's host:port, or "" while no replica leads.
	LeaderAddr() string
}

const (
	// clientHeader carries the address a forwarded request came from;
	// proxyHeader proves another replica sent it.
	clientHeader = "X-221B-Client"
	proxyHeader  = "X-221B-Proxy"
)

// leaderChecks is how often a forwarded stream checks that the leader it
// reaches still leads; when it moves, the stream ends and the browser
// reconnects to the new one.
const leaderChecks = 2 * time.Second

// leaderOnly says whether a path belongs to Sherlock or Watson: their
// routes for the local cluster (/api/v1/sherlock/…), for a connection
// (/api/v1/clusters/{cluster}/watson/…), the assistants' shared stream and
// their administrator settings.
func leaderOnly(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if parts[0] == "clusters" {
		if len(parts) < 3 {
			return false
		}
		parts = parts[2:]
	}
	switch parts[0] {
	case "sherlock", "watson", "assistants":
		return true
	}
	return false
}

// trustForwarded takes the client's address from a request another
// replica forwarded, and drops the headers from any other request, so no
// browser can claim an address.
func (s *Server) trustForwarded(r *http.Request) {
	client := r.Header.Get(clientHeader)
	token := r.Header.Get(proxyHeader)
	r.Header.Del(clientHeader)
	r.Header.Del(proxyHeader)
	if client == "" || s.opts.ProxyToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.ProxyToken)) != 1 {
		return
	}
	if net.ParseIP(client) == nil {
		return
	}
	// Everything after this reads the address from RemoteAddr: the audit
	// log, the sign-in limits.
	r.RemoteAddr = net.JoinHostPort(client, "0")
}

// forwardToLeader sends a Sherlock or Watson request on to the leader and
// says whether it did; the leader itself, and a single replica, keep it.
func (s *Server) forwardToLeader(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Leader == nil || s.opts.Leader.IsLeader() || !leaderOnly(r.URL.Path) {
		return false
	}
	addr := s.opts.Leader.LeaderAddr()
	if addr == "" {
		s.leaderUnavailable(w)
		return true
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		t := time.NewTicker(s.leaderChecks)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.opts.Leader.IsLeader() || s.opts.Leader.LeaderAddr() != addr {
					cancel()
					return
				}
			}
		}
	}()
	// The leader sets its own security headers; the proxy adds them to
	// these, which would send each twice.
	for _, kv := range securityHeaders {
		w.Header().Del(kv[0])
	}
	s.proxyFor(addr).ServeHTTP(w, r.WithContext(ctx))
	return true
}

// proxyFor builds the reverse proxy to a leader. The request goes as the
// browser sent it, with its Host and session cookie, so the leader checks
// the session and the site the same way.
func (s *Server) proxyFor(addr string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: addr}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			// Rewrite drops the X-Forwarded headers; the leader needs the
			// Ingress's to know the browser used HTTPS.
			for _, k := range []string{"X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For"} {
				if v := pr.In.Header.Values(k); len(v) > 0 {
					pr.Out.Header[k] = v
				}
			}
			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
			pr.Out.Header.Set(clientHeader, fromAddr(pr.In))
			pr.Out.Header.Set(proxyHeader, s.opts.ProxyToken)
		},
		// Event streams pass every event on as it comes.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.opts.Logger.Warn("the leader did not answer a forwarded request", "leader", addr, "path", r.URL.Path, "err", err)
			s.leaderUnavailable(w)
		},
	}
}

func (s *Server) leaderUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	jsonError(w, http.StatusServiceUnavailable, "Sherlock and Watson are moving to another replica; try again in a moment")
}
