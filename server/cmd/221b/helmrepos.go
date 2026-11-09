package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- Helm repositories (Phase 12, wave 5, HR-01 to HR-12, ADR-123 to
// ADR-125) ---

// openHelmRepos opens the chart repositories: their list in the settings
// ConfigMap (the key apps.RepositoriesKey), their passwords and tokens
// in the Secret <release>-helm-repositories when the console keeps its
// connections in Secrets, in memory otherwise.
func openHelmRepos(cfg config.Config, restCfg *rest.Config, settings kv.Backend) (*apps.Repos, error) {
	var secrets apps.RepoSecrets = &apps.MemorySecret{}
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(restCfg, ns, prefix+"-helm-repositories", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "helm-repositories", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return nil, fmt.Errorf("the Helm repositories' Secret: %w", err)
		}
		secrets = sk
	}
	return apps.NewRepos(apps.RepoOptions{Backend: settings, Secrets: secrets}), nil
}

// registerHelmReposBackup adds the chart repositories to every backup
// (HR-10, order 290): each repository with its password or token sealed
// with the install's backup key (ADR-137). A restore adds the ones
// missing, by name, and leaves the ones the console has; a repository
// whose password or token the backup's key does not open is restored
// without one, and listed to be given again in Settings › Applications.
func registerHelmReposBackup(r *backup.Registry, repos *apps.Repos) {
	if repos == nil {
		return
	}
	type entry struct {
		apps.Repository
		Secret string `json:"secret,omitempty"`
	}
	r.Register(backup.Part{Name: "helmRepositories", Label: "Helm repositories", Order: 290,
		How: "are added where missing, by name, with their passwords and tokens when the backup's key opens them",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			list, err := repos.List(ctx)
			if err != nil || len(list) == 0 {
				return nil, err
			}
			out := make([]entry, 0, len(list))
			for _, x := range list {
				e := entry{Repository: x}
				if x.HasSecret {
					_, secret, err := repos.Get(ctx, x.Name)
					if err != nil {
						return nil, err
					}
					e.Secret = env.Seal(secret)
				}
				out = append(out, e)
			}
			return out, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var list []entry
			if err := json.Unmarshal(raw, &list); err != nil {
				return 0, err
			}
			have, err := repos.List(ctx)
			if err != nil {
				return 0, err
			}
			names := map[string]bool{}
			for _, x := range have {
				names[x.Name] = true
			}
			n := 0
			for _, e := range list {
				if err := backup.CheckSecret(e.Secret); err != nil {
					return n, fmt.Errorf("the Helm repository %s: %w", e.Name, err)
				}
				if names[e.Name] {
					continue
				}
				in := apps.RepositoryInput{Kind: e.Kind, URL: e.URL, Auth: e.Auth, Username: e.Username}
				if e.HasSecret || e.Auth != apps.AuthNone {
					name := fmt.Sprintf("the %s of the Helm repository %s", map[bool]string{true: "token", false: "password"}[e.Auth == apps.AuthToken], e.Name)
					where := "Settings › Applications › Repositories › " + e.Name
					secret, _ := env.Open(e.Secret, name, where)
					if secret != "" {
						in.Secret = &secret
					} else {
						// Without its password or token it is kept as needing
						// none, until a SuperAdmin gives it again.
						in.Auth, in.Username = apps.AuthNone, ""
						if e.Secret == "" {
							env.SetAgain(name, where)
						}
					}
				}
				if _, _, err := repos.Save(ctx, e.Name, in, env.By); err != nil {
					env.Note("Helm repository %s: %v", e.Name, err)
					continue
				}
				n++
			}
			return n, nil
		},
	})
}
