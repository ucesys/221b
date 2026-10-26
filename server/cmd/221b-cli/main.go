// Command 221b is the console's command-line companion (TB-20). It talks
// to the console only, never to Kubernetes: a port forward the console
// created and approved reaches this computer through it.
//
//	221b forward https://console.example --cluster prod-eu --session 3f9a… [--local 15432]
//
// The forward listens on a local port and carries each TCP connection to
// the console over one WebSocket (TB-19), where the console opens the
// kubelet's port forward as the person who asked for it. The console owns
// the session: it limits, ends and audits it, and says so here. The
// forward's token, its one credential, is asked for when the command runs,
// hidden, so it stays out of shell histories and process lists; a script
// gives it in $TWOTWENTYONEB_TOKEN or on standard input.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"github.com/ucesys/221b/server/internal/tunnel"
	"github.com/ucesys/221b/server/internal/version"
)

const usage = `221b is the 221B console's command-line companion.

Usage:
  221b forward <console-url> --session ID [--cluster ID] [--local PORT|auto] [--address 127.0.0.1]
  221b version

The console's port-forward pane shows the forward command with its values
filled in: copy it from there. It then asks for the forward's token, shown
beside the command in the pane, and does not echo it. A script sets
TWOTWENTYONEB_TOKEN instead, or pipes the token into the command's input;
--token TOKEN works too, but lands in the shell's history.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "forward":
		if err := forward(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "221b:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("221b", version.String())
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "221b: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

// options are forward's flags.
type options struct {
	console  string
	cluster  string
	session  string
	token    string
	local    string
	address  string
	insecure bool
}

func parse(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.cluster, "cluster", "", "the cluster's ID in the console (empty: the console's own)")
	fs.StringVar(&o.session, "session", "", "the session's ID")
	fs.StringVar(&o.token, "token", os.Getenv("TWOTWENTYONEB_TOKEN"), "the forward's token; left out, it is asked for (or read from $TWOTWENTYONEB_TOKEN)")
	fs.StringVar(&o.local, "local", "", "the local port: a number, or auto (the default) for any free port")
	fs.StringVar(&o.address, "address", "127.0.0.1", "the local address to listen on")
	fs.BoolVar(&o.insecure, "insecure-skip-tls-verify", false, "do not check the console's certificate (a development console only)")
	// The console URL may come before or after the flags.
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return o, err
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) != 1 {
		return o, errors.New("give the console's URL once, as the console's pane shows it")
	}
	o.console = strings.TrimRight(pos[0], "/")
	if o.session == "" {
		return o, errors.New("--session is needed: copy the command from the console's port-forward pane")
	}
	return o, nil
}

// askToken reads the forward's token when neither the flag nor the
// environment gave it: hidden, from the terminal, or one line from a pipe.
func askToken(in *os.File, out io.Writer) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprint(out, "The forward's token, from the console's pane (not shown): ")
		b, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("reading the token: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	b, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the token from standard input: %w", err)
	}
	return strings.TrimSpace(b), nil
}

// tunnelURL is the session's tunnel on the console.
func tunnelURL(o options) (string, error) {
	u, err := url.Parse(o.console)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not the console's URL", o.console)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("the console's URL starts with https:// or http://, not %s://", u.Scheme)
	}
	base := strings.TrimRight(u.Path, "/") + "/api/v1/"
	if o.cluster != "" && o.cluster != "local" {
		base += "clusters/" + url.PathEscape(o.cluster) + "/"
	}
	u.Path = base + "sessions/" + url.PathEscape(o.session) + "/tunnel"
	return u.String(), nil
}

func forward(args []string) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	target, err := tunnelURL(o)
	if err != nil {
		return err
	}
	if o.token == "" {
		if o.token, err = askToken(os.Stdin, os.Stderr); err != nil {
			return err
		}
		if o.token == "" {
			return errors.New("the forward's token is needed: it is shown beside the command in the console's pane")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := *websocket.DefaultDialer
	d.HandshakeTimeout = 20 * time.Second
	if o.insecure {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // asked for, on a development console
	}
	h := http.Header{"Authorization": {"Bearer " + o.token}, "User-Agent": {"221b-cli/" + version.Version}}
	conn, resp, err := d.DialContext(ctx, target, h)
	if err != nil {
		if resp != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("the console refused the tunnel (%s): %s", resp.Status, message(b))
		}
		return fmt.Errorf("reaching the console: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	t := &cliTunnel{ws: conn, conns: map[uint32]*localConn{}}

	// Listen before saying hello, so the console shows the real address.
	ln, err := listen(o)
	if err != nil {
		return err
	}
	defer ln.Close()
	host, _ := os.Hostname()
	hello, _ := json.Marshal(tunnel.HelloBody{Version: version.Version, Host: host, Listen: ln.Addr().String()})
	if err := t.write(tunnel.Frame{Type: tunnel.Hello, Payload: hello}); err != nil {
		return err
	}
	first, err := t.read()
	if err != nil {
		return fmt.Errorf("the console closed the tunnel: %w", err)
	}
	if first.Type == tunnel.End {
		return errors.New(string(first.Payload))
	}
	var info tunnel.InfoBody
	if first.Type != tunnel.Info || json.Unmarshal(first.Payload, &info) != nil {
		return errors.New("the console did not answer as a 221B console does")
	}
	fmt.Printf("Forwarding %s -> %s\n", ln.Addr(), info.Target)
	if info.Ends != "" {
		if ends, err := time.Parse(time.RFC3339, info.Ends); err == nil {
			fmt.Printf("The session ends at %s, or after %s without traffic. Ctrl-C stops it here; End in the console ends it.\n", ends.Local().Format("15:04"), info.Idle)
		}
	}

	done := make(chan error, 1)
	go func() { done <- t.readLoop() }()
	go t.accept(ln)
	select {
	case <-ctx.Done():
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stopped"), time.Now().Add(time.Second))
		fmt.Println("\nStopped. The session stays open in the console until it ends; run the command again to reconnect.")
		return nil
	case err := <-done:
		var end endErr
		if errors.As(err, &end) {
			fmt.Println(string(end))
			return nil
		}
		return err
	}
}

func listen(o options) (net.Listener, error) {
	addr := o.address
	if o.local == "" || o.local == "auto" {
		return net.Listen("tcp", net.JoinHostPort(addr, "0"))
	}
	p, err := strconv.Atoi(o.local)
	if err != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("--local is a port number or auto, not %q", o.local)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(addr, o.local))
	if err != nil {
		return nil, fmt.Errorf("listening on %s:%d: %w (try --local auto)", addr, p, err)
	}
	return ln, nil
}

// message is the error a console's JSON answer carries, or the answer.
func message(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(b))
}

type endErr string

func (e endErr) Error() string { return string(e) }

// cliTunnel is this end of the tunnel.
type cliTunnel struct {
	ws    *websocket.Conn
	wmu   sync.Mutex
	mu    sync.Mutex
	conns map[uint32]*localConn
	next  uint32
}

// localConn is one TCP connection on the local port. Data from the
// console queues in order and its own goroutine writes it, so a program
// that reads slowly holds up only its own connection.
type localConn struct {
	c      net.Conn
	mu     sync.Mutex
	q      [][]byte
	queued int
	sig    chan struct{}
	done   chan struct{}
	once   sync.Once
}

// maxQueued is how much a connection may fall behind before it is dropped.
const maxQueued = 64 << 20

func newLocalConn(c net.Conn) *localConn {
	return &localConn{c: c, sig: make(chan struct{}, 1), done: make(chan struct{})}
}

// push queues data for the program; false when it has fallen too far behind.
func (l *localConn) push(b []byte) bool {
	l.mu.Lock()
	if l.queued+len(b) > maxQueued {
		l.mu.Unlock()
		return false
	}
	l.q = append(l.q, b)
	l.queued += len(b)
	l.mu.Unlock()
	select {
	case l.sig <- struct{}{}:
	default:
	}
	return true
}

// writeLoop writes the queue to the program until the connection closes.
func (l *localConn) writeLoop(failed func()) {
	for {
		select {
		case <-l.done:
			return
		case <-l.sig:
		}
		for {
			l.mu.Lock()
			if len(l.q) == 0 {
				l.mu.Unlock()
				break
			}
			b := l.q[0]
			l.q = l.q[1:]
			l.queued -= len(b)
			l.mu.Unlock()
			if _, err := l.c.Write(b); err != nil {
				failed()
				return
			}
		}
	}
}

func (l *localConn) close() {
	l.once.Do(func() {
		close(l.done)
		l.c.Close()
	})
}

func (t *cliTunnel) write(f tunnel.Frame) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = t.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return t.ws.WriteMessage(websocket.BinaryMessage, tunnel.Encode(f))
}

func (t *cliTunnel) read() (tunnel.Frame, error) {
	for {
		kind, b, err := t.ws.ReadMessage()
		if err != nil {
			return tunnel.Frame{}, err
		}
		if kind != websocket.BinaryMessage {
			continue
		}
		return tunnel.Decode(b)
	}
}

func (t *cliTunnel) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		t.mu.Lock()
		t.next++
		id := t.next
		lc := newLocalConn(c)
		t.conns[id] = lc
		t.mu.Unlock()
		if t.write(tunnel.Frame{Type: tunnel.Open, Conn: id, Payload: []byte(c.RemoteAddr().String())}) != nil {
			lc.close()
			return
		}
		go lc.writeLoop(func() { t.drop(id, true) })
		go func() {
			buf := make([]byte, tunnel.MaxData)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					if t.write(tunnel.Frame{Type: tunnel.Data, Conn: id, Payload: append([]byte(nil), buf[:n]...)}) != nil {
						t.drop(id, false)
						return
					}
				}
				if err != nil {
					t.drop(id, true)
					return
				}
			}
		}()
	}
}

// drop closes a local connection, and tells the console when it was this
// side that closed.
func (t *cliTunnel) drop(id uint32, tell bool) {
	t.mu.Lock()
	lc := t.conns[id]
	delete(t.conns, id)
	t.mu.Unlock()
	if lc == nil {
		return
	}
	lc.close()
	if tell {
		_ = t.write(tunnel.Frame{Type: tunnel.Close, Conn: id})
	}
}

func (t *cliTunnel) readLoop() error {
	for {
		f, err := t.read()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return endErr("The console closed the tunnel.")
			}
			return fmt.Errorf("the tunnel broke: %w", err)
		}
		switch f.Type {
		case tunnel.Data:
			t.mu.Lock()
			lc := t.conns[f.Conn]
			t.mu.Unlock()
			if lc == nil {
				continue
			}
			if !lc.push(f.Payload) {
				fmt.Fprintf(os.Stderr, "connection %d: the program read too slowly; closed\n", f.Conn)
				t.drop(f.Conn, true)
			}
		case tunnel.Close:
			if len(f.Payload) > 0 {
				fmt.Fprintf(os.Stderr, "connection %d: %s\n", f.Conn, f.Payload)
			}
			t.drop(f.Conn, false)
		case tunnel.End:
			msg := strings.TrimSpace(string(f.Payload))
			if msg == "" {
				msg = "The session ended."
			}
			return endErr(msg)
		}
	}
}
