package casework

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/mail"
)

// fakeMail is Settings › Email for the tests: on or off, and what it
// was given.
type fakeMail struct {
	mu   sync.Mutex
	on   bool
	link string
	sent []mail.Message
	// fail fails that many sends first.
	fail int
}

func (f *fakeMail) On(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on
}

func (f *fakeMail) Link(context.Context) string { return f.link }

func (f *fakeMail) Send(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.on {
		return mail.ErrOff
	}
	if f.fail > 0 {
		f.fail--
		return errors.New("421 try again later")
	}
	f.sent = append(f.sent, m)
	return nil
}

// take returns what was sent since the last take, by recipient.
func (f *fakeMail) take() map[string][]mail.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]mail.Message{}
	for _, m := range f.sent {
		out[m.To] = append(out[m.To], m)
	}
	f.sent = nil
	return out
}

// mailWorld is a world whose cases are emailed, gathered for 20 ms.
func mailWorld(t *testing.T) (*world, *fakeMail) {
	t.Helper()
	w := setup(t)
	fm := &fakeMail{on: true, link: "https://221b.example.com"}
	w.svc.o.MailWindow = 500 * time.Millisecond // wide enough for -race
	w.svc.o.MailRetries = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	// Rita and Otto have no address in what the case keeps: the accounts
	// know Rita's and Otto's.
	w.svc.SetMail(fm, func(id string) string {
		return map[string]string{"rita": "rita@corp.example", "otto": "otto@corp.example"}[id]
	})
	return w, fm
}

// secrets are what an email never carries: the report's credential, a
// reply's and a note's text.
var secrets = []string{"hunter2", "token=abc123", "We are on it", "Looks like the cache", "db-password"}

func noSecrets(t *testing.T, got map[string][]mail.Message) {
	t.Helper()
	for to, ms := range got {
		for _, m := range ms {
			for _, s := range secrets {
				if strings.Contains(m.Subject+m.Text+m.HTML, s) {
					t.Errorf("the email to %s carries %q:\n%s", to, s, m.Text)
				}
			}
		}
	}
}

// The people involved in a case are emailed when it is opened and when
// it changes (EM-05 to EM-07): its reporter gets a receipt; the changes
// within the window are one email; each person hears of the others'
// changes, not their own; an internal note reaches only the people who
// work the case; an email holds no message's text, and links to the case
// and to every case the person is involved in.
func TestThePeopleInvolvedAreEmailed(t *testing.T) {
	w, fm := mailWorld(t)
	ctx := context.Background()
	rec, err := w.iss.Submit(ctx, rita, issues.Submission{Text: "The shop is down\nIt says password=hunter2 in the log."})
	if err != nil {
		t.Fatal(err)
	}
	n := rec.Report.Number
	w.svc.WaitMail()
	got := fm.take()
	if len(got) != 1 || len(got["rita@corp.example"]) != 1 {
		t.Fatalf("opened: %+v", got)
	}
	m := got["rita@corp.example"][0]
	if !strings.HasPrefix(m.Subject, "[221B] C-1 opened: The shop is down") || !strings.Contains(m.Text, "Opened by Rita") || !strings.Contains(m.Text, "you reported it") {
		t.Errorf("the receipt: %s\n%s", m.Subject, m.Text)
	}
	if !strings.Contains(m.Text, "https://221b.example.com/#/aiops/inbox/case/1\n") || !strings.Contains(m.Text, "https://221b.example.com/#/aiops/inbox?show=all&who=involved") ||
		!strings.Contains(m.HTML, `href="https://221b.example.com/#/aiops/inbox/case/1"`) {
		t.Errorf("the links:\n%s\n%s", m.Text, m.HTML)
	}
	noSecrets(t, got)

	// Within one window: Olivia takes it at P1 and replies; Otto adds an
	// internal note; Olivia renames it (not emailed).
	id := caseID(n)
	if _, err := w.svc.Update(ctx, id, Change{Assignee: &issues.Person{ID: "me"}, Priority: pr(1)}, olivia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Say(ctx, id, Message{Text: "We are on it. token=abc123"}, olivia); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Say(ctx, id, Message{Text: "Looks like the cache, db-password in values.", Internal: true}, otto); err != nil {
		t.Fatal(err)
	}
	title := "Shop down"
	if _, err := w.svc.Update(ctx, id, Change{Title: &title}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	got = fm.take()
	noSecrets(t, got)
	if len(got) != 3 || len(got["rita@corp.example"]) != 1 || len(got["olivia@corp.example"]) != 1 || len(got["otto@corp.example"]) != 1 {
		t.Fatalf("one email each: %+v", got)
	}
	r := got["rita@corp.example"][0].Text
	if !strings.Contains(r, "Olivia: priority P2 → P1, assigned to Olivia, now In progress") || !strings.Contains(r, "Olivia: replied to the reporter") || strings.Contains(r, "internal note") {
		t.Errorf("Rita's:\n%s", r)
	}
	if !strings.Contains(got["rita@corp.example"][0].Subject, "[221B] C-1 changed: Shop down") || !strings.Contains(r, "P1 · In progress · assigned to Olivia") {
		t.Errorf("Rita's subject and facts: %s\n%s", got["rita@corp.example"][0].Subject, r)
	}
	o := got["olivia@corp.example"][0].Text
	if strings.Contains(o, "Olivia:") || !strings.Contains(o, "Otto: added an internal note") || !strings.Contains(o, "it is assigned to you") {
		t.Errorf("Olivia's (not her own changes):\n%s", o)
	}
	x := got["otto@corp.example"][0].Text
	if strings.Contains(x, "Otto:") || !strings.Contains(x, "Olivia: replied to the reporter") || !strings.Contains(x, "you wrote in its conversation") {
		t.Errorf("Otto's:\n%s", x)
	}

	// Closing it: the reporter and Otto hear; Olivia does not.
	if _, err := w.svc.Close(ctx, id, CloseInput{Outcome: issues.OutcomeNoAction, Explanation: "It was the db-password rotation."}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	got = fm.take()
	noSecrets(t, got)
	if len(got) != 2 || got["olivia@corp.example"] != nil || !strings.Contains(got["rita@corp.example"][0].Text, "Olivia: closed it as no action needed") ||
		!strings.Contains(got["rita@corp.example"][0].Text, "Closed as no action needed") {
		t.Fatalf("closed: %+v", got)
	}
	// What was audited names the people, not their addresses.
	if !strings.Contains(w.audit.String(), `"action":"case.mail"`) || strings.Contains(w.audit.String(), "rita@corp.example") {
		t.Errorf("audit: %s", w.audit.String())
	}
}

// Nothing is sent while email is off, nor to a person without an
// address; a send the server refuses is tried again (EM-07).
func TestEmailIsSentOnlyWhenOnAndAgainWhenRefused(t *testing.T) {
	w, fm := mailWorld(t)
	ctx := context.Background()
	fm.on = false
	if _, err := w.svc.Create(ctx, NewCase{Title: "Disk full"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	if got := fm.take(); len(got) != 0 {
		t.Fatalf("sent while off: %+v", got)
	}
	fm.on = true
	fm.fail = 2
	if _, err := w.svc.Create(ctx, NewCase{Title: "Disk full again"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	if got := fm.take(); len(got["olivia@corp.example"]) != 1 || !strings.Contains(got["olivia@corp.example"][0].Text, "Opened by Olivia") {
		t.Fatalf("after two refusals: %+v", got)
	}
	fm.fail = 5
	if _, err := w.svc.Create(ctx, NewCase{Title: "Disk full a third time"}, olivia); err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	if got := fm.take(); len(got) != 0 {
		t.Fatalf("sent: %+v", got)
	}
	if !strings.Contains(w.audit.String(), "not sent to Olivia after 3 tries: 421 try again later") {
		t.Errorf("the failure is not audited: %s", w.audit.String())
	}
	// Sam has an address but nothing to do with the case; a case opened
	// by an agent's run has nobody to email.
	fm.fail = 0
	if _, err := w.svc.Update(ctx, "C-2", Change{Priority: pr(4)}, sam); err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	if got := fm.take(); len(got) != 1 || len(got["olivia@corp.example"]) != 1 || !strings.Contains(got["olivia@corp.example"][0].Text, "Sam: priority P2 → P4") {
		t.Fatalf("Sam's change: %+v", got)
	}
}

// Who is involved is what the case records (EM-05), and "Cases I'm
// involved in" lists the same cases (EM-08), from the list's ?involved=me
// or the Inbox's ?who=involved.
func TestCasesImInvolvedIn(t *testing.T) {
	w, _ := mailWorld(t)
	ctx := context.Background()
	rec, err := w.iss.Submit(ctx, rita, issues.Submission{Text: "Checkout fails"})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := w.svc.Create(ctx, NewCase{Title: "Nightly backup slow"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Create(ctx, NewCase{Title: "Nobody's"}, sam); err != nil {
		t.Fatal(err)
	}
	report := caseID(rec.Report.Number)
	// Otto writes on the report; Olivia owns it.
	if _, err := w.svc.Say(ctx, report, Message{Text: "Checking.", Internal: true}, otto); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Update(ctx, report, Change{Assignee: &issues.Person{ID: "olivia", Name: "Olivia"}}, sam); err != nil {
		t.Fatal(err)
	}
	ids := func(c Caller) []string {
		list, err := w.svc.List(ctx, c, Filter{Involved: true})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, b := range list {
			if !b.Involved {
				t.Errorf("%s is listed without involved", b.ID)
			}
			out = append(out, b.ID)
		}
		return out
	}
	if got := strings.Join(ids(olivia), ","); got != report+","+mine.ID && got != mine.ID+","+report {
		t.Errorf("Olivia's: %s", got)
	}
	if got := strings.Join(ids(otto), ","); got != report {
		t.Errorf("Otto's: %s", got)
	}
	// Sam opened C-3 and assigned the report: he opened one, and assigning
	// someone else involves nobody but them.
	if got := strings.Join(ids(sam), ","); got != "C-3" {
		t.Errorf("Sam's: %s", got)
	}
	all, _ := w.svc.List(ctx, otto, Filter{})
	for _, b := range all {
		if b.Involved != (b.ID == report) {
			t.Errorf("%s involved=%v for Otto", b.ID, b.Involved)
		}
	}
	r, _ := w.iss.Report(ctx, system, rec.Report.Number)
	var who []string
	for _, p := range issues.Involved(&r) {
		who = append(who, p.ID+":"+strings.Join(p.Why, "+"))
	}
	if strings.Join(who, " ") != "rita:reported olivia:assigned otto:wrote" {
		t.Errorf("involved: %v", who)
	}
}

// A change applied that fixed a case makes its approvers involved, and
// says so to the others (EM-05).
func TestTheApproversOfAFixAreInvolved(t *testing.T) {
	w, fm := mailWorld(t)
	ctx := context.Background()
	v, err := w.svc.Create(ctx, NewCase{Title: "Web OOM"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.WaitMail()
	fm.take()
	card := cards.Card{ID: "PC-7", State: cards.Applied, Proposal: cards.Proposal{Title: "Raise the limit", Resolves: []string{v.ID}},
		Approvals: []cards.Decision{{By: cards.Person{Key: "otto", Name: "Otto"}, At: w.now}}}
	w.svc.CardChanged(card)
	w.svc.WaitMail()
	got := fm.take()
	if len(got) != 1 || !strings.Contains(got["olivia@corp.example"][0].Text, "Otto: fix applied by the change PC-7") {
		t.Fatalf("%+v", got)
	}
	list, _ := w.svc.List(ctx, otto, Filter{Involved: true})
	if len(list) != 1 || list[0].ID != v.ID {
		t.Fatalf("Otto's: %+v", list)
	}
}

// Without the console's address the email says where to look instead.
func TestAnEmailWithoutTheConsolesAddress(t *testing.T) {
	r := issues.Case{Number: 4, Title: "Slow", Priority: 3, Status: issues.StatusNew}
	m := CaseEmail(r, issues.Involvement{Why: []string{issues.InvolvedFollows}}, []mailChange{{what: "now New", byName: "Sam"}}, "")
	if strings.Contains(m.Text, "http") || !strings.Contains(m.Text, "search for C-4") || !strings.Contains(m.Text, "you follow it") || !strings.Contains(m.Text, "not assigned") {
		t.Errorf("%s", m.Text)
	}
}
