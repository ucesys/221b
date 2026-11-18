package certpin

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"
)

// Transport is base with t's trust and a client certificate, when there
// is either; base as it is otherwise, or when it is not an
// *http.Transport (tests' own).
func Transport(base http.RoundTripper, t Trust, cert *tls.Certificate) http.RoundTripper {
	if t.Empty() && cert == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	tr, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	tr = tr.Clone()
	tr.TLSClientConfig = Config(tr.TLSClientConfig, t)
	if cert != nil {
		tr.TLSClientConfig.Certificates = []tls.Certificate{*cert}
	}
	return tr
}

// Client is hc (nil is http.DefaultClient's settings) with t's trust and
// a client certificate; hc itself when there is neither.
func Client(hc *http.Client, t Trust, cert *tls.Certificate) *http.Client {
	if t.Empty() && cert == nil {
		return hc
	}
	out := &http.Client{}
	if hc != nil {
		*out = *hc
	}
	out.Transport = Transport(out.Transport, t, cert)
	return out
}

type ctxKey struct{}

// With is ctx carrying t, added to what it carries (t first): a request
// made with it through a ContextTransport trusts t. It carries the trust
// of a fetch through code that builds its own requests (go-git).
func With(ctx context.Context, t Trust) context.Context {
	have, _ := ctx.Value(ctxKey{}).(Trust)
	t = t.Or(have)
	if t.Empty() {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, t)
}

// From is the trust ctx carries.
func From(ctx context.Context) Trust {
	t, _ := ctx.Value(ctxKey{}).(Trust)
	return t
}

// maxTransports bounds the transports a ContextTransport keeps, one per
// trust.
const maxTransports = 64

// ContextTransport sends each request with the trust its context
// carries (With): base for none, a copy of base with that trust
// otherwise, kept for the next request with the same trust.
type ContextTransport struct {
	Base *http.Transport
	mu   sync.Mutex
	by   map[Trust]*http.Transport
}

func (c *ContextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t := From(r.Context())
	if t.Empty() {
		return c.Base.RoundTrip(r)
	}
	return c.transport(t).RoundTrip(r)
}

func (c *ContextTransport) transport(t Trust) *http.Transport {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tr, ok := c.by[t]; ok {
		return tr
	}
	if c.by == nil || len(c.by) >= maxTransports {
		for _, old := range c.by {
			old.CloseIdleConnections()
		}
		c.by = map[Trust]*http.Transport{}
	}
	tr := c.Base.Clone()
	tr.TLSClientConfig = Config(tr.TLSClientConfig, t)
	c.by[t] = tr
	return tr
}

// CloseIdleConnections closes every kept transport's idle connections.
func (c *ContextTransport) CloseIdleConnections() {
	c.Base.CloseIdleConnections()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tr := range c.by {
		tr.CloseIdleConnections()
	}
}
