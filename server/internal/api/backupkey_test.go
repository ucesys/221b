package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
)

const (
	testKeyA = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	testKeyB = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

// keyed is a registry with the install's backup key, as main makes one.
func keyed(text string) *backup.Registry {
	r := backup.NewRegistry()
	r.SetKey("221b-backup-key", func(context.Context) (*backup.SecretsKey, error) { return backup.ParseSecretsKey([]byte(text)) })
	return r
}

// ADR-137: a backup carries each secret sealed with the install's key. A
// console with the same key takes the connection's token and the
// application's repository token back; one with another key asks first
// (409), then restores the rest and lists every secret with where to set
// it. No secret and no key reaches the file in clear, the logs or the
// audit log.
func TestSecretsComeBackWithTheSameBackupKey(t *testing.T) {
	ctx := context.Background()
	secret := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJzeXN0ZW06c2VydmljZWFjY291bnQ6b3BzOmNvbnNvbGUifQ.c2ln"
	repoToken := "ghp_verysecret0123"
	store, auditStore := apps.NewMemoryStore(), &audit.MemoryStore{}
	srv, c, m, logs, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Backup, o.Apps = keyed(testKeyA), store
		o.Audit.SetStore(auditStore)
	})
	prod := clusters.Spec{ID: "prod", Name: "Prod", Env: clusters.EnvProduction, Server: "https://10.0.0.7:6443", CA: clusters.CASkip, Auth: clusters.AuthToken, Mode: clusters.ModeRead}
	if err := m.Store().Save(ctx, prod, &clusters.Credential{Token: secret}); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, apps.Record{Cluster: clusters.LocalID, Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://github.com/example/shop", Ref: "main", RefKind: apps.RefBranch}}, &repoToken); err != nil {
		t.Fatal(err)
	}
	resp, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the backup: %s", resp.Status)
	}
	body = unzip(t, body)
	fpA := keyed(testKeyA)
	k, _, _ := fpA.Key(ctx)
	for _, leak := range []string{secret, repoToken, testKeyA, clusters.Placeholder} {
		if strings.Contains(body, leak) {
			t.Fatalf("the backup holds %q:\n%s", leak, body)
		}
	}
	var f backupFile
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatal(err)
	}
	if f.Secrets == nil || f.Secrets.Key != k.Fingerprint() || len(f.Apps) != 1 || !backup.SealedValue(f.Apps[0].RepoToken) {
		t.Fatalf("the header %+v, the applications %+v", f.Secrets, f.Apps)
	}
	var sealed string
	for _, x := range f.Connections {
		if x.ID == "prod" && x.Credential != nil {
			sealed = x.Credential.Token
		}
	}
	if !backup.SealedValue(sealed) {
		t.Fatalf("Prod's token in the backup: %q", sealed)
	}
	_, parts := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup/parts", "")
	if !strings.Contains(parts, `"fingerprint":"`+k.Fingerprint()+`"`) || !strings.Contains(parts, `"secret":"221b-backup-key"`) || strings.Contains(parts, testKeyA) {
		t.Fatalf("the parts: %s", parts)
	}

	// The same key, on a console with neither.
	store2, audit2 := apps.NewMemoryStore(), &audit.MemoryStore{}
	srv2, c2, m2, logs2, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Backup, o.Apps = keyed(testKeyA), store2
		o.Audit.SetStore(audit2)
	})
	in, _ := json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"connections": true, "apps": true}})
	resp, out := send(t, c2, http.MethodPost, srv2.URL+"/api/v1/admin/restore", string(in))
	var rep restoreReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the restore: %s %s", resp.Status, out)
	}
	if rep.Restored["connections"] != 1 || rep.Restored["apps"] != 1 || len(rep.Secrets) != 0 {
		t.Fatalf("restored %+v, secrets %+v, notes %v", rep.Restored, rep.Secrets, rep.Notes)
	}
	if _, cred, err := m2.Store().Get(ctx, "prod"); err != nil || cred.Token != secret {
		t.Fatalf("Prod's token after the restore: %v", err)
	}
	if _, tok, err := store2.Get(ctx, clusters.LocalID, "web", "shop"); err != nil || tok != repoToken {
		t.Fatalf("the application's token after the restore: %v", err)
	}

	// Another key: asked first, then everything else, and the list.
	store3 := apps.NewMemoryStore()
	srv3, c3, m3, logs3, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Backup, o.Apps = keyed(testKeyB), store3
	})
	resp, out = send(t, c3, http.MethodPost, srv3.URL+"/api/v1/admin/restore", string(in))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(out, `"code":"backup-key"`) || !strings.Contains(out, "Restore the rest without them?") {
		t.Fatalf("another key, not asked: %s %s", resp.Status, out)
	}
	if _, ok := m3.Summary("prod"); ok {
		t.Fatal("a restore refused applied something")
	}
	in, _ = json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"connections": true, "apps": true}, WithoutSecrets: true})
	resp, out = send(t, c3, http.MethodPost, srv3.URL+"/api/v1/admin/restore", string(in))
	rep = restoreReport{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("another key, asked: %s %s", resp.Status, out)
	}
	if rep.Restored["connections"] != 1 || rep.Restored["apps"] != 1 || !hasSecret(rep.Secrets, "the token of the connection Prod", "Clusters › Prod › Edit connection") || !hasSecret(rep.Secrets, "the repository token of the application web/shop", "Applications › web/shop › Source") || rep.Secrets[0].Part != "Cluster connections" {
		t.Fatalf("restored %+v, secrets %+v", rep.Restored, rep.Secrets)
	}
	if _, cred, err := m3.Store().Get(ctx, "prod"); err != nil || cred.Token != clusters.Placeholder {
		t.Fatalf("Prod's token with another key: %q %v", cred.Token, err)
	}
	if _, tok, _ := store3.Get(ctx, clusters.LocalID, "web", "shop"); tok != "" {
		t.Fatal("the application's token with another key")
	}

	// Nothing secret in the logs or the audit log; the audit log names the
	// key by fingerprint.
	var details []string
	for _, st := range []*audit.MemoryStore{auditStore, audit2} {
		events, _ := st.List(ctx)
		for _, e := range events {
			details = append(details, e.Action+" "+e.Detail)
		}
	}
	all := strings.Join(details, "\n") + logs.String() + logs2.String() + logs3.String()
	for _, leak := range []string{secret, repoToken, testKeyA, testKeyB, sealed} {
		if strings.Contains(all, leak) {
			t.Fatalf("%q in the logs or the audit log:\n%s", leak, all)
		}
	}
	if !strings.Contains(all, "sealing=sealed fingerprint="+k.Fingerprint()) || !strings.Contains(all, "sealing=opened fingerprint="+k.Fingerprint()) {
		t.Fatalf("the audit log:\n%s", strings.Join(details, "\n"))
	}
}

// ADR-137: a local user restored with the backup's key gets their password
// back; with another key the user is created without one, and listed.
func TestAUserComesBackWithTheirPassword(t *testing.T) {
	ctx := context.Background()
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key  string
		same bool
	}{{testKeyA, true}, {testKeyB, false}} {
		acc := auth.NewAccounts(keptBackend{&auth.MemoryBackend{}}, nil, nil, false, nil)
		s := &Server{opts: Options{Auth: accountsOnly{accounts: acc}}}
		rep := restoreReport{Restored: map[string]int{}}
		users := func(text string) *backup.Registry {
			r := keyed(text)
			r.Register(backup.Part{Name: "users", Label: "Local users",
				Export: func(ctx context.Context, env *backup.Env) (any, error) {
					return []backupUser{{Username: "carl", Password: env.Seal(hash)}}, nil
				},
				Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
					var list []backupUser
					if err := json.Unmarshal(raw, &list); err != nil {
						return 0, err
					}
					s.restoreUsers(httptest.NewRequest(http.MethodPost, "/api/v1/admin/restore", nil), list, env, &rep)
					return rep.Restored["users"], nil
				}})
			return r
		}
		f, err := users(testKeyA).Export(ctx, &backup.Env{By: "ops"})
		if err != nil || strings.Contains(string(f.Parts["users"]), hash) {
			t.Fatalf("the backup: %s %v", f.Parts["users"], err)
		}
		out := users(tc.key).Restore(ctx, f, map[string]bool{"users": true}, &backup.Env{By: "ops"})
		u, ok := acc.Find("carl")
		if !ok || (u.PasswordHash == hash) != tc.same {
			t.Fatalf("key %s: carl %+v", tc.key[:4], ok)
		}
		if listed := hasSecret(out.Secrets, "the password of the user carl", "Administration › Users › carl › Set password"); listed == tc.same {
			t.Fatalf("key %s: secrets %+v", tc.key[:4], out.Secrets)
		}
	}
}
