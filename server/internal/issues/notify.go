package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// The webhook (IS-30) carries what changed in summaries: a report's
// number, status, priority, service and plain summary, a known issue's
// title, status and last update. Never a report's text or conversation,
// which may hold what a reporter should not have pasted, and never the
// technical summary, which names resources.

// hookReport is a report as the webhook carries it.
type hookReport struct {
	Number   int     `json:"number"`
	Status   Status  `json:"status"`
	Outcome  Outcome `json:"outcome,omitempty"`
	Priority string  `json:"priority"`
	Service  string  `json:"service,omitempty"`
	Known    string  `json:"knownIssue,omitempty"`
	Summary  string  `json:"summary,omitempty"`
}

// hookKnown is a known issue as the webhook carries it.
type hookKnown struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Status   KnownStatus `json:"status"`
	Expected string      `json:"expected,omitempty"`
	Update   string      `json:"update,omitempty"`
}

// hookBody is what the webhook is sent. Text is for Slack and chat tools
// that show one line.
type hookBody struct {
	Text   string      `json:"text"`
	Event  string      `json:"event"`
	At     time.Time   `json:"at"`
	Report *hookReport `json:"report,omitempty"`
	Known  *hookKnown  `json:"knownIssue,omitempty"`
}

// hookOf builds what the webhook is sent for an event.
func hookOf(e Event, company string) hookBody {
	b := hookBody{Event: e.Kind, At: e.At}
	prefix := ""
	if company != "" {
		prefix = company + ": "
	}
	if r := e.Report; r != nil {
		b.Report = &hookReport{Number: r.Number, Status: r.Status, Outcome: r.Outcome, Priority: fmt.Sprintf("P%d", r.Priority), Service: r.Service, Known: r.KnownIssue, Summary: r.Plain}
		what := map[string]string{EventReportCreated: "received", EventReportLinked: "linked", EventReportStatus: "now " + string(r.Status), EventReportMessage: "has a new message", EventReportTriaged: "triaged"}[e.Kind]
		b.Text = fmt.Sprintf("%sReport #%d %s (P%d", prefix, r.Number, what, r.Priority)
		if r.Service != "" {
			b.Text += ", " + r.Service
		}
		if r.KnownIssue != "" {
			b.Text += ", " + r.KnownIssue
		}
		b.Text += ")"
		if r.Plain != "" {
			b.Text += ": " + r.Plain
		}
	}
	if k := e.Known; k != nil {
		b.Known = &hookKnown{ID: k.ID, Title: k.Title, Status: k.Status, Expected: k.Expected}
		if u := k.LastUpdate(); u != nil {
			b.Known.Update = u.Text
		}
		b.Text = fmt.Sprintf("%s%s %s: %s", prefix, k.ID, knownStatusWords[k.Status], k.Title)
		if b.Known.Update != "" {
			b.Text += " · " + b.Known.Update
		}
	}
	return b
}

// notify sends an event to the webhook, when one is set and wants it.
// Drafts are no one's business but the operators', and nothing is sent
// for them.
func (s *Service) notify(e Event) {
	if e.Known != nil && e.Known.Draft {
		return
	}
	s.cacheMu.Lock()
	cached := s.cache
	s.cacheMu.Unlock()
	if cached == nil {
		return
	}
	set := cached.settings()
	if set.Webhook.URL == "" || (len(set.Webhook.Events) > 0 && !contains(set.Webhook.Events, e.Kind)) {
		return
	}
	body, err := json.Marshal(hookOf(e, set.Company))
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, set.Webhook.URL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := s.http.Do(req)
		if err != nil {
			s.log.Warn("the issue webhook could not be reached", "event", e.Kind, "err", err)
			return
		}
		res.Body.Close()
		if res.StatusCode >= 300 {
			s.log.Warn("the issue webhook refused an event", "event", e.Kind, "status", res.StatusCode)
		}
	}()
}
