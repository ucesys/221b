package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/version"
)

// registerBackupParts adds the Phase 12 packages' records to every backup
// (ADR-80): the AI agents' providers, agents, instructions, test cases and
// pilots, each provider's credential sealed with the install's backup key
// (ADR-137), and the issue service's reports, known issues, services and
// settings, without its API tokens.
func registerBackupParts(r *backup.Registry, agents *ai.Service, issueSvc *issues.Service) {
	if agents != nil {
		r.Register(backup.Part{Name: "ai", Label: "AI agents", Order: 200,
			How: "replace the current ones: providers, agents, their instructions and versions, test cases and pilots; a provider's credential comes back where this console has none",
			Export: func(ctx context.Context, env *backup.Env) (any, error) {
				keys, err := agents.Export(ctx, env.Seal)
				if err != nil || len(keys) == 0 {
					return nil, err
				}
				return keys, nil
			},
			Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
				var keys map[string]json.RawMessage
				if err := json.Unmarshal(raw, &keys); err != nil {
					env.Note("AI agents: %v", err)
					return 0, backup.ErrNotApplied
				}
				creds, err := aiCredentials(keys)
				if err != nil {
					env.Note("AI agents: %v", err)
					return 0, backup.ErrNotApplied
				}
				if err := agents.Import(ctx, keys); err != nil {
					env.Note("AI agents: %v", err)
					return 0, backup.ErrNotApplied
				}
				for _, c := range creds {
					if agents.KeysFor(c.id).Info().Set {
						continue
					}
					name, where := "the credential of the AI provider "+c.name, "Settings › AI › Providers › "+c.name
					key, _ := env.Open(c.sealed, name, where)
					if key == "" {
						continue
					}
					if err := agents.SetCredential(ctx, c.id, key, env.By); err != nil {
						env.Note("AI agents: the credential of %s was not saved: %v", c.name, err)
						env.SetAgain(name, where)
					}
				}
				return len(keys), nil
			},
		})
	}
	if issueSvc != nil {
		r.Register(backup.Part{Name: "issues", Label: "Issues", Order: 210,
			How:     "are added where missing: known issues and services, and the cases' numbering (the cases themselves are in the cases' part); the issue settings are replaced",
			LeftOut: []string{"the issue service's API tokens (make new ones after a restore)"},
			Export: func(ctx context.Context, env *backup.Env) (any, error) {
				b, err := issueSvc.Export(ctx)
				if err != nil {
					return nil, err
				}
				return json.RawMessage(b), nil
			},
			Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
				res, err := issueSvc.Import(ctx, raw, true, issues.Person{ID: env.By})
				if err != nil {
					env.Note("issues: %v", err)
					return 0, backup.ErrNotApplied
				}
				if res.Kept > 0 {
					env.Note("issues: %d records were here already and stayed as they are", res.Kept)
				}
				return res.Known + res.Services, nil
			},
		})
	}
}

// registerKnowledgeBackup adds Mycroft's knowledge to every backup (Phase
// 12): its sources and where the wiki is kept, each credential sealed with
// the install's backup key (ADR-137), the manifest of what was read, and the Confluence
// publisher's settings and pages. The wiki's pages have their own history
// in git or S3, and the index is built again.
func registerKnowledgeBackup(r *backup.Registry, svc *knowledge.Service) {
	if svc == nil {
		return
	}
	r.Register(backup.Part{Name: "knowledge", Label: "Knowledge", Order: 230,
		How: "replace the current ones: Mycroft's sources and where the wiki is kept, the manifest of what was read, and the Confluence publisher's settings; a credential comes back where this console has none",
		LeftOut: []string{
			"the wiki's pages, which keep their own history in git or S3, and the search index, which is built again",
		},
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			part, err := svc.Export(ctx, env.Seal)
			if err != nil || part == nil {
				return nil, err
			}
			return part, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			n, notes, err := svc.Import(ctx, raw, env.Open)
			for _, note := range notes {
				env.Note("%s", note)
			}
			if err != nil {
				env.Note("knowledge: %v", err)
				return 0, backup.ErrNotApplied
			}
			return n, nil
		},
	})
}

// --- Jobs (Phase 12) ---

// registerJobsBackup adds the jobs to every backup (JB-21): what each job
// does, when, on which clusters, its budget and state, and who may edit
// jobs. The runs and findings are left out: a history, possibly large,
// that the next runs make again.
func registerJobsBackup(r *backup.Registry, svc *jobs.Service) {
	if svc == nil {
		return
	}
	r.Register(backup.Part{Name: "jobs", Label: "Jobs", Order: 220,
		How:     "replace the current ones: each job's schedule or event, clusters, budget and state, and who may edit jobs; their schedules count from the restore",
		LeftOut: []string{"the jobs' runs and findings (a history; the next runs make new ones)"},
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			x, err := svc.Export(ctx)
			if err != nil || x == nil {
				return nil, err
			}
			return x, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			n, err := svc.Import(ctx, raw, env.By)
			if err != nil {
				env.Note("jobs: %v", err)
				return 0, backup.ErrNotApplied
			}
			return n, nil
		},
	})
}

// --- Findings, exceptions and RCAs (Phase 12) ---

// registerFixesBackup adds the fixes to every backup (order 265, wave 6g):
// added where missing, by id.
func registerFixesBackup(r *backup.Registry, svc *fixes.Service) {
	if svc == nil {
		return
	}
	r.Register(backup.Part{Name: "fixes", Label: "Fixes", Order: 265,
		How: "are added where missing, by id: the fixes, active, pending and inactive, with the cases each fixed",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			b, err := svc.Export(ctx)
			if err != nil {
				return nil, err
			}
			return json.RawMessage(b), nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			n, err := svc.Import(ctx, raw)
			if err != nil {
				env.Note("fixes: %v", err)
				return n, backup.ErrNotApplied
			}
			return n, nil
		},
	})
}

// registerLessonsBackup adds the accepted exceptions and the RCAs to every
// backup (FX-11, order 270): added where missing, by id, on a restore.
func registerLessonsBackup(r *backup.Registry, svc *lessons.Service) {
	if svc == nil {
		return
	}
	r.Register(backup.Part{Name: "lessons", Label: "Exceptions and RCAs", Order: 270,
		How: "are added where missing: the accepted exceptions (removed and expired ones too) and the cases' RCAs",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			x, err := svc.Export(ctx)
			if err != nil || x == nil {
				return nil, err
			}
			return x, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			n, err := svc.Import(ctx, raw)
			if err != nil {
				env.Note("exceptions and RCAs: %v", err)
				return n, backup.ErrNotApplied
			}
			return n, nil
		},
	})
}

// --- Watson cards (Phase 12, ADR-99) ---

// registerCardsBackup adds the agents' proposals to every backup (order
// 240): the cards with their decision history, the standing approvals and
// the notification settings, each channel's address sealed with the
// install's backup key (ADR-137).
func registerCardsBackup(r *backup.Registry, st *cards.Store) {
	if st == nil {
		return
	}
	r.Register(backup.Part{Name: "cases", Label: "Agents' proposed changes", Order: 240,
		How: "are added where missing, with their decisions; standing approvals are added or replaced; the notification settings are replaced, a channel's address coming back where this console has none",
		Export: func(ctx context.Context, env *backup.Env) (any, error) {
			b, err := st.Export(ctx, env.Seal)
			if err != nil {
				return nil, err
			}
			if len(b.Cards) == 0 && len(b.Standing) == 0 && b.Notify == nil {
				return nil, nil
			}
			return b, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
			var b cards.Backup
			if err := json.Unmarshal(raw, &b); err != nil {
				env.Note("agents' proposed changes: %v", err)
				return 0, backup.ErrNotApplied
			}
			if b.Notify != nil {
				for _, ch := range b.Notify.Channels {
					if err := backup.CheckSecret(ch.URL); err != nil {
						env.Note("agents' proposed changes: the channel %q holds an address in clear, not a sealed one or the placeholder; it was not restored", ch.Name)
						return 0, backup.ErrNotApplied
					}
				}
			}
			n, notes, err := st.Import(ctx, b, env.Open)
			for _, line := range notes {
				env.Note("%s", line)
			}
			if err != nil {
				env.Note("agents' proposed changes: %v", err)
				return n, backup.ErrNotApplied
			}
			return n, nil
		},
	})
}

// --- end of Watson cards.

// aiCredential is a provider's credential as a backup carries it.
type aiCredential struct{ id, name, sealed string }

// aiCredentials reads the providers' credentials from the AI part, and
// refuses one in clear.
func aiCredentials(keys map[string]json.RawMessage) ([]aiCredential, error) {
	raw, ok := keys[ai.ProvidersKey]
	if !ok {
		return nil, nil
	}
	var ps struct {
		List []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Credential string `json:"credential"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, err
	}
	var out []aiCredential
	for _, p := range ps.List {
		if p.Credential == "" || p.ID == "" {
			continue
		}
		if err := backup.CheckSecret(p.Credential); err != nil {
			return nil, fmt.Errorf("the provider %s: %w", p.ID, err)
		}
		out = append(out, aiCredential{id: p.ID, name: cmp.Or(p.Name, p.ID), sealed: p.Credential})
	}
	return out, nil
}

// wireBackups opens the backups and registers each package's part.
func (w *wiring) wireBackups(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Backups (Phase 12, ADR-80 to ADR-82): every package registers
	// its part of a backup in backup.Default (the API registers the
	// console's own). The scheduled backups to S3 keep their settings
	// beside the console's, their keys in the Secret <release>-backup, and
	// run on the leader only.
	var backupKeys backup.SecretStore = &backup.MemorySecret{}
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(w.restCfg, ns, prefix+"-backup", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "backup", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return fmt.Errorf("the backups' Secret: %w", err)
		}
		backupKeys = sk
	}
	// The backups' key (ADR-137): the Secret <release>-backup-key, made
	// here at start when there is none, and read again for every backup
	// and restore, so a key put back by hand is the one used. Never logged.
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		name := prefix + backup.KeySecretSuffix
		ks, err := kube.NewSecretKey(w.restCfg, ns, name, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "backup-key", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return fmt.Errorf("the backup key's Secret: %w", err)
		}
		ks.WithAnnotations(map[string]string{"helm.sh/resource-policy": "keep"})
		if k, err := backup.EnsureSecretsKey(ctx, ks); err != nil {
			log.Warn("the backup key is not ready: backups fail until it is", "secret", ns+"/"+name, "err", err)
		} else {
			log.Info("backup key ready", "secret", ns+"/"+name, "fingerprint", k.Fingerprint())
		}
		backup.Default.SetKey(name, func(ctx context.Context) (*backup.SecretsKey, error) { return backup.EnsureSecretsKey(ctx, ks) })
	}
	backups := backup.NewScheduler(backup.Options{Registry: backup.Default, Store: w.spendBackend, Secrets: backupKeys, Leads: w.elector.IsLeader, Audit: w.auditLog, Version: version.String(), Logger: log.With("component", "backup")})
	go backups.Run(ctx)
	registerBackupParts(backup.Default, w.aiSvc.Service, w.issueSvc)
	// Jobs (Phase 12): the backups to S3 show as a job, and the jobs'
	// settings are in every backup.
	jobs.Register(jobs.BackupKind(backups))
	registerJobsBackup(backup.Default, w.jobsW.svc)
	registerKnowledgeBackup(backup.Default, w.knowledgeSvc)
	registerCardsBackup(backup.Default, w.cardStore) // Watson cards (Phase 12)
	registerFixesBackup(backup.Default, w.fixesSvc)
	registerLessonsBackup(backup.Default, w.lessonsSvc)  // Findings, exceptions and RCAs (Phase 12)
	registerHelmReposBackup(backup.Default, w.helmRepos) // Helm repositories (Phase 12, wave 5)
	// Case work (Phase 12): the cases in every backup.
	registerCaseworkBackup(backup.Default, w.caseSvc)
	w.backups = backups
	return nil
}
