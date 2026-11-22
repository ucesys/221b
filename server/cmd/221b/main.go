// Command 221b serves the 221B console.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
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
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/leader"
	"github.com/ucesys/221b/server/internal/lessons"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/mail"
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
	// The console starts itself again to render one chart (apps/child.go);
	// it is not a command for people, so help does not list it.
	if len(os.Args) > 1 && os.Args[1] == apps.RenderChartCommand {
		os.Exit(apps.RenderChartMain(os.Stdin, os.Stdout))
	}
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
	authCfg  auth.Config
	authFile auth.FileConfig
	// sessionsLeft empties the session Secrets that were copied into the
	// database and could not be emptied at the start, and says whether it
	// is done (DB-66); nil when nothing is left.
	sessionsLeft func(ctx context.Context) bool
	twoPerson    *twoperson.Store
	authSvc      *auth.Service
	people       *access.Cache
	// gaugesAt is when the figures the metrics show of what is kept
	// elsewhere were last asked for, in nanoseconds (metrics.go).
	gaugesAt atomic.Int64

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

	// The database (database.go), where every record set is kept (DB-69),
	// and the domains whose records it holds, for GET /api/v1/database.
	database      *db.DB
	schemaVersion int
	inDatabase    []string
	// notCopied are the keys whose one-time copy from a ConfigMap failed
	// at this start (noteNotCopied).
	notCopied map[string]bool
	// databaseWait is how long a start waits for the database before it
	// gives up and the console stops (DB-02); 0 is the default (a test
	// sets it).
	databaseWait time.Duration
	// importIDs are the names record sets are copied under, each once;
	// importClash those named twice (claimImport).
	importIDs   map[string]bool
	importClash []string
	// sources are the ConfigMaps of before the copies read, with the
	// record sets each holds (readFrom), deleted once those are copied
	// (removeCopiedSources, DB-72); copying is the copies that run beside
	// the wiring (the audit log's), which the deletion waits for.
	sourcesMu sync.Mutex
	sources   map[string]*sourceUse
	copying   sync.WaitGroup
	// moveAgainEvery is how often a copy of the connections that failed
	// at the start is tried again; 0 is the default (a test sets it).
	moveAgainEvery time.Duration

	// The settings and what is kept beside them (settings.go): spendBackend
	// is their store, in the database. settingsSource is the one-time
	// copies' source, the settings ConfigMap of a release before 0.13 with
	// the audit log's and the session records' own (nil when none is
	// named), and stored are the ConfigMaps of before whose sizes the
	// metrics show until they are gone.
	shared         *settings.Service
	spendBackend   kv.Backend
	settingsSource kv.Backend
	stored         storedObjects

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
	helmRepos *apps.Repos
	// creds are Administration › Credentials (0.12.1, credentials.go).
	creds       *credentials.Service
	watsonSvc   *watson.Service
	toolboxCfg  toolbox.Config
	hosts       *nodessh.Hosts
	sshSvc      *nodessh.Service
	autoEngine  *automation.Engine
	autoStore   *automation.Store
	sessionsMgr *sessions.Manager
	// sessionRecords are the ended sessions' records, which the backup
	// reads and merges into.
	sessionRecords sessions.Archive

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
	// caseBackend is what the case work reads and writes through: the
	// casework ConfigMap, with what the database keeps of it routed there.
	caseBackend kv.Backend
	// cardsBackend is the same for the cards: the proposed changes'
	// ConfigMap, with what the database keeps of it routed there.
	cardsBackend kv.Backend
	// knowledgeBackend is the same for Mycroft's knowledge.
	knowledgeBackend kv.Backend
	// issueBackend is the same for the issue service.
	issueBackend kv.Backend
	mailSvc      *mail.Service
	lessonsSvc   *lessons.Service

	// The backups and the HTTP API.
	backups *backup.Scheduler
	handler *api.Server
}

func run(args []string) error { return serve(context.Background(), args, nil) }

// processLogs sets the loggers the process has one of (serve).
var processLogs sync.Once

// hooks are what a test of the whole start gives serve: where the logs
// and the audit log go, what to set on the wiring before anything is
// wired, and what to call once the console listens.
type hooks struct {
	logs, audit io.Writer
	wiring      func(w *wiring)
	serving     func(w *wiring, addr string)
}

// serve wires the console and serves it until ctx ends, a signal asks it
// to stop, or it stops itself.
func serve(parent context.Context, args []string, h *hooks) error {
	parsed, err := config.Parse(args)
	if err != nil {
		return err
	}
	if h == nil {
		h = &hooks{}
	}
	if h.logs == nil {
		h.logs = os.Stderr
	}
	if h.audit == nil {
		h.audit = os.Stdout
	}
	log := newLogger(parsed, h.logs)
	// The process's own loggers are set once: a process is one console,
	// and a test that starts a second leaves the first one's in place.
	processLogs.Do(func() {
		slog.SetDefault(log)
		// client-go logs through klog; send it to the same structured log.
		klog.SetSlogLogger(log.With("component", "client-go"))
	})
	// The hosts applications' charts are fetched from (apps.allowedHosts).
	if err := apps.SetAllowedHosts(parsed.AppsAllowedHosts); err != nil {
		return fmt.Errorf("apps.allowedHosts: %w", err)
	}

	// The audit log (P3-10) owns standard output; the ordinary logs go to
	// standard error.
	w := &wiring{cfg: parsed, log: log, auditLog: audit.New(audit.Options{Out: h.audit, WebhookURL: parsed.AuditWebhookURL, WebhookToken: parsed.AuditWebhookToken, Logger: log.With("component", "audit")})}
	if h.wiring != nil {
		h.wiring(w)
	}
	if err := w.wireAuthConfig(); err != nil {
		return err
	}
	// From here on, the configuration as sign-in left it.
	cfg := w.cfg
	setMemoryLimit(log)

	if w.restCfg, w.mode, err = kube.RestConfig(kube.ConnectOptions{Kubeconfig: cfg.Kubeconfig, Context: cfg.Context, APIServer: cfg.APIServer}); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// The database is opened before anything that keeps records is made
	// (DB-59), so each store made below can be given it: the sign-in's, the
	// connections', the applications', the credentials'. It uses nothing
	// but the configuration and the log, and nothing listens until it and
	// the rest are done, so the start's order is all that changes. A
	// database that does not answer within the wait ends the start: a
	// console does not start without its database (DB-02).
	if w.cfg.ConnectionsSecrets == "" {
		return errNoRelease
	}
	if err := w.wireDatabase(ctx); err != nil {
		return err
	}
	defer w.database.Close()
	if err := w.wireAuth(ctx); err != nil {
		return err
	}
	if err := w.wireLocalCluster(); err != nil {
		return err
	}
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
	auditStore := w.keepAudit(ctx, w.settingsSource, log.With("component", "audit"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := auditStore.Close(ctx); err != nil {
			log.Warn("saving the audit log at shutdown failed", "err", err)
		}
	}()
	if err := w.wireSherlock(ctx); err != nil {
		return err
	}
	if err := w.wireCredentials(ctx); err != nil {
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
	if len(w.importClash) > 0 {
		return fmt.Errorf("two record sets would be copied into the database under one name, and the second never would be: %s; give one a name of its own (db.Doc.ImportAs)", strings.Join(w.importClash, ", "))
	}
	// The ConfigMaps of before, each deleted once every record set it
	// held is in the database (DB-72), when every copy has run.
	go func() {
		w.copying.Wait()
		w.waitForHelmThenRemove(ctx)
	}()
	if err := w.wireAPI(); err != nil {
		return err
	}
	// Administration › Credentials (0.12.1, CR-12): the secrets kept with what
	// used them move to credentials, on the leader.
	go w.migrateCredentials(ctx)
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
			return
		}
		// Runs live on the leader: one that loses the lead stops its
		// runs, so the next leader never meets one still going.
		if w.autoEngine != nil {
			w.autoEngine.StopAll("the console running it lost the lead and stopped it; nodes mid-script were stopped, and nothing was retried")
		}
		if w.aiSvc.Service != nil {
			w.aiSvc.StopAllTests()
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
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("serving", "addr", ln.Addr().String(), "cluster", cfg.ClusterName, "mode", string(w.mode), "version", version.String(), "secrets_read", cfg.SecretsRead, "secrets_reveal", cfg.SecretsRead && cfg.SecretsReveal, "auth", cfg.AuthMode)
		errCh <- srv.Serve(ln)
	}()
	if h.serving != nil {
		h.serving(w, ln.Addr().String())
	}
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

// auditRetention is how long the database keeps an audit event for the
// Audit log page; the stream and the webhook are the full record.
const auditRetention = 365 * 24 * time.Hour

// keepAudit gives the log its store for Administration › Audit log, the
// database's audit_events (Phase 13), and returns it to close. from is
// the copy's source and nothing else: where a release before 0.13 kept
// the audit log, the settings ConfigMap or the audit log's own; nil when
// none is named.
func (w *wiring) keepAudit(ctx context.Context, from kv.Backend, logger *slog.Logger) interface {
	Close(ctx context.Context) error
} {
	l := w.auditLog
	st := audit.NewPGStore(w.database, logger)
	l.SetStore(st)
	w.claimImport(watson.MaskedImportID)
	w.inDatabase = append(w.inDatabase, "activity")
	// What a release before Phase 13 kept in the ConfigMap, once, beside
	// the rest of the start; the ConfigMap is deleted once it is copied.
	copies := hasSource(from)
	if copies {
		w.readFrom(from, audit.StoreKey, audit.StoreKey)
		w.copying.Add(1)
	}
	go func() {
		if copies {
			ictx, cancel := context.WithTimeout(ctx, time.Minute)
			n, err := st.Import(ictx, &audit.KVStore{Backend: from, Key: audit.StoreKey, Logger: logger}, "configmap key "+audit.StoreKey)
			cancel()
			switch {
			case err != nil:
				logger.Warn("copying the audit log from its ConfigMap failed; the next start tries again", "err", err)
			case n > 0:
				logger.Info("copied the audit log from its ConfigMap into the database", "events", n)
				// What a console from before wrote, after a rollback, names
				// a command's parameters with their values.
				w.hideCommandParameters(ctx, logger, true)
			}
			w.copying.Done()
		}
		w.hideCommandParameters(ctx, logger, false)
		// Retention, once a day on whichever replica gets the lock.
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			tctx, cancel := context.WithTimeout(ctx, time.Minute)
			if n, err := st.Trim(tctx, time.Now().Add(-auditRetention)); err != nil {
				logger.Warn("trimming the audit log failed", "err", err)
			} else if n > 0 {
				logger.Info("trimmed the audit log", "events", n)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return st
}

// hideCommandParameters hides the values of the commands' parameters in
// the Watson events of the audit log recorded before DB-74, as they are
// recorded since: once, under watson.MaskedImportID, and again after a
// copy from the ConfigMap (again). The stream and the webhook had them as
// they were; they are not reached.
func (w *wiring) hideCommandParameters(ctx context.Context, logger *slog.Logger, again bool) {
	hctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rewrite := func(ctx context.Context, tx pgx.Tx) (int, error) {
		return audit.Rewrite(ctx, tx, "watson.", watson.MaskedLike, watson.MaskDetail)
	}
	n := 0
	var err error
	if again {
		err = w.database.Tx(hctx, func(tx pgx.Tx) (err error) { n, err = rewrite(hctx, tx); return err })
	} else {
		_, err = w.database.ImportOnce(hctx, watson.MaskedImportID, "audit_events", func(ctx context.Context, tx pgx.Tx) (int, error) {
			var err error
			n, err = rewrite(ctx, tx)
			return n, err
		})
	}
	switch {
	case err != nil:
		logger.Warn("hiding the commands' parameters in the audit log's Watson events failed; the next start tries again", "err", err)
	case n > 0:
		logger.Info("hid the values of the commands' parameters in the audit log's Watson events recorded before", "events", n)
	}
}

func newLogger(cfg config.Config, out io.Writer) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}
