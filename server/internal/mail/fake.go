package mail

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Capture is a Sender that keeps what it is given, for tests: Fail makes
// it fail, and the next sends with it.
type Capture struct {
	mu   sync.Mutex
	sent []Captured
	// Fail, when set, is the error each send answers with, and the
	// message is not kept.
	Fail error
	// FailTimes fails that many sends with Fail (or a generic error)
	// before the next one goes; 0 with Fail set fails every one.
	FailTimes int
}

// Captured is one message as a Capture or the fake server kept it.
type Captured struct {
	At      time.Time `json:"at"`
	Server  string    `json:"server,omitempty"`
	User    string    `json:"user,omitempty"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Subject string    `json:"subject,omitempty"`
	Text    string    `json:"text,omitempty"`
	HTML    string    `json:"html,omitempty"`
	// Data is the message as SMTP carried it, for the fake server.
	Data string `json:"data,omitempty"`
}

// Send keeps the message, or fails.
func (c *Capture) Send(_ context.Context, srv Server, m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.FailTimes > 0 {
		c.FailTimes--
		if c.Fail != nil {
			return c.Fail
		}
		return errors.New("550 refused by the test")
	}
	if c.Fail != nil {
		return c.Fail
	}
	c.sent = append(c.sent, Captured{At: time.Now(), Server: srv.Address(), User: srv.Username, From: m.From, To: m.To, Subject: m.Subject, Text: m.Text, HTML: m.HTML})
	return nil
}

// Sent are the messages kept, oldest first.
func (c *Capture) Sent() []Captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Captured(nil), c.sent...)
}

// Reset forgets the messages kept.
func (c *Capture) Reset() {
	c.mu.Lock()
	c.sent = nil
	c.mu.Unlock()
}

// FakeServer is an SMTP server in memory, for the simulator and the
// tests (EM-10): it takes every message, in the clear and without
// sign-in (security none), and keeps the last 200. Handler serves them as
// JSON (GET) and forgets them (DELETE).
type FakeServer struct {
	mu   sync.Mutex
	msgs []Captured
	ln   net.Listener
}

// ListenFake serves a fake SMTP server at addr ("127.0.0.1:0" for any
// port).
func ListenFake(addr string) (*FakeServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f := &FakeServer{ln: ln}
	go f.serve()
	return f, nil
}

// Addr is where it listens.
func (f *FakeServer) Addr() string { return f.ln.Addr().String() }

// Close stops it.
func (f *FakeServer) Close() error { return f.ln.Close() }

// Messages are the messages taken, oldest first.
func (f *FakeServer) Messages() []Captured {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Captured(nil), f.msgs...)
}

// Handler serves the messages: GET lists them as {"messages": [...]},
// DELETE forgets them.
func (f *FakeServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": f.Messages()})
		case http.MethodDelete:
			f.mu.Lock()
			f.msgs = nil
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func (f *FakeServer) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.session(conn)
	}
}

func (f *FakeServer) session(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 fake-smtp.221b ESMTP")
	var from string
	var to []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"):
			say("250-fake-smtp.221b")
			say("250 8BITMIME")
		case strings.HasPrefix(verb, "HELO"):
			say("250 fake-smtp.221b")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			from, to = pathOf(line[10:]), nil
			say("250 OK")
		case strings.HasPrefix(verb, "RCPT TO:"):
			to = append(to, pathOf(line[8:]))
			say("250 OK")
		case verb == "DATA":
			say("354 go on")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			f.keep(from, to, data.String())
			say("250 OK: queued")
		case verb == "RSET":
			from, to = "", nil
			say("250 OK")
		case verb == "NOOP":
			say("250 OK")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not here")
		}
	}
}

func (f *FakeServer) keep(from string, to []string, data string) {
	subject := ""
	for _, l := range strings.Split(data, "\r\n") {
		if l == "" {
			break
		}
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, "Subject") {
			subject = decodeHeader(strings.TrimSpace(v))
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range to {
		f.msgs = append(f.msgs, Captured{At: time.Now(), From: from, To: t, Subject: subject, Data: data})
	}
	if len(f.msgs) > 200 {
		f.msgs = f.msgs[len(f.msgs)-200:]
	}
}

// decodeHeader reads a header's encoded words.
func decodeHeader(v string) string {
	if s, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
		return s
	}
	return v
}

// pathOf is the address of a MAIL FROM or RCPT TO, without its brackets
// and parameters (BODY=8BITMIME).
func pathOf(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "<") {
		if i := strings.Index(v, ">"); i > 0 {
			return v[1:i]
		}
	}
	if f := strings.Fields(v); len(f) > 0 {
		return f[0]
	}
	return v
}
