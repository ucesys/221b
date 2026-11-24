package auth_test

import (
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
