package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
)

// Star us on GitHub (CU-21): the dialog asks only the SuperAdmins the
// values name, once a session; Never show this again turns the setting
// off for everyone, audited; a SuperAdmin turns it on again in General.
func TestTheStarPromptAsksTheInstallersCU21(t *testing.T) {
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	hash, _ := auth.HashPassword("correct horse battery staple")
	var auditOut bytes.Buffer
	log := audit.New(audit.Options{Out: &auditOut})
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeLocal,
		Local: []auth.LocalUser{
			{Username: "adam", Name: "Adam", Groups: []string{"web-ops"}, PasswordHash: hash},
			{Username: "sam", Name: "Sam", Roles: []string{"superAdmin"}, PasswordHash: hash},
			{Username: "sue", Name: "Sue", Roles: []string{"superAdmin"}, PasswordHash: hash},
		},
		LocalGroups: []auth.LocalGroup{{Name: "web-ops", Role: roles.Admin, Namespaces: []string{"web"}}},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, quietLog(), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	// Sam is named in the values; Sue stands for a SuperAdmin made in the console.
	installer := func(sess auth.Session) bool { return sess.Admin && sess.User.Username == "sam" }
	srv := httptest.NewServer(New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Auth: svc, Audit: log, Logger: quietLog(), Installer: installer,
		UI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })}))
	t.Cleanup(srv.Close)
	prompt := func(c *http.Client) starPrompt {
		t.Helper()
		r, b := send(t, c, "GET", srv.URL+"/api/v1/star-prompt", "")
		var p starPrompt
		if r.StatusCode != http.StatusOK || json.Unmarshal([]byte(b), &p) != nil {
			t.Fatalf("the prompt: %d %s", r.StatusCode, b)
		}
		return p
	}
	sam, sue, adam := signIn(t, srv, "sam"), signIn(t, srv, "sue"), signIn(t, srv, "adam")
	first := prompt(sam)
	if !first.Show || len(first.Session) != 16 {
		t.Fatalf("the installer is not asked: %+v", first)
	}
	if again := prompt(signIn(t, srv, "sam")); !again.Show || again.Session == first.Session {
		t.Fatalf("another sign-in is another session: %+v, was %+v", again, first)
	}
	if p := prompt(sue); p.Show || p.Session != "" {
		t.Fatalf("a SuperAdmin made in the console is asked: %+v", p)
	}
	if p := prompt(adam); p.Show {
		t.Fatalf("an Admin is asked: %+v", p)
	}
	for _, c := range []*http.Client{adam, sue} {
		if r, _ := send(t, c, "POST", srv.URL+"/api/v1/star-prompt/never", "{}"); r.StatusCode != http.StatusForbidden {
			t.Fatalf("one not asked turns it off: %d", r.StatusCode)
		}
	}
	if r, b := send(t, sam, "POST", srv.URL+"/api/v1/star-prompt/never", "{}"); r.StatusCode != http.StatusOK {
		t.Fatalf("Never show this again: %d %s", r.StatusCode, b)
	}
	if shared.Current().AsksForStar() || prompt(sam).Show {
		t.Fatal("still asked after Never show this again")
	}
	if !strings.Contains(auditOut.String(), `"action":"settings.update"`) || !strings.Contains(auditOut.String(), "asking to star 221B on GitHub turned off: Never show this again") {
		t.Fatalf("audit: %s", auditOut.String())
	}
	if r, b := send(t, sam, "GET", srv.URL+"/api/v1/settings", ""); !strings.Contains(b, `"starPrompt":false`) {
		t.Fatalf("the settings say: %d %s", r.StatusCode, b)
	}
	// An Admin's save of the settings keeps it; a SuperAdmin turns it on in General.
	if r, _ := send(t, sue, "PUT", srv.URL+"/api/v1/settings", `{"refreshSeconds": 20}`); r.StatusCode != http.StatusOK || shared.Current().AsksForStar() {
		t.Fatalf("a save without it changed it: %d", r.StatusCode)
	}
	if r, b := send(t, sue, "PUT", srv.URL+"/api/v1/settings", `{"refreshSeconds": 20, "starPrompt": true}`); r.StatusCode != http.StatusOK {
		t.Fatalf("turned on again: %d %s", r.StatusCode, b)
	}
	if !prompt(sam).Show || !strings.Contains(auditOut.String(), "asking to star 221B on GitHub on") {
		t.Fatalf("on again: %s", auditOut.String())
	}
}
