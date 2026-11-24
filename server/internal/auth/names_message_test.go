package auth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/roles"
)

// The roles review of November 24, 2026 (skipped, then asked for by the
// owner): the start-up errors for a local user's or group's name left out
// that a user name from values may have + and the length limits, so the
// message did not say what was wrong with a name too long. They now state
// the rules ValidUsername applies.
func TestTheStartUpErrorsStateTheNameRules(t *testing.T) {
	hash := hashOf(t, goodPassword)
	err := auth.ValidateLocalUsers([]auth.LocalUser{{Email: "ops!k8s@corp.example", PasswordHash: hash}})
	if err == nil {
		t.Fatal("a name with ! was taken")
	}
	for _, want := range []string{"local user ops!k8s@corp.example", "@ and +", "at most 254 characters", "letters (read in lowercase), digits, dot, dash, underscore"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the local user's error %q lacks %q", err, want)
		}
	}
	long := strings.Repeat("a", roles.MaxEmailUsername+1)
	if err := auth.ValidateLocalUsers([]auth.LocalUser{{Username: long, PasswordHash: hash}}); err == nil || !strings.Contains(err.Error(), "at most 254 characters") {
		t.Errorf("a user name of 255 characters: %v", err)
	}
	// What the message says is allowed is allowed.
	if err := auth.ValidateLocalUsers([]auth.LocalUser{{Email: "Ops+K8s@corp.example", PasswordHash: hash}}); err != nil {
		t.Errorf("a name the message allows was refused: %v", err)
	}

	group := strings.Repeat("g", roles.MaxUsername+1)
	_, err = auth.New(auth.Config{Mode: auth.ModeLocal, LocalGroups: []auth.LocalGroup{{Name: group, Role: roles.Viewer}}}, nil, nil, nil)
	if err == nil {
		t.Fatal("a group name of 65 characters was taken")
	}
	for _, want := range []string{"local group", "lowercase letters, digits, dot, dash and underscore", "at most 64 characters"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the local group's error %q lacks %q", err, want)
		}
	}
	_, err = auth.New(auth.Config{Mode: auth.ModeLocal, LocalGroups: []auth.LocalGroup{{Name: "ops", Role: roles.SuperAdmin}}}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "use viewer, admin or reporter") || strings.Contains(err.Error(), "characters") {
		t.Errorf("a group of a role it may not have: %v", err)
	}
}

// A user name made in the console, on the first-run page or the Users
// page, was refused with "use lowercase letters, digits, dot, dash and
// underscore", which a name too long or . or .. already follows: the
// message did not say what was wrong. It now states the rule
// ValidUsername applies (strict), with its 64 characters, as the start-up
// errors do (RB-16).
func TestTheConsoleNameErrorsStateTheRule(t *testing.T) {
	long := strings.Repeat("a", roles.MaxUsername+1)
	want := []string{"lowercase letters, digits, dot, dash and underscore", "at most 64 characters", "not . or .."}

	e := newEnv(t, localConfig(t))
	c := client(t)
	for _, name := range []string{long, ".."} {
		body := readAll(t, e.post(c, "/setup", url.Values{"form": {formIn(t, c, e.app.URL+"/setup")}, "token": {"s3cret"}, "username": {name}, "password": {goodPassword}, "again": {goodPassword}}))
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("the first-run page's error for %q lacks %q", name, w)
			}
		}
	}

	a, root := adminEnv(t)
	for _, name := range []string{long, "Alice"} {
		code, b := a.api(root, http.MethodPost, "/api/v1/admin/users", map[string]any{"username": name, "password": goodPassword})
		if code != http.StatusBadRequest {
			t.Errorf("creating %q: %d %s", name, code, b)
		}
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("the Users page's error for %q, %s, lacks %q", name, b, w)
			}
		}
	}
	// What the message allows is allowed: 64 characters.
	if code, b := a.api(root, http.MethodPost, "/api/v1/admin/users", map[string]any{"username": long[1:], "password": goodPassword}); code != http.StatusCreated {
		t.Errorf("a name of 64 characters: %d %s", code, b)
	}
}
