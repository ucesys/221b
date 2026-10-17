// Package ldap is a small LDAP client for 221B's sign-in (RB-19, RB-22):
// simple bind, search with paging, StartTLS, and the filters and group
// resolution a directory login needs. It uses only the standard library.
package ldap

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// Result codes the client acts on (RFC 4511 section 4.1.9).
const (
	ResultSuccess            = 0
	ResultSizeLimit          = 4
	ResultInvalidCredentials = 49
	ResultNoSuchObject       = 32
)

// Error is an LDAP result that was not a success.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("LDAP result %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("LDAP result %d", e.Code)
}

// Config says how to reach a directory.
type Config struct {
	// URL is ldap://host:389 or ldaps://host:636.
	URL string
	// StartTLS upgrades an ldap:// connection before anything is sent.
	StartTLS bool
	// RootCAs verify the server; nil uses the system's.
	RootCAs *x509.CertPool
	// Timeout bounds dialing and every request.
	Timeout time.Duration
	// Dialer replaces the network dial, for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Conn is one connection to a directory. It is not safe for concurrent use.
type Conn struct {
	c       net.Conn
	r       *bufio.Reader
	id      int
	timeout time.Duration
}

// Dial connects, and with StartTLS or ldaps:// protects the connection
// before returning. Nothing, a password included, is sent in the clear to a
// server that was asked to be reached securely.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Hostname() == "" {
		return nil, fmt.Errorf("the LDAP URL %q is not ldap://host or ldaps://host", cfg.URL)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"ldap": "389", "ldaps": "636"}[u.Scheme]
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	raw, err := dial(dctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", u.Host, err)
	}
	tlsCfg := &tls.Config{ServerName: u.Hostname(), RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12}
	conn := &Conn{c: raw, timeout: cfg.Timeout}
	if u.Scheme == "ldaps" {
		t := tls.Client(raw, tlsCfg)
		_ = raw.SetDeadline(time.Now().Add(cfg.Timeout))
		if err := t.HandshakeContext(dctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("the TLS handshake with %s: %w", u.Host, err)
		}
		conn.c = t
	}
	conn.r = bufio.NewReader(conn.c)
	if u.Scheme == "ldap" && cfg.StartTLS {
		if err := conn.startTLS(tlsCfg); err != nil {
			raw.Close()
			return nil, err
		}
	}
	return conn, nil
}

// Close ends the session politely and drops the connection.
func (c *Conn) Close() {
	_ = c.write(&element{id: classApplication | 2})
	_ = c.c.Close()
}

func (c *Conn) write(op *element) error {
	c.id++
	msg := seq(tagSequence, integer(tagInteger, c.id), op)
	_ = c.c.SetWriteDeadline(time.Now().Add(c.timeout))
	_, err := c.c.Write(msg.bytes())
	return err
}

// message is a decoded LDAPMessage.
type message struct {
	id       int
	op       *element
	controls []*element
}

func (c *Conn) read() (*message, error) {
	_ = c.c.SetReadDeadline(time.Now().Add(c.timeout))
	head := make([]byte, 2)
	if _, err := io.ReadFull(c.r, head); err != nil {
		return nil, err
	}
	buf := append([]byte(nil), head...)
	if head[1]&0x80 != 0 {
		k := int(head[1] & 0x7f)
		if k == 0 || k > 4 {
			return nil, errors.New("ldap: a bad length")
		}
		ext := make([]byte, k)
		if _, err := io.ReadFull(c.r, ext); err != nil {
			return nil, err
		}
		buf = append(buf, ext...)
		l := 0
		for _, b := range ext {
			l = l<<8 | int(b)
		}
		if l > maxMessage {
			return nil, errors.New("ldap: a message too large")
		}
		body := make([]byte, l)
		if _, err := io.ReadFull(c.r, body); err != nil {
			return nil, err
		}
		buf = append(buf, body...)
	} else {
		body := make([]byte, int(head[1]))
		if _, err := io.ReadFull(c.r, body); err != nil {
			return nil, err
		}
		buf = append(buf, body...)
	}
	e, _, err := decode(buf, 0)
	if err != nil {
		return nil, err
	}
	if len(e.children) < 2 {
		return nil, errors.New("ldap: a message without an operation")
	}
	id, err := e.children[0].int()
	if err != nil {
		return nil, err
	}
	m := &message{id: id, op: e.children[1]}
	for _, ch := range e.children[2:] {
		if ch.id == classContext|constructed {
			m.controls = ch.children
		}
	}
	return m, nil
}

// result reads an LDAPResult: code, matched DN, diagnostic message.
func result(op *element) *Error {
	if len(op.children) < 3 {
		return &Error{Code: -1, Message: "a malformed result"}
	}
	code, err := op.children[0].int()
	if err != nil {
		return &Error{Code: -1, Message: "a malformed result code"}
	}
	return &Error{Code: code, Message: string(op.children[2].content)}
}

// Bind authenticates with a DN and a password. An empty password is refused
// here: LDAP treats it as an anonymous bind, which succeeds, and a login must
// never succeed that way.
func (c *Conn) Bind(dn, password string) error {
	if dn != "" && password == "" {
		return &Error{Code: ResultInvalidCredentials, Message: "an empty password"}
	}
	req := &element{id: classApplication | constructed | 0, children: []*element{integer(tagInteger, 3), octets(tagOctetString, dn), octets(classContext, password)}}
	if err := c.write(req); err != nil {
		return err
	}
	m, err := c.read()
	if err != nil {
		return err
	}
	if m.op.id != classApplication|constructed|1 {
		return fmt.Errorf("ldap: expected a bind response, got tag %#x", m.op.id)
	}
	if r := result(m.op); r.Code != ResultSuccess {
		return r
	}
	return nil
}

const oidStartTLS = "1.3.6.1.4.1.1466.20037"

func (c *Conn) startTLS(cfg *tls.Config) error {
	req := &element{id: classApplication | constructed | 23, children: []*element{octets(classContext|0, oidStartTLS)}}
	if err := c.write(req); err != nil {
		return err
	}
	m, err := c.read()
	if err != nil {
		return err
	}
	if m.op.id != classApplication|constructed|24 {
		return fmt.Errorf("ldap: expected a StartTLS response, got tag %#x", m.op.id)
	}
	if r := result(m.op); r.Code != ResultSuccess {
		return fmt.Errorf("the server refused StartTLS: %w", r)
	}
	t := tls.Client(c.c, cfg)
	_ = c.c.SetDeadline(time.Now().Add(c.timeout))
	if err := t.Handshake(); err != nil {
		return fmt.Errorf("the TLS handshake after StartTLS: %w", err)
	}
	c.c, c.r = t, bufio.NewReader(t)
	return nil
}

// Scope of a search.
const (
	ScopeBase = 0
	ScopeOne  = 1
	ScopeSub  = 2
)

// Entry is one result of a search.
type Entry struct {
	DN    string
	Attrs map[string][]string
}

// First is the attribute's first value, or "".
func (e Entry) First(attr string) string {
	if v := e.Attrs[strings.ToLower(attr)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Values are the attribute's values.
func (e Entry) Values(attr string) []string { return e.Attrs[strings.ToLower(attr)] }

// SearchRequest asks for entries.
type SearchRequest struct {
	Base   string
	Scope  int
	Filter string
	Attrs  []string
	// PageSize asks the server for results in pages of this size (0: none).
	PageSize int
	// SizeLimit stops a search at that many entries (0: the server's).
	SizeLimit int
}

const oidPaged = "1.2.840.113556.1.4.319"

// Search runs a search and returns every entry, following the pages.
func (c *Conn) Search(req SearchRequest) ([]Entry, error) {
	filter, err := compileFilter(req.Filter)
	if err != nil {
		return nil, err
	}
	var out []Entry
	var cookie []byte
	for {
		attrs := &element{id: tagSequence | constructed}
		for _, a := range req.Attrs {
			attrs.children = append(attrs.children, octets(tagOctetString, a))
		}
		op := &element{id: classApplication | constructed | 3, children: []*element{
			octets(tagOctetString, req.Base), integer(tagEnumerated, req.Scope), integer(tagEnumerated, 0),
			integer(tagInteger, req.SizeLimit), integer(tagInteger, int(c.timeout/time.Second)), boolean(false), filter, attrs,
		}}
		c.id++
		msg := seq(tagSequence, integer(tagInteger, c.id), op)
		if req.PageSize > 0 {
			value := seq(tagSequence, integer(tagInteger, req.PageSize), octets(tagOctetString, string(cookie)))
			ctl := seq(tagSequence, octets(tagOctetString, oidPaged), boolean(false), octets(tagOctetString, string(value.bytes())))
			msg.children = append(msg.children, &element{id: classContext | constructed, children: []*element{ctl}})
		}
		_ = c.c.SetWriteDeadline(time.Now().Add(c.timeout))
		if _, err := c.c.Write(msg.bytes()); err != nil {
			return nil, err
		}
		id := c.id
		cookie = nil
	read:
		for {
			m, err := c.read()
			if err != nil {
				return nil, err
			}
			if m.id != id {
				continue
			}
			switch m.op.id {
			case classApplication | constructed | 4:
				e, err := entryOf(m.op)
				if err != nil {
					return nil, err
				}
				out = append(out, e)
			case classApplication | constructed | 19:
				// A referral to another server: not followed.
			case classApplication | constructed | 5:
				if r := result(m.op); r.Code != ResultSuccess {
					if r.Code == ResultSizeLimit {
						r.Message = "the server's size limit stopped the search; raise it or turn on paging (auth.ldap.pageSize)"
					}
					return nil, r
				}
				cookie = pagedCookie(m.controls)
				break read
			default:
				return nil, fmt.Errorf("ldap: unexpected tag %#x in a search", m.op.id)
			}
		}
		if len(cookie) == 0 {
			return out, nil
		}
	}
}

func entryOf(op *element) (Entry, error) {
	if len(op.children) < 2 {
		return Entry{}, errors.New("ldap: a malformed entry")
	}
	e := Entry{DN: string(op.children[0].content), Attrs: map[string][]string{}}
	for _, a := range op.children[1].children {
		if len(a.children) < 2 {
			continue
		}
		name := strings.ToLower(string(a.children[0].content))
		for _, v := range a.children[1].children {
			e.Attrs[name] = append(e.Attrs[name], string(v.content))
		}
	}
	return e, nil
}

// pagedCookie reads the paged-results control of a search's end.
func pagedCookie(controls []*element) []byte {
	for _, ctl := range controls {
		if len(ctl.children) < 2 || string(ctl.children[0].content) != oidPaged {
			continue
		}
		val := ctl.children[len(ctl.children)-1]
		inner, _, err := decode(val.content, 0)
		if err != nil || len(inner.children) < 2 {
			return nil
		}
		return inner.children[1].content
	}
	return nil
}
