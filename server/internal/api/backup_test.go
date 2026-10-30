package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
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
	in, _ := json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"settings": true, "users": true, "apps": true, "toolboxSessions": true, "sherlockSpend": true, "audit": true}})
	resp, body = send(t, c2, http.MethodPost, srv2.URL+"/api/v1/admin/restore", string(in))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the restore: %s %s", resp.Status, body)
	}
	var rep restoreReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Restored["settings"] != 1 || rep.Restored["apps"] != 1 || rep.Restored["toolboxSessions"] != 1 || rep.Restored["sherlockSpend"] != 1 || rep.Restored["audit"] == 0 {
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
