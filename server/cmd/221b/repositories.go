package main

import (
	"context"
	"strings"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/knowledge"
)

// --- Settings › Repositories (0.12.1, RP-01 to RP-08) ---

// wireGitRepositories has the applications read the git repositories
// they name from Settings › Repositories, and list what names each.
func (w *wiring) wireGitRepositories() {
	if w.helmRepos == nil || w.appStore == nil {
		return
	}
	w.appStore = apps.WithRepositories(w.appStore, w.helmRepos)
	w.helmRepos.AddUsers(apps.AppRepoUses(w.appStore))
	w.helmRepos.AddRenamer(apps.AppRepoRenamer(w.appStore))
}

// wireKnowledgeRepositories has Mycroft's git sources read the
// repositories they name, and say which they name.
func (w *wiring) wireKnowledgeRepositories(svc *knowledge.Service) {
	repos := w.helmRepos
	if repos == nil || svc == nil {
		return
	}
	svc.SetGitRepos(func(ctx context.Context) (map[string]knowledge.GitRepo, error) {
		list, err := repos.GitRepos(ctx)
		if err != nil {
			return nil, err
		}
		m := make(map[string]knowledge.GitRepo, len(list))
		for name, x := range list {
			m[name] = knowledge.GitRepo{URL: x.URL, Credential: x.Credential}
		}
		return m, nil
	})
	repos.AddUsers(func(ctx context.Context) ([]apps.RepoUser, error) {
		uses, err := svc.RepoUses(ctx)
		out := make([]apps.RepoUser, 0, len(uses))
		for _, u := range uses {
			out = append(out, apps.RepoUser{Repository: u.Repository, Label: u.Label})
		}
		return out, err
	})
	repos.AddRenamer(svc.RenameGitRepository)
}

// knowledgeGit makes repositories of Mycroft's git sources' addresses.
type knowledgeGit struct{ svc *knowledge.Service }

func (k knowledgeGit) GitUsers(ctx context.Context) ([]apps.GitUser, error) {
	list, err := k.svc.GitSourceUsers(ctx)
	out := make([]apps.GitUser, 0, len(list))
	for _, u := range list {
		out = append(out, apps.GitUser{Key: u.ID, Label: u.Label, URL: u.URL, Credential: u.Credential, Waiting: u.Waiting})
	}
	return out, err
}

func (k knowledgeGit) UseRepository(ctx context.Context, key, name string) error {
	return k.svc.UseGitRepository(ctx, key, name)
}

// migrateGitRepositories makes a git repository of every address the
// applications and Mycroft's git sources typed before 0.12.1 (RP-05), on
// the leader, after their secrets became credentials: a run finds only
// the users not switched yet.
func (w *wiring) migrateGitRepositories(ctx context.Context) error {
	if w.helmRepos == nil {
		return nil
	}
	var consumers []apps.GitConsumer
	if w.appStore != nil {
		consumers = append(consumers, apps.AppGitConsumer{Store: w.appStore})
	}
	if w.knowledgeSvc != nil {
		consumers = append(consumers, knowledgeGit{w.knowledgeSvc})
	}
	created, err := w.helmRepos.MigrateGit(ctx, consumers, "the upgrade to 0.12.1")
	for _, c := range created {
		w.log.Info("a git repository was made of an address typed before 0.12.1", "repository", c.Name, "address", c.URL, "from", strings.Join(c.From, ", "))
		w.auditLog.Record(audit.Event{Action: "helmrepo.save", Who: audit.Who{Subject: "221b", Name: "the upgrade to 0.12.1"}, Target: c.Name,
			Detail: "git repository made at the upgrade: " + c.URL + ", used by " + strings.Join(c.From, ", "), Outcome: audit.OutcomeOK})
	}
	return err
}
