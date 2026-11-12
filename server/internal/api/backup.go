package api

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	tb "github.com/ucesys/221b/server/internal/toolbox"
)

// Backup and restore (Administration › Backup, asked October 30, 2026):
// one gzipped JSON file with everything the console keeps of its own,
// settings and records alike, where every secret is sealed with the
// install's backup key (ADR-137, asked November 9, 2026; before, and
// without a key, a placeholder, clusters.Placeholder); and the restore of
// the parts a SuperAdmin picks from such a file, which takes a secret back
// only where the console has none, and lists every secret it could not
// open to set again. Since Phase 12 the file is
// made of parts registered with package backup (ADR-80): the console's
// own are registered here, and every other package registers its own.

// backupFormat marks the file; a restore takes it and the first format,
// which left the secrets out rather than holding placeholders.
const (
	backupFormat   = backup.Format
	backupFormatV1 = backup.FormatV1
)

const (
	ActionBackup     = "backup.export"
	ActionRestore    = "backup.restore"
	ActionBackupOpen = "backup.open"
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
	// Password is the password's hash, sealed (ADR-137), or the
	// placeholder.
	Password string `json:"password,omitempty"`
}

// backupConnection is a connection and its credential: each secret in it
// sealed, or the placeholder.
type backupConnection struct {
	clusters.Spec
	Credential *backupCredential `json:"credential,omitempty"`
}

// backupCredential is clusters.Credential as a backup writes it, field
// for field, so the one converts to the other.
type backupCredential struct {
	Token            string `json:"token,omitempty"`
	Kubeconfig       string `json:"kubeconfig,omitempty"`
	Context          string `json:"context,omitempty"`
	CertPEM          string `json:"certPem,omitempty"`
	KeyPEM           string `json:"keyPem,omitempty"`
	CAPEM            string `json:"caPem,omitempty"`
	OIDCIssuer       string `json:"oidcIssuer,omitempty"`
	OIDCClientID     string `json:"oidcClientId,omitempty"`
	OIDCClientSecret string `json:"oidcClientSecret,omitempty"`
	OIDCRefreshToken string `json:"oidcRefreshToken,omitempty"`
	OIDCScopes       string `json:"oidcScopes,omitempty"`
}

// backupApp is an application's record; RepoToken is its repository
// token, sealed, or the placeholder.
type backupApp struct {
	apps.Record
	RepoToken string `json:"repoToken,omitempty"`
}

// backupAutomation is Automation's records: every script with its
// versions as kept, each cluster's fleets' parameters (a secret parameter
// is a reference, never a value), the run profiles one adds and the
// groups. A restore brings each script back as a draft of its newest
// version: approvals are given here, never by a file.
type backupAutomation struct {
	Scripts   []automation.Script                             `json:"scripts,omitempty"`
	Variables map[string]map[string]automation.FleetVariables `json:"variables,omitempty"`
	Profiles  []automation.RunProfile                         `json:"runProfiles,omitempty"`
	Groups    []automation.Group                              `json:"groups,omitempty"`
}

// restoreReport says what a restore did.
type restoreReport = backup.Report

// restoreRequest is a backup file and the parts of it to apply, and where
// the file came from, for the audit log (a file's name, or the S3 object).
//
// WithoutSecrets is the SuperAdmin's yes to restoring a file whose
// secrets this install's key cannot open (ADR-137): without it such a
// restore is refused, and applies nothing.
type restoreRequest struct {
	Backup         backup.File     `json:"backup"`
	Parts          map[string]bool `json:"parts"`
	Source         string          `json:"source,omitempty"`
	WithoutSecrets bool            `json:"withoutSecrets,omitempty"`
}

func (s *Server) backupRoutes() {
	s.registerBackupParts()
	s.handle("GET /api/v1/admin/backup", "admin-backup", s.getBackup)
	s.handle("GET /api/v1/admin/backup/parts", "admin-backup-parts", s.getBackupParts)
	s.handle("POST /api/v1/admin/backup/open", "admin-backup-open", s.postBackupOpen)
	s.handle("POST /api/v1/admin/restore", "admin-restore", s.postRestore)
	if s.opts.BackupS3 != nil {
		s.backupS3Routes()
	}
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

// sshAuthorityBackup is the SSH authority's key as a backup carries it,
// sealed (ADR-137); *nodessh.Service implements it.
type sshAuthorityBackup interface {
	AuthorityKey(ctx context.Context) ([]byte, error)
	RestoreAuthority(ctx context.Context, pem []byte) error
}

// restoreRequestKey carries the restoring request to the console's own
// parts, which record what they restore as its person.
type restoreRequestKey struct{}

func requestOf(ctx context.Context) *http.Request {
	r, _ := ctx.Value(restoreRequestKey{}).(*http.Request)
	return r
}

// fromReport hands a helper's report to the restore: its notes, and its
// count, or nothing applied.
func fromReport(env *backup.Env, rep restoreReport, part string) (int, error) {
	for _, n := range rep.Notes {
		env.Note("%s", n)
	}
	if n, ok := rep.Restored[part]; ok {
		return n, nil
	}
	return 0, backup.ErrNotApplied
}

// helper runs one of the restore helpers below for a part.
func helper(env *backup.Env, part string, f func(rep *restoreReport)) (int, error) {
	rep := restoreReport{Restored: map[string]int{}}
	f(&rep)
	return fromReport(env, rep, part)
}

// skip notes why a part is not applied.
func skip(env *backup.Env, format string, a ...any) (int, error) {
	env.Note(format, a...)
	return 0, backup.ErrNotApplied
}

// rawKV is a key of the storage as kept, or nothing.
func (s *Server) rawKV(ctx context.Context, key string) (any, error) {
	var raw json.RawMessage
	if _, ok := s.loadKV(ctx, key, &raw); ok {
		return raw, nil
	}
	return nil, nil
}

// registerBackupParts registers the console's own parts of a backup, in
// the order a restore applies them: the hosts before the pairings, since a
// host's pairing is of a host here.
func (s *Server) registerBackupParts() {
	r := s.backups
	r.LeftOut("sign-in sessions", "Watson's cards and Sherlock's conversations, which live in memory until the console restarts")
	r.Register(backup.Part{Name: "settings", Label: "Settings", Order: 10,
		How: "replace the current ones: refresh, time format, fleets, acknowledged alerts, Sherlock and Watson",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			if s.opts.Settings == nil {
				return nil, nil
			}
			st := s.opts.Settings.Current().Settings
			return &st, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var in settings.Settings
			if err := json.Unmarshal(raw, &in); err != nil {
				return skip(env, "settings: %v", err)
			}
			if s.opts.Settings == nil {
				return skip(env, "settings: this console keeps no settings")
			}
			if err := s.restorable(in); err != nil {
				return skip(env, "settings: %v", err)
			}
			before := s.opts.Settings.Current()
			after, err := s.opts.Settings.Update(ctx, in)
			if err != nil {
				return skip(env, "settings: %v", err)
			}
			s.recordRestoredSettings(requestOf(ctx), before.Settings, after.Settings)
			return 1, nil
		}})
	r.Register(backup.Part{Name: "users", Label: "Local users", Order: 20,
		How: "are added, with their password when the backup's key opens it, or updated: name, email, groups, super roles; a user here keeps their password",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			acc := s.accounts()
			if acc == nil {
				return nil, nil
			}
			var users []backupUser
			for _, u := range acc.List() {
				if u.Managed {
					continue
				}
				users = append(users, backupUser{Username: u.Username, Name: u.Name, Email: u.Email, Groups: u.Groups, Super: u.Super, Disabled: u.Disabled, Created: u.Created, LastSignIn: u.LastSignIn, Password: env.Seal(u.PasswordHash)})
			}
			return users, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var users []backupUser
			if err := json.Unmarshal(raw, &users); err != nil {
				return skip(env, "users: %v", err)
			}
			return helper(env, "users", func(rep *restoreReport) { s.restoreUsers(requestOf(ctx), users, env, rep) })
		}})
	r.Register(backup.Part{Name: "connections", Label: "Cluster connections", Order: 30,
		How: "missing here are added with their credentials when the backup's key opens them, or wait for them to be pasted again; a connection here keeps its own",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			if s.opts.Clusters == nil {
				return nil, nil
			}
			var out []backupConnection
			for _, sum := range s.opts.Clusters.List() {
				c := backupConnection{Spec: sum.Spec}
				if !sum.Local {
					cred, err := s.opts.Clusters.Sealed(ctx, sum.ID, env.Seal)
					if err != nil {
						return nil, fmt.Errorf("reading the connection %s: %w", sum.ID, err)
					}
					bc := backupCredential(cred)
					c.Credential = &bc
				}
				out = append(out, c)
			}
			return out, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var list []backupConnection
			if err := json.Unmarshal(raw, &list); err != nil {
				return skip(env, "connections: %v", err)
			}
			return helper(env, "connections", func(rep *restoreReport) { s.restoreConnections(requestOf(ctx), list, env, rep) })
		},
		Check: func(ctx context.Context, raw json.RawMessage, env *backup.Env) {
			var list []backupConnection
			if env.Picked("connections") || json.Unmarshal(raw, &list) != nil {
				return
			}
			var missing []string
			for _, c := range list {
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
				env.Note("these connections are not here and were not restored: %s", strings.Join(missing, "; "))
			}
		}})
	r.Register(backup.Part{Name: "apps", Label: "Helm applications", Order: 40,
		How: "are added or replaced: source, values, parameters, commands; a repository token comes back when the backup's key opens it and this console has none. One this console would refuse is left out and named",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			if s.opts.Apps == nil {
				return nil, nil
			}
			recs, err := s.opts.Apps.List(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading the applications: %w", err)
			}
			var out []backupApp
			for _, rec := range recs {
				a := backupApp{Record: rec}
				if rec.HasToken {
					_, tok, err := s.opts.Apps.Get(ctx, rec.Cluster, rec.Namespace, rec.Name)
					if err != nil {
						return nil, fmt.Errorf("reading the application %s/%s: %w", rec.Namespace, rec.Name, err)
					}
					a.RepoToken = env.Seal(tok)
				}
				out = append(out, a)
			}
			return out, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var list []backupApp
			if err := json.Unmarshal(raw, &list); err != nil {
				return skip(env, "applications: %v", err)
			}
			return helper(env, "apps", func(rep *restoreReport) { s.restoreApps(ctx, list, env, rep) })
		}})
	// Two secrets that are parts of their own, sealed (ADR-137): each
	// is set here when this console has none, and listed to set again when
	// the backup's key does not open it.
	const sherlockKeyWhere = "Settings › Sherlock › API key"
	r.Register(backup.Part{Name: "sherlockKey", Label: "Sherlock's API key", Order: 50,
		How: "is set here when this console has none; a key here stays",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			if k := s.opts.SherlockKeys; k != nil && k.Info().Set && !k.Fixed() {
				return env.Seal(k.Key()), nil
			}
			return nil, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var sealed string
			if err := json.Unmarshal(raw, &sealed); err != nil {
				return skip(env, "Sherlock's API key: %v", err)
			}
			k := s.opts.SherlockKeys
			if k == nil {
				return skip(env, "Sherlock's API key: this console has no place for it")
			}
			if k.Info().Set {
				if err := backup.CheckSecret(sealed); err != nil {
					return skip(env, "Sherlock's API key: %v", err)
				}
				return skip(env, "Sherlock's API key: this console has its own and keeps it")
			}
			key, err := env.Open(sealed, "Sherlock's API key", sherlockKeyWhere)
			if err != nil {
				return skip(env, "Sherlock's API key: %v", err)
			}
			if key == "" {
				return 0, backup.ErrNotApplied
			}
			if err := k.SetFor(ctx, s.sherlockProvider(s.sherlockSettings()).Auth, key, env.By); err != nil {
				env.SetAgain("Sherlock's API key", sherlockKeyWhere)
				return skip(env, "Sherlock's API key: %v", err)
			}
			s.record(requestOf(ctx), audit.ActionSherlockSettings, "sherlock", "restored: the API key, from a backup")
			return 1, nil
		},
		Check: func(ctx context.Context, raw json.RawMessage, env *backup.Env) {
			if !env.Picked("sherlockKey") && (s.opts.SherlockKeys == nil || !s.opts.SherlockKeys.Info().Set) {
				env.SetAgain("Sherlock's API key", sherlockKeyWhere)
			}
		}})
	const sshWhere = "Nodes › Pair for SSH, each node again (or put the Secret <release>-ssh-ca back before restoring)"
	r.Register(backup.Part{Name: "sshAuthority", Label: "The SSH authority's key", Order: 60,
		How: "is set here when this console has no SSH authority yet, so the pairings work again; an authority here stays",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			ca, ok := s.opts.SSH.(sshAuthorityBackup)
			if !ok || s.opts.SSH.Fingerprint() == "" {
				return nil, nil
			}
			if !env.Sealing() {
				return backup.Placeholder, nil
			}
			pem, err := ca.AuthorityKey(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading the SSH authority's key: %w", err)
			}
			return env.Seal(string(pem)), nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var sealed string
			if err := json.Unmarshal(raw, &sealed); err != nil {
				return skip(env, "the SSH authority's key: %v", err)
			}
			ca, ok := s.opts.SSH.(sshAuthorityBackup)
			if !ok {
				return skip(env, "the SSH authority's key: SSH is off on this console")
			}
			if s.opts.SSH.Fingerprint() != "" {
				if err := backup.CheckSecret(sealed); err != nil {
					return skip(env, "the SSH authority's key: %v", err)
				}
				return skip(env, "the SSH authority's key: this console has its own SSH authority and keeps it; pairings made with the backup's work again only with that one")
			}
			pem, err := env.Open(sealed, "the SSH authority's private key", sshWhere)
			if err != nil {
				return skip(env, "the SSH authority's key: %v", err)
			}
			if pem == "" {
				return 0, backup.ErrNotApplied
			}
			if err := ca.RestoreAuthority(ctx, []byte(pem)); err != nil {
				env.SetAgain("the SSH authority's private key", sshWhere)
				return skip(env, "the SSH authority's key: %v", err)
			}
			s.record(requestOf(ctx), ActionRestore, "ssh-authority", "restored the SSH authority's key, fingerprint "+s.opts.SSH.Fingerprint())
			return 1, nil
		},
		Check: func(ctx context.Context, raw json.RawMessage, env *backup.Env) {
			if !env.Picked("sshAuthority") && (s.opts.SSH == nil || s.opts.SSH.Fingerprint() == "") {
				env.SetAgain("the SSH authority's private key", sshWhere)
			}
		}})
	r.Register(backup.Part{Name: "toolboxSessions", Label: "Toolbox session records", Order: 70,
		How:     "are merged with those here",
		LeftOut: []string{"the Toolbox's session recordings, which are in the transcript store"},
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			var list []sessions.Record
			if _, ok := s.loadKV(ctx, sessions.HistoryKey, &list); ok {
				return list, nil
			}
			return nil, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var list []sessions.Record
			if err := json.Unmarshal(raw, &list); err != nil {
				return skip(env, "toolbox sessions: %v", err)
			}
			if s.opts.KV == nil {
				return skip(env, "toolbox sessions: this console has no storage for them")
			}
			n, err := s.mergeSessions(ctx, list)
			if err != nil {
				return skip(env, "toolbox sessions: %v", err)
			}
			return n, nil
		}})
	r.Register(backup.Part{Name: "sherlockSpend", Label: "Sherlock's spend", Order: 80,
		How:    "replaces the ledger here",
		Export: func(ctx context.Context, env *backup.Env) (any, error) { return s.rawKV(ctx, sherlock.SpendKey) },
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			if s.opts.KV == nil {
				return skip(env, "Sherlock's spend: this console has no storage for it")
			}
			if _, err := s.opts.KV.SaveKey(ctx, sherlock.SpendKey, raw, ""); err != nil {
				return skip(env, "Sherlock's spend: %v", err)
			}
			if s.opts.Sherlock != nil {
				s.opts.Sherlock.Ledger().Reload()
			}
			return 1, nil
		}})
	r.Register(backup.Part{Name: "sshHosts", Label: "Hosts outside Kubernetes", Order: 90,
		How:    "missing here are added with their pinned host keys; a host or node of the same name here keeps its place",
		Export: func(ctx context.Context, env *backup.Env) (any, error) { return s.rawKV(ctx, nodessh.HostsKey) },
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			return helper(env, "sshHosts", func(rep *restoreReport) { s.restoreHosts(requestOf(ctx), raw, rep) })
		}})
	r.Register(backup.Part{Name: "sshPairings", Label: "SSH pairings", Order: 100,
		How:    "are added to those here, each checked first; they work with the same SSH authority Secret",
		Export: func(ctx context.Context, env *backup.Env) (any, error) { return s.rawKV(ctx, nodessh.PairingsKey) },
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var list map[string]nodessh.Pairing
			if s.opts.KV == nil {
				return skip(env, "SSH pairings: this console has no storage for them")
			}
			if err := json.Unmarshal(raw, &list); err != nil {
				return skip(env, "SSH pairings: %v", err)
			}
			n, err := s.restorePairings(ctx, list, env.Note)
			if err != nil {
				return skip(env, "SSH pairings: %v", err)
			}
			return n, nil
		}})
	r.Register(backup.Part{Name: "audit", Label: "Audit log", Order: 110,
		How: "is merged with the events here, each marked as restored",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			if s.opts.Audit == nil {
				return nil, nil
			}
			events, err := s.opts.Audit.Events(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading the audit log: %w", err)
			}
			return events, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var events []audit.Event
			if err := json.Unmarshal(raw, &events); err != nil {
				return skip(env, "audit log: %v", err)
			}
			if s.opts.Audit == nil || !s.opts.Audit.Stored() {
				return skip(env, "audit log: this console keeps no audit log to add to")
			}
			have, err := s.opts.Audit.Events(ctx)
			if err != nil {
				return skip(env, "audit log: %v", err)
			}
			seen := map[string]bool{}
			for _, e := range have {
				seen[auditKey(e)] = true
			}
			var add []audit.Event
			for _, e := range events {
				if !seen[auditKey(e)] {
					seen[auditKey(e)] = true
					add = append(add, e)
				}
			}
			return s.opts.Audit.Import(add, env.Taken), nil
		}})
	r.Register(backup.Part{Name: "automation", Label: "Automation", Order: 120,
		How:     "scripts come back as drafts to submit for review again; run profiles and groups are added; fleet parameters apply, or on production wait for another SuperAdmin",
		LeftOut: []string{"Automation's runs and the facts read from nodes"},
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			st := s.opts.AutomationStore
			if st == nil {
				return nil, nil
			}
			a := backupAutomation{}
			var err error
			if a.Scripts, err = st.Scripts(ctx); err == nil {
				if a.Variables, err = st.AllVariables(ctx); err == nil {
					var profiles []automation.RunProfile
					if profiles, err = st.Profiles(ctx); err == nil {
						for _, p := range profiles {
							if !p.Builtin {
								a.Profiles = append(a.Profiles, p)
							}
						}
						a.Groups, err = st.Groups(ctx)
					}
				}
			}
			if err != nil {
				return nil, fmt.Errorf("reading Automation's records: %w", err)
			}
			return &a, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var a backupAutomation
			if err := json.Unmarshal(raw, &a); err != nil {
				return skip(env, "Automation: %v", err)
			}
			return helper(env, "automation", func(rep *restoreReport) { s.restoreAutomation(requestOf(ctx), a, env.Taken, rep) })
		}})
}

// backupBy names the person taking or restoring a backup.
func backupBy(r *http.Request) string {
	sess, _ := auth.SessionFrom(r.Context())
	return cmp.Or(sess.User.Email, sess.User.Name)
}

// countDetail is what a backup holds, part by part, for the audit log.
func countDetail(f *backup.File) string {
	var parts []string
	for _, name := range f.Names() {
		parts = append(parts, fmt.Sprintf("%s=%d", name, backup.Count(f.Parts[name])))
	}
	return strings.Join(parts, " ")
}

// getBackup is the backup as a download; ?optional=1 takes the optional
// parts too (chat conversations).
func (s *Server) getBackup(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	f, err := s.backups.Export(r.Context(), &backup.Env{By: backupBy(r), Taken: s.opts.Now(), Version: s.opts.Version, Optional: r.URL.Query().Get("optional") == "1"})
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	gz, err := f.Gzip()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.record(r, ActionBackup, "backup", fmt.Sprintf("%s bytes=%d%s", countDetail(f), len(gz), sealingDetail(f)))
	s.noteFirstBackup(r) // Getting started (Phase 12)
	h := w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+f.Name()+`"`)
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(gz)
}

// sealingDetail names the key a backup's secrets are sealed with, by
// fingerprint, for the audit log; never the key. (Words such as "secret="
// would have the audit log's scrubbing hide what follows.)
func sealingDetail(f *backup.File) string {
	if f.Secrets == nil {
		return " sealing=placeholders"
	}
	return " sealing=sealed fingerprint=" + f.Secrets.Key
}

// backupKeyInfo is what the page learns of the install's backup key
// (ADR-137): its fingerprint and the Secret that holds it, never the key.
type backupKeyInfo struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Secret      string `json:"secret,omitempty"`
	Error       string `json:"error,omitempty"`
}

// getBackupParts lists the parts a backup has, for the page's labels, and
// names the install's backup key by fingerprint.
func (s *Server) getBackupParts(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	var info backupKeyInfo
	k, secret, err := s.backups.Key(r.Context())
	info.Secret = secret
	if err != nil {
		info.Error = err.Error()
	} else if k != nil {
		info.Fingerprint = k.Fingerprint()
	}
	out, _ := json.Marshal(map[string]any{"parts": s.backups.Infos(), "key": info})
	writeJSON(w, r, out)
}

// postBackupOpen reads an uploaded file the page cannot: one encrypted
// with the scheduled backups' passphrase (ADR-82), opened here with the
// passphrase this console keeps, and sent back as JSON for the restore.
func (s *Server) postBackupOpen(w http.ResponseWriter, r *http.Request) {
	if !s.superAdmin(w, r) {
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "the file is over 64 MiB")
		return
	}
	var f *backup.File
	if s.opts.BackupS3 != nil {
		f, err = s.opts.BackupS3.Open(r.Context(), b)
	} else {
		f, err = backup.Read(b, nil)
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.record(r, ActionBackupOpen, "backup", "opened an encrypted backup taken "+f.Taken.Format(time.RFC3339)+": "+countDetail(f))
	out, err := json.Marshal(f)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, out)
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
	var in restoreRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the backup is not valid JSON: "+err.Error())
		return
	}
	if err := in.Backup.CheckFormat(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	b := &in.Backup
	// A file whose secrets this install's key cannot open is restored
	// only on the SuperAdmin's yes (ADR-137): the page asks, and lists
	// afterwards every secret to set again.
	check := s.backups.CheckKey(r.Context(), b)
	if check.Sealed && !check.Same && !in.WithoutSecrets {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusConflict)
		msg := "The backup's secrets are sealed with another backup key than this install's, so they cannot be restored. Restore the rest without them?"
		if check.Here == "" {
			msg = "The backup's secrets are sealed with a backup key, and this install has none, so they cannot be restored. Restore the rest without them?"
		}
		out, _ := json.Marshal(map[string]any{"error": msg, "code": "backup-key", "key": check})
		_, _ = w.Write(append(out, '\n'))
		return
	}
	ctx := context.WithValue(r.Context(), restoreRequestKey{}, r)
	rep := s.backups.Restore(ctx, b, in.Parts, &backup.Env{By: backupBy(r), Taken: b.Taken, Version: b.Version})

	parts := []string{}
	for p := range in.Parts {
		if in.Parts[p] {
			parts = append(parts, p)
		}
	}
	sort.Strings(parts)
	detail := "parts=" + strings.Join(parts, ",") + " taken=" + b.Taken.Format(time.RFC3339) + " version=" + strconv.Quote(b.Version)
	if in.Source != "" {
		detail += " from=" + strconv.Quote(in.Source)
	}
	for _, p := range parts {
		detail += " " + p + "=" + strconv.Itoa(rep.Restored[p])
	}
	switch {
	case !check.Sealed:
		detail += " sealing=placeholders"
	case check.Same:
		detail += " sealing=opened fingerprint=" + check.File
	default:
		detail += " sealing=not-opened fingerprint=" + check.File
	}
	detail += " toSetAgain=" + strconv.Itoa(len(rep.Secrets))
	s.record(r, ActionRestore, "backup", detail)
	out, _ := json.Marshal(rep)
	writeJSON(w, r, out)
}

// restoreApps brings the applications back: each checked as one created
// here, and saved with the token here when there is one, or else with the
// backup's when its key opens it.
func (s *Server) restoreApps(ctx context.Context, list []backupApp, env *backup.Env, rep *restoreReport) {
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	if s.opts.Apps == nil {
		note("applications: this console keeps none")
		return
	}
	n := 0
	for _, a := range list {
		rec := a.Record
		if s.opts.Clusters != nil {
			if _, ok := s.opts.Clusters.Summary(rec.Cluster); !ok {
				note("application %s/%s: no connection %q here; add the connection first", rec.Namespace, rec.Name, rec.Cluster)
				continue
			}
		}
		if err := s.checkRestoredApp(ctx, &rec); err != nil {
			note("application %s/%s is not restored: %v", rec.Namespace, rec.Name, err)
			continue
		}
		cur, _, err := s.opts.Apps.Get(ctx, rec.Cluster, rec.Namespace, rec.Name)
		// A token here stays; the file's is taken only where there is
		// none, and only when the backup's key opens it.
		hadToken := rec.HasToken || a.RepoToken != ""
		rec.HasToken = err == nil && cur.HasToken
		var token *string
		if hadToken && !rec.HasToken {
			name, where := fmt.Sprintf("the repository token of the application %s/%s", rec.Namespace, rec.Name), fmt.Sprintf("Applications › %s/%s › Source", rec.Namespace, rec.Name)
			tok, err := env.Open(a.RepoToken, name, where)
			switch {
			case err != nil:
				note("application %s/%s: %v; it is saved without a token", rec.Namespace, rec.Name, err)
				env.SetAgain(name, where)
			case tok != "":
				token, rec.HasToken = &tok, true
			case a.RepoToken == "":
				// A file of the first format: no token, nor a placeholder.
				env.SetAgain(name, where)
			}
		}
		if err := s.opts.Apps.Save(ctx, rec, token); err != nil {
			note("application %s/%s: %v", rec.Namespace, rec.Name, err)
			continue
		}
		n++
	}
	rep.Restored["apps"] = n
}

// restoreHosts brings back the hosts outside Kubernetes missing here,
// each checked as one added by hand; a host or node of the same name here
// keeps its place.
func (s *Server) restoreHosts(r *http.Request, raw json.RawMessage, rep *restoreReport) {
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	var list map[string]nodessh.Host
	if err := json.Unmarshal(raw, &list); err != nil {
		note("hosts: %v", err)
		return
	}
	if s.opts.Hosts == nil {
		note("hosts: this console keeps no hosts (SSH or Automation is off)")
		return
	}
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for _, k := range keys {
		h, err := s.opts.Hosts.RestoreHost(r.Context(), list[k])
		if err != nil {
			note("host %s is not restored: %v", k, err)
			continue
		}
		s.recordHost(r, audit.ActionHostAdd, h.Cluster, h.Name, audit.OutcomeOK, fmt.Sprintf("restored: host %s at %s port %d, login user %s, %s", h.Name, h.Address, h.Port, h.LoginUser, h.State))
		n++
	}
	rep.Restored["sshHosts"] = n
}

// restoreConnections brings back the connections missing here, each with
// its credential when the backup's key opens it (ADR-137); a secret not
// opened is the placeholder, and the connection shows as failed, saying
// so, until it is pasted again. A connection here keeps its own
// credential.
func (s *Server) restoreConnections(r *http.Request, list []backupConnection, env *backup.Env, rep *restoreReport) {
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	if s.opts.Clusters == nil {
		note("connections: this console keeps no connections")
		return
	}
	n := 0
	for _, c := range list {
		if c.Local {
			continue
		}
		if _, ok := s.opts.Clusters.Summary(c.ID); ok {
			note("connection %s is here already and keeps its own credential", c.Name)
			continue
		}
		cred, err := restoredCredential(c, env)
		if err == nil {
			_, err = s.opts.Clusters.Restore(r.Context(), c.Spec, cred)
		}
		if err != nil {
			s.recordCluster(r, audit.ActionClusterAdd, c.ID, audit.OutcomeFailed, "restored: "+err.Error())
			note("connection %s is not restored: %v", c.Name, err)
			continue
		}
		how := "with its credential"
		if cred.HasPlaceholder() {
			how = "its credential to paste again"
		}
		s.recordCluster(r, audit.ActionClusterAdd, c.ID, audit.OutcomeOK, fmt.Sprintf("restored: %s (%s), %s, sign-in by %s, %s, %s", c.Name, c.Env, c.Server, c.Auth, c.Mode, how))
		n++
	}
	rep.Restored["connections"] = n
}

// restoredCredential is a backup's credential as a restore saves it. A
// secret sealed with this install's key is opened: only a console with
// the key could have sealed it, so it is this install's own, and the
// connection starts with it. Any other secret is the placeholder, listed
// to paste again; one in clear is refused, since a file is no way around
// the connection editor's checks. A file of the first format, which had
// no credential, gets placeholders where its sign-in method keeps it.
func restoredCredential(c backupConnection, env *backup.Env) (clusters.Credential, error) {
	var cred clusters.Credential
	if c.Credential != nil {
		cred = clusters.Credential(*c.Credential)
	}
	for _, f := range cred.Secrets() {
		if err := backup.CheckSecret(*f); err != nil {
			return clusters.Credential{}, errors.New("the file holds a secret in clear where a backup holds a sealed one or a placeholder; add the connection on the Clusters page instead")
		}
	}
	where := "Clusters › " + c.Name + " › Edit connection"
	fields := []struct {
		name string
		f    *string
	}{{"token", &cred.Token}, {"kubeconfig", &cred.Kubeconfig}, {"client certificate", &cred.CertPEM}, {"client key", &cred.KeyPEM}, {"OIDC client secret", &cred.OIDCClientSecret}, {"OIDC refresh token", &cred.OIDCRefreshToken}}
	held := false
	for _, x := range fields {
		if *x.f == "" {
			continue
		}
		held = true
		v, err := env.Open(*x.f, "the "+x.name+" of the connection "+c.Name, where)
		if err != nil {
			return clusters.Credential{}, err
		}
		if v == "" {
			v = clusters.Placeholder
		}
		*x.f = v
	}
	if !held {
		switch c.Auth {
		case clusters.AuthToken:
			cred.Token = clusters.Placeholder
		case clusters.AuthKubeconfig:
			cred.Kubeconfig = clusters.Placeholder
		case clusters.AuthCert:
			cred.CertPEM, cred.KeyPEM = clusters.Placeholder, clusters.Placeholder
		case clusters.AuthOIDC:
			cred.OIDCRefreshToken = clusters.Placeholder
		}
		if cred.HasPlaceholder() {
			env.SetAgain("the credential of the connection "+c.Name, where)
		}
	}
	return cred, nil
}

// recordRestoredSettings names what a restore changed of Sherlock and
// Watson, as their own routes do.
func (s *Server) recordRestoredSettings(r *http.Request, before, after settings.Settings) {
	if d := sherlockChange(before.SherlockOrDefault(), after.SherlockOrDefault()); d != "no change" {
		s.record(r, audit.ActionSherlockSettings, "sherlock", "restored: "+d)
	}
	if d := watsonChange(before.WatsonOrDefault(), after.WatsonOrDefault()); d != "no change" {
		s.record(r, ActionWatsonSettings, "watson", "restored: "+d)
	}
}

// restorable holds a backup's settings to what the settings pages accept: a
// file is no way around them.
func (s *Server) restorable(in settings.Settings) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if in.Sherlock != nil && s.opts.SherlockProviderLocked && !reflect.DeepEqual(s.sherlockSettings().Provider, in.Sherlock.Provider) {
		return errors.New("the chart fixes Sherlock's provider (sherlock.lockProvider), and the backup's differs; change it there")
	}
	return nil
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
// others created without a password. Making someone a SuperAdmin waits
// for another SuperAdmin, as it does on the Users page.
func (s *Server) restoreUsers(r *http.Request, users []backupUser, env *backup.Env, rep *restoreReport) {
	ctx := r.Context()
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	sess, _ := auth.SessionFrom(ctx)
	split := func(before auth.Account, u backupUser) auth.SuperChange {
		if tp := s.twoPersonAuth(); tp != nil {
			return tp.SuperAdminHold(sess, before, u.Super, u.Groups)
		}
		return auth.SuperChange{Super: u.Super, Groups: u.Groups}
	}
	acc := s.accounts()
	if acc == nil || !acc.Persistent() {
		note("users: this console keeps no local users of its own (sign-in is off, or they are not kept in a Secret)")
		return
	}
	n := 0
	for _, u := range users {
		cur, exists := acc.Find(u.Username)
		if exists && cur.Managed {
			note("user %s is defined in the deployment's values and stays as it is", u.Username)
			continue
		}
		var c auth.SuperChange
		if !exists {
			c = split(auth.Account{Username: u.Username, Email: u.Email}, u)
			if _, err := acc.Create(ctx, auth.NewAccount{Username: u.Username, Name: u.Name, Email: u.Email, Password: randomPassword(), Groups: c.Groups}); err != nil {
				note("user %s: %v", u.Username, err)
				continue
			}
			// Created with a password nobody knows; the backup's, when its
			// key opens it, replaces it (ADR-137).
			name, where := "the password of the user "+u.Username, "Administration › Users › "+u.Username+" › Set password"
			hash, err := env.Open(u.Password, name, where)
			switch {
			case err != nil:
				note("user %s: %v", u.Username, err)
				env.SetAgain(name, where)
			case hash != "":
				if err := acc.RestorePasswordHash(ctx, u.Username, hash); err != nil {
					note("user %s: %v", u.Username, err)
					env.SetAgain(name, where)
				}
			case u.Password == "":
				env.SetAgain(name, where)
			}
			cur, _ = acc.Find(u.Username)
		} else {
			c = split(cur, u)
			// A file with no name or email has nothing to say about them.
			name, email, disabled := cmp.Or(u.Name, cur.Name), cmp.Or(u.Email, cur.Email), u.Disabled
			if _, _, err := acc.Update(ctx, u.Username, auth.Change{Name: &name, Email: &email, Disabled: &disabled}); err != nil {
				note("user %s: %v", u.Username, err)
				continue
			}
			var add, remove []string
			for _, g := range c.Groups {
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
		if !slices.Equal(cur.Super, c.Super) {
			if _, err := acc.SetSuper(ctx, u.Username, c.Super); err != nil {
				note("user %s, super roles: %v", u.Username, err)
			}
		}
		if c.Hold != nil {
			if _, err := s.twoPersonAuth().RequestSuperAdmin(ctx, r, sess, *c.Hold); err != nil {
				note("user %s is not made a SuperAdmin: %v", u.Username, err)
			} else {
				note("user %s becomes a SuperAdmin once another SuperAdmin approves it on Administration › Approvals", u.Username)
			}
		}
		if !exists && u.Disabled {
			disabled := true
			_, _, _ = acc.Update(ctx, u.Username, auth.Change{Disabled: &disabled})
		}
		n++
	}
	rep.Restored["users"] = n
}

// mergeSessions adds the backup's session records to those kept, by id,
// and keeps as many as the toolbox does.
func (s *Server) mergeSessions(ctx context.Context, add []sessions.Record) (int, error) {
	keep := tb.Default().History.Keep
	if s.opts.Sessions != nil {
		keep = s.opts.Sessions.Config().History.Keep
	}
	h := &sessions.KVHistory{Backend: s.opts.KV, Key: sessions.HistoryKey, Keep: keep}
	return h.Merge(ctx, add)
}

// checkRestoredApp holds an application from a file to what creating one
// accepts: its names, its source and repository host, its values and
// parameters, its commands and its fleet. A file is no way around them.
func (s *Server) checkRestoredApp(ctx context.Context, rec *apps.Record) error {
	if len(validation.IsDNS1123Label(rec.Namespace)) > 0 || len(validation.IsDNS1123Subdomain(rec.Name)) > 0 || len(rec.Name) > 53 {
		return errors.New("its release name or namespace is not valid")
	}
	src, err := apps.Normalize(rec.Source)
	if err == nil {
		err = apps.CheckRepoHost(ctx, src.Repo)
	}
	if err != nil {
		return err
	}
	rec.Source = src
	for i, p := range rec.Params {
		p = strings.TrimSpace(p)
		if k, _, ok := strings.Cut(p, "="); !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("parameter %d is not key=value", i+1)
		}
		rec.Params[i] = p
	}
	if _, err := apps.Values(rec.Values, rec.Params); err != nil {
		return err
	}
	rec.ValuesFile = strings.TrimSpace(rec.ValuesFile)
	if !validValuesFile(rec.ValuesFile) {
		return errors.New("its values file is not a YAML file in the chart's directory")
	}
	for i := range rec.Commands {
		c := &rec.Commands[i]
		if err := apps.CheckCommandName(c.Name); err != nil {
			return fmt.Errorf("command %q: %w", c.Name, err)
		}
		params, err := checkCommandParams(c.Params)
		if err != nil {
			return fmt.Errorf("command %s: %w", c.Name, err)
		}
		c.Params = params
	}
	rec.FleetLabel = ""
	if rec.Fleet != "" {
		var list []fleets.Fleet
		if s.opts.Settings != nil {
			list = s.opts.Settings.Current().Fleets[rec.Cluster]
		}
		i := slices.IndexFunc(list, func(f fleets.Fleet) bool { return f.ID == rec.Fleet })
		if i < 0 || list[i].Deploy == "" {
			return fmt.Errorf("the cluster has no fleet %q with a deploy label", rec.Fleet)
		}
		rec.FleetLabel = list[i].Deploy
	}
	return nil
}

// checkRestoredPairing holds a pairing from a file to what pairing a node
// records: a connection here, a node's name, a Unix login user, a port and
// host keys that read as keys.
func (s *Server) checkRestoredPairing(p nodessh.Pairing) error {
	if s.opts.Clusters != nil {
		if _, ok := s.opts.Clusters.Summary(p.Cluster); !ok {
			return fmt.Errorf("no connection %q here", p.Cluster)
		}
	}
	if len(validation.IsDNS1123Subdomain(p.Node)) > 0 {
		return errors.New("the node's name is not valid")
	}
	if !tb.LoginUserRE.MatchString(p.LoginUser) {
		return errors.New("the login user is not a Unix user name")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("the port is not 1 to 65535")
	}
	if len(p.HostKeys) == 0 {
		return errors.New("it has no host key")
	}
	for _, k := range p.HostKeys {
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k)); err != nil {
			return errors.New("a host key is not readable")
		}
	}
	return nil
}

// restorePairings adds the file's valid pairings to those kept, by node,
// and says which it left out and why.
func (s *Server) restorePairings(ctx context.Context, list map[string]nodessh.Pairing, note func(string, ...any)) (int, error) {
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	add := map[string]nodessh.Pairing{}
	for _, k := range keys {
		p := list[k]
		if err := s.checkRestoredPairing(p); err != nil {
			note("SSH pairing of %s/%s is not restored: %v", p.Cluster, p.Node, err)
			continue
		}
		add[p.Cluster+"/"+p.Node] = p
	}
	if len(add) == 0 {
		return 0, nil
	}
	for try := 0; try < 8; try++ {
		have := map[string]nodessh.Pairing{}
		b, version, err := s.opts.KV.LoadKey(ctx, nodessh.PairingsKey)
		if err == nil {
			if err := json.Unmarshal(b, &have); err != nil {
				return 0, err
			}
		} else if !errors.Is(err, kv.ErrNotFound) {
			return 0, err
		}
		for k, p := range add {
			have[k] = p
		}
		out, _ := json.Marshal(have)
		if _, err = s.opts.KV.SaveKey(ctx, nodessh.PairingsKey, out, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return len(add), err
	}
	return 0, errors.New("the pairings kept changing; try again")
}

// restoreAutomation brings Automation's records back. Each script comes
// back as a draft of its newest version, made by the person restoring, so
// it is reviewed here again. Fleet parameters on a production cluster wait
// for another SuperAdmin's approval, as a change made by hand does when it
// matters; elsewhere they apply at once.
func (s *Server) restoreAutomation(r *http.Request, a backupAutomation, taken time.Time, rep *restoreReport) {
	note := func(format string, args ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, args...)) }
	st := s.opts.AutomationStore
	if st == nil {
		note("Automation: it is not part of this console")
		return
	}
	ctx := r.Context()
	who := s.automationWho(r)
	groups, err := st.Groups(ctx)
	if err != nil {
		note("Automation: %v", err)
		return
	}
	n := 0
	for _, g := range a.Groups {
		if slices.ContainsFunc(groups, func(x automation.Group) bool { return x.Name == g.Name }) {
			continue
		}
		if err := st.SaveGroup(ctx, "", g); err != nil {
			note("Automation group %s: %v", g.Name, err)
			continue
		}
		n++
	}
	scripts, err := st.Scripts(ctx)
	if err != nil {
		note("Automation: %v", err)
		return
	}
	drafts := 0
	for _, sc := range a.Scripts {
		if len(sc.Versions) == 0 {
			continue
		}
		newest := sc.Versions[len(sc.Versions)-1]
		exists := slices.ContainsFunc(scripts, func(x automation.Script) bool { return x.Name == sc.Name })
		saved, err := st.Save(ctx, sc.Name, sc.Group, newest.Text, who, !exists)
		if err != nil {
			note("Automation script %s: %v", sc.Name, err)
			continue
		}
		if v := saved.Latest(); v.State == automation.StateDraft {
			drafts++
		}
		n++
	}
	if drafts > 0 {
		note("%d Automation scripts came back as drafts; submit each for review, since approvals are never restored", drafts)
	}
	for _, p := range a.Profiles {
		if p.Builtin {
			continue
		}
		if err := st.SetProfile(ctx, p); err != nil {
			note("Automation run profile %s: %v", p.ID, err)
			continue
		}
		n++
	}
	reason := "restored from the backup of " + taken.UTC().Format("2 Jan 2006 15:04 MST")
	clusterIDs := make([]string, 0, len(a.Variables))
	for c := range a.Variables {
		clusterIDs = append(clusterIDs, c)
	}
	sort.Strings(clusterIDs)
	for _, cluster := range clusterIDs {
		production := false
		if s.opts.Clusters != nil {
			sum, ok := s.opts.Clusters.Summary(cluster)
			if !ok {
				note("Automation parameters of cluster %q: no connection here; add it first", cluster)
				continue
			}
			production = sum.Env == clusters.EnvProduction
		}
		have, err := st.Variables(ctx, cluster)
		if err != nil {
			note("Automation parameters of cluster %q: %v", cluster, err)
			continue
		}
		fleetIDs := make([]string, 0, len(a.Variables[cluster]))
		for f := range a.Variables[cluster] {
			fleetIDs = append(fleetIDs, f)
		}
		sort.Strings(fleetIDs)
		for _, fleet := range fleetIDs {
			next, before := a.Variables[cluster][fleet], have[fleet]
			if err := automation.ValidateVariables(next); err != nil {
				note("Automation parameters of fleet %s: %v", fleet, err)
				continue
			}
			if reflect.DeepEqual(next, before) {
				continue
			}
			changed := automation.ChangedVariables(before, next)
			if next.CeilingOrDefault() != before.CeilingOrDefault() {
				changed = append(changed, "privilege ceiling")
			}
			ch := automation.VariableChange{Cluster: cluster, Fleet: fleet, FleetName: s.fleetName(cluster, fleet), By: who, Reason: reason, Changed: changed, Before: before, After: next, State: automation.ChangeApplied,
				Impact: []automation.ImpactItem{{Script: "every script of this fleet", Decision: automation.ImpactApproval, Why: "the parameters come from a backup", By: "rule"}}}
			if production {
				ch.State = automation.ChangeWaiting
				if _, err := st.NewChange(ctx, ch); err != nil {
					note("Automation parameters of fleet %s: %v", fleet, err)
					continue
				}
				note("Automation parameters of fleet %s on production cluster %s wait for another SuperAdmin's approval", ch.FleetName, cluster)
				n++
				continue
			}
			if err := st.SetVariables(ctx, cluster, fleet, next); err != nil {
				note("Automation parameters of fleet %s: %v", fleet, err)
				continue
			}
			_, _ = st.NewChange(ctx, ch)
			n++
		}
	}
	rep.Restored["automation"] = n
}

// fleetName is how a fleet reads on the page.
func (s *Server) fleetName(cluster, id string) string {
	if id == allNodes {
		return "All nodes"
	}
	if s.opts.Settings != nil {
		for _, f := range s.opts.Settings.Current().Fleets[cluster] {
			if f.ID == id {
				return f.Name
			}
		}
	}
	return id
}
