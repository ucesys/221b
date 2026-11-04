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

// Several replicas (P7-02): one, the leader, reads the clusters and runs
// Sherlock and Watson (ADR-60, ADR-70); the others sign people in and
// forward every other API request to it. Every replica checks the session
// first, so only signed-in requests travel between them, and tells the
// leader whose request it is, so the audit log and the sign-in limits see
// the person's address rather than the replica's.

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

// localPaths are the API paths every replica serves itself (ADR-70): the
// person's own session and the account administration, which read and
// write Kubernetes rather than a cluster's cache. Sign-in, set-up, the
// probes, the metrics and the UI are outside /api/v1/ and local too.
var localPaths = []string{"/api/v1/me", "/api/v1/admin/users", "/api/v1/admin/ldap/refresh"}

// leaderOnly says whether a path is the leader's: every API path but the
// replica-local ones, so a new data route is forwarded unless it is
// listed here. The leader reads the clusters, so only it has their rows,
// usage figures and connection states, and only it runs Sherlock, Watson,
// the Toolbox and the applications.
func leaderOnly(path string) bool {
	if !strings.HasPrefix(path, "/api/v1/") {
		return false
	}
	for _, p := range localPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return false
		}
	}
	return true
}

// trustForwarded takes the client's address from a request another
// replica forwarded, and drops the headers from any other request, so no
// browser can claim an address. It reports whether a replica sent it.
func (s *Server) trustForwarded(r *http.Request) bool {
	client := r.Header.Get(clientHeader)
	token := r.Header.Get(proxyHeader)
	r.Header.Del(clientHeader)
	r.Header.Del(proxyHeader)
	if client == "" || s.opts.ProxyToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.opts.ProxyToken)) != 1 {
		return false
	}
	if net.ParseIP(client) == nil {
		return false
	}
	// Everything after this reads the address from RemoteAddr: the audit
	// log, the sign-in limits.
	r.RemoteAddr = net.JoinHostPort(client, "0")
	return true
}

// forwardToLeader sends a request for the leader on to it and says
// whether it did; the leader itself, and a single replica, keep it.
// A request another replica forwarded is not sent on again: while the
// leader changes, two replicas could each take the other for it and pass
// a request back and forth.
func (s *Server) forwardToLeader(w http.ResponseWriter, r *http.Request, forwarded bool) bool {
	if s.opts.Leader == nil || s.opts.Leader.IsLeader() || !leaderOnly(r.URL.Path) {
		return false
	}
	addr := s.opts.Leader.LeaderAddr()
	if addr == "" || forwarded {
		s.leaderUnavailable(w)
		s.opts.Logger.Debug("request", "method", r.Method, "path", r.URL.Path, "code", http.StatusServiceUnavailable, "leader", addr, "forwarded", forwarded)
		return true
	}
	// Logged here, since the leader answers it: which leader, and how.
	sw := &statusWriter{ResponseWriter: w}
	w = sw
	start := time.Now()
	defer func() {
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		s.opts.Logger.Debug("request forwarded", "method", r.Method, "path", r.URL.Path, "leader", addr, "code", code, "duration", time.Since(start))
	}()
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
		// Before the leader answered: it is gone, or moved while it was
		// asked, and the app tries again. Saying nothing would be an empty
		// 200.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) {
				s.opts.Logger.Warn("the leader did not answer a forwarded request", "leader", addr, "path", r.URL.Path, "err", err)
			}
			s.leaderUnavailable(w)
		},
	}
}

func (s *Server) leaderUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	jsonError(w, http.StatusServiceUnavailable, "The console is moving to another replica; try again in a moment")
}
