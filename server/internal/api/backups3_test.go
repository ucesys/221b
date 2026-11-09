package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/s3/s3test"
)

// The backup's placeholder is the connections'.
func TestOnePlaceholder(t *testing.T) {
	if backup.Placeholder != clusters.Placeholder {
		t.Fatalf("%q and %q", backup.Placeholder, clusters.Placeholder)
	}
}

// Administration › Backup's S3 card (ADR-81), through the API: the keys go
// in and never come out, the connection test, a run, the bucket's list,
// and a restore from the bucket through the same restore as a file, with
// a part another package registered; an encrypted file opens here.
func TestScheduledBackupsToS3ThroughTheAPI(t *testing.T) {
	key := awsv4.Credentials{AccessKeyID: "AKIABACKUP", SecretAccessKey: "backup-secret-value"}
	fake := s3test.New("backups", "eu-central-1", key)
	defer fake.Close()
	reg := backup.NewRegistry()
	jobs := map[string]string{"nightly": "0 3 * * *"}
	restored := map[string]string{}
	reg.Register(backup.Part{Name: "jobs", Label: "Jobs", How: "are added", Order: 300,
		Export: func(context.Context, *backup.Env) (any, error) { return jobs, nil },
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			if err := json.Unmarshal(raw, &restored); err != nil {
				return 0, err
			}
			return len(restored), nil
		}})
	kvb := &kv.Memory{}
	srv, c, _, auditOut, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		sched := backup.NewScheduler(backup.Options{Registry: reg, Store: kvb, Secrets: &backup.MemorySecret{}, Audit: o.Audit, HTTP: fake.Client(), Version: "0.12.0 (test)"})
		o.KV, o.Backup, o.BackupS3 = kvb, reg, sched
	})

	var v backupS3View
	_, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup/s3", "")
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.Saved || v.Settings.Retention.Count != 14 || v.Schedule != "Every day at 03:00, UTC" {
		t.Fatalf("before anything is saved: %s", body)
	}
	set := backup.S3Settings{Enabled: true, Bucket: "backups", Prefix: "prod/", Region: "eu-central-1", Endpoint: fake.URL, PathStyle: true, Credentials: backup.CredentialsKeys,
		Schedule: v.Settings.Schedule, Retention: backup.Retention{Mode: backup.KeepCount, Count: 5}, SSE: "AES256"}
	in, _ := json.Marshal(map[string]any{"settings": set, "keys": map[string]string{"accessKeyId": key.AccessKeyID, "secretAccessKey": key.SecretAccessKey}})
	steps := struct{ Steps []backup.Step }{}
	_, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/backup/s3/test", string(in))
	if err := json.Unmarshal([]byte(body), &steps); err != nil || len(steps.Steps) != 5 || !steps.Steps[4].OK {
		t.Fatalf("the test: %s", body)
	}
	resp, body := send(t, c, http.MethodPut, srv.URL+"/api/v1/admin/backup/s3", string(in))
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "backup-secret-value") || strings.Contains(body, "AKIABACKUP") || !strings.Contains(body, `"accessKey":true`) {
		t.Fatalf("the save: %s %s", resp.Status, body)
	}
	resp, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/backup/s3/schedule", `{"kind":"weekly","at":"02:15","days":["mon","fri"],"timeZone":"Europe/Berlin"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"description":"Every week on Monday and Friday at 02:15, Europe/Berlin"`) || strings.Count(body, "T") < 3 {
		t.Fatalf("the schedule in words: %s", body)
	}
	if _, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/backup/s3/schedule", `{"kind":"cron","cron":"* *"}`); !strings.Contains(body, "five fields") {
		t.Fatalf("a bad schedule: %s", body)
	}

	resp, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/backup/s3/run", "")
	var run backup.Run
	if err := json.Unmarshal([]byte(body), &run); err != nil || resp.StatusCode != http.StatusOK || !run.OK || run.By != "ops@corp.example" {
		t.Fatalf("run now: %s %s", resp.Status, body)
	}
	obj := fake.Objects()[run.Key]
	if obj.SSE != "AES256" || bytes.Contains(obj.Body, []byte("backup-secret-value")) {
		t.Fatalf("the object: %+v", obj.SSE)
	}
	var list struct{ Backups []backup.Listed }
	_, body = send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup/s3/objects", "")
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Backups) != 1 || list.Backups[0].Key != run.Key {
		t.Fatalf("the list: %s", body)
	}
	resp, body = send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup/s3/object?key="+run.Key, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"format":"221b-backup/2"`) || !strings.Contains(body, `"jobs":{"nightly":"0 3 * * *"}`) || !strings.Contains(body, `"settings":`) {
		t.Fatalf("the object for a restore: %s %s", resp.Status, body)
	}
	restore := `{"backup":` + body + `,"parts":{"jobs":true},"source":"s3://backups/` + run.Key + `"}`
	resp, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/restore", restore)
	var rep restoreReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil || resp.StatusCode != http.StatusOK || rep.Restored["jobs"] != 1 || restored["nightly"] != "0 3 * * *" {
		t.Fatalf("the restore from the bucket: %s %s", resp.Status, body)
	}
	_, body = send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup/parts", "")
	if !strings.Contains(body, `{"name":"jobs","label":"Jobs","how":"are added","restorable":true}`) || !strings.Contains(body, `"name":"settings"`) {
		t.Fatalf("the parts: %s", body)
	}

	// Encrypted on the console: the file in the bucket opens only here.
	set.Encrypt = true
	in, _ = json.Marshal(map[string]any{"settings": set, "keys": map[string]string{"passphrase": "a long and secret passphrase"}})
	if resp, body := send(t, c, http.MethodPut, srv.URL+"/api/v1/admin/backup/s3", string(in)); resp.StatusCode != http.StatusOK || strings.Contains(body, "a long and secret") {
		t.Fatalf("encryption on: %s %s", resp.Status, body)
	}
	_, body = send(t, c, http.MethodPost, srv.URL+"/api/v1/admin/backup/s3/run", "")
	_ = json.Unmarshal([]byte(body), &run)
	sealed := fake.Objects()[run.Key].Body
	if !strings.HasSuffix(run.Key, ".enc") || !backup.Sealed(sealed) {
		t.Fatalf("not sealed: %s", run.Key)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/admin/backup/open", bytes.NewReader(sealed))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := c.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("opening the sealed file: %v %v", res.Status, err)
	}
	res.Body.Close()

	log := auditOut.String()
	for _, want := range []string{`"action":"backup.s3.settings"`, "secret access key replaced", `"action":"backup.s3.test"`, `"action":"backup.s3.run"`, `"action":"backup.s3.read"`, `from=\"s3://backups/prod/`, `"action":"backup.open"`} {
		if !strings.Contains(log, want) {
			t.Errorf("the audit log lacks %s", want)
		}
	}
	for _, secret := range []string{"backup-secret-value", "a long and secret passphrase"} {
		if strings.Contains(log, secret) {
			t.Errorf("the audit log holds %q", secret)
		}
	}
	vw := signIn(t, srv, "viewer@corp.example")
	for _, p := range []string{"/api/v1/admin/backup/s3", "/api/v1/admin/backup/s3/objects", "/api/v1/admin/backup/parts"} {
		if resp, _ := send(t, vw, http.MethodGet, srv.URL+p, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a viewer read %s: %s", p, resp.Status)
		}
	}
}
