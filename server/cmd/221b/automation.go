package main

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// Automation (Phase 11 part 2, ADR-73).

// wireAutomation opens Automation when the chart turns it on.
func (w *wiring) wireAutomation(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Automation (Phase 11 part 2, ADR-73): scripts run over SSH to
	// nodes (wireSSH), with their records in the database (Phase 13).
	// Runs live on the leader, like Watson's commands.
	var autoEngine *automation.Engine
	var autoStore *automation.Store
	if cfg.Automation {
		// The copy's source and nothing else: the ConfigMap a release
		// before 0.13 kept the scripts in, and the one it kept the runs and
		// the nodes' facts in when the chart names one. A ConfigMap that
		// is named and cannot be read stops the start: a copy must not be
		// taken for made when its source was not there.
		var from kv.Backend
		if cfg.AutomationConfigMap != "" {
			cm, err := kube.NewConfigMapStorage(w.restCfg, cfg.AutomationConfigMap)
			if err != nil {
				return fmt.Errorf("automation's ConfigMap: %w", err)
			}
			l := limited(cm, objAutomation, log.With("component", "storage"))
			from, w.stored = l, append(w.stored, l)
			if cfg.AutomationRunsConfigMap != "" {
				runs, err := openConfigMap(ctx, w.restCfg, cfg.AutomationRunsConfigMap)
				if err != nil {
					return fmt.Errorf("automation's runs: %w", err)
				}
				rl := limited(runs, objAutomationRuns, log.With("component", "storage"))
				w.stored = append(w.stored, rl)
				// The runs and the facts are copied from wherever they
				// were kept: their own ConfigMap when there is one.
				routes := map[string]kv.Backend{}
				for _, k := range automation.RunsKeys {
					routes[k] = rl
				}
				from = &kv.Routed{Default: l, Routes: routes}
			}
		}
		outDir := ""
		if w.toolboxCfg.Transcripts == toolbox.TranscriptsPVC {
			outDir = w.toolboxCfg.TranscriptDir
		}
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
		w.inDatabase = append(w.inDatabase, "automation")
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
