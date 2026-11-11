package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// clock is a fake time the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	s      *Service
	clock  *clock
	audits []AuditRecord
	events []Event
	mu     sync.Mutex
}

func newFixture(t *testing.T, o Options) *fixture {
	t.Helper()
	f := &fixture{clock: &clock{t: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}}
	o.Now = f.clock.now
	o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	o.Audit = func(a AuditRecord) { f.mu.Lock(); f.audits = append(f.audits, a); f.mu.Unlock() }
	f.s = New(o)
	f.s.OnChange(func(e Event) { f.mu.Lock(); f.events = append(f.events, e); f.mu.Unlock() })
	return f
}

func (f *fixture) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}

var (
	ctx      = context.Background()
	alice    = Caller{Person: Person{ID: "alice", Name: "Alice"}, Report: true}
	bob      = Caller{Person: Person{ID: "bob", Name: "Bob"}, Report: true}
	carol    = Caller{Person: Person{ID: "carol", Name: "Carol"}, Report: true}
	operator = Caller{Person: Person{ID: "olga", Name: "Olga"}, Report: true, Operator: true}
	admin    = Caller{Person: Person{ID: "root", Name: "Root"}, Report: true, Operator: true, Admin: true}
	nobody   = Caller{Person: Person{ID: "vic", Name: "Vic"}}
)

// seed makes the shop service and its known issue.
func seed(t *testing.T, f *fixture) KnownIssue {
	t.Helper()
	if _, err := f.s.SaveService(ctx, operator, "", ServiceDef{Name: "Shop", Description: "The online store", Aliases: []string{"web shop", "store"}, Contacts: []string{"carol"},
		RunsOn: []RunsOn{{Kind: "url", Value: "https://shop.example.com"}, {Kind: "namespace", Value: "shop", Cluster: "prod"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveService(ctx, operator, "", ServiceDef{Name: "Reports", Description: "Business reports and dashboards", Aliases: []string{"bi", "dashboards"}}); err != nil {
		t.Fatal(err)
	}
	k, err := f.s.SaveKnown(ctx, operator, "", KnownInput{Title: ptr("Checkout times out in the EU store"), Symptoms: []string{"checkout hangs after pressing pay", "504 gateway timeout at checkout"},
		Services: []string{"shop"}, Status: ptr(KnownFixInProgress), Workaround: ptr("Payments are not taken twice, so it is safe to try again in a few minutes."), Expected: ptr("today, 15:00")})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A report is scrubbed before it is kept, and the reporter is told.
func TestAReportIsScrubbedAndTheReporterTold(t *testing.T) {
	f := newFixture(t, Options{})
	rec, err := f.s.Submit(ctx, alice, Submission{Text: "Login fails. I tried password=hunter2secret and the token ghp_abcdefghijklmnopqrstuvwxyz0123456789 too"})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Removed || !rec.Report.Scrubbed {
		t.Fatal("the reporter was not told something was removed")
	}
	if strings.Contains(rec.Report.Text, "hunter2secret") || strings.Contains(rec.Report.Text, "ghp_") || !strings.Contains(rec.Report.Text, "[REDACTED]") {
		t.Fatalf("kept %q", rec.Report.Text)
	}
	clean, _ := f.s.Submit(ctx, alice, Submission{Text: "The reports page is slow"})
	if clean.Removed {
		t.Fatal("a clean report was said to be scrubbed")
	}
	if _, err := f.s.Submit(ctx, alice, Submission{Text: "   "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty report: %v", err)
	}
	if _, err := f.s.Submit(ctx, alice, Submission{Text: strings.Repeat("x", MaxText+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a long report: %v", err)
	}
	if _, err := f.s.Submit(ctx, nobody, Submission{Text: "hello"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("someone without Report: %v", err)
	}
}

// Numbers only grow, across saves and conflicts with another writer.
func TestReportsAreNumberedInOrder(t *testing.T) {
	mem := &kv.Memory{}
	f := newFixture(t, Options{Backend: mem})
	other := New(Options{Backend: mem, Now: f.clock.now, Logger: f.s.log})
	var got []int
	for i := range 6 {
		svc := f.s
		if i%2 == 1 {
			svc = other
		}
		f.clock.add(time.Second * 3)
		rec, err := svc.Submit(ctx, alice, Submission{Text: fmt.Sprintf("problem %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rec.Report.Number)
	}
	if fmt.Sprint(got) != "[1 2 3 4 5 6]" {
		t.Fatalf("%v", got)
	}
}

// A report that says what a known issue's symptoms say is linked to it,
// and told its workaround and expected fix; one about something else is
// not; one about part of it is a candidate below the threshold.
func TestMatchingAndConfidence(t *testing.T) {
	f := newFixture(t, Options{})
	k := seed(t, f)
	rec, err := f.s.Submit(ctx, alice, Submission{Text: "Checkout page hangs after I press Pay, since about 10:30. Then it shows 504 Gateway Timeout on shop.example.com/checkout."})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Known == nil || rec.Known.ID != k.ID || rec.Report.MatchConfidence < 80 {
		t.Fatalf("not linked: %+v %d", rec.Known, rec.Report.MatchConfidence)
	}
	if rec.Known.Workaround == "" || rec.Known.Expected != "today, 15:00" || rec.Report.Status != StatusClosed || rec.Report.Outcome != OutcomeKnownIssue || rec.Report.Service != "shop" {
		t.Fatalf("%+v", rec)
	}
	other, _ := f.s.Submit(ctx, bob, Submission{Text: "I can't upload files larger than 10 MB to the wiki"})
	if other.Known != nil || other.Report.Status != StatusNew {
		t.Fatalf("an unrelated report was linked: %+v", other.Known)
	}
	m, err := f.s.MatchText(ctx, "checkout is slow")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Known) == 0 || m.Known[0].Confidence >= m.Threshold || m.Known[0].Confidence < 20 {
		t.Fatalf("a partial match: %+v", m.Known)
	}
	typo, _ := f.s.MatchText(ctx, "chekout hangs when I pres pay")
	if len(typo.Known) == 0 || typo.Known[0].Confidence < 60 {
		t.Fatalf("a misspelt report: %+v", typo.Known)
	}
	// The service alone, by its address.
	svc, _ := f.s.MatchText(ctx, "https://shop.example.com/account shows a blank page")
	if len(svc.Services) == 0 || svc.Services[0].ID != "shop" || svc.Services[0].Confidence < serviceThreshold {
		t.Fatalf("%+v", svc.Services)
	}
	// A draft proposed by an agent matches nothing until accepted.
	d, err := f.s.Propose(ctx, SourceAgent, KnownInput{Title: ptr("Dashboards show yesterday's numbers"), Symptoms: []string{"dashboards show yesterday's numbers"}})
	if err != nil || !d.Draft {
		t.Fatalf("%+v %v", d, err)
	}
	if m, _ := f.s.MatchText(ctx, "the dashboards show yesterday's numbers"); len(m.Known) > 0 && m.Known[0].ID == d.ID {
		t.Fatal("a draft matched")
	}
	if _, err := f.s.Accept(ctx, operator, d.ID); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.s.MatchText(ctx, "the dashboards show yesterday's numbers"); len(m.Known) == 0 || m.Known[0].ID != d.ID || m.Known[0].Confidence < 80 {
		t.Fatalf("%+v", m.Known)
	}
}

// "No, mine is something else" unlinks the report for good and sends it
// to investigation.
func TestUnlinkSendsAReportToInvestigation(t *testing.T) {
	inv := &fakeInvestigator{done: make(chan int, 4)}
	f := newFixture(t, Options{Investigator: inv})
	seed(t, f)
	rec, _ := f.s.Submit(ctx, alice, Submission{Text: "checkout hangs after pressing pay, 504 gateway timeout"})
	if rec.Known == nil {
		t.Fatal("not linked")
	}
	if _, err := f.s.Unlink(ctx, bob, rec.Report.Number); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else unlinked it: %v", err)
	}
	r, err := f.s.Unlink(ctx, alice, rec.Report.Number)
	if err != nil {
		t.Fatal(err)
	}
	if r.KnownIssue != "" || r.Status != StatusNew || r.Outcome != "" || len(r.Rejected) != 1 {
		t.Fatalf("%+v", r)
	}
	select {
	case n := <-inv.done:
		if n != r.Number {
			t.Fatalf("investigated #%d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not investigated")
	}
	waitFor(t, func() bool {
		got, _ := f.s.Report(ctx, operator, r.Number)
		return got.Technical != ""
	})
	got, _ := f.s.Report(ctx, operator, r.Number)
	if got.Plain != "The payment provider is slow." || len(got.Messages) == 0 {
		t.Fatalf("%+v", got)
	}
	var draft Message
	for _, m := range got.Messages {
		if m.Draft {
			draft = m
		}
	}
	if draft.ID == 0 || draft.DraftBy != "agent" {
		t.Fatal("the investigator's reply was not kept as a draft")
	}
	// The reporter does not see the draft, nor the technical summary.
	mine, _ := f.s.Report(ctx, alice, r.Number)
	st, _ := f.s.load(ctx, true)
	v := st.reportView(alice, &mine, f.clock.now())
	for _, m := range v.Messages {
		if m.Draft {
			t.Fatal("the reporter saw a draft")
		}
	}
	if v.Technical != "" || v.Cause != "" || v.Plain == "" {
		t.Fatalf("%+v", v.Case)
	}
	// Only an operator's Send sends it.
	sent, err := f.s.Send(ctx, operator, r.Number, draft.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if sent.FirstResponse == nil {
		t.Fatal("the first response was not noted")
	}
	if _, err := f.s.Send(ctx, alice, r.Number, draft.ID, ""); !errors.Is(err, ErrDenied) {
		t.Fatalf("%v", err)
	}
	// A rejected known issue is not matched to it again.
	if _, err := f.s.Triage(ctx, operator, r.Number, Triage{}); err != nil {
		t.Fatal(err)
	}
	again, _ := f.s.Report(ctx, operator, r.Number)
	if again.KnownIssue != "" {
		t.Fatal("linked again")
	}
}

type fakeInvestigator struct {
	done chan int
}

func (f *fakeInvestigator) Investigate(_ context.Context, r Case) (Finding, error) {
	f.done <- r.Number
	return Finding{Technical: "upstream payments-api p99 at 31s in namespace shop", Plain: "The payment provider is slow.", Cause: "payments-api latency",
		Reply: "We are looking at the payment provider. password=letmein", Service: "shop", Confidence: 90}, nil
}

func (f *fakeInvestigator) Verify(context.Context, Case) (Verdict, error) {
	return Verdict{Fixed: true}, nil
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// Alike open reports on one service, sent within the window, are noted as
// similar; on another service, or outside the window, they are not.
func TestDuplicatesAreFoundOnTheSameServiceWithinTheWindow(t *testing.T) {
	f := newFixture(t, Options{})
	seed(t, f)
	first, _ := f.s.Submit(ctx, alice, Submission{Text: "The web shop basket is empty after login, all items gone"})
	if first.Report.Service != "shop" {
		t.Fatalf("service %q", first.Report.Service)
	}
	f.clock.add(time.Hour)
	second, _ := f.s.Submit(ctx, bob, Submission{Text: "web shop: my basket is empty after I log in, items gone"})
	if fmt.Sprint(second.Report.Similar) != fmt.Sprintf("[%d]", first.Report.Number) {
		t.Fatalf("similar %v", second.Report.Similar)
	}
	unlike, _ := f.s.Submit(ctx, carol, Submission{Text: "web shop invoices show the wrong VAT rate"})
	if len(unlike.Report.Similar) != 0 {
		t.Fatalf("similar %v", unlike.Report.Similar)
	}
	f.clock.add(7 * time.Hour)
	late, _ := f.s.Submit(ctx, carol, Submission{Text: "web shop basket is empty after login, items gone"})
	if len(late.Report.Similar) != 0 {
		t.Fatalf("outside the window: %v", late.Report.Similar)
	}
	other, _ := f.s.Submit(ctx, carol, Submission{Text: "dashboards basket is empty after login, items gone"})
	if len(other.Report.Similar) != 0 {
		t.Fatalf("another service: %v", other.Report.Similar)
	}
	// Marking one a duplicate closes it as such.
	d, err := f.s.Triage(ctx, operator, second.Report.Number, Triage{DuplicateOf: ptr(first.Report.Number)})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusClosed || d.Outcome != OutcomeDuplicate || d.Explanation == "" || d.DuplicateOf != first.Report.Number {
		t.Fatalf("%+v", d)
	}
}

// Who sees what: a reporter their own and what they follow, a service's
// contacts every report on it, operators everything; someone else
// nothing, as if it did not exist.
func TestVisibilityByRole(t *testing.T) {
	f := newFixture(t, Options{})
	seed(t, f)
	shop, _ := f.s.Submit(ctx, alice, Submission{Text: "https://shop.example.com is down"})
	bi, _ := f.s.Submit(ctx, bob, Submission{Text: "the dashboards are empty"})
	if shop.Report.Service != "shop" || bi.Report.Service != "reports" {
		t.Fatalf("services %q %q", shop.Report.Service, bi.Report.Service)
	}
	seen := func(c Caller) string {
		list, err := f.s.Reports(ctx, c, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range list {
			out = append(out, fmt.Sprint(r.Number))
		}
		return strings.Join(out, ",")
	}
	if got := seen(alice); got != "1" {
		t.Errorf("alice sees %s", got)
	}
	if got := seen(carol); got != "1" {
		t.Errorf("the shop's contact sees %s", got)
	}
	if got := seen(operator); got != "2,1" {
		t.Errorf("an operator sees %s", got)
	}
	if got := seen(nobody); got != "" {
		t.Errorf("someone else sees %s", got)
	}
	if _, err := f.s.Report(ctx, alice, bi.Report.Number); !errors.Is(err, ErrNotFound) {
		t.Errorf("alice read bob's: %v", err)
	}
	if _, err := f.s.Watch(ctx, alice, bi.Report.Number, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("alice followed bob's: %v", err)
	}
	// Support files for bob; bob sees it as his, the operator as filed.
	filed, err := f.s.Submit(ctx, operator, Submission{Text: "bob's laptop cannot reach the VPN", For: &Person{ID: "bob", Name: "Bob"}})
	if err != nil || filed.Report.Reporter.ID != "bob" || filed.Report.FiledBy == nil || filed.Report.FiledBy.ID != "olga" {
		t.Fatalf("%+v %v", filed.Report, err)
	}
	if _, err := f.s.Submit(ctx, alice, Submission{Text: "for bob", For: &Person{ID: "bob"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("a reporter filed for someone else: %v", err)
	}
	if got := seen(bob); got != "3,2" {
		t.Errorf("bob sees %s", got)
	}
	// Support who filed it still writes on it as an operator: a note
	// stays internal and a reply is an operator's (CS-15).
	if _, err := f.s.AddMessage(ctx, operator, filed.Report.Number, "VPN gateway again", true); err != nil {
		t.Fatalf("support's note on a report they filed: %v", err)
	}
	if r, err := f.s.AddMessage(ctx, operator, filed.Report.Number, "We are on it.", false); err != nil || r.Messages[len(r.Messages)-1].Kind != FromOperator {
		t.Fatalf("support's reply on a report they filed: %+v %v", r.Messages, err)
	}
	// Following a report shows it.
	if _, err := f.s.Watch(ctx, carol, shop.Report.Number, true); err != nil {
		t.Fatal(err)
	}
	// What a contact sees is the reporter's view.
	f.s.Draft(ctx, operator, shop.Report.Number, "We restart the shop pods in namespace shop on prod.", "")
	f.s.AddMessage(ctx, operator, shop.Report.Number, "internal: node worker-7 is bad", true)
	r, _ := f.s.Report(ctx, carol, shop.Report.Number)
	st, _ := f.s.load(ctx, true)
	v := st.reportView(carol, &r, f.clock.now())
	if len(v.Messages) != 0 || v.SLA != nil || !v.Watching || v.Mine {
		t.Fatalf("%+v", v)
	}
	op := st.reportView(operator, &r, f.clock.now())
	if len(op.Messages) != 2 || op.SLA == nil || op.SLA.ResponseDue == nil {
		t.Fatalf("%+v", op)
	}
}

// A token files as its account, with its scopes and nothing more; a
// revoked or made-up one is refused alike; the hash, never the token, is
// kept.
func TestTokensAndTheirScopes(t *testing.T) {
	mem := &kv.Memory{}
	f := newFixture(t, Options{Backend: mem})
	if _, _, err := f.s.CreateToken(ctx, operator, TokenInput{Name: "x", Account: "alerts", Scopes: []string{ScopeCreate}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("an operator made a token: %v", err)
	}
	if _, _, err := f.s.CreateToken(ctx, admin, TokenInput{Name: "x", Account: "Alerts!", Scopes: []string{ScopeCreate}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad account: %v", err)
	}
	tok, secret, err := f.s.CreateToken(ctx, admin, TokenInput{Name: "Alertmanager", Account: "alertmanager", Scopes: []string{ScopeCreate}})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := mem.LoadKey(ctx, ConfigKey)
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), hashToken(secret)) || tok.Hash != "" {
		t.Fatal("the token itself was kept, or its hash shown")
	}
	h := f.s.Handler(func(*http.Request) (Caller, bool) { return Caller{}, false })
	call := func(method, path, auth, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call("POST", "/api/issues/v1/intake/reports", "Bearer "+secret, `{"text":"disk on db-1 is 95% full"}`)
	if code != http.StatusCreated {
		t.Fatalf("%d %v", code, out)
	}
	rep := out["report"].(map[string]any)
	if rep["reporter"].(map[string]any)["id"] != "system:alertmanager" {
		t.Fatalf("%v", rep["reporter"])
	}
	if code, _ := call("GET", "/api/issues/v1/intake/reports/1", "Bearer "+secret, ""); code != http.StatusForbidden {
		t.Fatalf("read without the scope: %d", code)
	}
	if code, _ := call("POST", "/api/issues/v1/intake/reports", "Bearer "+secret, `{"text":"for bob","for":{"id":"bob"}}`); code != http.StatusForbidden {
		t.Fatalf("filed for someone without the scope: %d", code)
	}
	for _, bad := range []string{"", "Bearer nonsense", "Bearer " + secret + "x", "Basic " + secret} {
		if code, _ := call("POST", "/api/issues/v1/intake/reports", bad, `{"text":"x"}`); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("%q: %d", bad, code)
		}
	}
	// Without a session the rest of the API is closed, token or not.
	if code, _ := call("GET", "/api/issues/v1/reports", "Bearer "+secret, ""); code != http.StatusUnauthorized {
		t.Fatalf("a token reached the session API: %d", code)
	}
	if err := f.s.RevokeToken(ctx, admin, tok.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("POST", "/api/issues/v1/intake/reports", "Bearer "+secret, `{"text":"x"}`); code != http.StatusForbidden {
		t.Fatalf("a revoked token: %d", code)
	}
	if n := strings.Count(strings.Join(f.actions(), " "), ActionToken); n != 2 {
		t.Fatalf("%d token records", n)
	}
	// One that may read reads what its account filed, and only that.
	_, reader, _ := f.s.CreateToken(ctx, admin, TokenInput{Name: "Desk", Account: "desk", Scopes: []string{ScopeCreate, ScopeRead, ScopeOnBehalf}})
	code, out = call("POST", "/api/issues/v1/intake/reports", "Bearer "+reader, `{"text":"printer jammed","for":{"id":"bob","name":"Bob"}}`)
	if code != http.StatusCreated {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := call("GET", "/api/issues/v1/intake/reports/2", "Bearer "+reader, ""); code != http.StatusOK {
		t.Fatalf("read its own: %d", code)
	}
	if code, _ := call("GET", "/api/issues/v1/intake/reports/1", "Bearer "+reader, ""); code != http.StatusNotFound {
		t.Fatalf("read another account's: %d", code)
	}
}

// One reporter may send so many reports an hour; operators are not held
// to it, and the hour passes.
func TestReportsAreRateLimited(t *testing.T) {
	f := newFixture(t, Options{})
	set := DefaultSettings()
	set.RatePerHour = 3
	if _, err := f.s.SaveSettings(ctx, admin, set); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := f.s.Submit(ctx, alice, Submission{Text: fmt.Sprintf("problem %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Submit(ctx, alice, Submission{Text: "one more"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("%v", err)
	}
	if _, err := f.s.Submit(ctx, bob, Submission{Text: "bob's first"}); err != nil {
		t.Fatalf("another reporter: %v", err)
	}
	for range 5 {
		if _, err := f.s.Submit(ctx, operator, Submission{Text: "filed by support"}); err != nil {
			t.Fatalf("an operator: %v", err)
		}
	}
	f.clock.add(time.Hour)
	if _, err := f.s.Submit(ctx, alice, Submission{Text: "an hour later"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SaveSettings(ctx, operator, set); !errors.Is(err, ErrDenied) {
		t.Fatalf("an operator changed the settings: %v", err)
	}
	set.MatchThreshold = 0
	if _, err := f.s.SaveSettings(ctx, admin, set); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}

// The store stays within its limits: past the retention a closed report
// loses its text and keeps how it ended; when room runs out those go
// first, counted; and when that is not enough, the save is refused,
// saying what to do.
func TestStorageLimitsAndRetention(t *testing.T) {
	f := newFixture(t, Options{Limits: Limits{IndexBytes: 12 << 10}})
	day := 24 * time.Hour
	set := DefaultSettings()
	set.RetentionDays, set.RatePerHour = 30, 1000
	f.s.SaveSettings(ctx, admin, set)
	for i := range 10 {
		rec, err := f.s.Submit(ctx, alice, Submission{Text: fmt.Sprintf("problem %d %s", i, strings.Repeat("word ", 100))})
		if err != nil {
			t.Fatal(err)
		}
		f.s.SetStatus(ctx, operator, rec.Report.Number, StatusClosed, OutcomeNoAction, "Not for now.")
	}
	f.clock.add(31 * day)
	if err := f.s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := f.s.Report(ctx, operator, 1)
	if !r.Pruned || r.Text != "" || r.Outcome != OutcomeNoAction || len(r.Messages) != 0 {
		t.Fatalf("%+v", r)
	}
	// New reports push the pruned ones out, counted.
	for i := range 100 {
		if _, err := f.s.Submit(ctx, alice, Submission{Text: fmt.Sprintf("new %d %s", i, strings.Repeat("w ", 60))}); err != nil {
			if !errors.Is(err, ErrFull) {
				t.Fatal(err)
			}
			if !strings.Contains(err.Error(), "shorten the retention") {
				t.Fatalf("%v", err)
			}
			st, _ := f.s.load(ctx, true)
			if st.index.Archived[string(OutcomeNoAction)] != 10 {
				t.Fatalf("archived %v", st.index.Archived)
			}
			if objs, _ := f.s.cases.List(ctx); len(objs) != len(st.index.Lines) {
				t.Fatalf("%d case objects for %d lines", len(objs), len(st.index.Lines))
			}
			if _, err := f.s.Report(ctx, operator, 1); !errors.Is(err, ErrNotFound) {
				t.Fatal("a pruned report stayed while the store was full")
			}
			return
		}
	}
	t.Fatal("never full")
}

// The webhook carries summaries, never a report's text or conversation.
func TestTheWebhookCarriesSummariesOnly(t *testing.T) {
	got := make(chan []byte, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- b
	}))
	defer srv.Close()
	f := newFixture(t, Options{})
	set := DefaultSettings()
	set.Company, set.Webhook = "Example IT", Webhook{URL: srv.URL, Events: []string{EventReportCreated}}
	if _, err := f.s.SaveSettings(ctx, admin, set); err != nil {
		t.Fatal(err)
	}
	f.s.Submit(ctx, alice, Submission{Text: "my secret project codename is BLUEBIRD and the VPN drops"})
	select {
	case b := <-got:
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(body["text"].(string), "Example IT: Report #1 received") || bytes.Contains(b, []byte("BLUEBIRD")) {
			t.Fatalf("%s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook was not called")
	}
}

// The page's notices: a status change, a reply and an update of the known
// issue followed, since the last time it asked.
func TestUpdatesForTheReportPage(t *testing.T) {
	f := newFixture(t, Options{})
	k := seed(t, f)
	rec, _ := f.s.Submit(ctx, alice, Submission{Text: "the reports page is slow"})
	since := f.clock.now()
	f.clock.add(time.Minute)
	f.s.SetStatus(ctx, operator, rec.Report.Number, StatusInProgress, "", "")
	f.s.AddMessage(ctx, operator, rec.Report.Number, "Looking at it now.", false)
	f.s.Follow(ctx, alice, k.ID, true)
	f.s.AddUpdate(ctx, operator, k.ID, "Cause found.", KnownIdentified)
	list, err := f.s.Updates(ctx, alice, since)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, n := range list {
		texts = append(texts, n.Text)
	}
	all := strings.Join(texts, " | ")
	for _, want := range []string{"#1 is being worked on.", "A reply on #1 from Olga.", "Checkout times out in the EU store: Cause found."} {
		if !strings.Contains(all, want) {
			t.Errorf("%q not in %q", want, all)
		}
	}
	if list, _ := f.s.Updates(ctx, bob, since); len(list) != 0 {
		t.Fatalf("bob was told %v", list)
	}
}

// A backup carries the records but no token, and a restore adds what is
// missing, keeps what is there, and numbers on above both.
func TestExportAndImport(t *testing.T) {
	f := newFixture(t, Options{})
	seed(t, f)
	set := DefaultSettings()
	set.Company = "Example IT"
	f.s.SaveSettings(ctx, admin, set)
	f.s.Submit(ctx, alice, Submission{Text: "first"})
	f.s.Submit(ctx, alice, Submission{Text: "second"})
	_, secret, _ := f.s.CreateToken(ctx, admin, TokenInput{Name: "x", Account: "x", Scopes: []string{ScopeCreate}})
	b, err := f.s.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(hashToken(secret))) || !bytes.Contains(b, []byte(BackupFormat)) {
		t.Fatal("a token's hash went into the backup")
	}
	g := newFixture(t, Options{})
	g.s.Submit(ctx, bob, Submission{Text: "here already"})
	res, err := g.s.Import(ctx, b, true, Person{ID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kept != 0 || res.Known != 1 || res.Services != 2 || !res.Settings {
		t.Fatalf("%+v", res)
	}
	// The cases are objects of their own, in a part of their own.
	objs, err := f.s.ExportCases(ctx)
	if err != nil || len(objs) != 2 {
		t.Fatalf("cases: %d %v", len(objs), err)
	}
	added, kept, err := g.s.ImportCases(ctx, objs, Person{ID: "root"})
	if err != nil || added != 1 || kept != 1 {
		t.Fatalf("cases restored %d, kept %d: %v", added, kept, err)
	}
	if r, _ := g.s.Report(ctx, operator, 2); r.Text != "second" {
		t.Fatalf("restored %q", r.Text)
	}
	if got, _ := g.s.Settings(ctx); got.Company != "Example IT" {
		t.Fatalf("%+v", got)
	}
	if r, _ := g.s.Report(ctx, operator, 1); r.Text != "here already" {
		t.Fatalf("%q", r.Text)
	}
	rec, _ := g.s.Submit(ctx, carol, Submission{Text: "after the restore"})
	if rec.Report.Number != 3 {
		t.Fatalf("#%d", rec.Report.Number)
	}
	if _, err := g.s.Import(ctx, []byte(`{"format":"other"}`), false, Person{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}

// Through the API, a reporter's view has no drafts, internal notes or
// technical summary, and the page's calls answer as the page expects.
func TestTheAPIAsTheReportPageUsesIt(t *testing.T) {
	f := newFixture(t, Options{})
	seed(t, f)
	who := alice
	h := f.s.Handler(func(*http.Request) (Caller, bool) { return who, who.ID != "" })
	call := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	code, body := call("POST", "/api/issues/v1/reports", `{"text":"checkout hangs after pressing pay, 504 gateway timeout"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"workaround":"Payments are not taken twice`) || !strings.Contains(body, `"expected":"today, 15:00"`) {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, "matchConfidence") || strings.Contains(body, "symptoms") {
		t.Fatalf("the reporter was shown the match's workings: %s", body)
	}
	f.s.Draft(ctx, operator, 1, "a draft reply", "")
	f.s.AddMessage(ctx, operator, 1, "an internal note", true)
	_, body = call("GET", "/api/issues/v1/reports", "")
	if strings.Contains(body, "a draft reply") || strings.Contains(body, "an internal note") {
		t.Fatalf("%s", body)
	}
	if code, _ := call("POST", "/api/issues/v1/reports", `{"text":"x","extra":1}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d", code)
	}
	if code, _ := call("POST", "/api/issues/v1/reports/1/unlink", ""); code != http.StatusOK {
		t.Fatalf("unlink: %d", code)
	}
	if code, _ := call("PUT", "/api/issues/v1/known/KI-1/follow", ""); code != http.StatusOK {
		t.Fatalf("follow: %d", code)
	}
	if code, _ := call("GET", "/api/issues/v1/settings", ""); code != http.StatusForbidden {
		t.Fatalf("a reporter read the settings: %d", code)
	}
	if code, _ := call("PATCH", "/api/issues/v1/reports/1", `{"priority":1}`); code != http.StatusForbidden {
		t.Fatalf("a reporter triaged: %d", code)
	}
	code, body = call("GET", "/api/issues/v1/me", "")
	if code != http.StatusOK || !strings.Contains(body, `"reportURL":"/report"`) {
		t.Fatalf("%d %s", code, body)
	}
	who = Caller{}
	if code, _ := call("GET", "/api/issues/v1/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("nobody: %d", code)
	}
	who = operator
	code, body = call("GET", "/api/issues/v1/reports?scope=all&status=new", "")
	if code != http.StatusOK || !strings.Contains(body, `"sla"`) || !strings.Contains(body, `"status":"new"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body = call("GET", "/api/issues/v1/reports/1", ""); code != http.StatusOK || !strings.Contains(body, "an internal note") {
		t.Fatalf("%d %s", code, body)
	}
	// Priorities are 0 to 4 (CF-02).
	if code, body := call("PATCH", "/api/issues/v1/reports/1", `{"priority":0}`); code != http.StatusOK || !strings.Contains(body, `"priority":0`) {
		t.Fatalf("P0: %d %s", code, body)
	}
	if code, _ := call("PATCH", "/api/issues/v1/reports/1", `{"priority":5}`); code != http.StatusBadRequest {
		t.Fatalf("P5: %d", code)
	}
	if code, body := call("POST", "/api/issues/v1/match", `{"text":"checkout hangs"}`); code != http.StatusOK || !strings.Contains(body, "KI-1") {
		t.Fatalf("%d %s", code, body)
	}
}

// A known issue the housekeeping made inactive is left out for reporters:
// the report page's list, its match and the cases' known issues; an
// operator asking for all still sees it (CF-63).
func TestInactiveKnownIssuesAreHiddenFromReportersCF63(t *testing.T) {
	f := newFixture(t, Options{})
	k := seed(t, f)
	f.clock.add(time.Minute)
	dup, err := f.s.SaveKnown(ctx, operator, "", KnownInput{Title: ptr("Checkout times out in the EU store"), Symptoms: []string{"checkout hangs after pressing pay"}, Status: ptr(KnownIdentified)})
	if err != nil {
		t.Fatal(err)
	}
	made, err := f.s.HousekeepKnown(ctx, systemPerson)
	if err != nil || len(made) != 1 || made[0].ID != dup.ID || made[0].Of != k.ID {
		t.Fatalf("%v %+v", err, made)
	}
	ids := func(list []KnownIssue) []string {
		var out []string
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	for _, c := range []struct {
		who  Caller
		all  bool
		want string
	}{{alice, false, k.ID}, {alice, true, k.ID}, {operator, false, k.ID}, {operator, true, dup.ID + "," + k.ID}} {
		list, err := f.s.Known(ctx, c.who, c.all)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(ids(list), ","); got != c.want {
			t.Fatalf("%s all=%v: %s, want %s", c.who.ID, c.all, got, c.want)
		}
	}
	h := f.s.Handler(func(*http.Request) (Caller, bool) { return alice, true })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/issues/v1/known", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), dup.ID) || !strings.Contains(w.Body.String(), k.ID) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m, err := f.s.MatchText(ctx, "checkout hangs after pressing pay")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range m.Known {
		if x.ID == dup.ID {
			t.Fatalf("an inactive known issue was matched: %+v", m.Known)
		}
	}
	// Made active again, reporters see it again.
	if _, err := f.s.SaveKnown(ctx, operator, dup.ID, KnownInput{Inactive: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.s.Known(ctx, alice, false); len(list) != 2 {
		t.Fatalf("made active: %v", ids(list))
	}
}

// A reporter may choose the priority, 1 to 4, which the SLA follows, and
// the connected cluster it is about; the intake API takes both too
// (CF-64).
func TestAReporterChoosesPriorityAndClusterCF64(t *testing.T) {
	f := newFixture(t, Options{Clusters: func() []Cluster {
		return []Cluster{{ID: "local", Name: "local"}, {ID: "c-prod", Name: "Production"}, {ID: "c-bare"}}
	}})
	if got := f.s.Clusters(); len(got) != 3 || got[1] != (Cluster{ID: "c-prod", Name: "Production"}) || got[2].Name != "c-bare" {
		t.Fatalf("%+v", got)
	}
	rec, err := f.s.Submit(ctx, alice, Submission{Text: "The intranet is down for my whole team", Priority: ptr(1), Cluster: "c-prod"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Report.Priority != 1 || rec.Report.PriorityBy != "reporter" || rec.Report.Cluster != "c-prod" || rec.Report.SLAFrom == nil {
		t.Fatalf("%+v", rec.Report)
	}
	set, _ := f.s.Settings(ctx)
	if sla := set.slaOf(&rec.Report, f.clock.now()); sla == nil || sla.Target != set.Targets["1"] || sla.Target == set.Targets["2"] {
		t.Fatalf("the SLA does not follow the chosen priority: %+v", sla)
	}
	plain, err := f.s.Submit(ctx, alice, Submission{Text: "A question about the VPN"})
	if err != nil || plain.Report.Priority != DefaultPriority || plain.Report.PriorityBy != "default" || plain.Report.Cluster != "" {
		t.Fatalf("%v %+v", err, plain.Report)
	}
	for _, in := range []Submission{{Text: "x", Priority: ptr(0)}, {Text: "x", Priority: ptr(5)}, {Text: "x", Cluster: "nowhere"}} {
		if _, err := f.s.Submit(ctx, alice, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	if !strings.Contains(strings.Join(func() []string {
		var out []string
		for _, a := range f.audits {
			out = append(out, a.Detail)
		}
		return out
	}(), "\n"), "P1 as the reporter chose, on c-prod") {
		t.Fatalf("the audit does not say it: %+v", f.audits)
	}

	// The report page: the clusters on me, by id and name only; the
	// choices sent; an unknown cluster is a 400.
	h := f.s.Handler(func(*http.Request) (Caller, bool) { return bob, true })
	call := func(method, path, body string, header ...string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if len(header) == 1 {
			req.Header.Set("Authorization", header[0])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	code, body := call("GET", "/api/issues/v1/me", "")
	if code != http.StatusOK || !strings.Contains(body, `"clusters":[{"id":"local","name":"local"},{"id":"c-prod","name":"Production"},{"id":"c-bare","name":"c-bare"}]`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := call("POST", "/api/issues/v1/reports", `{"text":"Builds fail on prod","priority":3,"cluster":"c-prod"}`); code != http.StatusCreated || !strings.Contains(body, `"priority":3`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := call("POST", "/api/issues/v1/reports", `{"text":"Builds fail","cluster":"elsewhere"}`); code != http.StatusBadRequest || !strings.Contains(body, "no connected cluster") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := call("POST", "/api/issues/v1/reports", `{"text":"Builds fail","priority":0}`); code != http.StatusBadRequest {
		t.Fatalf("P0 from a reporter: %d", code)
	}
	// The intake API.
	_, secret, err := f.s.CreateToken(ctx, admin, TokenInput{Name: "Helpdesk", Account: "helpdesk", Scopes: []string{ScopeCreate}})
	if err != nil {
		t.Fatal(err)
	}
	code, body = call("POST", "/api/issues/v1/intake/reports", `{"text":"Mail is slow","priority":4,"cluster":"local"}`, "Bearer "+secret)
	if code != http.StatusCreated || !strings.Contains(body, `"priority":4`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := call("POST", "/api/issues/v1/intake/reports", `{"text":"Mail is slow","cluster":"c-gone"}`, "Bearer "+secret); code != http.StatusBadRequest {
		t.Fatalf("an unknown cluster through intake: %d", code)
	}
	if n := len(f.s.Clusters()); n != 3 {
		t.Fatal(n)
	}
	if New(Options{}).Clusters() == nil {
		t.Fatal("no clusters must still be a list")
	}
}
