package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
)

// --- Administration › Credentials (0.12.1, CR-01 to CR-14) ---

// wireCredentials opens the credentials: their secrets in the Secret
// <release>-credentials when the console may keep Secrets, with their
// list in the database (DB-60); in memory, both, without the Secret. The
// applications, the Helm repositories and Mycroft's sources say which
// ones they use (Used by): the API registers the first two, the knowledge
// the last.
func (w *wiring) wireCredentials(ctx context.Context) error {
	var secrets credentials.SecretStore = &credentials.MemorySecret{}
	inSecret := w.cfg.ConnectionsSecrets != ""
	if inSecret {
		ns, prefix, _ := strings.Cut(w.cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(w.restCfg, ns, prefix+"-credentials", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "credentials", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return fmt.Errorf("the credentials' Secret: %w", err)
		}
		secrets = sk
	}
	w.creds = credentials.New(credentials.Options{Secrets: secrets, Repo: w.credentialsList(ctx, secrets, inSecret), Logger: w.log.With("component", "credentials")})
	return nil
}

// credentialsList is where the credentials' list is kept: the database's
// rows (DB-60), when the secrets are kept in a Secret. What the Secret of
// a release before 0.13 held under "index" is copied once and then leaves
// it (DB-61); the secrets stay there, untouched.
//
// With the secrets in memory (no -connections-secrets: a console run by
// hand, connections.store: memory) the list stays in memory too, and nil
// is returned: a list that outlived the secrets it names would show, after
// a restart, credentials that no use could send.
func (w *wiring) credentialsList(ctx context.Context, secrets credentials.SecretStore, inSecret bool) credentials.Repo {
	if !inSecret {
		w.claimImport(credentials.ListImportID)
		return nil
	}
	w.moveFromSecrets(ctx, credentials.ListImportID, "the credentials' list",
		func(ctx context.Context) (int, error) { return credentials.CopyList(ctx, w.database, secrets) },
		func(ctx context.Context) (int, error) {
			removed, err := credentials.RemoveList(ctx, secrets)
			if removed {
				return 1, err
			}
			return 0, err
		})
	list := credentials.NewPGRepo(w.database)
	// The two halves that do not go together, said once at the start: a
	// database restored to an earlier time, or a write cut short. Nothing
	// is removed on that guess.
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if noRow, noSecret, err := credentials.Unmatched(cctx, list, secrets); err != nil {
		w.dbLog().Warn("the credentials' list and their secrets could not be compared", "err", err)
	} else if len(noRow) > 0 || len(noSecret) > 0 {
		w.dbLog().Warn("the credentials' list in the database and the secrets in their Secret do not match: a secret without a credential is kept and never used (a SuperAdmin removes it with DELETE /api/v1/credential-secrets/{id}), and a credential without its secret cannot be used until it is given again in Administration › Credentials",
			"secrets_without_credential", noRow, "credentials_without_secret", noSecret)
	}
	return list
}

// appCredentials move the applications' tokens of before 0.12.1 to
// credentials (CR-12); the API registers their uses.
type appCredentials struct{ store apps.Store }

// Find lists the applications that keep a token of before 0.12.1 for
// their git repository: GitHub took it as the password of x-access-token,
// so it becomes a token sent as Basic for that user, for the repository's
// host and owner.
func (a appCredentials) Find(ctx context.Context) ([]credentials.Found, []string, error) {
	recs, err := a.store.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []credentials.Found
	for _, rec := range recs {
		if !rec.HasToken || rec.CredMigrated || rec.Source.FromRepository() {
			continue
		}
		_, tok, err := a.store.Get(ctx, rec.Cluster, rec.Namespace, rec.Name)
		if err != nil {
			return nil, nil, err
		}
		if tok == "" {
			continue
		}
		out = append(out, credentials.Found{
			Key: rec.Cluster + "/" + rec.Namespace + "/" + rec.Name, Label: apps.Label(rec),
			Endpoint: apps.RepoOwner(rec.Source.Repo), Kind: credentials.KindToken,
			SendAs: credentials.SendAs{Method: credentials.SendBasic, Username: "x-access-token"},
			Secret: credentials.Secret{Token: tok},
		})
	}
	return out, nil, nil
}

// Switch has an application name its credential; its token stays in its
// Secret, no longer read.
func (a appCredentials) Switch(ctx context.Context, key, credential string) error {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return fmt.Errorf("no application %q", key)
	}
	var err error
	for range 3 {
		var rec apps.Record
		if rec, _, err = a.store.Get(ctx, parts[0], parts[1], parts[2]); err != nil {
			return err
		}
		rec.Credential, rec.CredMigrated = credential, true
		if err = a.store.Save(ctx, rec, nil); !errors.Is(err, apps.ErrConflict) {
			return err
		}
	}
	return err
}

// repoCredentials move the Helm repositories' passwords and tokens of
// before 0.12.1 to credentials (CR-12).
type repoCredentials struct{ repos *apps.Repos }

// Find lists the repositories that keep a password or token: a user and
// a password becomes one, a token one sent as Bearer, for the
// repository's address.
func (r repoCredentials) Find(ctx context.Context) ([]credentials.Found, []string, error) {
	list, secrets, err := r.repos.Legacy(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]credentials.Found, 0, len(list))
	for i, x := range list {
		f := credentials.Found{Key: x.Name, Label: "Helm repository " + x.Name, Endpoint: x.URL}
		if x.Auth == apps.AuthBasic {
			f.Kind, f.Username, f.Secret.Password = credentials.KindUserPass, x.Username, secrets[i]
		} else {
			f.Kind, f.Secret.Token = credentials.KindToken, secrets[i]
			f.SendAs = credentials.SendAs{Method: credentials.SendBearer}
		}
		out = append(out, f)
	}
	return out, nil, nil
}

func (r repoCredentials) Switch(ctx context.Context, name, credential string) error {
	return r.repos.SwitchCredential(ctx, name, credential)
}

// migrateCredentials moves the secrets kept with what used them to
// credentials (CR-12): once this replica leads, and every quarter of an
// hour after, so a backup of before 0.12.1 restored moves too. A
// consumer moved is marked so, and each run finds only the others.
func (w *wiring) migrateCredentials(ctx context.Context) {
	var consumers []credentials.Consumer
	if w.appStore != nil {
		consumers = append(consumers, appCredentials{w.appStore})
	}
	if w.helmRepos != nil {
		consumers = append(consumers, repoCredentials{w.helmRepos})
	}
	if w.knowledgeSvc != nil {
		consumers = append(consumers, knowledge.CredentialConsumer{S: w.knowledgeSvc})
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	next := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !w.elector.IsLeader() || time.Now().Before(next) {
			continue
		}
		next = time.Now().Add(15 * time.Minute)
		mctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		created, err := w.creds.Migrate(mctx, consumers, "the upgrade to 0.12.1")
		// Then the git addresses become repositories of Administration › Settings ›
		// Repositories (RP-05), with the credentials just made.
		if err == nil {
			if gerr := w.migrateGitRepositories(mctx); gerr != nil {
				w.log.Warn("making repositories of the git addresses typed before 0.12.1 stopped; it runs again later", "err", gerr)
				next = time.Now().Add(time.Minute)
			}
		}
		cancel()
		for _, c := range created {
			w.log.Info("a credential was made of secrets kept before 0.12.1", "credential", c.Name, "from", strings.Join(c.From, ", "))
			w.auditLog.Record(audit.Event{Action: "credential.save", Who: audit.Who{Subject: "221b", Name: "the upgrade to 0.12.1"}, Target: c.Name, Detail: "credential made at the upgrade from " + strings.Join(c.From, ", ") + "; their own secrets stay where they were, no longer read", Outcome: audit.OutcomeOK})
		}
		if err != nil {
			w.log.Warn("moving the kept secrets to Administration › Credentials stopped; it runs again later", "err", err)
			next = time.Now().Add(time.Minute)
		}
	}
}

// registerCredentialsBackup adds the credentials to every backup (CR-13,
// order 35, before the applications, repositories and sources that name
// them): each secret sealed with the install's backup key (ADR-137). A
// restore adds the ones missing, by id; one whose secret the backup's key
// does not open is listed to give again.
func registerCredentialsBackup(r *backup.Registry, creds *credentials.Service) {
	if creds == nil {
		return
	}
	r.Register(backup.Part{Name: "credentials", Label: "Credentials", Order: 35,
		How: "are added where missing, by id, with their secrets when the backup's key opens them",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			list, err := creds.Export(ctx, env.Seal)
			if err != nil || len(list) == 0 {
				return nil, err
			}
			return list, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var list []credentials.BackupEntry
			if err := json.Unmarshal(raw, &list); err != nil {
				return 0, err
			}
			return creds.Import(ctx, list, backup.CheckSecret, env.Open, env.Note)
		},
	})
}
