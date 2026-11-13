package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
)

const pw = "hunter2-s3cret-pa55"

var sam = Person{Subject: "sam", Email: "sam@corp.example", Name: "Sam", From: "10.0.0.7"}

func setup(t *testing.T) (*Service, *Capture, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	cap := &Capture{}
	s := New(Options{Backend: &kv.Memory{}, Sender: cap, Audit: audit.New(audit.Options{Out: &out}),
		Link: func(context.Context) string { return "https://221b.example.com/" }})
	return s, cap, &out
}

// The settings are checked as given (EM-01): a host name without a
// scheme, a port, one of the three securities, one line each, a From
// address; no password in the clear, none without a user name.
func TestTheSettingsAreChecked(t *testing.T) {
	good := Input{Host: "smtp.example.com", Security: "starttls", Username: "221b", From: "221b@example.com"}
	c, err := Validate(good, pw)
	if err != nil || c.Port != 587 || c.Security != SecurityStartTLS {
		t.Fatalf("good: %+v %v", c, err)
	}
	if c, err := Validate(Input{Host: "SMTP.Example.com", Security: "tls", From: "a@example.com"}, ""); err != nil || c.Port != 465 || c.Host != "smtp.example.com" {
		t.Errorf("tls: %+v %v", c, err)
	}
	if c, err := Validate(Input{Host: "10.0.0.25", Security: "none", From: "a@example.com"}, ""); err != nil || c.Port != 25 {
		t.Errorf("a relay: %+v %v", c, err)
	}
	if c, err := Validate(Input{}, ""); err != nil || c.Configured() {
		t.Errorf("off: %+v %v", c, err)
	}
	bad := []struct {
		in Input
		pw string
	}{
		{Input{Host: "https://smtp.example.com", From: "a@example.com"}, ""},
		{Input{Host: "smtp.example.com:587", From: "a@example.com"}, ""},
		{Input{Host: "smtp.example.com", Port: 70000, From: "a@example.com"}, ""},
		{Input{Host: "smtp.example.com", Security: "ssl", From: "a@example.com"}, ""},
		{Input{Host: "smtp.example.com", From: "not an address"}, ""},
		{Input{Host: "smtp.example.com", From: "Ops <a@example.com>"}, ""},
		{Input{Host: "smtp.example.com", From: "a@example.com\r\nBcc: x@evil.example"}, ""},
		{Input{Host: "smtp.example.com", Username: "u\r\nRCPT TO:<x@evil.example>", From: "a@example.com"}, pw},
		{Input{Host: "smtp.example.com", From: ""}, ""},
		{Input{Host: "", From: "a@example.com"}, ""},
		{Input{Host: "smtp.example.com", Security: "none", Username: "u", From: "a@example.com"}, pw},
		{Input{Host: "smtp.example.com", From: "a@example.com"}, pw},
		{Input{Host: "smtp.example.com", Username: "u", From: "a@example.com"}, "two\nlines"},
	}
	for _, b := range bad {
		if _, err := Validate(b.in, b.pw); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", b.in, err)
		}
	}
}

// Email is on only once configured and tested at the settings as they
// stand (EM-02): a change of any field that reaches the server, the
// password's too, turns it off until a test passes again; saving the
// same settings keeps it on.
func TestEmailIsOnOnlyAfterATestOfTheSettingsAsTheyStand(t *testing.T) {
	s, cap, out := setup(t)
	ctx := context.Background()
	if v, _ := s.Settings(ctx); v.State != StateNotConfigured || v.On || v.Port != 587 || v.Link != "https://221b.example.com" {
		t.Fatalf("new: %+v", v)
	}
	if err := s.Send(ctx, Message{To: "x@example.com", Subject: "s", Text: "t"}); !errors.Is(err, ErrOff) {
		t.Fatalf("sent while off: %v", err)
	}
	in := Input{Host: "smtp.example.com", Security: "starttls", Username: "221b", From: "221b@example.com", Password: pw}
	v, err := s.Save(ctx, in, sam)
	if err != nil || v.State != StateNotTested || v.On || !v.PasswordSet || v.InSecret {
		t.Fatalf("saved: %+v %v", v, err)
	}
	if _, err := s.Test(ctx, "", Person{Name: "No address"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a test with no address: %v", err)
	}
	// A test to the SuperAdmin's own address by default.
	v, err = s.Test(ctx, "", sam)
	if err != nil || v.State != StateOn || !v.On || v.Tested == nil || v.Tested.By != "Sam" || v.Tested.To != "sam@corp.example" {
		t.Fatalf("tested: %+v %v", v, err)
	}
	sent := cap.Sent()
	if len(sent) != 1 || sent[0].To != "sam@corp.example" || sent[0].From != "221b@example.com" || sent[0].User != "221b" || !strings.Contains(sent[0].Text, "https://221b.example.com/") {
		t.Fatalf("the test email: %+v", sent)
	}
	if !s.On(ctx) {
		t.Fatal("not on")
	}
	if err := s.Send(ctx, Message{To: "ann@example.com", Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The same settings again, the password kept: still on.
	in.Password = ""
	if v, err := s.Save(ctx, in, sam); err != nil || !v.On {
		t.Fatalf("saved again: %+v %v", v, err)
	}
	// Any field changed turns it off.
	for _, change := range []func(*Input){
		func(i *Input) { i.Port = 2525 },
		func(i *Input) { i.Security = "tls" },
		func(i *Input) { i.Username = "other" },
		func(i *Input) { i.From = "ops@example.com" },
		func(i *Input) { i.Host = "mail.example.com" },
		func(i *Input) { i.Password = "a-new-one" },
	} {
		x := in
		change(&x)
		v, err := s.Save(ctx, x, sam)
		if err != nil || v.On || v.State != StateNotTested || v.Tested != nil || v.LastTest == nil {
			t.Fatalf("%+v: %+v %v", x, v, err)
		}
		if err := s.Send(ctx, Message{To: "ann@example.com", Subject: "s", Text: "t"}); !errors.Is(err, ErrOff) {
			t.Fatalf("sent untested: %v", err)
		}
		if v, err := s.Test(ctx, "ops@example.com", sam); err != nil || !v.On {
			t.Fatalf("tested again: %+v %v", v, err)
		}
		in = x
		in.Password = ""
	}
	// A failed test says why, without the password, and leaves it off.
	cap.Fail = errors.New("535 authentication failed for a-new-one")
	in.Port = 587
	if _, err := s.Save(ctx, in, sam); err != nil {
		t.Fatal(err)
	}
	v, err = s.Test(ctx, "ops@example.com", sam)
	if err != nil || v.On || v.State != StateFailed || v.Tested == nil || v.Tested.OK || !strings.Contains(v.Tested.Error, "535") || strings.Contains(v.Tested.Error, "a-new-one") {
		t.Fatalf("failed test: %+v %v", v, err)
	}
	// Turning it off takes the password away too.
	if v, err := s.Save(ctx, Input{}, sam); err != nil || v.State != StateNotConfigured || v.PasswordSet {
		t.Fatalf("off: %+v %v", v, err)
	}
	// The password is in no answer and no audit record; changes and tests
	// are audited.
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), pw) || strings.Contains(out.String(), pw) || strings.Contains(out.String(), "a-new-one") {
		t.Fatalf("the password leaked: %s %s", b, out.String())
	}
	for _, want := range []string{`"action":"mail.settings"`, `"action":"mail.test"`, "password set", "email is off until a test passes", `"outcome":"failed"`, "test email sent to sam@corp.example"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the audit log lacks %q: %s", want, out.String())
		}
	}
}

// A test of settings changed meanwhile by another replica does not turn
// the new ones on.
func TestATestCountsOnlyForItsSettings(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, Input{Host: "smtp.example.com", From: "a@example.com", Security: "starttls"}, sam); err != nil {
		t.Fatal(err)
	}
	c, _, _ := s.load(ctx)
	c.Tested = &Test{At: time.Now(), OK: true, Gen: c.Gen - 1}
	b, _ := json.Marshal(c)
	_, _ = s.o.Backend.SaveKey(ctx, ConfigKey, b, "")
	if s.On(ctx) {
		t.Fatal("a test of older settings turned it on")
	}
}

// The real sender talks SMTP to the simulator's fake server (EM-10): the
// message arrives with its subject, plain text and HTML; a server that
// offers no STARTTLS is refused rather than used in the clear.
func TestSMTPAgainstTheFakeServer(t *testing.T) {
	f, err := ListenFake("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	host, port := splitAddr(t, f.Addr())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := Message{From: "221b@example.com", To: "ann@example.com", Subject: "[221B] C-1 opened: ünïcode", Text: "Line one\nLine two", HTML: "<p>Line one</p>"}
	if err := (SMTP{}).Send(ctx, Server{Host: host, Port: port, Security: SecurityNone}, m); err != nil {
		t.Fatal(err)
	}
	got := f.Messages()
	if len(got) != 1 || got[0].To != "ann@example.com" || got[0].From != "221b@example.com" || got[0].Subject != m.Subject {
		t.Fatalf("%+v", got)
	}
	for _, want := range []string{"multipart/alternative", "text/plain; charset=utf-8", "text/html; charset=utf-8", "Line one\r\nLine two", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(got[0].Data, want) {
			t.Errorf("the message lacks %q:\n%s", want, got[0].Data)
		}
	}
	err = (SMTP{}).Send(ctx, Server{Host: host, Port: port, Security: SecurityStartTLS, Username: "u", Password: pw}, m)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") || strings.Contains(err.Error(), pw) {
		t.Fatalf("STARTTLS: %v", err)
	}
	if len(f.Messages()) != 1 {
		t.Fatal("sent without STARTTLS")
	}
	// Through the service, as the simulator's console is set up.
	s := New(Options{})
	ctx2 := context.Background()
	if _, err := s.Save(ctx2, Input{Host: host, Port: port, Security: SecurityNone, From: "221b@example.com"}, sam); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Test(ctx2, "", sam); err != nil || !v.On {
		t.Fatalf("test: %+v %v", v, err)
	}
	if got := f.Messages(); len(got) != 2 || got[1].To != "sam@corp.example" || got[1].Subject != "221B: test email" {
		t.Fatalf("%+v", got)
	}
	// Nobody listening: the reason, not the stack.
	f.Close()
	v, err := s.Test(ctx2, "", sam)
	if err != nil || v.On || v.Tested == nil || !strings.Contains(v.Tested.Error, "could not be reached") {
		t.Fatalf("closed: %+v %v", v.Tested, err)
	}
}

// A message cannot gain a header through an address or its subject.
func TestAMessageCarriesNoInjectedHeader(t *testing.T) {
	if _, err := Build(Message{From: "a@example.com", To: "b@example.com\r\nBcc: c@evil.example", Subject: "s", Text: "t"}, time.Now()); err == nil {
		t.Error("a line break in the recipient")
	}
	b, err := Build(Message{From: "a@example.com", To: "b@example.com", Subject: "hi\r\nBcc: c@evil.example", Text: "t"}, time.Now())
	if err != nil || strings.Contains(string(b), "\r\nBcc:") {
		t.Errorf("a line break in the subject: %v\n%s", err, b)
	}
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	var port int
	for _, r := range addr[i+1:] {
		port = port*10 + int(r-'0')
	}
	return addr[:i], port
}
