package auth_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
)

// ask makes a request with the headers given and says how it was answered.
func (e *env) ask(c *http.Client, path string, headers map[string]string) int {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.app.URL+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func idleEnv(t *testing.T) (*env, *http.Client) {
	t.Helper()
	e := newEnv(t, auth.Config{
		Mode:        auth.ModeLocal,
		Local:       []auth.LocalUser{{Email: "ops@corp.example", PasswordHash: hashOf(t, goodPassword)}},
		SessionIdle: 30 * time.Minute,
		SessionMax:  24 * time.Hour,
	})
	return e, e.mustSignIn("ops@corp.example", goodPassword)
}

// A tab left open: what the page asks by itself sees the session and its
// expiry, and does not keep it (P3-01).
func TestWhatThePageAsksByItselfDoesNotKeepASession(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"a marked request": {"X-221B-Background": "1"},
		"an event stream":  {"Accept": "text/event-stream"},
		"a websocket":      {"Connection": "Upgrade", "Upgrade": "websocket"},
	} {
		t.Run(name, func(t *testing.T) {
			e, c := idleEnv(t)
			for range 2 {
				*e.now = e.now.Add(14 * time.Minute)
				if code := e.ask(c, "/api/v1/snapshot", headers); code != http.StatusOK {
					t.Fatalf("a live session was answered %d", code)
				}
			}
			*e.now = e.now.Add(3 * time.Minute)
			if code := e.ask(c, "/api/v1/snapshot", headers); code != http.StatusUnauthorized {
				t.Fatalf("31 minutes of polling only: %d", code)
			}
		})
	}
}

// A person reading and clicking: every request not marked counts.
func TestARequestNotMarkedKeepsASession(t *testing.T) {
	e, c := idleEnv(t)
	for range 4 {
		*e.now = e.now.Add(20 * time.Minute)
		if code := e.ask(c, "/api/v1/me", nil); code != http.StatusOK {
			t.Fatalf("an active session was answered %d", code)
		}
	}
}

// Typing into a terminal reaches the server on an open connection, and
// counts as a request does.
func TestTypingInATerminalKeepsASession(t *testing.T) {
	e, c := idleEnv(t)
	sess := auth.Session{ID: sessionID(t, e, c)}
	for range 4 {
		*e.now = e.now.Add(20 * time.Minute)
		e.svc.Active(sess)
	}
	if code := e.ask(c, "/api/v1/snapshot", map[string]string{"X-221B-Background": "1"}); code != http.StatusOK {
		t.Fatalf("a person typing lost the session: %d", code)
	}
}

func sessionID(t *testing.T, e *env, c *http.Client) string {
	t.Helper()
	u, _ := url.Parse(e.app.URL)
	for _, k := range c.Jar.Cookies(u) {
		if k.Name == "221b_session" {
			return k.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

// An LDAP session's stream reads the groups again as they age; that is the
// console looking, not the person using it.
func TestAnOpenStreamReadingLDAPGroupsDoesNotKeepASession(t *testing.T) {
	e, _ := ldapEnvWith(t, func(c *auth.Config) { c.SessionIdle = 30 * time.Minute })
	c := e.mustSignIn("omar", goodPassword)
	sess := auth.Session{ID: sessionID(t, e, c)}
	for range 5 {
		*e.now = e.now.Add(5 * time.Minute)
		if _, ok := e.svc.Current(sess); !ok {
			t.Fatal("the session ended early")
		}
	}
	*e.now = e.now.Add(6 * time.Minute)
	if _, ok := e.svc.Current(sess); ok {
		t.Fatal("an open stream kept the session past its idle limit")
	}
}
