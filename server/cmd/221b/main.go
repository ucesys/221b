// Command 221b serves the 221B console.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/leader"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/toolbox"
	"github.com/ucesys/221b/server/internal/twoperson"
	"github.com/ucesys/221b/server/internal/version"
	"github.com/ucesys/221b/server/internal/watson"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := hashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, "221b:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "221b:", err)
		os.Exit(1)
	}
}

// wiring is the console as run wires it (A-3): each feature's wireX, in a
// file of its own, reads what the features before it opened and keeps
// what it opens here. run calls them in order, then serves.
type wiring struct {
	// The configuration, the logs and the connection to the cluster the
	// console runs against.
	cfg      config.Config
	log      *slog.Logger
	auditLog *audit.Log
	restCfg  *rest.Config
	mode     kube.Mode

	// Sign-in (auth.go).
	authCfg      auth.Config
	authFile     auth.FileConfig
	sessionStore *kube.SessionStore
	twoPerson    *twoperson.Store
	authSvc      *auth.Service
	people       *access.Cache

	// The clusters (clusters.go) and the election (leader.go).
	cluster    *kube.Cluster
	activity   *leader.Activity
	store      *live.Store
	manager    *clusters.Manager
	appStore   apps.Store
	elector    *leader.Elector
	leading    chan struct{}
	lost       chan struct{}
	leaderDone chan struct{}

	// The settings and what is kept beside them (settings.go); stored are
	// the ConfigMaps whose sizes the metrics show.
	shared       *settings.Service
	spendBackend kv.Backend
	stored       storedObjects

	// Sherlock and the agents (sherlock.go). Sherlock's ask_knowledge reads
	// the last four once they are opened, further on.
	keys          *sherlock.Keys
	secretRef     string
	chartProvider sherlock.Provider
	aiSvc         aiService
	sherlockSvc   *sherlock.Service
	knowledgeSvc  *knowledge.Service
	knownSvc      *issues.Service
	fixesSvc      *fixes.Service
	exceptionsSvc *lessons.Service

	// Watson, the toolbox, SSH and Automation.
	helmRepos   *apps.Repos
	watsonSvc   *watson.Service
	toolboxCfg  toolbox.Config
	hosts       *nodessh.Hosts
	sshSvc      *nodessh.Service
	autoEngine  *automation.Engine
	autoStore   *automation.Store
	sessionsMgr *sessions.Manager

	// The jobs, the cases, Mycroft's knowledge and the proposed changes.
	jobsW            *jobsWiring
	issueSvc         *issues.Service
	issueAPI         *issues.Service
	issueStored      *kv.Limited
	mycroftProposals *knowledgeProposer
	knowledgeStored  *kv.Limited
	cardStore        *cards.Store
	cardsStored      *kv.Limited
	caseSvc          *casework.Service
	caseStored       *kv.Limited
	lessonsSvc       *lessons.Service

	// The backups and the HTTP API.
	backups *backup.Scheduler
	handler *api.Server
}

func run(args []string) error {
	parsed, err := config.Parse(args)
	if err != nil {
		return err
	}
	log := newLogger(parsed)
	slog.SetDefault(log)

	// The audit log (P3-10) owns standard output; the ordinary logs go to
	// standard error.
	w := &wiring{cfg: parsed, log: log, auditLog: audit.New(audit.Options{Out: os.Stdout, WebhookURL: parsed.AuditWebhookURL, WebhookToken: parsed.AuditWebhookToken, Logger: log.With("component", "audit")})}
	if err := w.wireAuthConfig(); err != nil {
		return err
	}
	// From here on, the configuration as sign-in left it.
	cfg := w.cfg
	// client-go logs through klog; send it to the same structured log.
	klog.SetSlogLogger(log.With("component", "client-go"))
	setMemoryLimit(log)

	if w.restCfg, w.mode, err = kube.RestConfig(kube.ConnectOptions{Kubeconfig: cfg.Kubeconfig, Context: cfg.Context, APIServer: cfg.APIServer}); err != nil {
		return err
	}
	if err := w.wireAuth(); err != nil {
		return err
	}
	if err := w.wireLocalCluster(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w.startAuth(ctx)
	if err := w.wireLeader(ctx); err != nil {
		return err
	}
	if err := w.wireClusters(ctx); err != nil {
		return err
	}
	if err := w.wireSettings(ctx); err != nil {
		return err
	}
	if err := w.manager.Start(ctx); err != nil {
		return fmt.Errorf("cluster connections: %w", err)
	}
	// Connections added, changed or removed through another replica.
	go w.manager.Run(ctx, 5*time.Second)
	w.wireAccess(ctx)
	if err := w.wireSherlockKeys(ctx); err != nil {
		return err
	}
	w.twoPerson.SetBackend(w.spendBackend)
	if auditStore := keepAudit(w.auditLog, w.spendBackend, log.With("component", "audit")); auditStore != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := auditStore.Close(ctx); err != nil {
				log.Warn("saving the audit log at shutdown failed", "err", err)
			}
		}()
	}
	if err := w.wireSherlock(ctx); err != nil {
		return err
	}
	if err := w.wireHelmRepos(); err != nil {
		return err
	}
	w.wireWatson(ctx)
	if err := w.wireSSH(ctx); err != nil {
		return err
	}
	if err := w.wireAutomation(ctx); err != nil {
		return err
	}
	if err := w.wireJobs(ctx); err != nil {
		return err
	}
	if err := w.wireIssues(ctx); err != nil {
		return err
	}
	if err := w.wireKnowledge(ctx); err != nil {
		return err
	}
	if err := w.wireCards(ctx); err != nil {
		return err
	}
	if err := w.wireCasework(ctx); err != nil {
		return err
	}
	w.wireSessions(ctx)
	if err := w.wireBackups(ctx); err != nil {
		return err
	}
	if err := w.wireAPI(); err != nil {
		return err
	}
	// Jobs (Phase 12): the agents read through the handler's tools as
	// the reader identity; the schedules and alerts are looked at on the
	// leader.
	w.jobsW.svc.SetAgents(agentRunner{ai: w.aiSvc.Service, engine: w.sherlockSvc, handler: w.handler, auth: w.authSvc})
	go w.jobsW.svc.Run(ctx)
	// The deployed roles are compared with the shipped ones every five
	// minutes, for the Permissions page, the audit log and rbac.enforce;
	// on the leader only, and at once when a replica becomes it.
	go w.handler.RunRoleChecks(ctx)
	w.activity.OnChange(func(active bool) {
		if active {
			w.handler.CheckRolesSoon()
		} else if w.autoEngine != nil {
			// Runs live on the leader: one that loses the lead stops its
			// runs, so the next leader never meets one still going.
			w.autoEngine.StopAll("the console running it lost the lead and stopped it; nodes mid-script were stopped, and nothing was retried")
		}
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           w.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: event streams stay open; each write sets its own
		// deadline instead. Requests share ctx, so streams end on shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("serving", "addr", cfg.Listen, "cluster", cfg.ClusterName, "mode", string(w.mode), "version", version.String(), "secrets_read", cfg.SecretsRead, "secrets_reveal", cfg.SecretsRead && cfg.SecretsReveal, "auth", cfg.AuthMode)
		errCh <- srv.ListenAndServe()
	}()
	lostLease := false
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	case <-w.lost:
		lostLease = true
		log.Error("the Lease was lost; a cold standby cannot watch again, so this replica exits and starts afresh (replicas.warmStandby=false)")
		stop()
	}
	log.Info("shutting down")
	workCtx, cancelWork := context.WithTimeout(context.Background(), workWithin(cfg.TerminationGrace))
	defer cancelWork()
	shutdownCtx, cancel := context.WithTimeout(workCtx, 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	// Answers and commands outlive their requests, and each writes its
	// audit record as it ends, so the log closes after them.
	var work sync.WaitGroup
	work.Go(func() { w.sherlockSvc.Drain(workCtx, settleWithin) })
	work.Go(func() { w.watsonSvc.Drain(workCtx, settleWithin) })
	work.Wait()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), closeWithin)
	defer cancelClose()
	w.auditLog.Close(closeCtx)
	// The leader gives its Lease up on the way out, so another replica
	// takes over at once.
	select {
	case <-w.leaderDone:
	case <-closeCtx.Done():
	}
	if lostLease {
		return errors.New("the Lease was lost; a cold standby exits to start afresh")
	}
	return err
}

// What a stopping console does inside the pod's termination grace period:
// answers and commands get what is left of it after these. settleWithin is
// for those still running to end once stopped (a helm upgrade marks its
// release failed, rather than leave it pending), closeWithin for the audit
// log to reach its store and webhook.
const (
	settleWithin = 2 * time.Second
	closeWithin  = 3 * time.Second
	stopMargin   = time.Second
)

// workWithin is how long a stopping console waits for answers and commands
// to finish by themselves, counted from the signal.
func workWithin(grace time.Duration) time.Duration {
	return max(grace-settleWithin-closeWithin-stopMargin, 0)
}

// keepAudit gives the log its store for Administration › Audit log, and
// returns it to close: beside the settings, or in memory on a laptop, where
// the page says so.
func keepAudit(l *audit.Log, b kv.Backend, logger *slog.Logger) *audit.KVStore {
	if _, inMemory := b.(*kv.Memory); inMemory {
		l.KeepInMemory(audit.DefaultKeep)
		return nil
	}
	st := audit.NewKVStore(b, logger)
	l.SetStore(st)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := st.Sweep(ctx); err != nil {
			logger.Warn("cleaning the kept audit log failed", "err", err)
		}
	}()
	return st
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
