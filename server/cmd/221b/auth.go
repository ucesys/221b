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

	"k8s.io/client-go/rest"

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
		// A session made while the session store is away (DB-65).
		OutageSession: cfg.SessionOutage,
		Title:         "221B · " + cfg.ClusterName,
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
func (w *wiring) wireAuth(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	var err error
	// The users created in the console are kept in a Secret of the console's
	// own namespace when running in a cluster, otherwise in memory (RB-46).
	usersRef := cfg.UsersSecret
	if usersRef == "" && cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		usersRef = ns + "/" + prefix + "-users"
	}
	// With LDAP sign-in the local accounts stay: they still sign in when the
	// directory cannot be reached (break glass).
	localAccounts := w.authCfg.Mode == auth.ModeLocal || w.authCfg.Mode == auth.ModeLDAP
	if usersRef != "" && localAccounts {
		ns, name, ok := strings.Cut(usersRef, "/")
		if !ok || ns == "" || name == "" {
			return fmt.Errorf("-users-secret must be namespace/name, got %q", usersRef)
		}
		sk, err := kube.NewSecretKey(w.signInConfig(), ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "users", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		w.authCfg.Accounts = auth.SecretBackend(sk, usersKey)
	} else if localAccounts {
		log.Warn("users created in the console are kept in memory and lost on restart; set -users-secret namespace/name, or -connections-secrets, to keep them in a Secret")
	}
	// Sign-ins are kept where every replica knows them and a restart keeps
	// them (P7-02) when running in a cluster: rows of the database when
	// there is one (DB-66), else Secrets of the console's namespace, one per
	// session. Otherwise (no -connections-secrets: the simulator, a console
	// run by hand) they are kept in memory, with or without a database, as
	// the accounts made in the console and the key that signs a session
	// are there: a session that outlived a restart would be of an account
	// that did not.
	var sessionStore *kube.SessionStore
	inDatabase := false
	if cfg.ConnectionsSecrets != "" && w.authCfg.Mode != auth.ModeNone {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sessionStore, err = kube.NewSessionStore(w.restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return err
		}
		if w.database != nil {
			// The Secrets are read once more, for the copy, and emptied; they
			// are not watched, and none is made from here on.
			w.authCfg.SessionStore = w.sessionsInDatabase(ctx, sessionStore)
			sessionStore, inDatabase = nil, true
		} else {
			w.authCfg.SessionStore = sessionStore
		}
		// The key that signs a session made while the session store is
		// away (DB-65): the Secret <release>-session-key, made here at
		// start when there is none, as the backups' key is, and read again
		// when a signature does not verify, so a key put back by hand
		// reaches every replica. Never logged. Without Secrets the key is
		// this process's own.
		name := prefix + auth.SessionKeySecretSuffix
		ks, err := kube.NewSecretKey(w.signInConfig(), ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "session-key", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return fmt.Errorf("the session key's Secret: %w", err)
		}
		key := auth.NewSessionKey(ks, nil)
		kctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := key.Ensure(kctx); err != nil {
			log.Warn("the session key is not ready: until it is, a sign-in made while the session store is away is known to this replica only", "secret", ns+"/"+name, "err", err)
		} else {
			log.Info("session key ready", "secret", ns+"/"+name)
		}
		cancel()
		w.authCfg.SessionKey = key
	} else {
		w.claimImport(auth.SessionsImportID)
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
	if inDatabase {
		// The database answers again: the sessions ended here meanwhile are
		// removed from it at once, so the other replicas stop taking them.
		w.database.OnAvailable(authSvc.Sessions().StoreIsBack)
	}
	return nil
}

// signInConfig is the connection the accounts' Secret and the session
// key's are read and written through: the console's own, also on a console
// started without its database, whose other clients are refused a write of
// a ConfigMap or a Secret (DB-67). Sign-in never depends on the database,
// and these two Secrets never move.
func (w *wiring) signInConfig() *rest.Config {
	if w.signInCfg != nil {
		return w.signInCfg
	}
	return w.restCfg
}

// sessionSecrets is what is still done with the session Secrets once the
// sessions are rows: they are read for the copy, and emptied after it.
type sessionSecrets interface {
	All(ctx context.Context) (map[string][]byte, error)
	Empty(ctx context.Context, key string) (bool, error)
}

// sessionsInDatabase keeps the sign-in sessions in the database (DB-66),
// and moves there the ones their Secrets hold: copied once, so whoever is
// signed in stays signed in across the upgrade, and then taken out of the
// Secrets, which are left with no keys (no Secret is deleted because a
// record set moved, DB-61). Emptying is not tidying: a session's Secret
// left whole would let a console from before the move take up a session
// that was ended since. Only what the copy took, or what has expired, is
// emptied (auth.SessionsToEmpty); a copy that fails empties nothing and
// does not stop the start; an emptying that fails is tried again every
// minute (startAuth) and at the next start.
func (w *wiring) sessionsInDatabase(ctx context.Context, secrets sessionSecrets) auth.SharedStore {
	idle := w.authCfg.SessionIdle
	if idle <= 0 {
		idle = 8 * time.Hour
	}
	empty := func(ctx context.Context) (int, error) {
		recs, err := secrets.All(ctx)
		if err != nil {
			return 0, err
		}
		keys, err := auth.SessionsToEmpty(ctx, w.database, recs, idle, time.Now())
		if err != nil {
			return 0, err
		}
		n := 0
		var first error
		for _, key := range keys {
			emptied, err := secrets.Empty(ctx, key)
			if err != nil {
				if first == nil {
					first = err
				}
				if ctx.Err() != nil {
					break
				}
				continue
			}
			if emptied {
				n++
			}
		}
		return n, first
	}
	failed := false
	const what = "the sign-in sessions"
	w.moveFromSecrets(ctx, auth.SessionsImportID, what,
		func(ctx context.Context) (int, error) {
			return auth.CopySessions(ctx, w.database, secrets.All, idle, time.Now())
		},
		func(ctx context.Context) (int, error) {
			n, err := empty(ctx)
			failed = err != nil
			return n, err
		})
	if failed {
		w.sessionsLeft = func(ctx context.Context) bool {
			failed = false
			w.removeCopiedFromSecrets(ctx, auth.SessionsImportID, what, func(ctx context.Context) (int, error) {
				n, err := empty(ctx)
				failed = err != nil
				return n, err
			})
			return !failed
		}
	}
	w.inDatabase = append(w.inDatabase, "identity")
	return auth.NewPGSessions(w.database, nil)
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
	if left := w.sessionsLeft; left != nil {
		// Session Secrets that were copied and could not be emptied at the
		// start: until they are, a console from before the move could take
		// up a session ended since.
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if left(ctx) {
						return
					}
				}
			}
		}()
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
