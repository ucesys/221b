package cards

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// Notifications (ADR-102): a new card is announced on the channels a
// SuperAdmin sets up: a generic webhook, a Slack or a Microsoft Teams
// incoming webhook. A message carries a summary and a link to the card,
// never a secret, the agent's reasoning or evidence, the commands, or any
// output. A channel's address is itself a secret (whoever has it can
// post), so it is kept in a Secret and the page sees only whether one is
// set.

// Kinds of channel.
const (
	ChannelWebhook = "webhook"
	ChannelSlack   = "slack"
	ChannelTeams   = "teams"
)

// MaxChannels bounds the channels.
const MaxChannels = 10

// urlsKey is the Secret's key that holds the addresses, by channel.
const urlsKey = "notify-urls"

// Channel is one place new cards are announced.
type Channel struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	On   bool   `json:"on"`
}

// NotifySettings are the channels, and the console's address the links
// start with.
type NotifySettings struct {
	Link     string    `json:"link,omitempty"`
	Channels []Channel `json:"channels"`
}

// ChannelView is a channel as the page sees it: whether its address is
// set, never the address, and how the last message went on this replica.
type ChannelView struct {
	Channel
	URLSet    bool       `json:"urlSet"`
	LastAt    *time.Time `json:"lastAt,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

// NotifyView is the settings as the page sees them.
type NotifyView struct {
	Link     string        `json:"link"`
	Channels []ChannelView `json:"channels"`
	// InSecret says the addresses are kept in a Secret; false, in memory.
	InSecret bool `json:"inSecret"`
}

// ChannelInput is a channel as the page saves it: a new address, or the
// one kept, or none.
type ChannelInput struct {
	Channel
	URL      string `json:"url,omitempty"`
	ClearURL bool   `json:"clearUrl,omitempty"`
}

// NotifyInput is the settings as the page saves them.
type NotifyInput struct {
	Link     string         `json:"link"`
	Channels []ChannelInput `json:"channels"`
}

// NotifyBackup is the settings in a backup, each address the placeholder.
type NotifyBackup struct {
	Link     string          `json:"link,omitempty"`
	Channels []ChannelBackup `json:"channels,omitempty"`
}

// ChannelBackup is a channel in a backup.
type ChannelBackup struct {
	Channel
	URL string `json:"url,omitempty"`
}

type sentStatus struct {
	at  time.Time
	err string
}

// memorySecret keeps the addresses without a Secret.
type memorySecret struct {
	mu   sync.Mutex
	data map[string][]byte
	n    int
}

func (m *memorySecret) LoadKey(_ context.Context, key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	return append([]byte(nil), b...), fmt.Sprint(m.n), ok, nil
}

func (m *memorySecret) SaveKeyIf(_ context.Context, key string, b []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version != fmt.Sprint(m.n) {
		return errors.New("the addresses changed since they were read")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte(nil), b...)
	m.n++
	return nil
}

func (s *Store) secrets() SecretStore {
	if s.opts.Secrets != nil {
		return s.opts.Secrets
	}
	return &s.memSecrets
}

// urls reads the channels' addresses.
func (s *Store) urls(ctx context.Context) (map[string]string, string, error) {
	b, version, found, err := s.secrets().LoadKey(ctx, urlsKey)
	if err != nil {
		return nil, "", err
	}
	out := map[string]string{}
	if found && len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, "", fmt.Errorf("the channels' addresses cannot be read: %w", err)
		}
	}
	return out, version, nil
}

// Notifications are the settings as the page sees them.
func (s *Store) Notifications(ctx context.Context) (NotifyView, error) {
	set, _, err := loadJSON[NotifySettings](ctx, s.opts.Backend, NotifyKey)
	if err != nil {
		return NotifyView{}, err
	}
	urls, _, err := s.urls(ctx)
	if err != nil {
		return NotifyView{}, err
	}
	v := NotifyView{Link: set.Link, Channels: []ChannelView{}, InSecret: s.opts.Secrets != nil}
	for _, ch := range set.Channels {
		cv := ChannelView{Channel: ch, URLSet: urls[ch.ID] != ""}
		if st, ok := s.sent.Load(ch.ID); ok {
			x := st.(sentStatus)
			at := x.at
			cv.LastAt, cv.LastError = &at, x.err
		}
		v.Channels = append(v.Channels, cv)
	}
	return v, nil
}

// checkURL validates a channel's address: HTTPS, or HTTP for a webhook.
func checkURL(kind, raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || len(raw) > 2048:
		return fmt.Errorf("%w: the address is not a URL", ErrInvalid)
	case u.Scheme == "https":
	case u.Scheme == "http" && kind == ChannelWebhook:
	default:
		return fmt.Errorf("%w: a %s channel's address starts with https://", ErrInvalid, kind)
	}
	return nil
}

func newChannelID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "n" + hex.EncodeToString(b)
}

// SaveNotifications saves the channels: an address given replaces the
// one kept, ClearURL removes it, and a channel left out is removed with
// its address. Only whether an address was set reaches the audit log.
func (s *Store) SaveNotifications(ctx context.Context, in NotifyInput, by Person) (NotifyView, error) {
	in.Link = strings.TrimRight(strings.TrimSpace(in.Link), "/")
	if in.Link != "" {
		if u, err := url.Parse(in.Link); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return NotifyView{}, fmt.Errorf("%w: the console's address is its URL, such as https://221b.example.com", ErrInvalid)
		}
	}
	if len(in.Channels) > MaxChannels {
		return NotifyView{}, fmt.Errorf("%w: at most %d channels", ErrInvalid, MaxChannels)
	}
	urls, version, err := s.urls(ctx)
	if err != nil {
		return NotifyView{}, err
	}
	next := map[string]string{}
	set := NotifySettings{Link: in.Link, Channels: []Channel{}}
	var notes []string
	for _, ch := range in.Channels {
		ch.Name = strings.TrimSpace(ch.Name)
		ch.URL = strings.TrimSpace(ch.URL)
		switch {
		case ch.Kind != ChannelWebhook && ch.Kind != ChannelSlack && ch.Kind != ChannelTeams:
			return NotifyView{}, fmt.Errorf("%w: a channel is a webhook, slack or teams, not %q", ErrInvalid, ch.Kind)
		case ch.Name == "" || len(ch.Name) > 60:
			return NotifyView{}, fmt.Errorf("%w: give each channel a name of at most 60 characters", ErrInvalid)
		}
		if ch.ID == "" {
			ch.ID = newChannelID()
		}
		addr := urls[ch.ID]
		switch {
		case ch.URL != "":
			if err := checkURL(ch.Kind, ch.URL); err != nil {
				return NotifyView{}, fmt.Errorf("%s: %w", ch.Name, err)
			}
			addr = ch.URL
			notes = append(notes, ch.Name+": address set")
		case ch.ClearURL:
			addr = ""
			notes = append(notes, ch.Name+": address removed")
		}
		if ch.On && addr == "" {
			return NotifyView{}, fmt.Errorf("%w: give %s's address before turning it on", ErrInvalid, ch.Name)
		}
		if addr != "" {
			next[ch.ID] = addr
		}
		set.Channels = append(set.Channels, ch.Channel)
	}
	if !mapsEqual(urls, next) {
		b, _ := json.Marshal(next)
		if err := s.secrets().SaveKeyIf(ctx, urlsKey, b, version); err != nil {
			return NotifyView{}, fmt.Errorf("the channels' addresses were not saved: %w", err)
		}
	}
	s.mu.Lock()
	err = changeJSON(ctx, s.opts.Backend, NotifyKey, func(x *NotifySettings) error {
		*x = set
		return nil
	})
	s.mu.Unlock()
	if err != nil {
		return NotifyView{}, err
	}
	var names []string
	for _, ch := range set.Channels {
		names = append(names, fmt.Sprintf("%s (%s, on=%v)", ch.Name, ch.Kind, ch.On))
	}
	if s.opts.Audit != nil {
		detail := "channels: " + strings.Join(names, ", ")
		if len(names) == 0 {
			detail = "no channels"
		}
		if len(notes) > 0 {
			detail += "; " + strings.Join(notes, ", ")
		}
		s.opts.Audit.Record(audit.Event{Time: s.now(), Action: audit.ActionCardNotify, Who: audit.Who{Subject: by.Key, Name: by.Name}, Target: "notifications", Outcome: audit.OutcomeOK, Detail: detail})
	}
	return s.Notifications(ctx)
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// message is what a channel is told of a card: a summary and a link.
type message struct {
	Text string
	Link string
	Card summary
}

// summary is the card as a notification carries it.
type summary struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Cluster string    `json:"cluster"`
	Agent   string    `json:"agent"`
	Source  string    `json:"source,omitempty"`
	Needed  int       `json:"needed"`
	Expires time.Time `json:"expires"`
}

// messageFor is a card's announcement. The title and the source's label
// were scrubbed when the card was stored, and are again here; nothing
// else of the card is said.
func (s *Store) messageFor(c Card, link string) message {
	info, _ := s.opts.Cluster(c.Cluster)
	cluster := info.Name
	if cluster == "" {
		cluster = c.Cluster
	}
	sum := summary{ID: c.ID, Title: clean(c.Title, MaxTitle), Cluster: cluster, Agent: s.agentName(c.Agent), Source: clean(c.Source.Label, MaxTitle), Needed: c.Needed, Expires: c.Expires}
	people := "1 approval"
	if c.Needed > 1 {
		people = fmt.Sprintf("%d people's approval", c.Needed)
	}
	text := fmt.Sprintf("A proposal waits for approval on %s: %s. Proposed by %s", sum.Cluster, sum.Title, sum.Agent)
	if sum.Source != "" {
		text += ", " + sum.Source
	}
	text += fmt.Sprintf(". It needs %s and expires %s.", people, c.Expires.UTC().Format("2006-01-02 15:04 UTC"))
	m := message{Text: text, Card: sum}
	if link != "" {
		m.Link = link + "/#/cases/inbox?proposal=" + url.QueryEscape(c.ID)
	} else {
		m.Text += " Open Cases › Inbox in 221B."
	}
	return m
}

// slackEscape escapes what Slack's text reads as markup.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// payload is the request body for a kind of channel.
func payload(kind string, m message, test bool) []byte {
	event := "proposal.waiting"
	if test {
		event = "test"
	}
	var v any
	switch kind {
	case ChannelSlack:
		text := slackEscape(m.Text)
		if m.Link != "" {
			text += " <" + m.Link + "|Open in 221B>"
		}
		v = map[string]any{"text": text}
	case ChannelTeams:
		card := map[string]any{"type": "AdaptiveCard", "$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "version": "1.4",
			"body": []any{map[string]any{"type": "TextBlock", "text": m.Text, "wrap": true}}}
		if m.Link != "" {
			card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": "Open in 221B", "url": m.Link}}
		}
		v = map[string]any{"type": "message", "attachments": []any{map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": card}}}
	default:
		w := map[string]any{"event": event, "text": m.Text, "proposal": m.Card}
		if m.Link != "" {
			w["link"] = m.Link
		}
		v = w
	}
	b, _ := json.Marshal(v)
	return b
}

// send posts one message. The error names the answer's status, never the
// address.
func (s *Store) send(ctx context.Context, addr string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return errors.New("the address is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "221B")
	resp, err := s.opts.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("it did not answer in time")
		}
		return errors.New("it could not be reached")
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("it answered %d", resp.StatusCode)
	}
	return nil
}

// notify announces a new card on every channel that is on.
func (s *Store) notify(ctx context.Context, c Card) {
	set, _, err := loadJSON[NotifySettings](ctx, s.opts.Backend, NotifyKey)
	if err != nil || !slices.ContainsFunc(set.Channels, func(ch Channel) bool { return ch.On }) {
		return
	}
	urls, _, err := s.urls(ctx)
	if err != nil {
		s.opts.Logger.Warn("the notification channels' addresses could not be read", "err", err)
		return
	}
	m := s.messageFor(c, set.Link)
	for _, ch := range set.Channels {
		if !ch.On || urls[ch.ID] == "" {
			continue
		}
		s.deliver(ctx, ch, urls[ch.ID], payload(ch.Kind, m, false), c.ID)
	}
}

// deliver sends to one channel and keeps how it went.
func (s *Store) deliver(ctx context.Context, ch Channel, addr string, body []byte, about string) error {
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := s.send(sctx, addr, body)
	cancel()
	st := sentStatus{at: s.now()}
	if err != nil {
		st.err = err.Error()
		s.opts.Logger.Warn("a notification was not delivered", "channel", ch.Name, "err", err)
		if s.opts.Audit != nil {
			s.opts.Audit.Record(audit.Event{Time: s.now(), Action: audit.ActionCardNotifySent, Who: audit.Who{Name: "Watson"}, Target: "channel " + ch.Name, Outcome: audit.OutcomeFailed, Detail: fmt.Sprintf("%s (%s) about %s: %v", ch.Name, ch.Kind, about, err)})
		}
	}
	s.sent.Store(ch.ID, st)
	return err
}

// TestChannel sends a test message to a channel.
func (s *Store) TestChannel(ctx context.Context, id string, by Person) error {
	set, _, err := loadJSON[NotifySettings](ctx, s.opts.Backend, NotifyKey)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(set.Channels, func(ch Channel) bool { return ch.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	ch := set.Channels[i]
	urls, _, err := s.urls(ctx)
	if err != nil {
		return err
	}
	if urls[id] == "" {
		return fmt.Errorf("%w: %s has no address", ErrInvalid, ch.Name)
	}
	m := message{Text: "A test message from 221B, sent by " + by.Name + ": proposals that wait for approval are announced here."}
	if set.Link != "" {
		m.Link = set.Link + "/#/cases/inbox"
	}
	err = s.deliver(ctx, ch, urls[id], payload(ch.Kind, m, true), "a test")
	if s.opts.Audit != nil {
		outcome, detail := audit.OutcomeOK, "test message sent to "+ch.Name
		if err != nil {
			outcome, detail = audit.OutcomeFailed, "test message to "+ch.Name+": "+err.Error()
		}
		s.opts.Audit.Record(audit.Event{Time: s.now(), Action: audit.ActionCardNotifySent, Who: audit.Who{Subject: by.Key, Name: by.Name}, Target: "channel " + ch.Name, Outcome: outcome, Detail: detail})
	}
	return err
}
