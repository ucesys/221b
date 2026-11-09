package cards

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/backup"
)

// hook is a channel's receiving end.
type hook struct {
	mu     sync.Mutex
	bodies []string
	code   int
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(b))
	code := h.code
	h.mu.Unlock()
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
}

func (h *hook) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bodies...)
}

// A new card is announced on every channel that is on, with a summary and
// a link: never a secret, the reasoning, the evidence, the commands or any
// output. The channels' addresses are secrets: the page and the audit log
// see only whether one is set, and a backup holds the placeholder.
func TestANotificationCarriesASummaryAndALinkOnly(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	secrets := &backup.MemorySecret{}
	w.st.opts.Secrets = secrets
	hooks := map[string]*hook{ChannelWebhook: {}, ChannelSlack: {}, ChannelTeams: {}}
	var in NotifyInput
	in.Link = "https://221b.example.com/"
	for kind, h := range hooks {
		srv := httptest.NewServer(h)
		defer srv.Close()
		in.Channels = append(in.Channels, ChannelInput{Channel: Channel{Kind: kind, Name: "ops " + kind, On: true}, URL: srv.URL + "/hooks/T000/B000/XXXXSECRETXXXX"})
	}
	// Slack and Teams take HTTPS only; the test servers speak HTTP.
	if _, err := w.st.SaveNotifications(ctx, in, Person{Key: "root", Name: "Root"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain HTTP for Slack: %v", err)
	}
	for i := range in.Channels {
		in.Channels[i].Kind = ChannelWebhook
	}
	v, err := w.st.SaveNotifications(ctx, in, Person{Key: "root", Name: "Root"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "XXXXSECRETXXXX") || len(v.Channels) != 3 || !v.Channels[0].URLSet || v.Link != "https://221b.example.com" {
		t.Fatalf("the view: %s", raw)
	}
	if strings.Contains(w.audit.String(), "XXXXSECRETXXXX") || !strings.Contains(w.audit.String(), "address set") {
		t.Fatalf("audit: %s", w.audit.String())
	}
	w.fire("pod:shop/web-7f9", true)
	p := restart("prod-1", "")
	p.Reasoning = "The reason, with the token sk-ant-api03-reasoningSecretValue1234567890"
	p.Evidence[0].Text = "evidence line EVIDENCE-TEXT"
	id, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	w.st.Wait()
	for kind, h := range hooks {
		got := h.got()
		if len(got) != 1 {
			t.Fatalf("%s: %d messages", kind, len(got))
		}
		body := got[0]
		for _, never := range []string{"reasoningSecretValue", "EVIDENCE-TEXT", "rollout restart", "The reason", "XXXXSECRETXXXX"} {
			if strings.Contains(body, never) {
				t.Errorf("%s: the message holds %q: %s", kind, never, body)
			}
		}
		for _, want := range []string{"Restart shop/web", "prod-1", "Troubleshooter", "https://221b.example.com/#/cases/inbox?proposal=" + id, "2 people"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the message lacks %q: %s", kind, want, body)
			}
		}
	}
	// A failing channel is said on the page and in the audit log, without
	// its address.
	hooks[ChannelWebhook].code = http.StatusForbidden
	chID := ""
	for _, ch := range v.Channels {
		if strings.HasSuffix(ch.Name, ChannelWebhook) {
			chID = ch.ID
		}
	}
	if err := w.st.TestChannel(ctx, chID, Person{Key: "root", Name: "Root"}); err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "XXXX") {
		t.Fatalf("a failing test: %v", err)
	}
	v, _ = w.st.Notifications(ctx)
	for _, ch := range v.Channels {
		if ch.ID == chID && (ch.LastError == "" || ch.LastAt == nil) {
			t.Fatalf("no failure shown: %+v", ch)
		}
	}
	if strings.Contains(w.audit.String(), "XXXXSECRETXXXX") {
		t.Fatal("the audit log holds an address")
	}
	// Kept as they were when saved again without addresses; on with none
	// is refused.
	keep := NotifyInput{Link: v.Link}
	for _, ch := range v.Channels {
		keep.Channels = append(keep.Channels, ChannelInput{Channel: ch.Channel})
	}
	if v2, err := w.st.SaveNotifications(ctx, keep, Person{Key: "root", Name: "Root"}); err != nil || !v2.Channels[1].URLSet {
		t.Fatalf("kept: %v %+v", err, v2)
	}
	if _, err := w.st.SaveNotifications(ctx, NotifyInput{Channels: []ChannelInput{{Channel: Channel{Kind: ChannelSlack, Name: "new", On: true}}}}, Person{Key: "root"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("on without an address: %v", err)
	}
	// A backup holds the placeholder, and its restore keeps the addresses.
	b, err := w.st.Export(ctx, (&backup.Env{}).Seal)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(b)
	if strings.Contains(string(raw), "XXXXSECRETXXXX") || b.Notify == nil || b.Notify.Channels[0].URL != backup.Placeholder || len(b.Cards) != 1 {
		t.Fatalf("backup: %s", raw)
	}
	fresh := newWorld(t)
	fresh.st.opts.Secrets = &backup.MemorySecret{}
	env := &backup.Env{}
	n, notes, err := fresh.st.Import(ctx, b, env.Open)
	if err != nil || n != 2 || len(notes) != 0 || len(env.ToSetAgain()) != 3 || !strings.HasPrefix(env.ToSetAgain()[0].Where, "Settings › Watson › Notifications › ") {
		t.Fatalf("restored %d, %v, %v, to set again %+v", n, notes, err, env.ToSetAgain())
	}
	if c, err := fresh.st.Get(ctx, id); err != nil || c.Title != "Restart shop/web" || c.History[0].What == "" {
		t.Fatalf("restored card %+v %v", c, err)
	}
}

// What each kind of channel is sent.
func TestEachChannelGetsItsOwnShape(t *testing.T) {
	m := message{Text: "A proposal waits <here> & there", Link: "https://x/#/cases/inbox?proposal=PC-1", Card: summary{ID: "PC-1", Title: "t"}}
	var slack struct{ Text string }
	_ = json.Unmarshal(payload(ChannelSlack, m, false), &slack)
	if slack.Text != "A proposal waits &lt;here&gt; &amp; there <https://x/#/cases/inbox?proposal=PC-1|Open in 221B>" {
		t.Errorf("slack %q", slack.Text)
	}
	var teams struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type    string `json:"type"`
				Actions []struct{ URL string }
			}
		}
	}
	_ = json.Unmarshal(payload(ChannelTeams, m, false), &teams)
	if teams.Type != "message" || teams.Attachments[0].ContentType != "application/vnd.microsoft.card.adaptive" || teams.Attachments[0].Content.Actions[0].URL != m.Link {
		t.Errorf("teams %+v", teams)
	}
	var hookBody struct {
		Event    string
		Link     string
		Proposal summary
	}
	_ = json.Unmarshal(payload(ChannelWebhook, m, false), &hookBody)
	if hookBody.Event != "proposal.waiting" || hookBody.Proposal.ID != "PC-1" || hookBody.Link != m.Link {
		t.Errorf("webhook %+v", hookBody)
	}
}
