package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/fleets"
)

func stepOf(v SetupView, id string) SetupStep {
	for _, st := range v.Steps {
		if st.ID == id {
			return st
		}
	}
	return SetupStep{}
}

// Getting started (GS-01 to GS-05): every step is worked out from the
// console as it is, links to its screen, and can be dismissed and brought
// back by a SuperAdmin only, each change audited; the dismissals go into
// a backup and come back from one.
func TestGettingStarted(t *testing.T) {
	var opts *Options
	srv, ops, _, auditOut, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { opts = o })
	viewer := signIn(t, srv, "viewer@corp.example")
	if r, _ := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/setup", ""); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer read Getting started: %d", r.StatusCode)
	}
	if r, _ := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/setup/dismiss", `{"steps":["fleets"],"dismiss":true}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer dismissed a step: %d", r.StatusCode)
	}
	var v SetupView
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/setup", "", 200, &v)
	if len(v.Steps) != 9 || v.Steps[0].ID != "signin" || v.Steps[8].ID != "backup" || v.Complete {
		t.Fatalf("%+v", v)
	}
	for _, st := range v.Steps {
		if st.Link == "" || st.Where == "" || st.Why == "" || st.Detail == "" || !strings.HasPrefix(st.Docs, "docs/getting-started.md#") {
			t.Errorf("step %s is missing its words or its link: %+v", st.ID, st)
		}
	}
	// The chart's local users sign in; the cluster is connected; nothing
	// else is set up yet.
	if st := stepOf(v, "signin"); st.State != StepDone {
		t.Errorf("sign-in: %+v", st)
	}
	for _, id := range []string{"provider", "agents", "knowledge", "jobs", "fleets", "hosts", "backup"} {
		if st := stepOf(v, id); st.State != StepOpen {
			t.Errorf("%s: %+v", id, st)
		}
	}
	if v.Next != "clusters" && v.Next != "provider" {
		t.Errorf("next: %q", v.Next)
	}
	// A fleet made ticks Fleets, once the view is read again.
	if _, err := opts.Settings.UpdateFleets(context.Background(), "local", []fleets.Fleet{{ID: "web", Name: "Web", Labels: []string{"role=web"}, Taints: []string{}}}); err != nil {
		t.Fatal(err)
	}
	opts = nil
	// A download is the first backup.
	if r, _ := send(t, ops, http.MethodGet, srv.URL+"/api/v1/admin/backup", ""); r.StatusCode != 200 {
		t.Fatalf("backup: %d", r.StatusCode)
	}
	// Dismissing answers the view at once; a step not there is refused.
	if r, _ := send(t, ops, http.MethodPost, srv.URL+"/api/v1/setup/dismiss", `{"steps":["nowhere"],"dismiss":true}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown step: %d", r.StatusCode)
	}
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/setup/dismiss", `{"steps":["knowledge","jobs","fleets"],"dismiss":true}`, 200, &v)
	if st := stepOf(v, "knowledge"); st.State != StepDismissed || st.DismissedBy == "" || st.DismissedAt == nil {
		t.Fatalf("knowledge: %+v", st)
	}
	if st := stepOf(v, "fleets"); st.State != StepDone {
		t.Fatalf("a dismissed step that is done shows done: %+v", st)
	}
	if st := stepOf(v, "backup"); st.State != StepDone || !strings.Contains(st.Detail, "first downloaded") {
		t.Fatalf("backup: %+v", st)
	}
	if v.Dismissed != 2 {
		t.Fatalf("dismissed: %+v", v)
	}
	log := auditOut.String()
	if !strings.Contains(log, `"setup.dismiss"`) || !strings.Contains(log, "step 5, Knowledge: connect Mycroft's sources, dismissed") || strings.Count(log, `"setup.dismiss"`) != 3 {
		t.Fatalf("the audit log: %s", log)
	}
	v = SetupView{}
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/setup/dismiss", `{"steps":["knowledge"],"dismiss":false}`, 200, &v)
	if st := stepOf(v, "knowledge"); st.State != StepOpen || st.DismissedBy != "" {
		t.Fatalf("brought back: %+v", st)
	}
	if !strings.Contains(auditOut.String(), "Knowledge: connect Mycroft's sources, brought back") {
		t.Fatal("bringing a step back is not audited")
	}
	// Sherlock reads the same steps in words.
	words := SetupText(v)
	if !strings.Contains(words, "Next: step") || !strings.Contains(words, "(#/admin/settings/knowledge)") || !strings.Contains(words, "5. Knowledge: connect Mycroft's sources: open.") {
		t.Fatalf("%s", words)
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "correct horse") {
		t.Fatal("a secret in Getting started")
	}
}

// The backup part keeps the dismissals and the first backup, and a
// restore brings them back, dropping a step that is not one.
func TestGettingStartedBackupPart(t *testing.T) {
	srv, ops, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil)
	s := srv.Config.Handler.(*Server)
	var v SetupView
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/setup/dismiss", `{"steps":["hosts"],"dismiss":true}`, 200, &v)
	ctx := context.Background()
	rec, _, err := s.loadSetup(ctx)
	if err != nil || rec.Dismissed["hosts"].By == "" {
		t.Fatalf("%+v %v", rec, err)
	}
	if err := s.changeSetup(ctx, func(r *setupRecord) bool { r.Dismissed = nil; return true }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(setupRecord{Dismissed: map[string]setupMark{"hosts": rec.Dismissed["hosts"], "gone": rec.Dismissed["hosts"]}})
	for _, p := range s.backups.Parts() {
		if p.Name != "gettingStarted" {
			continue
		}
		n, err := p.Restore(ctx, raw, &backup.Env{})
		if err != nil || n != 1 {
			t.Fatalf("restored %d: %v", n, err)
		}
	}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/setup", "", 200, &v)
	if st := stepOf(v, "hosts"); st.State != StepDismissed {
		t.Fatalf("hosts: %+v", st)
	}
}

// Sherlock's setup skill (GS-07 to GS-10): a SuperAdmin's Sherlock reads
// Getting started with setup_status while a step is open, and answers
// what is next from it; a viewer's never gets the skill, and it ends once
// every step is done or dismissed.
func TestSherlockSetupSkill(t *testing.T) {
	srv, ops, fake, _, svc := aiSetup(t)
	var o aiOverview
	aiJSON(t, ops, http.MethodPut, srv.URL+"/api/v1/ai/agents/sherlock", `{"sherlockClusters":["local"]}`, 200, &o)
	var st sherlockStatus
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock", "", 200, &st)
	if !st.Setup {
		t.Fatalf("the skill is off for a SuperAdmin with steps open: %+v", st)
	}
	fake.Reset()
	m := askAndRead(t, srv, ops, svc, "What's next?")
	if !strings.Contains(m.Text, "Next is step") || !strings.Contains(m.Text, "never into this chat") {
		t.Fatalf("the answer: %q", m.Text)
	}
	offered := func() bool {
		for _, req := range fake.Requests() {
			if strings.Contains(string(req), `"name":"setup_status"`) && strings.Contains(string(req), "Setting the console up.") {
				return true
			}
		}
		return false
	}
	if !offered() {
		t.Fatal("setup_status and the skill's instructions were not sent")
	}
	// A viewer's Sherlock has no skill.
	viewer := signIn(t, srv, "viewer@corp.example")
	st = sherlockStatus{}
	aiJSON(t, viewer, http.MethodGet, srv.URL+"/api/v1/sherlock", "", 200, &st)
	if st.Setup {
		t.Fatal("a viewer has the setup skill")
	}
	// Every step dismissed: the skill is gone.
	var v SetupView
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/setup/dismiss", `{"steps":["signin","clusters","provider","agents","knowledge","jobs","fleets","hosts","backup"],"dismiss":true}`, 200, &v)
	if !v.Complete || v.Open != 0 {
		t.Fatalf("%+v", v)
	}
	st = sherlockStatus{}
	aiJSON(t, ops, http.MethodGet, srv.URL+"/api/v1/sherlock", "", 200, &st)
	if st.Setup {
		t.Fatal("the skill stayed on with every step done or dismissed")
	}
	aiJSON(t, ops, http.MethodPost, srv.URL+"/api/v1/sherlock/new", "", 200, nil)
	fake.Reset()
	askAndRead(t, srv, ops, svc, "Why is web/shop slow?")
	if offered() {
		t.Fatal("the skill was offered after setup ended")
	}
}
