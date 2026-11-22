package auth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/version"
)

// The console's version and build are told to people signed in, for the
// account menu's last line (RB-58), and never before sign-in: not on the
// sign-in page, not by /api/v1/me without a session, and not with sign-in
// off, where nobody is signed in.
func TestTheVersionIsToldToPeopleSignedInOnlyRB58(t *testing.T) {
	keep := [3]string{version.Version, version.Commit, version.Chart}
	t.Cleanup(func() { version.Version, version.Commit, version.Chart = keep[0], keep[1], keep[2] })
	version.Version, version.Commit, version.Chart = "v0.12.1-40-g9ef9683", "9ef9683a1b2c", "0.13.0"

	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{Mode: auth.ModeLocal, Local: []auth.LocalUser{{Email: "dev@corp.example", PasswordHash: hash}}, SessionIdle: 30 * time.Minute, SessionMax: 2 * time.Hour}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := console(t, svc)
	defer app.Close()
	c := client(t)

	// Before sign-in: the sign-in page and its files, and /api/v1/me, say nothing of it.
	for _, p := range []string{"/signin", "/signin.js", "/signin.css"} {
		resp, err := c.Get(app.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		if b := readAll(t, resp); strings.Contains(b, "0.13.0") || strings.Contains(b, "9ef9683") || strings.Contains(b, "0.12.1") {
			t.Fatalf("%s tells the version before sign-in: %s", p, b)
		}
	}
	resp, _ := c.Get(app.URL + "/api/v1/me")
	if b := readAll(t, resp); resp.StatusCode != http.StatusUnauthorized || strings.Contains(b, "0.13.0") || strings.Contains(b, "9ef9683") {
		t.Fatalf("/api/v1/me without a session: %s %s", resp.Status, b)
	}

	req, _ := http.NewRequest(http.MethodPost, app.URL+"/signin", strings.NewReader(url.Values{"email": {"dev@corp.example"}, "password": {"correct horse battery staple"}, "form": {formToken(t, c, app.URL)}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	var me auth.Me
	getJSON(t, c, app.URL+"/api/v1/me", &me)
	if me.User.Username != "dev@corp.example" || me.Version != "0.13.0" || me.Build != "9ef9683" {
		t.Fatalf("a build between releases: me %+v", me)
	}
	// A release is its version alone.
	version.Version = "v0.13.0"
	me = auth.Me{}
	getJSON(t, c, app.URL+"/api/v1/me", &me)
	if me.Version != "0.13.0" || me.Build != "" {
		t.Fatalf("a release: me %+v", me)
	}

	// Sign-in off: nobody is signed in, and the version is not told.
	none, err := auth.New(auth.Config{Mode: auth.ModeNone}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	open := console(t, none)
	defer open.Close()
	me = auth.Me{}
	getJSON(t, client(t), open.URL+"/api/v1/me", &me)
	if me.Mode != auth.ModeNone || me.Version != "" || me.Build != "" {
		t.Fatalf("sign-in off: me %+v", me)
	}
}
