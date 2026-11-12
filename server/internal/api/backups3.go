package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/schedule"
)

// Scheduled backups to S3 (Phase 12, ADR-81): Administration › Backup's
// card. SuperAdmins only, like the rest of the page; every route runs on
// the leader (ADR-70), where the schedule runs.

func (s *Server) backupS3Routes() {
	s.handle("GET /api/v1/admin/backup/s3", "admin-backup-s3", s.getBackupS3)
	s.handle("PUT /api/v1/admin/backup/s3", "admin-backup-s3-update", s.putBackupS3)
	s.handle("POST /api/v1/admin/backup/s3/test", "admin-backup-s3-test", s.postBackupS3Test)
	s.handle("POST /api/v1/admin/backup/s3/run", "admin-backup-s3-run", s.postBackupS3Run)
	s.handle("POST /api/v1/admin/backup/s3/schedule", "admin-backup-s3-schedule", s.postBackupS3Schedule)
	s.handle("GET /api/v1/admin/backup/s3/objects", "admin-backup-s3-objects", s.getBackupS3Objects)
	s.handle("GET /api/v1/admin/backup/s3/object", "admin-backup-s3-object", s.getBackupS3Object)
}

// backupS3View is the card: the settings, which keys the Secret holds
// (never the keys), the schedule in words and its next time, and the runs.
type backupS3View struct {
	Settings backup.S3Settings `json:"settings"`
	Saved    bool              `json:"saved"`
	Keys     backup.KeysState  `json:"keys"`
	// Identity says whether the pod has an AWS identity of its own.
	Identity bool          `json:"identity"`
	Schedule string        `json:"schedule"`
	Next     time.Time     `json:"next,omitzero"`
	Running  bool          `json:"running"`
	Status   backup.Status `json:"status"`
}

// backupS3Input is a save or a test: the settings, and the keys typed
// ("Replace…"), each left as it is when absent.
type backupS3Input struct {
	Settings backup.S3Settings `json:"settings"`
	Keys     backup.KeysChange `json:"keys"`
}

// whoOf is the request's person as the audit log names them.
func whoOf(r *http.Request) audit.Who {
	sess, _ := auth.SessionFrom(r.Context())
	return audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}
}

func (s *Server) backupS3View(ctx context.Context) (backupS3View, error) {
	b := s.opts.BackupS3
	set, saved, err := b.Settings(ctx)
	if err != nil {
		return backupS3View{}, err
	}
	v := backupS3View{Settings: set, Saved: saved, Identity: b.IdentityAvailable(), Running: b.Running(), Next: b.Next(set)}
	if v.Keys, err = b.Keys(ctx); err != nil {
		return v, err
	}
	if sc, err := schedule.Parse(set.Schedule); err == nil {
		v.Schedule = sc.Describe()
	}
	v.Status, err = b.Status(ctx)
	return v, err
}

func (s *Server) getBackupS3(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	v, err := s.backupS3View(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	out, _ := json.Marshal(v)
	writeJSON(w, r, out)
}

func (s *Server) putBackupS3(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	var in backupS3Input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the settings are not valid JSON: "+err.Error())
		return
	}
	_, change, err := s.opts.BackupS3.Save(r.Context(), in.Settings, in.Keys, backupBy(r))
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.record(r, backup.ActionS3Settings, "backup", change)
	s.getBackupS3(w, r)
}

func (s *Server) postBackupS3Test(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	var in backupS3Input
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the settings are not valid JSON: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	steps := s.opts.BackupS3.Test(ctx, in.Settings, in.Keys)
	outcome, said := audit.OutcomeOK, make([]string, 0, len(steps))
	for _, st := range steps {
		mark := "ok"
		if !st.OK {
			mark, outcome = "failed: "+st.Detail, audit.OutcomeFailed
		}
		said = append(said, st.Name+" "+mark)
	}
	s.recordOutcome(r, backup.ActionS3Test, "backup", fmt.Sprintf("s3://%s/%s: %s", strings.TrimSpace(in.Settings.Bucket), strings.TrimSpace(in.Settings.Prefix), strings.Join(said, "; ")), outcome)
	out, _ := json.Marshal(map[string]any{"steps": steps})
	writeJSON(w, r, out)
}

// postBackupS3Run runs a backup now; it goes on if the page closes.
func (s *Server) postBackupS3Run(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()
	run, err := s.opts.BackupS3.RunNow(ctx, whoOf(r), fromAddr(r))
	switch {
	case errors.Is(err, backup.ErrRunning):
		jsonError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, _ := json.Marshal(run)
	writeJSON(w, r, out)
}

// postBackupS3Schedule reads a schedule back as the page edits it: in
// words and its next three times, or what is wrong with it.
func (s *Server) postBackupS3Schedule(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	var sp schedule.Spec
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&sp); err != nil {
		jsonError(w, http.StatusBadRequest, "the schedule is not valid JSON: "+err.Error())
		return
	}
	res := map[string]any{}
	if sc, err := schedule.Parse(sp); err != nil {
		res["error"] = err.Error()
	} else {
		res["description"] = sc.Describe()
		next, at := []time.Time{}, s.opts.Now()
		for i := 0; i < 3; i++ {
			if at = sc.Next(at); at.IsZero() {
				break
			}
			next = append(next, at)
		}
		res["next"] = next
	}
	out, _ := json.Marshal(res)
	writeJSON(w, r, out)
}

func (s *Server) getBackupS3Objects(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	list, err := s.opts.BackupS3.List(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	if list == nil {
		list = []backup.Listed{}
	}
	out, _ := json.Marshal(map[string]any{"backups": list})
	writeJSON(w, r, out)
}

// getBackupS3Object reads one backup from the bucket, opened when it is
// encrypted, for the page's restore: the same picking and the same
// restore as a file chosen from the computer.
func (s *Server) getBackupS3Object(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	key := r.URL.Query().Get("key")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	f, err := s.opts.BackupS3.Fetch(ctx, key)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, backup.ActionS3Read, "backup", "read "+key+" for a restore: "+countDetail(f))
	out, err := json.Marshal(f)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, out)
}
