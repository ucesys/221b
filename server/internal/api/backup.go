package api

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	// SSHPairings are the nodes paired for SSH, as kept: public keys only.
	SSHPairings json.RawMessage `json:"sshPairings,omitempty"`
	// Audit is the audit log the console kept, newest first.
	Audit []audit.Event `json:"audit,omitempty"`
	// Automation is Automation's scripts, fleet parameters, run profiles
	// and groups.
	Automation *backupAutomation `json:"automation,omitempty"`
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

var backupLeftOut = []string{
	"the passwords of local users (their hashes stay in the console's Secret; a restored user gets a new password on the Users page)",
	"the credentials of cluster connections (tokens, kubeconfigs, certificates, OIDC secrets): a restore lists the connections to add again",
	"the repository tokens of applications",
	"Sherlock's API key",
	"the SSH authority's private key (the Secret <release>-ssh-ca): pairings work again only with that Secret; without it, pair the nodes again",
	"sign-in sessions",
	"Watson's cards and Sherlock's conversations, which live in memory until the console restarts",
	"the Toolbox's session recordings, which are in the transcript store",
	"Automation's runs and the facts read from nodes",
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
		for _, u := range acc.List() {
			if u.Managed {
				continue
			}
			f.Users = append(f.Users, backupUser{Username: u.Username, Name: u.Name, Email: u.Email, Groups: u.Groups, Super: u.Super, Disabled: u.Disabled, Created: u.Created, LastSignIn: u.LastSignIn})
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
		var pairings json.RawMessage
		if _, ok := s.loadKV(ctx, nodessh.PairingsKey, &pairings); ok {
			f.SSHPairings = pairings
		}
	}
	if s.opts.Audit != nil {
		events, err := s.opts.Audit.Events(ctx)
		if err != nil {
			return f, fmt.Errorf("reading the audit log: %w", err)
		}
		f.Audit = events
	}
	if st := s.opts.AutomationStore; st != nil {
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
			return f, fmt.Errorf("reading Automation's records: %w", err)
		}
		f.Automation = &a
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
	// Parts: settings, users, apps, toolboxSessions, sherlockSpend, sshPairings, audit, automation.
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
		} else if err := s.restorable(*b.Settings); err != nil {
			note("settings: %v", err)
		} else {
			before := s.opts.Settings.Current()
			if after, err := s.opts.Settings.Update(ctx, *b.Settings); err != nil {
				note("settings: %v", err)
			} else {
				rep.Restored["settings"] = 1
				s.recordRestoredSettings(r, before.Settings, after.Settings)
			}
		}
	}

	if in.Parts["users"] && len(b.Users) > 0 {
		s.restoreUsers(r, b.Users, &rep)
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
				if err := s.checkRestoredApp(ctx, &rec); err != nil {
					note("application %s/%s is not restored: %v", rec.Namespace, rec.Name, err)
					continue
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

	if in.Parts["sshPairings"] && len(b.SSHPairings) > 0 {
		var list map[string]nodessh.Pairing
		if s.opts.KV == nil {
			note("SSH pairings: this console has no storage for them")
		} else if err := json.Unmarshal(b.SSHPairings, &list); err != nil {
			note("SSH pairings: %v", err)
		} else if n, err := s.restorePairings(ctx, list, note); err != nil {
			note("SSH pairings: %v", err)
		} else {
			rep.Restored["sshPairings"] = n
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
				rep.Restored["audit"] = s.opts.Audit.Import(add, b.Taken)
			}
		}
	}

	if in.Parts["automation"] && b.Automation != nil {
		s.restoreAutomation(r, *b.Automation, b.Taken, &rep)
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
func (s *Server) restoreUsers(r *http.Request, users []backupUser, rep *restoreReport) {
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
	n, created := 0, 0
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
			created++
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
	if created > 0 {
		note("%d users were created without a password; set each one's on the Users page before they sign in", created)
	}
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
