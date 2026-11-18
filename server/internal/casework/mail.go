package casework

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/mail"
)

// Email about cases (EM-05 to EM-07, ADR-158). When Settings › Email is on
// (configured, and its last test with the settings as they stand passed),
// the people involved in a case (issues.Involved) are emailed when it is
// opened and when it changes: its status, its assignee, its priority, and
// a person's message in its conversation. The agents' steps, the
// analysis, a change of its title or what it is about, a draft, are not
// said. The changes to one case within MailWindow of the first are one
// email. Each person hears of the changes others made, never of their
// own; whoever opens a case gets its first email too, as a receipt. An
// internal note reaches only the people who work the case.
//
// An email names the case, its title, priority, status and assignee, says
// in a line each what changed and who did it, and links to the case and
// to every case the person is involved in. Never a message's text, an
// explanation, a reason, the analysis, a command or any output: what a
// person wrote may hold what they should not have pasted (the webhook's
// rule, IS-30). It is sent from the leader, in the background, again
// after 30 seconds, 2 and 10 minutes when the server does not take it.

// ActionMail is an email about a case that could not be sent, or the
// emails sent about one change (the audit log names the people, not
// their addresses).
const ActionMail = "case.mail"

// Mailer is Settings › Email: whether email is on, the console's address
// for the links, and one send (package mail's Service).
type Mailer interface {
	On(ctx context.Context) bool
	Link(ctx context.Context) string
	Send(ctx context.Context, m mail.Message) error
}

// DefaultMailWindow is how long the changes to one case gather before
// they are emailed.
const DefaultMailWindow = time.Minute

// defaultMailRetries are the waits before each send again.
var defaultMailRetries = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

// mailChange is one change to say: what, in a line, and who made it.
type mailChange struct {
	opened bool
	what   string
	// by is the id of the person who made it ("" for the console or an
	// agent); byName how the email names them.
	by, byName string
	// internal is an internal note's, for the people who work the case.
	internal bool
}

// mailBatch is the changes to one case waiting to be emailed.
type mailBatch struct {
	changes []mailChange
}

// mailer is the cases' side of email.
type mailer struct {
	mu      sync.Mutex
	pending map[int]*mailBatch
	wg      sync.WaitGroup
}

// SetMail gives the service Settings › Email and how to find a person's
// address by their id when the case does not hold it (local accounts,
// the directory).
func (s *Service) SetMail(m Mailer, emailOf func(id string) string) {
	s.o.Mail, s.o.EmailOf = m, emailOf
}

// WaitMail waits for the emails under way, for tests.
func (s *Service) WaitMail() { s.mail.wg.Wait() }

// tell gathers a change to a case for its email.
func (s *Service) tell(n int, ch mailChange) {
	if s.o.Mail == nil || n <= 0 || strings.TrimSpace(ch.what) == "" {
		return
	}
	ch.what = clip(ch.what, 200)
	ch.byName = clip(ch.byName, 80)
	s.mail.mu.Lock()
	defer s.mail.mu.Unlock()
	if s.mail.pending == nil {
		s.mail.pending = map[int]*mailBatch{}
	}
	b := s.mail.pending[n]
	if b == nil {
		b = &mailBatch{}
		s.mail.pending[n] = b
		s.mail.wg.Add(1)
		go s.mailAfter(n)
	}
	for _, x := range b.changes {
		if x == ch {
			return
		}
	}
	if len(b.changes) < 20 {
		b.changes = append(b.changes, ch)
	}
}

// mailAfter emails a case's changes once its window has passed.
func (s *Service) mailAfter(n int) {
	defer s.mail.wg.Done()
	t := time.NewTimer(s.o.MailWindow)
	select {
	case <-s.base.Done():
		t.Stop()
		return
	case <-t.C:
	}
	s.mail.mu.Lock()
	b := s.mail.pending[n]
	delete(s.mail.pending, n)
	s.mail.mu.Unlock()
	if b != nil {
		s.mailCase(s.base, n, b.changes)
	}
}

// outMail is one email to one person.
type outMail struct {
	m    mail.Message
	name string
}

// mailCase emails the people involved in a case what changed.
func (s *Service) mailCase(ctx context.Context, n int, changes []mailChange) {
	if !s.o.Mail.On(ctx) {
		return
	}
	r, err := s.o.Issues.Report(ctx, system, n)
	if err != nil {
		s.o.Logger.Warn("a case's email could not read the case", "case", caseID(n), "err", err)
		return
	}
	link := s.o.Mail.Link(ctx)
	var out []outMail
	skipped := 0
	for _, p := range issues.Involved(&r) {
		lines := linesFor(p, changes)
		if len(lines) == 0 {
			continue
		}
		addr := p.Email
		if addr == "" && s.o.EmailOf != nil {
			addr = s.o.EmailOf(p.ID)
		}
		if _, err := mail.CheckAddress("address", addr); err != nil {
			// No address known for them, or not one that can be sent to.
			skipped++
			continue
		}
		m := CaseEmail(r, p, lines, link)
		m.To = addr
		out = append(out, outMail{m: m, name: p.Label()})
	}
	if len(out) == 0 {
		return
	}
	var sent, failed []string
	why := ""
	for attempt := 0; ; attempt++ {
		var again []outMail
		for _, o := range out {
			err := s.o.Mail.Send(ctx, o.m)
			switch {
			case err == nil:
				sent = append(sent, o.name)
			case errors.Is(err, mail.ErrOff):
				// Turned off meanwhile: nothing more is sent.
				return
			default:
				why = err.Error()
				again = append(again, o)
			}
		}
		if len(again) == 0 || attempt >= len(s.o.MailRetries) || ctx.Err() != nil {
			for _, o := range again {
				failed = append(failed, o.name)
			}
			break
		}
		t := time.NewTimer(s.o.MailRetries[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		out = again
	}
	if len(sent) > 0 {
		detail := fmt.Sprintf("email about %s sent to %s", caseID(n), strings.Join(sent, ", "))
		if skipped > 0 {
			detail += fmt.Sprintf("; %d without an email address", skipped)
		}
		s.record(audit.Event{Time: s.now(), Action: ActionMail, Who: audit.Who{Name: "221B"}, Cluster: r.Cluster, Target: caseID(n), Outcome: audit.OutcomeOK, Detail: detail})
	}
	if len(failed) > 0 {
		s.o.Logger.Warn("an email about a case was not sent", "case", caseID(n), "people", len(failed), "err", why)
		s.record(audit.Event{Time: s.now(), Action: ActionMail, Who: audit.Who{Name: "221B"}, Cluster: r.Cluster, Target: caseID(n), Outcome: audit.OutcomeFailed,
			Detail: fmt.Sprintf("email about %s not sent to %s after %d tries: %s", caseID(n), strings.Join(failed, ", "), len(s.o.MailRetries)+1, why)})
	}
}

// linesFor are the changes one person hears of: the others', and the
// opening even when it was theirs; an internal note only when they work
// the case.
func linesFor(p issues.Involvement, changes []mailChange) []mailChange {
	var out []mailChange
	for _, c := range changes {
		if c.internal && !p.Works {
			continue
		}
		if !c.opened && c.by != "" && c.by == p.ID {
			continue
		}
		out = append(out, c)
	}
	return out
}

// whyWords say why a person gets the email.
var whyWords = map[string]string{
	issues.InvolvedReported: "you reported it",
	issues.InvolvedFiled:    "you filed it",
	issues.InvolvedOpened:   "you opened it",
	issues.InvolvedAssigned: "it is assigned to you",
	issues.InvolvedFollows:  "you follow it",
	issues.InvolvedWrote:    "you wrote in its conversation",
	issues.InvolvedClosed:   "you closed it",
	issues.InvolvedApproved: "you approved a change that fixed it",
}

// CaseLink is a case's address in the console; InvolvedLink the Inbox
// with every case the person is involved in (EM-08). Both are "" without
// the console's address.
func CaseLink(link string, n int) string {
	if link == "" {
		return ""
	}
	return link + "/#/aiops/inbox/case/" + url.PathEscape(strconv.Itoa(n))
}

// InvolvedLink is the Inbox with every case the person is involved in,
// closed ones too.
func InvolvedLink(link string) string {
	if link == "" {
		return ""
	}
	return link + "/#/aiops/inbox?show=all&who=involved"
}

// oneLineText is a text as one line of an email.
func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

// CaseEmail is the email to one person about a case's changes: plain
// text and simple HTML, with no address yet.
func CaseEmail(r issues.Case, p issues.Involvement, changes []mailChange, link string) mail.Message {
	id := caseID(r.Number)
	// A report without a title shows its first line, as everywhere.
	first, _, _ := strings.Cut(strings.TrimSpace(r.Text), "\n")
	title := oneLineText(clip(firstOf(r.Title, first, "(no title)"), 120))
	opened := len(changes) > 0 && changes[0].opened
	verb := "changed"
	if opened {
		verb = "opened"
	}
	subject := fmt.Sprintf("[221B] %s %s: %s", id, verb, title)
	status := shownStatus(r)
	if !r.Status.Open() && r.Outcome != "" {
		status = "Closed as " + outcomeWords(r.Outcome, r.KnownIssue)
	}
	facts := []string{fmt.Sprintf("P%d", r.Priority), status}
	if r.Owner != nil {
		facts = append(facts, "assigned to "+oneLineText(r.Owner.Label()))
	} else if r.Status.Open() {
		facts = append(facts, "not assigned")
	}
	var lines []string
	for _, c := range changes {
		l := oneLineText(c.what)
		if !c.opened && c.byName != "" {
			l = oneLineText(c.byName) + ": " + l
		}
		lines = append(lines, l)
	}
	var whys []string
	for _, w := range p.Why {
		if t, ok := whyWords[w]; ok {
			whys = append(whys, t)
		}
	}
	because := "you are involved in it"
	if len(whys) > 0 {
		because = joinWords(whys)
	}
	caseURL, allURL := CaseLink(link, r.Number), InvolvedLink(link)

	var t strings.Builder
	fmt.Fprintf(&t, "%s: %s\n%s\n\n", id, title, strings.Join(facts, " · "))
	if opened {
		t.WriteString("What happened:\n")
	} else {
		t.WriteString("What changed:\n")
	}
	for _, l := range lines {
		t.WriteString("- " + l + "\n")
	}
	t.WriteString("\n")
	if caseURL != "" {
		fmt.Fprintf(&t, "Open the case:\n%s\n\nEvery case you are involved in:\n%s\n\n", caseURL, allURL)
	} else {
		fmt.Fprintf(&t, "Open AIOps › Inbox in 221B and search for %s; \"I'm involved\" lists every case you are involved in.\n\n", id)
	}
	fmt.Fprintf(&t, "You get this email because %s. 221B emails the people involved in a case when it is opened and when it changes; replies to it are not read.\n", because)

	e := html.EscapeString
	var h strings.Builder
	h.WriteString(`<!doctype html><html><body style="font-family:system-ui,-apple-system,Segoe UI,sans-serif;font-size:14px;color:#1b1f24">`)
	fmt.Fprintf(&h, `<p style="margin:0 0 4px"><strong>%s</strong>: %s</p><p style="margin:0 0 12px;color:#57606a">%s</p>`, e(id), e(title), e(strings.Join(facts, " · ")))
	if opened {
		h.WriteString(`<p style="margin:0 0 4px">What happened:</p><ul style="margin:0 0 12px">`)
	} else {
		h.WriteString(`<p style="margin:0 0 4px">What changed:</p><ul style="margin:0 0 12px">`)
	}
	for _, l := range lines {
		fmt.Fprintf(&h, "<li>%s</li>", e(l))
	}
	h.WriteString("</ul>")
	if caseURL != "" {
		fmt.Fprintf(&h, `<p style="margin:0 0 12px"><a href="%s">Open %s</a> · <a href="%s">Every case you are involved in</a></p>`, e(caseURL), e(id), e(allURL))
	} else {
		fmt.Fprintf(&h, `<p style="margin:0 0 12px">Open AIOps › Inbox in 221B and search for %s; “I'm involved” lists every case you are involved in.</p>`, e(id))
	}
	fmt.Fprintf(&h, `<p style="margin:0;color:#57606a;font-size:12px">You get this email because %s. 221B emails the people involved in a case when it is opened and when it changes; replies to it are not read.</p></body></html>`, e(because))
	return mail.Message{Subject: subject, Text: t.String(), HTML: h.String()}
}

// joinWords joins "a", "b" and "c" as "a, b and c".
func joinWords(w []string) string {
	if len(w) < 2 {
		return strings.Join(w, "")
	}
	return strings.Join(w[:len(w)-1], ", ") + " and " + w[len(w)-1]
}

// mailBy is the caller as an email names who made a change.
func (c Caller) mailBy() (string, string) { return c.key(), firstOf(c.Name, c.Label()) }

// tellBy gathers a change a person made.
func (s *Service) tellBy(n int, c Caller, what string) {
	id, name := c.mailBy()
	s.tell(n, mailChange{what: what, by: id, byName: name})
}

// mailEvent hears the issue service's changes that the cases' own
// actions do not say themselves (EM-06): a case opened (by any way in),
// a message in its conversation, and a status the reporter or the console
// changed (the reporter's "Still broken", an answer, a timer's close).
func (s *Service) mailEvent(e issues.Event) {
	if s.o.Mail == nil || e.Report == nil {
		return
	}
	r := e.Report
	byID, byName := "", ""
	person := e.By != nil && !e.By.System && !strings.HasPrefix(e.By.ID, "system:")
	if person {
		byID, byName = e.By.ID, e.By.Label()
	}
	switch e.Kind {
	case issues.EventReportCreated:
		// A case an agent or the console opened has nobody involved yet.
		if !person && r.Reporter.ID == "" && r.FiledBy == nil {
			return
		}
		what := "Opened"
		switch {
		case person:
			what = "Opened by " + byName
		case r.AgentFound():
			what = "Opened from an agent's finding"
		case e.By != nil && e.By.Label() != "":
			what = "Opened through " + e.By.Label()
		}
		s.tell(r.Number, mailChange{opened: true, what: what, by: byID})
	case issues.EventReportMessage:
		if !person {
			return
		}
		var m *issues.Message
		for i := len(r.Messages) - 1; i >= 0; i-- {
			x := &r.Messages[i]
			if !x.Draft && (x.From.ID == byID || x.SentBy != nil && x.SentBy.ID == byID) {
				m = x
				break
			}
		}
		if m == nil {
			return
		}
		ch := mailChange{by: byID, byName: byName}
		switch {
		case m.Kind == issues.FromReporter && e.Answered:
			ch.what = "answered the question asked"
		case m.Kind == issues.FromReporter:
			ch.what = "wrote in the conversation"
		case m.Internal:
			ch.what, ch.internal = "added an internal note", true
		default:
			ch.what = "replied to the reporter"
		}
		s.tell(r.Number, ch)
	case issues.EventReportStatus:
		// The people who work the case change its status through the cases'
		// actions, which say it themselves.
		if person && byID != r.Reporter.ID && (r.FiledBy == nil || byID != r.FiledBy.ID) {
			return
		}
		what := "now " + shownStatus(*r)
		if !r.Status.Open() && r.Outcome != "" {
			what = "closed as " + outcomeWords(r.Outcome, r.KnownIssue)
		}
		if !person {
			byName = "221B"
		}
		s.tell(r.Number, mailChange{what: what, by: byID, byName: byName})
	}
}
