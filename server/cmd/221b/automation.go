package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// Automation (Phase 11 part 2, ADR-73).

// wireAutomation copies Automation's records of before into the
// database, and opens Automation when the chart turns it on.
//
// The copy runs whether or not Automation is on (decided by the owner on
// November 21, 2026): every ConfigMap of before is copied and deleted at
// the first start with the database, so a release upgraded with
// automation.enabled false keeps its scripts, runs and facts in the
// database, shown once Automation is turned on, and is left with no
// ConfigMap of before.
func (w *wiring) wireAutomation(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Automation (Phase 11 part 2, ADR-73): scripts run over SSH to
	// nodes (wireSSH), with their records in the database (Phase 13).
	// Runs live on the leader, like Watson's commands.
	var autoEngine *automation.Engine
	var autoStore *automation.Store
	{
		// The copy's source and nothing else: the ConfigMap a release
		// before 0.13 kept the scripts in, and the one it kept the runs and
		// the nodes' facts in when the chart names one; each nil when it
		// is not there (DB-72: deleted once copied). A ConfigMap that is
		// there and cannot be read stops the start: a copy must not be
		// taken for made when its source could not be read.
		var from kv.Backend
		cm, err := openSource(ctx, w.restCfg, sourceRef(cfg, objAutomation))
		if err != nil {
			return fmt.Errorf("automation's ConfigMap: %w", err)
		}
		runs, err := openSource(ctx, w.restCfg, sourceRef(cfg, objAutomationRuns))
		if err != nil {
			return fmt.Errorf("automation's runs: %w", err)
		}
		var scripts kv.Backend = &kv.Memory{}
		if cm != nil {
			l := limited(cm, objAutomation, log.With("component", "storage"))
			scripts, from, w.stored = l, l, append(w.stored, l)
		}
		if runs != nil {
			rl := limited(runs, objAutomationRuns, log.With("component", "storage"))
			w.stored = append(w.stored, rl)
			// The runs and the facts are copied from wherever they were
			// kept: their own ConfigMap when there is one.
			routes := map[string]kv.Backend{}
			for _, k := range automation.RunsKeys {
				routes[k] = rl
			}
			from = &kv.Routed{Default: scripts, Routes: routes}
		}
		// Where the sensitive values are kept is said before anything is
		// copied into the tables: a ConfigMap's variables copied now hide
		// their sensitive values in the same Secrets as every later save.
		sens, err := w.sensitiveStore()
		if err != nil {
			return err
		}
		automation.UseSensitive(w.database, sens)
		// What is known of each node is rows of host_facts, and the
		// scripts with their versions, the script groups, the execution
		// profiles, the variables, their changes and the runs rows of
		// their tables.
		autoStore = automation.NewStore(w.inTables(ctx, from, func(d *db.DB) map[string]movedDoc {
			return map[string]movedDoc{automation.FactsKey: automation.NewFactsPG(d),
				automation.GroupsKey: automation.NewGroupsPG(d), automation.ProfilesKey: automation.NewProfilesPG(d),
				automation.ScriptsKey: automation.NewScriptsPG(d), automation.VariablesKey: automation.NewVariablesPG(d),
				automation.ChangesKey: automation.NewChangesPG(d), automation.RunsKey: automation.NewRunsPG(d)}
		}), nil)
		// The runs and the run requests through their repository (DB-87).
		autoStore.KeepRunsIn(w.database)
		w.sensitiveValues(ctx, autoStore, sens)
		if !cfg.Automation {
			// Off: the records are copied (above) and kept, and nothing
			// offers them until Automation is turned on.
			autoStore = nil
		}
	}
	if autoStore != nil {
		w.inDatabase = append(w.inDatabase, "automation")
		outDir := ""
		if w.toolboxCfg.Transcripts == toolbox.TranscriptsPVC {
			outDir = w.toolboxCfg.TranscriptDir
		}
		autoEngine = automation.NewEngine(automation.Options{
			Node: func(cluster, node string) (automation.NodeState, bool) {
				_, client, ok := w.manager.Lookup(cluster)
				if !ok || client == nil {
					return automation.NodeState{}, false
				}
				nodes, ok := client.(interface{ Node(string) *corev1.Node })
				if !ok {
					return automation.NodeState{}, false
				}
				n := nodes.Node(node)
				if n == nil {
					// A host outside Kubernetes (Phase 12).
					if _, ok, _ := w.hosts.Known(cluster, node); ok {
						return automation.NodeState{Host: true}, true
					}
					return automation.NodeState{}, false
				}
				st := automation.NodeState{}
				_, st.ControlPlane = n.Labels["node-role.kubernetes.io/control-plane"]
				if _, master := n.Labels["node-role.kubernetes.io/master"]; master {
					st.ControlPlane = true
				}
				for _, c := range n.Status.Conditions {
					if c.Type == corev1.NodeReady {
						st.Ready = c.Status == corev1.ConditionTrue
					}
				}
				return st, true
			},
			Store:       autoStore,
			Exec:        w.sshSvc,
			Unreachable: func(err error) bool { return errors.Is(err, nodessh.ErrUnreachable) },
			Logger:      log.With("component", "automation"),
			OutputDir:   outDir,
			OnCommand: func(r automation.Run, node, what, outcome string) {
				o := audit.OutcomeOK
				if outcome != "ok" {
					o = audit.OutcomeFailed
				}
				w.auditLog.Record(audit.Event{Action: audit.ActionAutomationCommand, Who: audit.Who{Subject: r.By.User, Name: r.By.Name}, Cluster: r.Cluster, Target: node, Outcome: o,
					Detail: fmt.Sprintf("%s: %s on %s, for %s: %s", r.Watson(), what, node, r.By.Name, r.Basis)})
			},
			OnEnd: func(r automation.Run) {
				c := r.Counts()
				outcome := audit.OutcomeFailed
				switch r.State {
				case automation.RunSucceeded:
					outcome = audit.OutcomeOK
				case automation.RunCancelled:
					outcome = audit.OutcomeStopped
				}
				w.auditLog.Record(audit.Event{Action: audit.ActionAutomationRun, Who: audit.Who{Subject: r.By.User, Name: r.By.Name}, Cluster: r.Cluster, Target: r.Script, Outcome: outcome,
					Detail: fmt.Sprintf("run %d %s: v%d on fleet %s, %d nodes: %d changed, %d no change, %d failed, %d unreachable, %d skipped, %d cancelled. %s", r.ID, r.State, r.Version, r.FleetName, len(r.Nodes),
						c[automation.NodeChanged], c[automation.NodeNoChange], c[automation.NodeFailed], c[automation.NodeUnreachable], c[automation.NodeSkipped], c[automation.NodeCancelled], r.Note)})
			},
		})
	}
	w.autoEngine, w.autoStore = autoEngine, autoStore
	return nil
}

// sensitiveStore is where Automation keeps its sensitive values (DB-71):
// the Secrets of the console's namespace, beside the connections' and the
// applications', one a cluster; in memory when the connections are (the
// simulator, a console run by hand), as the other record sets split
// between a row and a Secret are.
func (w *wiring) sensitiveStore() (automation.SensitiveStore, error) {
	if w.cfg.ConnectionsSecrets == "" {
		return automation.NewMemorySensitive(), nil
	}
	ns, prefix, ok := strings.Cut(w.cfg.ConnectionsSecrets, "/")
	if !ok || ns == "" || prefix == "" {
		return nil, fmt.Errorf("-connections-secrets must be namespace/prefix, got %q", w.cfg.ConnectionsSecrets)
	}
	return automation.NewSecretSensitive(w.restCfg, ns, prefix, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/instance": prefix, "app.kubernetes.io/managed-by": "221b"})
}

// sensitiveValues has the store keep its sensitive values in sens. With a
// database the documents hide the values themselves, in the transaction
// that writes their rows, and the values that are still in the tables (a
// database of before step 0038, or one restored to an earlier time) are
// moved out now: a cluster whose Secret cannot be written keeps its values
// where they were, named in the log by cluster and variable, and the next
// start tries again.
func (w *wiring) sensitiveValues(ctx context.Context, st *automation.Store, sens automation.SensitiveStore) {
	log := w.dbLog()
	w.claimImport(automation.SensitiveImportID)
	autoLog := log
	if w.log != nil {
		autoLog = w.log.With("component", "automation")
	}
	st.SensitiveInTables(w.database, sens, autoLog)
	mctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	moved, _, err := automation.MoveSensitive(mctx, w.database, sens, log)
	if err == nil {
		// The record that it was done, with how many records the first move
		// changed; every start still looks, and finds nothing to move.
		_, err = w.database.ImportOnce(mctx, automation.SensitiveImportID, "tables", func(context.Context, pgx.Tx) (int, error) { return moved, nil })
	}
	if err != nil {
		log.Warn("moving Automation's sensitive values out of the database into their Secrets is not finished; the next start tries again", "err", err)
	}
	// The two halves that do not go together, said once at the start: a
	// database restored to an earlier time, or a write cut short. Nothing
	// is removed on that guess.
	if noValue, noRow, err := automation.Unmatched(mctx, w.database, sens); err != nil {
		log.Warn("Automation's sensitive variables in the database and their Secrets could not be compared", "err", err)
	} else if len(noValue) > 0 || len(noRow) > 0 {
		log.Warn("Automation's sensitive variables in the database and their Secrets do not match: a variable without its value reads as not set, and a run that needs it is refused until it is set again; a value no variable names is kept and never read",
			"variables_without_value", noValue, "values_without_variable", noRow)
	}
}
