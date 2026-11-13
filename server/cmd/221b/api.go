package main

import (
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/imagecheck"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/ui"
	"github.com/ucesys/221b/server/internal/version"
)

// The console's HTTP API.

// wireAPI makes the console's HTTP handler of everything opened before.
func (w *wiring) wireAPI() error {
	cfg, authCfg := w.cfg, w.authCfg
	rolePrefixes := authCfg.Prefixes
	if def := roles.DefaultPrefixes(); rolePrefixes.Local == "" || rolePrefixes.LDAP == "" {
		if rolePrefixes.Local == "" {
			rolePrefixes.Local = def.Local
		}
		if rolePrefixes.LDAP == "" {
			rolePrefixes.LDAP = def.LDAP
		}
	}
	proxies, err := auth.ParseProxies(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	// Newer images (HA-21, ADR-154): the leader asks the registries,
	// anonymously, in the background.
	images := imagecheck.New(imagecheck.Options{
		Client: &imagecheck.Client{UserAgent: "221B/" + version.String()},
		Active: w.activity.Active,
		Logger: w.log.With("component", "imagecheck"),
	})
	handler := api.New(api.Options{
		SSH:                     w.sshSvc,
		KV:                      w.spendBackend,
		Version:                 version.String(),
		Watson:                  w.watsonSvc,
		Apps:                    w.appStore,
		Repos:                   w.helmRepos, // Helm repositories (Phase 12, wave 5)
		ImageChecks:             images,
		SecretsReveal:           cfg.SecretsRead && cfg.SecretsReveal,
		Sessions:                w.sessionsMgr,
		Automation:              w.autoEngine,
		AutomationStore:         w.autoStore,
		TwoPerson:               w.twoPerson,
		Hosts:                   w.sshSvc,
		Backup:                  backup.Default,
		BackupS3:                w.backups,
		Issues:                  w.issueAPI,
		IssueRoles:              issueRoles(cfg),
		Jobs:                    w.jobsW.svc,
		Knowledge:               w.knowledgeSvc,
		Fixes:                   w.fixesSvc,
		Lessons:                 w.lessonsSvc,   // Findings, exceptions and RCAs (Phase 12)
		Cards:                   w.cardStore,    // Watson cards (Phase 12)
		CardRoles:               cardRoles(cfg), // Watson cards (Phase 12)
		Casework:                w.caseSvc,      // Case work (Phase 12)
		Mail:                    w.mailSvc,      // Settings › Email (EM-01)
		Sherlock:                w.sherlockSvc,
		SherlockKeys:            w.keys,
		AI:                      w.aiSvc.Service,
		SherlockDefaultURL:      cfg.SherlockAPIURL,
		SherlockProviderLocked:  cfg.SherlockLockProvider,
		SherlockDefaultProvider: w.chartProvider,
		Store:                   w.store,
		Cluster:                 w.cluster,
		Settings:                w.shared,
		Clusters:                w.manager,
		Auth:                    w.authSvc,
		Audit:                   w.auditLog,
		Access:                  w.people,
		Impersonation:           api.Impersonation{UserPrefix: cfg.ImpersonateUserPrefix, GroupPrefix: cfg.ImpersonateGroupPrefix},
		Roles:                   api.RolesOptions{Templates: w.authFile.RoleTemplates(), Secrets: cfg.SecretsRead, Exec: w.authFile.Templates.AdminExec, Enforce: w.authFile.RBACEnforce, Prefixes: rolePrefixes, Bindings: w.authFile.Bindings},
		HTTPSRedirect:           cfg.HTTPSRedirect,
		Leader:                  w.elector,
		Active:                  w.activity.Active,
		Standby:                 w.activity.Standby,
		Proxies:                 proxies,
		ProxyToken:              cfg.ProxyToken,
		Ready:                   w.cluster.Ready,
		UI:                      ui.Handler(),
		Logger:                  w.log.With("component", "http"),
		AllowedHosts:            cfg.AllowedHosts,
		Gauges:                  w.gauges,
	})
	w.handler = handler
	return nil
}
