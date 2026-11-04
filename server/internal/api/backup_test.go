package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// A backup (asked October 30, 2026) holds the settings, the applications,
// the Toolbox's session records, Sherlock's spend and the audit log, and
// no credential; a restore into another console brings the chosen parts
// back and says what is left to do by hand.
func TestABackupHoldsNoSecretsAndRestores(t *testing.T) {
	ctx := context.Background()
	store, kvb, auditStore := apps.NewMemoryStore(), &kv.Memory{}, &audit.MemoryStore{}
	var shared Settings
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps, o.KV, o.Version = store, kvb, "0.10.2 (test)"
		o.Audit.SetStore(auditStore)
		shared = o.Settings
	})
	token := "ghp_verysecret"
	if err := store.Save(ctx, apps.Record{Cluster: clusters.LocalID, Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://github.com/example/shop", Ref: "main", RefKind: apps.RefBranch}, Values: "replicas: 2", Commands: []apps.Command{{Name: "scale", Params: []string{"replicas=5"}}}}, &token); err != nil {
		t.Fatal(err)
	}
	if _, err := kvb.SaveKey(ctx, sessions.HistoryKey, []byte(`[{"id":"s1","cluster":"local","tool":"shell","summary":"shell in web/shop","owner":"Ops","ended":"2026-10-30T10:00:00Z","endedBy":"owner"}]`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := kvb.SaveKey(ctx, sherlock.SpendKey, []byte(`{"day":"2026-10-30","dayUSD":1.5,"dayAsked":3,"month":"2026-10","monthUSD":9,"monthAsked":20}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := kvb.SaveKey(ctx, nodessh.PairingsKey, []byte(`{"local/a":{"cluster":"local","node":"a","loginUser":"ops","port":22,"hostKeys":["`+hostKeyLine(t)+`"]}}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Update(ctx, settings.Settings{RefreshSeconds: 7}); err != nil {
		t.Fatal(err)
	}

	// The first export is itself audited; the second carries that event.
	send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup", "")
	resp, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup", "")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), `attachment; filename="221b-backup-`) {
		t.Fatalf("the backup: %s %q", resp.Status, resp.Header.Get("Content-Disposition"))
	}
	for _, secret := range []string{token, "passwordHash", "correct horse", "\"token\""} {
		if strings.Contains(body, secret) {
			t.Fatalf("the backup holds %q:\n%s", secret, body)
		}
	}
	var f backupFile
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.SSHPairings), `"local/a"`) || !strings.Contains(strings.Join(f.LeftOut, "\n"), "SSH authority") {
		t.Fatalf("the pairings in the backup: %s %v", f.SSHPairings, f.LeftOut)
	}
	if f.Format != backupFormat || f.Version != "0.10.2 (test)" || f.By != "ops@corp.example" || len(f.LeftOut) == 0 {
		t.Fatalf("the header: %+v", f)
	}
	if f.Settings == nil || f.Settings.RefreshSeconds != 7 {
		t.Fatalf("the settings: %+v", f.Settings)
	}
	if len(f.Apps) != 1 || !f.Apps[0].HasToken || f.Apps[0].Values != "replicas: 2" || len(f.Apps[0].Commands) != 1 {
		t.Fatalf("the applications: %+v", f.Apps)
	}
	var spend sherlock.Spend
	_ = json.Unmarshal(f.SherlockSpend, &spend)
	if len(f.ToolboxSessions) != 1 || f.ToolboxSessions[0].ID != "s1" || spend.MonthUSD != 9 {
		t.Fatalf("the records: %+v %s", f.ToolboxSessions, f.SherlockSpend)
	}
	if len(f.Connections) == 0 || !f.Connections[0].Local {
		t.Fatalf("the connections: %+v", f.Connections)
	}
	if len(f.Audit) == 0 || f.Audit[0].Action != ActionBackup {
		t.Fatalf("the audit log: %+v", f.Audit)
	}

	// Into another console.
	store2, kvb2, auditStore2 := apps.NewMemoryStore(), &kv.Memory{}, &audit.MemoryStore{}
	var shared2 Settings
	srv2, c2, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps, o.KV = store2, kvb2
		o.Audit.SetStore(auditStore2)
		shared2 = o.Settings
	})
	f.Connections = append(f.Connections, clusters.Spec{ID: "staging", Name: "Staging", Server: "https://10.0.0.9:6443", Auth: clusters.AuthToken})
	in, _ := json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"settings": true, "users": true, "apps": true, "toolboxSessions": true, "sherlockSpend": true, "sshPairings": true, "audit": true}})
	resp, body = send(t, c2, http.MethodPost, srv2.URL+"/api/v1/admin/restore", string(in))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the restore: %s %s", resp.Status, body)
	}
	var rep restoreReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Restored["settings"] != 1 || rep.Restored["apps"] != 1 || rep.Restored["toolboxSessions"] != 1 || rep.Restored["sherlockSpend"] != 1 || rep.Restored["sshPairings"] != 1 || rep.Restored["audit"] == 0 {
		t.Fatalf("restored: %+v", rep.Restored)
	}
	notes := strings.Join(rep.Notes, "\n")
	if _, ok := rep.Restored["users"]; ok {
		t.Fatalf("users were restored where the backup had none (accounts from values are not backed up): %+v", rep.Restored)
	}
	for _, want := range []string{"repository token", "Staging (https://10.0.0.9:6443, token)"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("the notes lack %q:\n%s", want, notes)
		}
	}
	if shared2.Current().RefreshSeconds != 7 {
		t.Fatalf("the settings did not come back: %+v", shared2.Current().Settings)
	}
	got, tok, err := store2.Get(ctx, clusters.LocalID, "web", "shop")
	if err != nil || tok != "" || got.HasToken || got.Values != "replicas: 2" || len(got.Commands) != 1 {
		t.Fatalf("the application after the restore: %+v %q %v", got, tok, err)
	}
	if list, _ := auditStore2.List(ctx); len(list) == 0 {
		t.Fatal("the audit events did not come back")
	}
	// The same again adds nothing twice.
	resp, body = send(t, c2, http.MethodPost, srv2.URL+"/api/v1/admin/restore", string(in))
	_ = json.Unmarshal([]byte(body), &rep)
	if resp.StatusCode != http.StatusOK || rep.Restored["toolboxSessions"] != 0 || rep.Restored["audit"] != 0 {
		t.Fatalf("restoring twice: %s %+v", resp.Status, rep.Restored)
	}

	if resp, _ := send(t, c2, http.MethodPost, srv2.URL+"/api/v1/admin/restore", `{"backup":{"format":"nope"},"parts":{}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a file that is not a backup: %s", resp.Status)
	}
	v := signIn(t, srv, "viewer@corp.example")
	if resp, _ := send(t, v, http.MethodGet, srv.URL+"/api/v1/admin/backup", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer took a backup: %s", resp.Status)
	}
}

// accountsOnly is sign-in as far as restoring users needs it.
type accountsOnly struct {
	Auth
	accounts *auth.Accounts
}

func (a accountsOnly) Accounts() *auth.Accounts { return a.accounts }

// keptBackend keeps accounts as a console that remembers them does.
type keptBackend struct{ auth.Backend }

// A backup that has no name or email for a user says nothing about them:
// a restore keeps what the console has, and takes what the file does have.
func TestARestoreKeepsANameAndEmailTheFileDoesNotHave(t *testing.T) {
	ctx := context.Background()
	acc := auth.NewAccounts(keptBackend{&auth.MemoryBackend{}}, nil, nil, false, nil)
	for _, u := range []string{"anna", "ben"} {
		if _, err := acc.Create(ctx, auth.NewAccount{Username: u, Name: "Name of " + u, Email: u + "@corp.example", Password: "correct horse battery staple"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{opts: Options{Auth: accountsOnly{accounts: acc}}}
	rep := restoreReport{Restored: map[string]int{}}
	s.restoreUsers(httptest.NewRequest(http.MethodPost, "/api/v1/admin/restore", nil), []backupUser{{Username: "anna"}, {Username: "ben", Name: "Ben B", Email: "ben.b@corp.example"}}, &rep)
	if rep.Restored["users"] != 2 {
		t.Fatalf("restored %+v", rep)
	}
	if u, _ := acc.Find("anna"); u.Name != "Name of anna" || u.Email != "anna@corp.example" {
		t.Errorf("blanked: %+v", u)
	}
	if u, _ := acc.Find("ben"); u.Name != "Ben B" || u.Email != "ben.b@corp.example" {
		t.Errorf("not restored: %+v", u)
	}
}

// A file is no way around the settings pages: what they refuse, a restore
// refuses, and says so.
func TestARestoreHoldsItsSettingsToWhatTheSettingsPagesAccept(t *testing.T) {
	var shared Settings
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.SherlockProviderLocked = true
		shared = o.Settings
	})
	restore := func(in settings.Settings) restoreReport {
		t.Helper()
		b, _ := json.Marshal(restoreInput{Backup: backupFile{Format: backupFormat, Settings: &in}, Parts: map[string]bool{"settings": true}})
		resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/restore", string(b))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the restore: %s %s", resp.Status, body)
		}
		var rep restoreReport
		if err := json.Unmarshal([]byte(body), &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	sherlockWith := func(p *sherlock.Provider) *settings.Sherlock {
		sh := settings.DefaultSherlock
		sh.Provider = p
		return &sh
	}
	for name, c := range map[string]struct {
		in   settings.Settings
		note string
	}{
		"a refresh out of range":     {settings.Settings{RefreshSeconds: 100000}, "refreshSeconds"},
		"Watson's limit exceeded":    {settings.Settings{RefreshSeconds: 9, Watson: &settings.Watson{ApproveAllMinutes: settings.MaxApproveAll + 1}}, "approveAllMinutes"},
		"a provider the chart fixes": {settings.Settings{RefreshSeconds: 9, Sherlock: sherlockWith(&sherlock.Provider{Kind: sherlock.KindOpenAI, Endpoint: "https://models.example/v1"})}, "sherlock.lockProvider"},
	} {
		rep := restore(c.in)
		if _, done := rep.Restored["settings"]; done || !strings.Contains(strings.Join(rep.Notes, "\n"), c.note) {
			t.Errorf("%s: %+v", name, rep)
		}
		if shared.Current().RefreshSeconds == c.in.RefreshSeconds {
			t.Errorf("%s: the settings were applied", name)
		}
	}
	if rep := restore(settings.Settings{RefreshSeconds: 9, Watson: &settings.Watson{ApproveAllMinutes: 30}, Sherlock: sherlockWith(nil)}); rep.Restored["settings"] != 1 || shared.Current().WatsonOrDefault().ApproveAllMinutes != 30 {
		t.Fatalf("good settings: %+v", rep)
	}
}

// A restore that changes the caps or the two-person rule says so in the
// audit log, in records of their own.
func TestARestoreNamesWhatItChangesOfSherlockAndWatson(t *testing.T) {
	srv, c, _, aud, _, _ := multiSetupWith(t, "ops@corp.example", nil)
	sh := settings.DefaultSherlock
	sh.DailyUSD, sh.MonthlyUSD = 1, 2
	b, _ := json.Marshal(restoreInput{Backup: backupFile{Format: backupFormat, Settings: &settings.Settings{RefreshSeconds: 9, Sherlock: &sh, Watson: &settings.Watson{TwoPerson: true}}}, Parts: map[string]bool{"settings": true}})
	if r, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/restore", string(b)); r.StatusCode != http.StatusOK {
		t.Fatalf("restore: %d %s", r.StatusCode, body)
	}
	details := map[string]string{}
	for _, e := range auditEvents(t, aud.String()) {
		details[e.Action] = e.Detail
	}
	if d := details[audit.ActionSherlockSettings]; !strings.Contains(d, "daily cap") || !strings.Contains(d, "monthly cap") {
		t.Errorf("Sherlock's record: %q", d)
	}
	if d := details[ActionWatsonSettings]; !strings.Contains(d, "two-person rule on") {
		t.Errorf("Watson's record: %q", d)
	}
}

// hostKeyLine is a host key as a pairing keeps it.
func hostKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// A restore holds what it brings back to what making it here accepts: an
// application or a pairing that would be refused is left out and named.
// Audit events say they were restored. Automation comes back with every
// script a draft to review again, and fleet parameters that apply where a
// change would.
func TestARestoreChecksWhatItBringsBack(t *testing.T) {
	ctx := context.Background()
	key := hostKeyLine(t)
	srcStore := automation.NewStore(&kv.Memory{}, nil)
	ann, sam := automation.Who{Name: "Ann", User: "ann"}, automation.Who{Name: "Sam", User: "sam"}
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho hi\n"
	if _, err := srcStore.Save(ctx, "hello", "ops", text, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := srcStore.Submit(ctx, "hello", 1, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := srcStore.Approve(ctx, "hello", 1, sam); err != nil {
		t.Fatal(err)
	}
	if err := srcStore.SetVariables(ctx, clusters.LocalID, "_all", automation.FleetVariables{Values: map[string]string{"MODE": "safe"}}); err != nil {
		t.Fatal(err)
	}
	if err := srcStore.SetProfile(ctx, automation.RunProfile{ID: "deployer", Name: "deployer", Kind: automation.ProfileKubernetes, Namespace: "ops", ServiceAccount: "deployer"}); err != nil {
		t.Fatal(err)
	}
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Automation, o.AutomationStore = automation.NewEngine(automation.Options{Store: srcStore, Exec: noExec{}}), srcStore
	})
	_, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup", "")
	var f backupFile
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatal(err)
	}
	if f.Automation == nil || len(f.Automation.Scripts) != 1 || len(f.Automation.Profiles) != 1 || f.Automation.Variables[clusters.LocalID]["_all"].Values["MODE"] != "safe" {
		t.Fatalf("Automation in the backup: %+v", f.Automation)
	}

	f.Apps = []apps.Record{
		{Cluster: clusters.LocalID, Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://github.com/example/shop", Ref: "main", RefKind: apps.RefBranch}},
		{Cluster: clusters.LocalID, Namespace: "web", Name: "odd", Source: apps.Source{Repo: "https://github.com/example/odd", Ref: "main", RefKind: apps.RefBranch}, ValuesFile: "../../elsewhere.yaml"},
		{Cluster: clusters.LocalID, Namespace: "web", Name: "cmd", Source: apps.Source{Repo: "https://github.com/example/cmd", Ref: "main", RefKind: apps.RefBranch}, Commands: []apps.Command{{Name: "go", Params: []string{"a=$(x)"}}}},
	}
	f.SSHPairings = json.RawMessage(`{"local/a":{"cluster":"local","node":"a","loginUser":"ops","port":22,"hostKeys":["` + key + `"]},` +
		`"local/b":{"cluster":"local","node":"b","loginUser":"Not A User","port":22,"hostKeys":["` + key + `"]},` +
		`"local/c":{"cluster":"local","node":"c","loginUser":"ops","port":22,"hostKeys":["not a key"]},` +
		`"gone/d":{"cluster":"gone","node":"d","loginUser":"ops","port":22,"hostKeys":["` + key + `"]}}`)
	f.Audit = []audit.Event{{Action: "settings.update", Outcome: "ok", Detail: "from the old console"}}

	appStore, kvb, auditStore := apps.NewMemoryStore(), &kv.Memory{}, &audit.MemoryStore{}
	dst := automation.NewStore(&kv.Memory{}, nil)
	srv2, c2, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps, o.KV = appStore, kvb
		o.Audit.SetStore(auditStore)
		o.Automation, o.AutomationStore = automation.NewEngine(automation.Options{Store: dst, Exec: noExec{}}), dst
	})
	in, _ := json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"apps": true, "sshPairings": true, "audit": true, "automation": true}})
	resp, body := send(t, c2, http.MethodPost, srv2.URL+"/api/v1/admin/restore", string(in))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the restore: %s %s", resp.Status, body)
	}
	var rep restoreReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(rep.Notes, "\n")
	if rep.Restored["apps"] != 1 || rep.Restored["sshPairings"] != 1 {
		t.Fatalf("restored %+v\n%s", rep.Restored, notes)
	}
	for _, want := range []string{"web/odd is not restored", "web/cmd is not restored", "local/b is not restored", "local/c is not restored", "gone/d is not restored", "drafts"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("the notes lack %q:\n%s", want, notes)
		}
	}
	events, _ := auditStore.List(ctx)
	found := false
	for _, e := range events {
		if e.Detail == "from the old console" {
			found = true
			if e.Restored == "" {
				t.Fatalf("a restored event reads as recorded here: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("the audit event did not come back")
	}
	scripts, _ := dst.Scripts(ctx)
	if len(scripts) != 1 || len(scripts[0].Versions) != 1 {
		t.Fatalf("scripts: %+v", scripts)
	}
	if v := scripts[0].Versions[0]; v.State != automation.StateDraft || len(v.Approvals) != 0 || v.Submitted != nil || v.Author.User == "ann" || scripts[0].Group != "ops" {
		t.Fatalf("the restored script carries its old review: %+v", v)
	}
	vars, _ := dst.Variables(ctx, clusters.LocalID)
	if vars["_all"].Values["MODE"] != "safe" {
		t.Fatalf("the parameters off production: %+v", vars)
	}
	if p, ok, _ := dst.Profile(ctx, "deployer"); !ok || p.ServiceAccount != "deployer" {
		t.Fatalf("the run profile: %+v %v", p, ok)
	}

	// On production the parameters wait for another SuperAdmin.
	prod := automation.NewStore(&kv.Memory{}, nil)
	srv3, c3, m3, _, local3, store3 := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Automation, o.AutomationStore = automation.NewEngine(automation.Options{Store: prod, Exec: noExec{}}), prod
	})
	if err := m3.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvProduction, Mode: clusters.ModeWrite}, local3, store3); err != nil {
		t.Fatal(err)
	}
	in, _ = json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"automation": true}})
	if resp, body := send(t, c3, http.MethodPost, srv3.URL+"/api/v1/admin/restore", string(in)); resp.StatusCode != http.StatusOK || !strings.Contains(body, "wait for another SuperAdmin") {
		t.Fatalf("on production: %s %s", resp.Status, body)
	}
	if vars, _ := prod.Variables(ctx, clusters.LocalID); len(vars) != 0 {
		t.Fatalf("parameters applied on production without approval: %+v", vars)
	}
	if waiting, _ := prod.WaitingChanges(ctx); len(waiting) != 1 || waiting[0].By.User == "" {
		t.Fatalf("waiting changes: %+v", waiting)
	}
}
