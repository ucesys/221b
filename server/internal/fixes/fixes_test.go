package fixes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var olivia = Person{Subject: "olivia", Name: "Olivia"}

func setup() (*Service, *time.Time) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	s := New(Options{Now: func() time.Time { return now }})
	return s, &now
}

func save(t *testing.T, s *Service, c, title, key, steps string) Saved {
	t.Helper()
	out, err := s.SaveFromCase(context.Background(), FromCase{Cases: []string{c}, Title: title, Key: key, Steps: steps}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A person accepts one of two contradicting fixes: both are active and
// the pair is not flagged again; deleting one ends the other's
// contradiction (CF-51, CF-53).
func TestAcceptingAContradictionCF51(t *testing.T) {
	s, _ := setup()
	ctx := context.Background()
	save(t, s, "C-1", "Web has no limits", "pod.no-limits", "Raise the memory limit to 512Mi in the chart")
	got := save(t, s, "C-2", "Web has no limits", "pod.no-limits", "Move the app to the operator")
	if got.Fix.Status != Pending || len(got.Contradicts) != 1 {
		t.Fatalf("%+v", got)
	}
	active := Active
	f, err := s.Update(ctx, "F-2", Edit{Status: &active}, olivia)
	if err != nil || f.Status != Active || len(f.Contradicts) != 0 || f.Accepted[0] != "F-1" {
		t.Fatalf("%v %+v", err, f)
	}
	f1, _ := s.Get(ctx, "F-1")
	if len(f1.Contradicts) != 0 || f1.Accepted[0] != "F-2" {
		t.Fatalf("F-1: %+v", f1)
	}
	// Nothing else holds F-1: it is active again, and the housekeeping
	// leaves the pair.
	done, _ := s.Housekeep(ctx)
	if f1, _ = s.Get(ctx, "F-1"); f1.Status != Active || len(done) != 0 {
		t.Fatalf("F-1 after housekeeping: %+v %+v", f1, done)
	}
	// A third that contradicts both: pending with each.
	save(t, s, "C-3", "Web has no limits", "pod.no-limits", "Delete the namespace and start over")
	f3, _ := s.Get(ctx, "F-3")
	if f3.Status != Pending || len(f3.Contradicts) != 2 {
		t.Fatalf("F-3: %+v", f3)
	}
	if _, err := s.Delete(ctx, "F-3", olivia); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"F-1", "F-2"} {
		if f, _ := s.Get(ctx, id); f.Status != Active || len(f.Contradicts) != 0 {
			t.Errorf("%s: %+v", id, f)
		}
	}
	// Turned off by a person.
	off := Inactive
	if f, _ := s.Update(ctx, "F-1", Edit{Status: &off}, olivia); f.Status != Inactive || f.Reason != ReasonPerson {
		t.Errorf("off: %+v", f)
	}
	empty := ""
	if _, err := s.Update(ctx, "F-2", Edit{Title: &empty}, olivia); err == nil {
		t.Error("a fix without a title was taken")
	}
}

// The housekeeping keeps the fix that fixed more cases, and flags fixes of
// one problem that say otherwise (CF-53).
func TestHousekeepingFixesCF53(t *testing.T) {
	s, now := setup()
	ctx := context.Background()
	save(t, s, "C-1", "Ingress certificate expired", "", "Renew the certificate with cert-manager")
	*now = now.Add(time.Minute)
	save(t, s, "C-2", "Disk of db-1 is full", "", "Rotate the logs")
	save(t, s, "C-3", "Disk of db-1 is full", "", "Rotate the logs") // joins F-2
	f2, _ := s.Get(ctx, "F-2")
	if len(f2.Cases) != 2 {
		t.Fatalf("F-2: %+v", f2)
	}
	title := "Disk of db-1 is full"
	steps := "Rotate the logs on the node"
	s.Update(ctx, "F-1", Edit{Title: &title, Steps: &steps}, olivia)
	done, err := s.Housekeep(ctx)
	if err != nil || len(done) != 1 || done[0].ID != "F-1" || done[0].Of != "F-2" || done[0].What != ReasonDuplicate {
		t.Fatalf("%v %+v", err, done)
	}
	f1, _ := s.Get(ctx, "F-1")
	f2, _ = s.Get(ctx, "F-2")
	if f1.Status != Inactive || f1.DuplicateOf != "F-2" || len(f2.Cases) != 3 || f1.UpdatedBy != "Housekeeping" {
		t.Fatalf("F-1 %+v F-2 %+v", f1, f2)
	}
}

// The agents get the active fixes a text names, not pending or inactive
// ones (CF-52).
func TestMatchCF52(t *testing.T) {
	s, _ := setup()
	ctx := context.Background()
	save(t, s, "C-1", "Ingress certificate expired", "", "Renew it")
	save(t, s, "C-2", "Disk of db-1 is full", "", "Rotate the logs")
	hits, _ := s.Match(ctx, "the ingress certificate expired again on prod", 3)
	if len(hits) != 1 || hits[0].Fix.ID != "F-1" {
		t.Fatalf("%+v", hits)
	}
	off := Inactive
	s.Update(ctx, "F-1", Edit{Status: &off}, olivia)
	if hits, _ = s.Match(ctx, "the ingress certificate expired again", 3); len(hits) != 0 {
		t.Errorf("an inactive fix matched: %+v", hits)
	}
}

// Who reads the cases reads the fixes; who works them changes them.
func TestRoutesCF51(t *testing.T) {
	s, _ := setup()
	save(t, s, "C-1", "Ingress certificate expired", "", "Renew it")
	mux := http.NewServeMux()
	reader := false
	for _, rt := range s.Routes(func(*http.Request) (Caller, bool) {
		return Caller{Person: olivia, Read: true, Change: !reader}, true
	}) {
		mux.HandleFunc(rt.Pattern, rt.Handler)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	if rec := do("GET", "/api/v1/aiops/fixes", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mayChange":true`) || !strings.Contains(rec.Body.String(), `"F-1"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do("PATCH", "/api/v1/aiops/fixes/F-1", `{"steps":"Renew it with cert-manager"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), "cert-manager") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := do("PATCH", "/api/v1/aiops/fixes/F-9", `{"steps":"x"}`); rec.Code != 404 {
		t.Errorf("missing: %d", rec.Code)
	}
	reader = true
	if rec := do("DELETE", "/api/v1/aiops/fixes/F-1", ""); rec.Code != 403 {
		t.Errorf("a reader deleted: %d", rec.Code)
	}
}
