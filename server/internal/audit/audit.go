// Package audit writes the audit log (P3-10): one JSON record per action
// that names the person, on standard output, and to a webhook when one is
// configured. The ordinary logs go to standard error, so the two streams can
// be routed apart.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Actions the console records.
const (
	ActionSignIn        = "signin"
	ActionSignInFailed  = "signin.failed"
	ActionSignOut       = "signout"
	ActionSettings      = "settings.update"
	ActionClusterAdd    = "cluster.add"
	ActionClusterUpdate = "cluster.update"
	ActionClusterRemove = "cluster.remove"
	ActionClusterTest   = "cluster.test"
)

// Outcomes of an action.
const (
	OutcomeOK     = "ok"
	OutcomeFailed = "failed"
	OutcomeDenied = "denied"
)

// Who names the person an action is attributed to.
type Who struct {
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Event is one record of the audit log.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	Who    Who       `json:"who"`
	// Cluster names the cluster the action concerns, when one does.
	Cluster string `json:"cluster,omitempty"`
	// Target names what the action touched: a setting, a connection.
	Target string `json:"target,omitempty"`
	// Detail says what changed or why it failed; it never holds a credential.
	Detail  string `json:"detail,omitempty"`
	Outcome string `json:"outcome"`
	// From is the address the request came from.
	From string `json:"from,omitempty"`
}

// Options configure the log.
type Options struct {
	// Out receives one JSON line per event; nil means os.Stdout is not
	// used and events go only to the webhook.
	Out io.Writer
	// WebhookURL receives every event as a JSON POST; empty sends none.
	WebhookURL string
	// WebhookToken is sent as a bearer token with each POST.
	WebhookToken string
	Client       *http.Client
	Logger       *slog.Logger
	Now          func() time.Time
}

// Log writes events.
type Log struct {
	opts  Options
	mu    sync.Mutex
	queue chan Event
	// Dropped counts events the webhook queue had no room for.
	dropped int
	wg      sync.WaitGroup
	stop    chan struct{}
}

// New starts the log. Close stops the webhook sender.
func New(o Options) *Log {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	l := &Log{opts: o, stop: make(chan struct{})}
	if o.WebhookURL != "" {
		l.queue = make(chan Event, 1000)
		l.wg.Add(1)
		go l.send()
	}
	return l
}

// Record writes one event. It never blocks on the webhook: the event is
// queued, and dropped with a warning when the queue is full.
func (l *Log) Record(e Event) {
	if e.Time.IsZero() {
		e.Time = l.opts.Now().UTC()
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeOK
	}
	if l.opts.Out != nil {
		b, err := json.Marshal(e)
		if err == nil {
			l.mu.Lock()
			_, _ = l.opts.Out.Write(append(b, '\n'))
			l.mu.Unlock()
		}
	}
	if l.queue != nil {
		select {
		case l.queue <- e:
		default:
			l.mu.Lock()
			l.dropped++
			n := l.dropped
			l.mu.Unlock()
			l.opts.Logger.Warn("audit webhook queue full; event dropped", "action", e.Action, "dropped", n)
		}
	}
}

// Dropped reports how many events the webhook never received.
func (l *Log) Dropped() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// Close waits for queued webhook deliveries, up to the context's deadline.
func (l *Log) Close(ctx context.Context) {
	if l.queue == nil {
		return
	}
	close(l.stop)
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (l *Log) send() {
	defer l.wg.Done()
	for {
		select {
		case e := <-l.queue:
			l.post(e)
		case <-l.stop:
			// Drain what is queued, then stop.
			for {
				select {
				case e := <-l.queue:
					l.post(e)
				default:
					return
				}
			}
		}
	}
}

// post delivers one event, trying three times with a short back-off.
func (l *Log) post(e Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, l.opts.WebhookURL, bytes.NewReader(b))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "221b-audit")
		if l.opts.WebhookToken != "" {
			req.Header.Set("Authorization", "Bearer "+l.opts.WebhookToken)
		}
		resp, err := l.opts.Client.Do(req)
		if err != nil {
			last = err
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return
		}
		last = &statusError{resp.StatusCode}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			break // the webhook refuses it; trying again will not help
		}
	}
	l.opts.Logger.Warn("audit webhook delivery failed", "action", e.Action, "err", last)
}

type statusError struct{ code int }

func (e *statusError) Error() string { return "the webhook answered " + http.StatusText(e.code) }
