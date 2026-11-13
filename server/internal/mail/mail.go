// Package mail is Settings › Email (EM-01 to EM-04, ADR-158): the SMTP
// server the console sends email through, its password kept like the
// console's other credentials, and the gate that sends nothing until a
// test with the settings as they stand got through.
//
// The settings (host, port, security, user name, From address) are kept
// in the casework ConfigMap (ConfigKey), with the last test and a
// generation that every change of a field that reaches the server, the
// password included, moves on: email is on only while the last test
// passed at the current generation. The password is kept in the Secret
// <release>-cases (PasswordKey), beside the notification channels'
// addresses, never returned by the API, never logged and never in a
// backup or the audit log: the page sees only whether one is set.
//
// What is sent and to whom is the caller's (package casework, EM-05 to
// EM-07); this package checks the address, builds the message and talks
// SMTP, through a Sender a test replaces.
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/scrub"
)

// ConfigKey is the casework ConfigMap's key of Settings › Email;
// PasswordKey the Secret's key of the password.
const (
	ConfigKey          = "cases-mail.json"
	DefaultConfigBytes = 4 << 10
	PasswordKey        = "mail-password"
)

// How the connection to the server is protected.
const (
	// SecurityStartTLS connects in the clear and upgrades with STARTTLS,
	// which the server must offer (port 587).
	SecurityStartTLS = "starttls"
	// SecurityTLS connects over TLS from the start (port 465).
	SecurityTLS = "tls"
	// SecurityNone sends in the clear: a relay inside the network that
	// needs no sign-in (port 25).
	SecurityNone = "none"
)

// DefaultPort is a security's usual port.
func DefaultPort(security string) int {
	switch security {
	case SecurityTLS:
		return 465
	case SecurityNone:
		return 25
	}
	return 587
}

// The audit log's actions.
const (
	ActionSettings = "mail.settings"
	ActionTest     = "mail.test"
)

// Errors the API turns into answers.
var (
	ErrInvalid = errors.New("not valid")
	// ErrOff says email is not on: not configured, or not tested since
	// the settings changed.
	ErrOff = errors.New("email is off")
)

// The states the page shows.
const (
	StateNotConfigured = "notConfigured"
	StateNotTested     = "notTested"
	StateFailed        = "failed"
	StateOn            = "on"
)

// Test is the last test: who sent it, to what address, when, and how it
// went, at which generation of the settings.
type Test struct {
	At    time.Time `json:"at"`
	By    string    `json:"by"`
	To    string    `json:"to"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	Gen   int       `json:"gen"`
}

// Config is Settings › Email as kept, without the password.
type Config struct {
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Security string `json:"security,omitempty"`
	Username string `json:"username,omitempty"`
	From     string `json:"from,omitempty"`
	// Gen moves on with every change that reaches the server (the
	// password's too): a test counts only at the generation it was sent.
	Gen       int       `json:"gen"`
	Updated   time.Time `json:"updated,omitzero"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
	Tested    *Test     `json:"tested,omitempty"`
}

// Configured says the settings name a server and a From address.
func (c Config) Configured() bool { return c.Host != "" && c.From != "" }

// On says email is on: configured, and the last test, at this
// generation, got through.
func (c Config) On() bool {
	return c.Configured() && c.Tested != nil && c.Tested.OK && c.Tested.Gen == c.Gen
}

// State is how the page reads the settings.
func (c Config) State() string {
	switch {
	case !c.Configured():
		return StateNotConfigured
	case c.On():
		return StateOn
	case c.Tested != nil && c.Tested.Gen == c.Gen && !c.Tested.OK:
		return StateFailed
	}
	return StateNotTested
}

// View is Settings › Email as the page sees it: whether a password is
// set, never the password.
type View struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	From        string `json:"from"`
	PasswordSet bool   `json:"passwordSet"`
	// InSecret says the password is kept in a Secret; false, in memory
	// until the console restarts.
	InSecret bool `json:"inSecret"`
	// State is notConfigured, notTested, failed or on; Tested the last
	// test at the current settings, LastTest the last test whatever the
	// settings were then.
	State     string     `json:"state"`
	On        bool       `json:"on"`
	Tested    *Test      `json:"tested,omitempty"`
	LastTest  *Test      `json:"lastTest,omitempty"`
	Updated   *time.Time `json:"updated,omitempty"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	// Link is the console's address the emails' links start with, Settings
	// › Watson's (the notification channels'); "" when none is set.
	Link string `json:"link"`
	// LastSent is how the last email went on this replica.
	LastSent *Sent `json:"lastSent,omitempty"`
}

// Sent is how one email went.
type Sent struct {
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// Input is Settings › Email as the page saves them: a new password, the
// one kept, or none.
type Input struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Security      string `json:"security"`
	Username      string `json:"username"`
	From          string `json:"from"`
	Password      string `json:"password,omitempty"`
	ClearPassword bool   `json:"clearPassword,omitempty"`
}

// Person is who changes or tests the settings, as the audit log names
// them.
type Person struct {
	Subject string
	Email   string
	Name    string
	From    string
}

func (p Person) who() audit.Who { return audit.Who{Subject: p.Subject, Email: p.Email, Name: p.Name} }

func (p Person) label() string {
	if p.Name != "" {
		return p.Name
	}
	if p.Email != "" {
		return p.Email
	}
	return p.Subject
}

// SecretStore keeps the password: one key of a Secret.
type SecretStore interface {
	LoadKey(ctx context.Context, key string) ([]byte, string, bool, error)
	SaveKeyIf(ctx context.Context, key string, b []byte, version string) error
}

// Server is where and how to send, with the password: never logged,
// never returned.
type Server struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
}

// Address is host:port.
func (s Server) Address() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

// Message is one email to one person.
type Message struct {
	From    string
	To      string
	Subject string
	Text    string
	// HTML is the same in simple HTML; "" sends plain text alone.
	HTML string
}

// Sender sends a message through a server: SMTP, or a test's double.
type Sender interface {
	Send(ctx context.Context, srv Server, m Message) error
}

// Options configure the service.
type Options struct {
	// Backend keeps the settings: the casework ConfigMap; nil is memory.
	Backend kv.Backend
	// Secrets keeps the password; nil keeps it in memory.
	Secrets SecretStore
	// Sender sends; nil is SMTP.
	Sender Sender
	// Link is the console's address the links start with (Settings ›
	// Watson's); nil is none.
	Link   func(ctx context.Context) string
	Audit  *audit.Log
	Now    func() time.Time
	Logger *slog.Logger
}

// Service keeps Settings › Email and sends through it.
type Service struct {
	o   Options
	mem memorySecret
	// mu serialises this replica's saves and tests.
	mu       sync.Mutex
	sentMu   sync.Mutex
	lastSent *Sent
}

// New builds the service.
func New(o Options) *Service {
	if o.Backend == nil {
		o.Backend = &kv.Memory{}
	}
	if o.Sender == nil {
		o.Sender = SMTP{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Service{o: o}
}

func (s *Service) now() time.Time { return s.o.Now().UTC() }

func (s *Service) secrets() SecretStore {
	if s.o.Secrets != nil {
		return s.o.Secrets
	}
	return &s.mem
}

// memorySecret keeps the password without a Secret.
type memorySecret struct {
	mu   sync.Mutex
	data map[string][]byte
	n    int
}

func (m *memorySecret) LoadKey(_ context.Context, key string) ([]byte, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	return append([]byte(nil), b...), strconv.Itoa(m.n), ok, nil
}

func (m *memorySecret) SaveKeyIf(_ context.Context, key string, b []byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if version != strconv.Itoa(m.n) {
		return errors.New("the password changed since it was read")
	}
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	if b == nil {
		delete(m.data, key)
	} else {
		m.data[key] = append([]byte(nil), b...)
	}
	m.n++
	return nil
}

// load reads the settings.
func (s *Service) load(ctx context.Context) (Config, string, error) {
	var c Config
	b, version, err := s.o.Backend.LoadKey(ctx, ConfigKey)
	if errors.Is(err, kv.ErrNotFound) {
		return c, version, nil
	}
	if err != nil {
		return c, "", err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, "", fmt.Errorf("Settings › Email cannot be read: %w", err)
	}
	return c, version, nil
}

// change changes the settings over the version read, again when another
// replica wrote first.
func (s *Service) change(ctx context.Context, f func(*Config) error) (Config, error) {
	for try := 0; try < 10; try++ {
		c, version, err := s.load(ctx)
		if err != nil {
			return Config{}, err
		}
		if err := f(&c); err != nil {
			return Config{}, err
		}
		b, _ := json.Marshal(c)
		if _, err = s.o.Backend.SaveKey(ctx, ConfigKey, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return c, err
	}
	return Config{}, errors.New("Settings › Email kept changing")
}

func (s *Service) password(ctx context.Context) (string, string, error) {
	b, version, found, err := s.secrets().LoadKey(ctx, PasswordKey)
	if err != nil {
		return "", "", fmt.Errorf("the email password cannot be read: %w", err)
	}
	if !found {
		return "", version, nil
	}
	return string(b), version, nil
}

func (s *Service) link(ctx context.Context) string {
	if s.o.Link == nil {
		return ""
	}
	return strings.TrimRight(s.o.Link(ctx), "/")
}

// Settings are Settings › Email as the page sees them.
func (s *Service) Settings(ctx context.Context) (View, error) {
	c, _, err := s.load(ctx)
	if err != nil {
		return View{}, err
	}
	pw, _, err := s.password(ctx)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, c, pw != ""), nil
}

func (s *Service) view(ctx context.Context, c Config, pwSet bool) View {
	v := View{Host: c.Host, Port: c.Port, Security: c.Security, Username: c.Username, From: c.From, PasswordSet: pwSet, InSecret: s.o.Secrets != nil,
		State: c.State(), On: c.On(), LastTest: c.Tested, UpdatedBy: c.UpdatedBy, Link: s.link(ctx)}
	if v.Security == "" {
		v.Security = SecurityStartTLS
	}
	if v.Port == 0 {
		v.Port = DefaultPort(v.Security)
	}
	if c.Tested != nil && c.Tested.Gen == c.Gen {
		v.Tested = c.Tested
	}
	if !c.Updated.IsZero() {
		u := c.Updated
		v.Updated = &u
	}
	s.sentMu.Lock()
	if s.lastSent != nil {
		x := *s.lastSent
		v.LastSent = &x
	}
	s.sentMu.Unlock()
	return v
}

// oneLine is a field of one line: no line breaks, which would let it add
// headers or SMTP commands, at most n characters.
func oneLine(name, v string, n int) (string, error) {
	v = strings.TrimSpace(v)
	if strings.ContainsAny(v, "\r\n\x00") {
		return "", fmt.Errorf("%w: the %s is one line", ErrInvalid, name)
	}
	if utf8.RuneCountInString(v) > n {
		return "", fmt.Errorf("%w: the %s is at most %d characters", ErrInvalid, name, n)
	}
	return v, nil
}

// CheckAddress checks an email address: one address, name@domain, with no
// display name.
func CheckAddress(what, addr string) (string, error) {
	addr, err := oneLine(what, addr, 254)
	if err != nil {
		return "", err
	}
	a, err := mail.ParseAddress(addr)
	if err != nil || a.Address != addr || !strings.Contains(addr[strings.LastIndex(addr, "@")+1:], ".") && !strings.HasSuffix(addr, "@localhost") {
		return "", fmt.Errorf("%w: the %s is an email address, such as 221b@example.com", ErrInvalid, what)
	}
	return addr, nil
}

// checkHost checks the server's name: a host name or an IP address, no
// scheme, no port.
func checkHost(h string) (string, error) {
	h, err := oneLine("server", h, 253)
	if err != nil {
		return "", err
	}
	if h == "" {
		return "", nil
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.String(), nil
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("%w: the server is a host name or an IP address, such as smtp.example.com, without https:// or a port", ErrInvalid)
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return "", fmt.Errorf("%w: the server is a host name or an IP address, such as smtp.example.com, without https:// or a port", ErrInvalid)
			}
		}
	}
	return strings.ToLower(h), nil
}

// Validate checks settings as given and returns them as kept; password is
// the one they would send with.
func Validate(in Input, password string) (Config, error) {
	host, err := checkHost(in.Host)
	if err != nil {
		return Config{}, err
	}
	sec := strings.ToLower(strings.TrimSpace(in.Security))
	if sec == "" {
		sec = SecurityStartTLS
	}
	if sec != SecurityStartTLS && sec != SecurityTLS && sec != SecurityNone {
		return Config{}, fmt.Errorf("%w: security is starttls, tls or none", ErrInvalid)
	}
	port := in.Port
	if port == 0 {
		port = DefaultPort(sec)
	}
	if port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("%w: the port is 1 to 65535", ErrInvalid)
	}
	user, err := oneLine("user name", in.Username, 256)
	if err != nil {
		return Config{}, err
	}
	from := strings.TrimSpace(in.From)
	if from != "" {
		if from, err = CheckAddress("From address", from); err != nil {
			return Config{}, err
		}
	}
	if (host == "") != (from == "") {
		return Config{}, fmt.Errorf("%w: give both the server and the From address, or neither to turn email off", ErrInvalid)
	}
	if host == "" {
		// Email off: no server, and no password goes with it.
		return Config{Port: port, Security: sec, Username: user}, nil
	}
	if strings.ContainsAny(password, "\r\n\x00") || len(password) > 1024 {
		return Config{}, fmt.Errorf("%w: the password is one line of at most 1024 bytes", ErrInvalid)
	}
	if sec == SecurityNone && (user != "" || password != "") {
		return Config{}, fmt.Errorf("%w: without TLS the password would cross the network in the clear; pick STARTTLS or TLS, or leave the user name and password empty for a relay that needs no sign-in", ErrInvalid)
	}
	if password != "" && user == "" {
		return Config{}, fmt.Errorf("%w: a password goes with a user name", ErrInvalid)
	}
	return Config{Host: host, Port: port, Security: sec, Username: user, From: from}, nil
}

// Save saves Settings › Email: a password given replaces the one kept,
// ClearPassword removes it. A change of anything that reaches the server
// turns email off until a test passes again (EM-02). Only whether the
// password changed reaches the audit log.
func (s *Service) Save(ctx context.Context, in Input, by Person) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pw, version, err := s.password(ctx)
	if err != nil {
		return View{}, err
	}
	next := pw
	switch {
	case in.Password != "":
		next = in.Password
	case in.ClearPassword:
		next = ""
	}
	c, err := Validate(in, next)
	if err != nil {
		return View{}, err
	}
	if c.Host == "" {
		// Email off: the password goes with the server.
		next = ""
	}
	pwChanged := next != pw
	if pwChanged {
		var b []byte
		if next != "" {
			b = []byte(next)
		}
		if err := s.secrets().SaveKeyIf(ctx, PasswordKey, b, version); err != nil {
			return View{}, fmt.Errorf("the email password was not saved: %w", err)
		}
	}
	var said []string
	saved, err := s.change(ctx, func(x *Config) error {
		for _, f := range []struct {
			name     string
			was, now string
		}{{"server", x.Host, c.Host}, {"port", portText(x.Port), portText(c.Port)}, {"security", x.Security, c.Security}, {"user name", x.Username, c.Username}, {"From address", x.From, c.From}} {
			if f.was != f.now {
				said = append(said, fmt.Sprintf("%s %s", f.name, orNone(f.now)))
			}
		}
		if pwChanged {
			said = append(said, map[bool]string{true: "password set", false: "password removed"}[next != ""])
		}
		if len(said) > 0 {
			x.Gen++
		}
		x.Host, x.Port, x.Security, x.Username, x.From = c.Host, c.Port, c.Security, c.Username, c.From
		x.Updated, x.UpdatedBy = s.now(), by.label()
		return nil
	})
	if err != nil {
		return View{}, err
	}
	if len(said) > 0 {
		detail := "Settings › Email: " + strings.Join(said, ", ")
		if saved.Configured() {
			detail += "; email is off until a test passes"
		} else {
			detail += "; email is off"
		}
		s.record(audit.Event{Action: ActionSettings, Who: by.who(), From: by.From, Target: "email", Outcome: audit.OutcomeOK, Detail: detail})
	}
	return s.view(ctx, saved, next != ""), nil
}

func portText(p int) string {
	if p == 0 {
		return ""
	}
	return strconv.Itoa(p)
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

func (s *Service) record(e audit.Event) {
	if s.o.Audit == nil {
		return
	}
	e.Time = s.now()
	s.o.Audit.Record(e)
}

// server is where to send now, with the password, and the settings.
func (s *Service) server(ctx context.Context) (Server, Config, error) {
	c, _, err := s.load(ctx)
	if err != nil {
		return Server{}, Config{}, err
	}
	pw, _, err := s.password(ctx)
	if err != nil {
		return Server{}, Config{}, err
	}
	return Server{Host: c.Host, Port: c.Port, Security: c.Security, Username: c.Username, Password: pw}, c, nil
}

// Test sends a test email to an address with the settings as saved and
// keeps how it went: passed, it turns email on (EM-02). The error says
// why it failed, never the password.
func (s *Service) Test(ctx context.Context, to string, by Person) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	to = strings.TrimSpace(to)
	if to == "" {
		to = by.Email
	}
	to, err := CheckAddress("test address", to)
	if err != nil {
		return View{}, err
	}
	srv, c, err := s.server(ctx)
	if err != nil {
		return View{}, err
	}
	if !c.Configured() {
		return View{}, fmt.Errorf("%w: save a server and a From address first", ErrInvalid)
	}
	link := s.link(ctx)
	text := "This is a test email from 221B, sent by " + by.label() + " from Settings › Email.\n\n" +
		"It got through: from now on the people involved in a case are emailed when it is opened and when it changes.\n"
	if link != "" {
		text += "\nThe console: " + link + "/\n"
	} else {
		text += "\nThe emails' links need the console's address, set in Settings › Watson › Notifications.\n"
	}
	m := Message{From: c.From, To: to, Subject: "221B: test email", Text: text}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	sendErr := s.o.Sender.Send(sctx, srv, m)
	cancel()
	t := Test{At: s.now(), By: by.label(), To: to, OK: sendErr == nil, Gen: c.Gen}
	if sendErr != nil {
		t.Error = Reason(sendErr, srv.Password)
	}
	saved, err := s.change(ctx, func(x *Config) error {
		if x.Gen != c.Gen {
			return fmt.Errorf("%w: Settings › Email changed during the test; test again", ErrInvalid)
		}
		x.Tested = &t
		return nil
	})
	if err != nil {
		return View{}, err
	}
	outcome, detail := audit.OutcomeOK, "test email sent to "+to+"; email is on"
	if sendErr != nil {
		outcome, detail = audit.OutcomeFailed, "test email to "+to+" not sent: "+t.Error
	}
	s.record(audit.Event{Action: ActionTest, Who: by.who(), From: by.From, Target: "email", Outcome: outcome, Detail: detail})
	s.noteSent(t.At, t.Error)
	return s.view(ctx, saved, srv.Password != ""), nil
}

// On says email is on now (EM-02).
func (s *Service) On(ctx context.Context) bool {
	c, _, err := s.load(ctx)
	return err == nil && c.On()
}

// Link is the console's address the emails' links start with; "" for
// none.
func (s *Service) Link(ctx context.Context) string { return s.link(ctx) }

// Send sends one email when email is on, from the From address; ErrOff
// otherwise. The error never holds the password.
func (s *Service) Send(ctx context.Context, m Message) error {
	srv, c, err := s.server(ctx)
	if err != nil {
		return err
	}
	if !c.On() {
		return ErrOff
	}
	m.From = c.From
	if _, err := CheckAddress("recipient", m.To); err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = s.o.Sender.Send(sctx, srv, m)
	if err != nil {
		err = errors.New(Reason(err, srv.Password))
	}
	s.noteSent(s.now(), errText(err))
	return err
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) noteSent(at time.Time, e string) {
	s.sentMu.Lock()
	s.lastSent = &Sent{At: at, Error: e}
	s.sentMu.Unlock()
}

// Reason is an error as the page and the log say it: one line, at most
// 300 characters, never the password, nothing that looks like a
// credential.
func Reason(err error, password string) string {
	if err == nil {
		return ""
	}
	t := err.Error()
	if password != "" {
		t = strings.ReplaceAll(t, password, scrub.Redacted)
	}
	t = strings.Join(strings.Fields(scrub.Text(t)), " ")
	if utf8.RuneCountInString(t) > 300 {
		t = string([]rune(t)[:299]) + "…"
	}
	return t
}
