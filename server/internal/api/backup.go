package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Backup and restore (Administration › Backup, asked October 30, 2026):
// one JSON file with everything the console keeps of its own, settings
// and records alike, and never a credential; and the restore of the
// parts a SuperAdmin picks from such a file.

// backupFormat marks the file; a restore refuses any other.
const backupFormat = "221b-backup/1"

const (
	ActionBackup  = "backup.export"
	ActionRestore = "backup.restore"
)

// backupUser is a local user without the password hash.
type backupUser struct {
	Username   string     `json:"username"`
	Name       string     `json:"name,omitempty"`
	Email      string     `json:"email,omitempty"`
	Groups     []string   `json:"groups,omitempty"`
	Super      []roles.ID `json:"super,omitempty"`
	Disabled   bool       `json:"disabled,omitempty"`
	Created    time.Time  `json:"created"`
	LastSignIn time.Time  `json:"lastSignIn,omitzero"`
}

// backupFile is the file.
type backupFile struct {
	Format  string    `json:"format"`
	Version string    `json:"version"`
	Taken   time.Time `json:"taken"`
	By      string    `json:"by,omitempty"`
	// LeftOut names what a backup never holds.
	LeftOut []string `json:"leftOut"`
	// Settings are the shared settings: refresh, time format, fleets,
	// acknowledged alerts, Sherlock's and Watson's settings, grace, banners.
	Settings *settings.Settings `json:"settings,omitempty"`
	// Users are the local users made in the console; those from the
	// deployment's values are not here (they come with the values).
	Users []backupUser `json:"users,omitempty"`
	// Connections are the cluster connections as the page shows them,
	// without their credentials: for the record, and to add again by hand.
	Connections []clusters.Spec `json:"connections,omitempty"`
	// Apps are the Helm applications' records: source, values and
	// parameters as typed, commands, and what was deployed from where.
	Apps []apps.Record `json:"apps,omitempty"`
	// ToolboxSessions are the records of ended Toolbox sessions.
	ToolboxSessions []sessions.Record `json:"toolboxSessions,omitempty"`
	// SherlockSpend is the spend ledger, as kept.
	SherlockSpend json.RawMessage `json:"sherlockSpend,omitempty"`
	// Audit is the audit log the console kept, newest first.
	Audit []audit.Event `json:"audit,omitempty"`
}

var backupLeftOut = []string{
	"the passwords of local users (their hashes stay in the console's Secret; a restored user gets a new password on the Users page)",
	"the credentials of cluster connections (tokens, kubeconfigs, certificates, OIDC secrets): a restore lists the connections to add again",
	"the repository tokens of applications",
	"Sherlock's API key",
	"sign-in sessions",
	"Watson's cards and Sherlock's conversations, which live in memory until the console restarts",
	"the Toolbox's session recordings, which are in the transcript store",
}

func (s *Server) backupRoutes() {
	s.handle("GET /api/v1/admin/backup", "admin-backup", s.getBackup)
	s.handle("POST /api/v1/admin/restore", "admin-restore", s.postRestore)
}

// loadKV reads one key of the key-value storage into v; false when there
// is none or no storage.
func (s *Server) loadKV(ctx context.Context, key string, v any) (string, bool) {
	if s.opts.KV == nil {
		return "", false
	}
	b, version, err := s.opts.KV.LoadKey(ctx, key)
	if err != nil {
		return version, false
	}
	return version, json.Unmarshal(b, v) == nil
}

func (s *Server) buildBackup(ctx context.Context, by string) (backupFile, error) {
	f := backupFile{Format: backupFormat, Version: s.opts.Version, Taken: s.opts.Now().UTC(), By: by, LeftOut: backupLeftOut}
	if s.opts.Settings != nil {
		st := s.opts.Settings.Current().Settings
		f.Settings = &st
	}
	if acc := s.accounts(); acc != nil {
		{
			for _, u := range acc.List() {
				if u.Managed {
					continue
				}
				f.Users = append(f.Users, backupUser{Username: u.Username, Name: u.Name, Email: u.Email, Groups: u.Groups, Super: u.Super, Disabled: u.Disabled, Created: u.Created, LastSignIn: u.LastSignIn})
			}
		}
	}
	if s.opts.Clusters != nil {
		for _, sum := range s.opts.Clusters.List() {
			f.Connections = append(f.Connections, sum.Spec)
		}
	}
	if s.opts.Apps != nil {
		recs, err := s.opts.Apps.List(ctx)
		if err != nil {
			return f, fmt.Errorf("reading the applications: %w", err)
		}
		f.Apps = recs
	}
	if s.opts.KV != nil {
		var list []sessions.Record
		if _, ok := s.loadKV(ctx, sessions.HistoryKey, &list); ok {
			f.ToolboxSessions = list
		}
		var spend json.RawMessage
		if _, ok := s.loadKV(ctx, sherlock.SpendKey, &spend); ok {
			f.SherlockSpend = spend
		}
	}
	if s.opts.Audit != nil {
		events, err := s.opts.Audit.Events(ctx)
		if err != nil {
			return f, fmt.Errorf("reading the audit log: %w", err)
		}
		f.Audit = events
	}
	return f, nil
}

func (s *Server) getBackup(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	by := sess.User.Email
	if by == "" {
		by = sess.User.Name
	}
	f, err := s.buildBackup(r.Context(), by)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	s.record(r, ActionBackup, "backup", fmt.Sprintf("users=%d connections=%d apps=%d sessions=%d audit=%d bytes=%d", len(f.Users), len(f.Connections), len(f.Apps), len(f.ToolboxSessions), len(f.Audit), len(b)))
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="221b-backup-`+f.Taken.Format("2006-01-02-150405")+`.json"`)
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// restoreInput is a backup file and the parts of it to apply.
type restoreInput struct {
	Backup backupFile `json:"backup"`
	// Parts: settings, users, apps, toolboxSessions, sherlockSpend, audit.
	Parts map[string]bool `json:"parts"`
}

// restoreReport says what a restore did.
type restoreReport struct {
	// Restored counts what each part applied.
	Restored map[string]int `json:"restored"`
	// Notes say what could not be applied, and what is left to do by hand.
	Notes []string `json:"notes"`
}

// randomPassword is a password nobody knows, for a user restored without
// one: a SuperAdmin sets a real one on the Users page.
func randomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) postRestore(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	var in restoreInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the backup is not valid JSON: "+err.Error())
		return
	}
	if in.Backup.Format != backupFormat {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("this is not a 221B backup (format %q, want %q)", in.Backup.Format, backupFormat))
		return
	}
	ctx := r.Context()
	rep := restoreReport{Restored: map[string]int{}, Notes: []string{}}
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	b := in.Backup

	if in.Parts["settings"] && b.Settings != nil {
		if s.opts.Settings == nil {
			note("settings: this console keeps no settings")
		} else if _, err := s.opts.Settings.Update(ctx, *b.Settings); err != nil {
			note("settings: %v", err)
		} else {
			rep.Restored["settings"] = 1
		}
	}

	if in.Parts["users"] && len(b.Users) > 0 {
		s.restoreUsers(ctx, b.Users, &rep)
	}

	if in.Parts["apps"] && len(b.Apps) > 0 {
		if s.opts.Apps == nil {
			note("applications: this console keeps none")
		} else {
			n := 0
			for _, rec := range b.Apps {
				if s.opts.Clusters != nil {
					if _, ok := s.opts.Clusters.Summary(rec.Cluster); !ok {
						note("application %s/%s: no connection %q here; add the connection first", rec.Namespace, rec.Name, rec.Cluster)
						continue
					}
				}
				cur, _, err := s.opts.Apps.Get(ctx, rec.Cluster, rec.Namespace, rec.Name)
				hadToken := rec.HasToken
				rec.HasToken = err == nil && cur.HasToken
				if hadToken && !rec.HasToken {
					note("application %s/%s: its repository token is not in the backup; paste it again in the source dialog", rec.Namespace, rec.Name)
				}
				if err := s.opts.Apps.Save(ctx, rec, nil); err != nil {
					note("application %s/%s: %v", rec.Namespace, rec.Name, err)
					continue
				}
				n++
			}
			rep.Restored["apps"] = n
		}
	}

	if in.Parts["toolboxSessions"] && len(b.ToolboxSessions) > 0 {
		if s.opts.KV == nil {
			note("toolbox sessions: this console has no storage for them")
		} else if n, err := s.mergeSessions(ctx, b.ToolboxSessions); err != nil {
			note("toolbox sessions: %v", err)
		} else {
			rep.Restored["toolboxSessions"] = n
		}
	}

	if in.Parts["sherlockSpend"] && len(b.SherlockSpend) > 0 {
		if s.opts.KV == nil {
			note("Sherlock's spend: this console has no storage for it")
		} else if _, err := s.opts.KV.SaveKey(ctx, sherlock.SpendKey, b.SherlockSpend, ""); err != nil {
			note("Sherlock's spend: %v", err)
		} else {
			if s.opts.Sherlock != nil {
				s.opts.Sherlock.Ledger().Reload()
			}
			rep.Restored["sherlockSpend"] = 1
		}
	}

	if in.Parts["audit"] && len(b.Audit) > 0 {
		if s.opts.Audit == nil || !s.opts.Audit.Stored() {
			note("audit log: this console keeps no audit log to add to")
		} else {
			have, err := s.opts.Audit.Events(ctx)
			if err != nil {
				note("audit log: %v", err)
			} else {
				seen := map[string]bool{}
				for _, e := range have {
					seen[auditKey(e)] = true
				}
				var add []audit.Event
				for _, e := range b.Audit {
					if !seen[auditKey(e)] {
						seen[auditKey(e)] = true
						add = append(add, e)
					}
				}
				rep.Restored["audit"] = s.opts.Audit.Import(add)
			}
		}
	}

	if len(b.Connections) > 0 {
		var missing []string
		for _, c := range b.Connections {
			if c.Local {
				continue
			}
			if s.opts.Clusters != nil {
				if _, ok := s.opts.Clusters.Summary(c.ID); ok {
					continue
				}
			}
			missing = append(missing, fmt.Sprintf("%s (%s, %s)", c.Name, c.Server, c.Auth))
		}
		if len(missing) > 0 {
			note("connections are never restored, since their credentials are not in a backup; add these again on the Clusters page: %s", strings.Join(missing, "; "))
		}
	}

	parts := []string{}
	for p := range in.Parts {
		if in.Parts[p] {
			parts = append(parts, p)
		}
	}
	sort.Strings(parts)
	detail := "parts=" + strings.Join(parts, ",") + " taken=" + b.Taken.Format(time.RFC3339) + " version=" + strconv.Quote(b.Version)
	for _, p := range parts {
		detail += " " + p + "=" + strconv.Itoa(rep.Restored[p])
	}
	s.record(r, ActionRestore, "backup", detail)
	out, _ := json.Marshal(rep)
	writeJSON(w, r, out)
}

// accounts returns the local accounts when sign-in keeps any.
func (s *Server) accounts() *auth.Accounts {
	if a, ok := s.opts.Auth.(interface{ Accounts() *auth.Accounts }); ok && s.opts.Auth != nil {
		return a.Accounts()
	}
	return nil
}

func auditKey(e audit.Event) string {
	return e.Time.UTC().Format(time.RFC3339Nano) + "|" + e.Action + "|" + e.Who.Subject + "|" + e.Target
}

// restoreUsers brings the local users back: those here are updated, the
// others created without a password.
func (s *Server) restoreUsers(ctx context.Context, users []backupUser, rep *restoreReport) {
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	acc := s.accounts()
	if acc == nil || !acc.Persistent() {
		note("users: this console keeps no local users of its own (sign-in is off, or they are not kept in a Secret)")
		return
	}
	n, created := 0, 0
	for _, u := range users {
		cur, exists := acc.Find(u.Username)
		if exists && cur.Managed {
			note("user %s is defined in the deployment's values and stays as it is", u.Username)
			continue
		}
		if !exists {
			if _, err := acc.Create(ctx, auth.NewAccount{Username: u.Username, Name: u.Name, Email: u.Email, Password: randomPassword(), Groups: u.Groups}); err != nil {
				note("user %s: %v", u.Username, err)
				continue
			}
			created++
			cur, _ = acc.Find(u.Username)
		} else {
			name, email, disabled := u.Name, u.Email, u.Disabled
			if _, _, err := acc.Update(ctx, u.Username, auth.Change{Name: &name, Email: &email, Disabled: &disabled}); err != nil {
				note("user %s: %v", u.Username, err)
				continue
			}
			var add, remove []string
			for _, g := range u.Groups {
				if !slices.Contains(cur.Groups, g) {
					add = append(add, g)
				}
			}
			for _, g := range cur.Groups {
				if !slices.Contains(u.Groups, g) {
					remove = append(remove, g)
				}
			}
			if len(add)+len(remove) > 0 {
				if _, err := acc.ChangeGroups(ctx, u.Username, add, remove); err != nil {
					note("user %s, groups: %v", u.Username, err)
				}
			}
		}
		if !slices.Equal(cur.Super, u.Super) {
			if _, err := acc.SetSuper(ctx, u.Username, u.Super); err != nil {
				note("user %s, super roles: %v", u.Username, err)
			}
		}
		if !exists && u.Disabled {
			disabled := true
			_, _, _ = acc.Update(ctx, u.Username, auth.Change{Disabled: &disabled})
		}
		n++
	}
	rep.Restored["users"] = n
	if created > 0 {
		note("%d users were created without a password; set each one's on the Users page before they sign in", created)
	}
}

// mergeSessions adds the backup's session records to those kept, by id.
func (s *Server) mergeSessions(ctx context.Context, add []sessions.Record) (int, error) {
	var err error
	for try := 0; try < 5; try++ {
		var list []sessions.Record
		raw, version, lerr := s.opts.KV.LoadKey(ctx, sessions.HistoryKey)
		if lerr != nil && !errors.Is(lerr, kv.ErrNotFound) {
			return 0, lerr
		}
		if lerr == nil {
			_ = json.Unmarshal(raw, &list)
		}
		have := map[string]bool{}
		for _, r := range list {
			have[r.ID] = true
		}
		n := 0
		for _, r := range add {
			if !have[r.ID] {
				have[r.ID] = true
				list = append(list, r)
				n++
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Ended.After(list[j].Ended) })
		if len(list) > 1000 {
			list = list[:1000]
		}
		b, _ := json.Marshal(list)
		if _, err = s.opts.KV.SaveKey(ctx, sessions.HistoryKey, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return n, err
	}
	return 0, err
}
