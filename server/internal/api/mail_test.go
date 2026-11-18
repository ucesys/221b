package api

import (
	"bytes"
	"context"
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
	"github.com/ucesys/221b/server/internal/mail"
	"github.com/ucesys/221b/server/internal/settings"
)

// Settings › Email is a SuperAdmin's (EM-01 to EM-04): the password is
// written, never read back; a test goes to the SuperAdmin's own address
// by default and turns email on; a change turns it off again.
func TestSettingsEmailIsASuperAdmins(t *testing.T) {
	src := &source{Source: fake.New("n1"), logs: map[string][]string{}}
	hash, _ := auth.HashPassword("correct horse battery staple")
	var auditOut bytes.Buffer
	log := audit.New(audit.Options{Out: &auditOut})
	svc, err := auth.New(auth.Config{
		Mode: auth.ModeLocal,
		Local: []auth.LocalUser{
			{Username: "adam", Name: "Adam", Email: "adam@corp.example", Roles: []string{"superViewer"}, PasswordHash: hash},
			{Username: "sam", Name: "Sam", Email: "sam@corp.example", Roles: []string{"superAdmin"}, PasswordHash: hash},
		},
		SessionIdle: 30 * time.Minute,
		SessionMax:  2 * time.Hour,
	}, quietLog(), log, nil)
	if err != nil {
		t.Fatal(err)
	}
	sent := &mail.Capture{}
	m := mail.New(mail.Options{Sender: sent, Audit: log, Logger: quietLog(), Link: func(context.Context) string { return "https://221b.example.com" }})
	shared := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quietLog(), nil)
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })
	srv := httptest.NewServer(New(Options{Store: live.New(src, live.Options{}), Cluster: src, Settings: shared, Auth: svc, Audit: log, UI: ui, Logger: quietLog(), Mail: m}))
	t.Cleanup(srv.Close)
	adam, sam := signIn(t, srv, "adam"), signIn(t, srv, "sam")
	const secret = "Sm7p-pa55word-0123"
	body := `{"host":"smtp.example.com","port":587,"security":"starttls","username":"221b","from":"221b@example.com","password":"` + secret + `"}`
	for _, req := range [][3]string{{http.MethodGet, "", ""}, {http.MethodPut, "", body}, {http.MethodPost, "/test", `{}`}} {
		if r, _ := send(t, adam, req[0], srv.URL+"/api/v1/settings/email"+req[1], req[2]); r.StatusCode != http.StatusForbidden {
			t.Errorf("an Admin %s %s: %d", req[0], req[1], r.StatusCode)
		}
	}
	r, b := send(t, sam, http.MethodPut, srv.URL+"/api/v1/settings/email", body)
	if r.StatusCode != 200 || strings.Contains(b, secret) || !strings.Contains(b, `"passwordSet":true`) || !strings.Contains(b, `"state":"notTested"`) || !strings.Contains(b, `"you":"sam@corp.example"`) {
		t.Fatalf("save: %d %s", r.StatusCode, b)
	}
	if r, b := send(t, sam, http.MethodPut, srv.URL+"/api/v1/settings/email", `{"host":"https://x","from":"221b@example.com"}`); r.StatusCode != http.StatusBadRequest || !strings.Contains(b, "host name") {
		t.Errorf("a bad host: %d %s", r.StatusCode, b)
	}
	r, b = send(t, sam, http.MethodPost, srv.URL+"/api/v1/settings/email/test", `{}`)
	if r.StatusCode != 200 || !strings.Contains(b, `"state":"on"`) || !strings.Contains(b, `"to":"sam@corp.example"`) {
		t.Fatalf("test: %d %s", r.StatusCode, b)
	}
	if got := sent.Sent(); len(got) != 1 || got[0].To != "sam@corp.example" {
		t.Fatalf("sent %+v", got)
	}
	r, b = send(t, sam, http.MethodPut, srv.URL+"/api/v1/settings/email", strings.Replace(body, `"password":"`+secret+`"`, `"password":""`, 1))
	if r.StatusCode != 200 || !strings.Contains(b, `"state":"on"`) {
		t.Fatalf("the same settings again: %d %s", r.StatusCode, b)
	}
	r, b = send(t, sam, http.MethodPut, srv.URL+"/api/v1/settings/email", strings.Replace(body, "587", "2525", 1))
	if r.StatusCode != 200 || !strings.Contains(b, `"state":"notTested"`) || strings.Contains(b, secret) {
		t.Fatalf("a new port: %d %s", r.StatusCode, b)
	}
	if strings.Contains(auditOut.String(), secret) || !strings.Contains(auditOut.String(), `"mail.settings"`) || !strings.Contains(auditOut.String(), `"mail.test"`) {
		t.Errorf("audit: %s", auditOut.String())
	}
}
