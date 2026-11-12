package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/ldap"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// Sign-in: its configuration, the users, the sessions and what each
// person may see.

const usersKey = "users.json"

// newDirectory builds the LDAP client from auth.ldap (RB-19).
func newDirectory(f auth.FileConfig, bindPassword string) (auth.Directory, time.Duration, error) {
	l := f.LDAP
	if l == nil {
		return nil, 0, errors.New("-auth-mode ldap needs auth.ldap in the auth config file")
	}
	every, err := l.SyncEvery()
	if err != nil {
		return nil, 0, err
	}
	o := ldap.Options{
		Config:       ldap.Config{URL: l.URL, StartTLS: l.StartTLS, Timeout: 15 * time.Second},
		BindDN:       l.BindDN,
		BindPassword: bindPassword,
		UserBase:     l.UserSearch.Base, UserFilter: l.UserSearch.Filter, UsernameAttr: l.UserSearch.UsernameAttr, NameAttr: l.UserSearch.NameAttr, EmailAttr: l.UserSearch.EmailAttr,
		GroupBase: l.GroupSearch.Base, GroupFilter: l.GroupSearch.Filter, GroupAttr: l.GroupSearch.NameAttr, MemberAttr: l.GroupSearch.MemberAttr,
		Nested:   l.GroupSearch.Nested == nil || *l.GroupSearch.Nested,
		PageSize: l.PageSize,
	}
	if l.CAFile != "" {
		pem, err := os.ReadFile(l.CAFile)
		if err != nil {
			return nil, 0, fmt.Errorf("reading the LDAP CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, 0, fmt.Errorf("%s holds no certificate", l.CAFile)
		}
		o.RootCAs = pool
	}
	d, err := ldap.New(o)
	return d, every, err
}

// hashPassword prints the hash of a password read from standard input, for
// the local accounts in the chart's values: 221b hash-password < file.
func hashPassword() error {
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(strings.TrimRight(string(b), "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

// wireAuthConfig reads how people sign in: the flags, the local users,
// and the file the chart renders with the roles, the groups and the
// directory. With sign-in off it turns off what needs a known person.
func (w *wiring) wireAuthConfig() error {
	cfg := &w.cfg
	log := w.log
	authCfg := auth.Config{
		Mode:        auth.Mode(cfg.AuthMode),
		OIDC:        auth.OIDCConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, Scopes: cfg.OIDCScopes, RedirectURL: cfg.OIDCRedirectURL, UsernameClaim: cfg.OIDCUsernameClaim, GroupsClaim: cfg.OIDCGroupsClaim, ProviderName: cfg.OIDCProviderName},
		Admins:      cfg.Admins,
		AdminGroups: cfg.AdminGroups,
		SessionIdle: cfg.SessionIdle,
		SessionMax:  cfg.SessionMax,
		Title:       "221B · " + cfg.ClusterName,
	}
	if cfg.LocalUsersFile != "" {
		users, err := auth.ReadLocalUsers(cfg.LocalUsersFile)
		if err != nil {
			return err
		}
		authCfg.Local = users
	}
	// The roles, the groups and the directory (Phase 6.1) come in a file the
	// chart renders; the secrets come through the environment.
	authFile, err := auth.ReadFileConfig(cfg.AuthConfigFile)
	if err != nil {
		return err
	}
	authFile.Apply(&authCfg)
	authCfg.Prefixes.OIDCUser, authCfg.Prefixes.OIDCGroup = cfg.ImpersonateUserPrefix, cfg.ImpersonateGroupPrefix
	authCfg.SetupToken = cfg.SetupToken
	authCfg.AccessTTL = cfg.AccessTTL
	if authCfg.Mode == auth.ModeLDAP {
		dir, every, err := newDirectory(authFile, cfg.LDAPBindPassword)
		if err != nil {
			return err
		}
		authCfg.Directory, authCfg.SyncInterval = dir, every
	}
	authCfg.Anonymous = roles.ID(cfg.AnonymousRole)
	if authCfg.Mode == auth.ModeNone {
		// Nobody is known, so nothing that needs a known person runs: SSH,
		// Automation and showing Secret values stay off whatever the chart
		// says, and everyone holds the anonymous role.
		cfg.Automation, cfg.SecretsReveal = false, false
		log.Warn("sign-in is off: everyone who reaches the console holds the anonymous role, and SSH, Automation and Secret values are off; set -auth-mode oidc, ldap or local to use them", "anonymous_role", cfg.AnonymousRole)
	}
	w.authCfg, w.authFile = authCfg, authFile
	return nil
}

// wireAuth opens the sign-in: the users created in the console, the
// sessions and what waits for a second SuperAdmin.
func (w *wiring) wireAuth() error {
	cfg, log := w.cfg, w.log
	var err error
	// The users created in the console are kept in a Secret of the console's
	// own namespace when running in a cluster, otherwise in memory (RB-46).
	usersRef := cfg.UsersSecret
	if usersRef == "" && cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		usersRef = ns + "/" + prefix + "-users"
	}
	if usersRef != "" && w.authCfg.Mode == auth.ModeLocal {
		ns, name, ok := strings.Cut(usersRef, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("-users-secret must be namespace/name, got %q", usersRef)
		}
		sk, err := kube.NewSecretKey(w.restCfg, ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "users", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		w.authCfg.Accounts = auth.SecretBackend(sk, usersKey)
	} else if w.authCfg.Mode == auth.ModeLocal {
		log.Warn("users created in the console are kept in memory and lost on restart; set -users-secret namespace/name, or -connections-secrets, to keep them in a Secret")
	}
	// Sign-ins are kept in Secrets of the console's namespace when running
	// in a cluster, one per session, so every replica knows them and they
	// survive a restart (P7-02); otherwise in memory.
	var sessionStore *kube.SessionStore
	if cfg.ConnectionsSecrets != "" && w.authCfg.Mode != auth.ModeNone {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sessionStore, err = kube.NewSessionStore(w.restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		w.authCfg.SessionStore = sessionStore
	}
	// What waits for a second SuperAdmin (review item 14), kept beside the
	// settings once their storage is open.
	twoPerson := twoperson.New(nil, nil)
	w.authCfg.TwoPerson = twoPerson
	authSvc, err := auth.New(w.authCfg, log.With("component", "auth"), w.auditLog, nil)
	if err != nil {
		return err
	}
	w.sessionStore, w.twoPerson, w.authSvc = sessionStore, twoPerson, authSvc
	return nil
}

// startAuth keeps the sessions and accounts shared with the other
// replicas, and the directory, read.
func (w *wiring) startAuth(ctx context.Context) {
	log := w.log
	if w.sessionStore != nil {
		go w.sessionStore.Run(ctx)
		syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if !w.sessionStore.Synced(syncCtx) {
			log.Warn("the shared sessions are not cached yet; reading them one by one")
		}
		cancel()
	}
	// Accounts created, changed or removed through another replica take
	// effect here within seconds.
	go w.authSvc.RunAccounts(ctx, 5*time.Second)
	go w.authSvc.RunDirectory(ctx)
}

// wireAccess keeps what each person may see, and forgets the sessions
// and people gone.
func (w *wiring) wireAccess(ctx context.Context) {
	cfg := w.cfg
	// What each person may see (P3-02): Kubernetes is asked and the answers
	// kept for a while; people who have not asked for an hour are forgotten.
	var people *access.Cache
	if w.authCfg.Mode != auth.ModeNone {
		people = access.NewCache(access.Options{TTL: cfg.AccessTTL})
	}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.authSvc.Sweep()
				if people != nil {
					people.Sweep(time.Hour)
				}
			}
		}
	}()
	w.people = people
}
