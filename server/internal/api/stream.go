package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
)

// A browser opens at most six connections to a host over HTTP/1.1, for
// all its tabs, and each event stream holds one. Sherlock and Watson
// therefore share one stream (assistants/events), so a page of the console
// holds two: the live views and the assistants. Their own streams stay for
// other clients.

// streamPart writes its events to a shared stream until ctx ends or a
// write fails.
type streamPart func(ctx context.Context, write func(event string, v any) error)

// eventStream is one Server-Sent Events response that several parts
// write to, one event at a time.
type eventStream struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (e *eventStream) raw(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.WriteString(e.w, s); err != nil {
		return err
	}
	return e.rc.Flush()
}

func (e *eventStream) send(event string, v any) error {
	b, _ := json.Marshal(v)
	return e.raw(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
}

// sessionStands says whether the request's session still stands as it did
// when the response began: it has not ended (signed out, reset, disabled,
// deleted, expired) and Kubernetes would see the same person. A stream that
// outlives it ends at its next heartbeat; the browser, connecting again, is
// sent to sign in or sees what the person may see now (RB-05, RB-46, RB-53).
func (s *Server) sessionStands(r *http.Request) bool {
	cur, ok := s.opts.Auth.(interface {
		Current(auth.Session) (auth.Session, bool)
	})
	if !ok {
		return true
	}
	sess, has := auth.SessionFrom(r.Context())
	if !has || sess.ID == "" {
		return true
	}
	now, ok := cur.Current(sess)
	if !ok {
		return false
	}
	return now.Admin == sess.Admin && s.opts.Impersonation.personOf(now.User).Key() == s.opts.Impersonation.personOf(sess.User).Key()
}

// serveStream answers with an event stream that each part writes to, its
// events named with the part's prefix, and a keep-alive comment while
// they are quiet. It returns when the request ends or a write fails.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, parts map[string]streamPart) {
	if n := s.streams.Add(1); n > int64(s.opts.MaxStreams) {
		s.streams.Add(-1)
		w.Header().Set("Retry-After", "10")
		jsonError(w, http.StatusServiceUnavailable, "too many open event streams")
		return
	}
	defer s.streams.Add(-1)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	es := &eventStream{w: w, rc: http.NewResponseController(w)}
	if es.raw("retry: 3000\n\n") != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var wg sync.WaitGroup
	for prefix, part := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A part that stops (its write failed) ends the stream; the
			// browser connects again and has everything anew.
			defer cancel()
			part(ctx, func(event string, v any) error { return es.send(prefix+event, v) })
		}()
	}
	beat := time.NewTicker(s.opts.Heartbeat)
	defer beat.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-beat.C:
			if !s.sessionStands(r) || es.raw(": keep-alive\n\n") != nil {
				cancel()
			}
		}
	}
	// Nothing may write once the handler returns.
	wg.Wait()
}

// assistantsEvents streams Sherlock's conversation and Watson's commands
// together, their events named sherlock-… and watson-…, for one
// connection per page instead of two.
func (s *Server) assistantsEvents(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	parts := map[string]streamPart{}
	if s.opts.Sherlock != nil && s.opts.Settings != nil {
		parts["sherlock-"] = s.sherlockPart(r)
	}
	if s.opts.Watson != nil {
		// Without a running connection there is no Watson here, as on
		// Watson's own stream; the browser learns it by the missing status.
		if c, ok := s.watsonClusterQuiet(r); ok {
			parts["watson-"] = s.watsonPart(r, c)
		}
	}
	s.serveStream(w, r, parts)
}
