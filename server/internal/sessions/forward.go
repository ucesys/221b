package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/tunnel"
)

// FrameConn carries tunnel frames: a WebSocket in the server, a pipe in
// tests.
type FrameConn interface {
	ReadFrame() (tunnel.Frame, error)
	WriteFrame(tunnel.Frame) error
}

// ErrCLI is a CLI the console will not serve.
var ErrCLI = errors.New("the CLI cannot be used")

// keepConns bounds the connections listed in a forward's pane.
const keepConns = 20

// clip bounds what the CLI says about itself, which the pane shows and the
// audit log keeps.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Tunnel serves the CLI of a port forward until it disconnects or the
// session ends (TB-19). The token is the forward's own, shown only in
// its owner's pane.
func (m *Manager) Tunnel(ctx context.Context, id, token string, fc FrameConn) error {
	m.mu.Lock()
	s := m.all[id]
	if s == nil || s.Req.Tool != toolbox.ToolForward {
		m.mu.Unlock()
		return ErrNotFound
	}
	if !m.checkToken(s, token) {
		m.mu.Unlock()
		return ErrToken
	}
	if s.State != StateLive {
		m.mu.Unlock()
		return ErrEnded
	}
	m.mu.Unlock()

	first, err := fc.ReadFrame()
	if err != nil {
		return err
	}
	var hello tunnel.HelloBody
	if first.Type != tunnel.Hello || json.Unmarshal(first.Payload, &hello) != nil {
		return fmt.Errorf("%w: it did not say hello", ErrCLI)
	}
	if min := m.opts.Config.PortForward.CLIMinVersion; min != "" && !tunnel.DevBuild(hello.Version) && tunnel.Older(hello.Version, min) {
		msg := fmt.Sprintf("This console needs the 221b CLI %s or newer; this one is %s.", min, hello.Version)
		_ = fc.WriteFrame(tunnel.Frame{Type: tunnel.End, Payload: []byte(msg)})
		return fmt.Errorf("%w: %s", ErrCLI, msg)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	write := func(f tunnel.Frame) error {
		wmu.Lock()
		defer wmu.Unlock()
		return fc.WriteFrame(f)
	}

	m.mu.Lock()
	if s.State != StateLive {
		m.mu.Unlock()
		return ErrEnded
	}
	fwd := s.fwd
	s.Forward.CLI, s.Forward.Listen = clip(hello.Host, 120), clip(hello.Listen, 120)
	s.LastActive = m.opts.Now()
	m.changedLocked(s)
	m.mu.Unlock()
	if fwd == nil {
		fwd, err = s.backend.Forwarder(ctx, s.Ns, s.Pod)
		if err != nil {
			_ = write(tunnel.Frame{Type: tunnel.End, Payload: []byte(statusMessage(err))})
			return err
		}
		m.mu.Lock()
		if s.State != StateLive {
			m.mu.Unlock()
			fwd.Close()
			return ErrEnded
		}
		s.fwd = fwd
		m.mu.Unlock()
	}
	// Inline, as every record is: the log keeps the order things happened in.
	m.record(ActionOpen, s, s.Owner, audit.OutcomeOK, fmt.Sprintf("cli=%q listen=%q version=%q", clip(hello.Host, 120), clip(hello.Listen, 120), clip(hello.Version, 40)))
	info, _ := json.Marshal(tunnel.InfoBody{Session: s.ID, Target: fmt.Sprintf("%s/%s:%d", s.Ns, s.Pod, s.Req.Port), Port: s.Req.Port, Ends: s.Ends.UTC().Format("2006-01-02T15:04:05Z"), Idle: m.opts.Config.IdleTimeout.D().String()})
	if err := write(tunnel.Frame{Type: tunnel.Info, Payload: info}); err != nil {
		return err
	}

	// The session's end closes the tunnel.
	go func() {
		m.mu.Lock()
		done := make(chan struct{})
		v := &Viewer{Out: make(chan []byte), ReadOnly: true, done: done}
		s.viewers[v] = struct{}{}
		ended := s.State == StateEnded
		m.mu.Unlock()
		if ended {
			cancel()
			return
		}
		select {
		case <-done:
			m.mu.Lock()
			note := s.EndNote
			if note == "" {
				note = "The session ended."
			}
			m.mu.Unlock()
			_ = write(tunnel.Frame{Type: tunnel.End, Payload: []byte(note)})
			cancel()
		case <-ctx.Done():
			m.mu.Lock()
			delete(s.viewers, v)
			m.mu.Unlock()
		}
	}()

	conns := map[uint32]io.ReadWriteCloser{}
	var cmu sync.Mutex
	defer func() {
		cmu.Lock()
		for _, c := range conns {
			c.Close()
		}
		cmu.Unlock()
		m.mu.Lock()
		s.Forward.CLI = ""
		s.Forward.Open = 0
		m.changedLocked(s)
		m.mu.Unlock()
	}()
	count := func(id uint32, n int, in bool, closed bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		now := m.opts.Now()
		if n > 0 {
			s.LastActive, s.Forward.Last = now, now
			if in {
				s.Forward.BytesIn += int64(n)
			} else {
				s.Forward.BytesOut += int64(n)
			}
		}
		for i := range s.Forward.Conns {
			if s.Forward.Conns[i].ID == id {
				s.Forward.Conns[i].Bytes += int64(n)
				if closed && s.Forward.Conns[i].Closed.IsZero() {
					s.Forward.Conns[i].Closed = now
					s.Forward.Open--
				}
			}
		}
		if closed || now.Sub(s.lastRev) > 2e9 {
			m.changedLocked(s)
		}
	}
	for {
		f, err := fc.ReadFrame()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch f.Type {
		case tunnel.Open:
			c, err := fwd.Dial(s.Req.Port)
			if err != nil {
				_ = write(tunnel.Frame{Type: tunnel.Close, Conn: f.Conn, Payload: []byte(statusMessage(err))})
				continue
			}
			cmu.Lock()
			conns[f.Conn] = c
			cmu.Unlock()
			m.mu.Lock()
			now := m.opts.Now()
			s.Forward.Open++
			s.Forward.Total++
			s.Forward.Conns = append(s.Forward.Conns, Conn{ID: f.Conn, From: string(f.Payload), Opened: now})
			if len(s.Forward.Conns) > keepConns {
				s.Forward.Conns = s.Forward.Conns[len(s.Forward.Conns)-keepConns:]
			}
			s.LastActive = now
			m.changedLocked(s)
			m.mu.Unlock()
			go func(id uint32, c io.ReadWriteCloser) {
				buf := make([]byte, tunnel.MaxData)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						count(id, n, false, false)
						if write(tunnel.Frame{Type: tunnel.Data, Conn: id, Payload: append([]byte(nil), buf[:n]...)}) != nil {
							return
						}
					}
					if err != nil {
						msg := ""
						if err != io.EOF {
							msg = err.Error()
						}
						_ = write(tunnel.Frame{Type: tunnel.Close, Conn: id, Payload: []byte(msg)})
						cmu.Lock()
						if conns[id] == c {
							delete(conns, id)
						}
						cmu.Unlock()
						c.Close()
						count(id, 0, false, true)
						return
					}
				}
			}(f.Conn, c)
		case tunnel.Data:
			cmu.Lock()
			c := conns[f.Conn]
			cmu.Unlock()
			if c == nil {
				continue
			}
			count(f.Conn, len(f.Payload), true, false)
			if _, err := c.Write(f.Payload); err != nil {
				c.Close()
			}
		case tunnel.Close:
			cmu.Lock()
			c := conns[f.Conn]
			delete(conns, f.Conn)
			cmu.Unlock()
			if c != nil {
				c.Close()
				count(f.Conn, 0, true, true)
			}
		}
	}
}
