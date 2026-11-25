package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// A backup is no way around the two-person rule: an email in the
// deployment's admin list would make its user a SuperAdmin at once, so a
// restore leaves it out when another SuperAdmin could approve, as the
// Users page refuses it (the review of the auth package, finding 4).
func TestARestoreLeavesOutAnEmailInTheAdminList(t *testing.T) {
	ctx := context.Background()
	tp := twoperson.New(nil, nil)
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.New(auth.Config{
		Mode:      auth.ModeLocal,
		Admins:    []string{"ops@corp.example", "chief"},
		Local:     []auth.LocalUser{{Username: "root", PasswordHash: hash, Roles: []string{"superAdmin"}}},
		TwoPerson: tp,
		Accounts:  keptBackend{&auth.MemoryBackend{}},
	}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc := svc.Accounts()
	if _, err := acc.Create(ctx, auth.NewAccount{Username: "anna", Email: "anna@corp.example", Password: "correct horse battery staple"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{Auth: svc, TwoPerson: tp}}
	root := auth.Session{User: auth.User{Subject: "local:root", Username: "root"}, Method: "local", Admin: true}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/restore", nil).WithContext(auth.WithSession(ctx, root))
	rep := restoreReport{Restored: map[string]int{}}
	s.restoreUsers(r, []backupUser{{Username: "x", Email: "Ops@corp.example"}, {Username: "anna", Email: "ops@corp.example"}}, &backup.Env{}, &rep)
	if u, ok := acc.Find("x"); !ok || u.Email != "" {
		t.Errorf("x: %+v %v", u, ok)
	}
	if u, _ := acc.Find("anna"); u.Email != "anna@corp.example" {
		t.Errorf("anna: %+v", u)
	}
	if n := strings.Count(strings.Join(rep.Notes, "\n"), "admin list"); n != 2 {
		t.Errorf("the notes: %q", rep.Notes)
	}
}
