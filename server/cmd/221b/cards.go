package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/settings"
)

// The agents' proposals as durable Watson cards (Phase 12, ADR-99 to
// ADR-102): their ConfigMap, the Secret of the notification channels'
// addresses, and what the console actions work on.

type cardsOptions struct {
	auditLog *audit.Log
	manager  *clusters.Manager
	shared   *settings.Service
	agents   *ai.Service
	leads    func() bool
	log      *slog.Logger
}

// cardLimits are what the cards fit their records to: the limits of the
// proposals ConfigMap's keys.
func cardLimits() cards.Limits {
	l := layout[objProposals]
	return cards.Limits{CardsBytes: l[cards.CardsKey].Bytes, StandingBytes: l[cards.StandingKey].Bytes, NotifyBytes: l[cards.NotifyKey].Bytes, ChatsBytes: l[cards.ChatsKey].Bytes}
}

// openCards opens the cards: in <release>-proposals when the chart names
// it, in memory otherwise; the channels' addresses in the Secret
// <release>-cases beside the connections. It returns the ConfigMap too,
// for the metrics of its size.
func openCards(ctx context.Context, cfg config.Config, restCfg *rest.Config, o cardsOptions) (*cards.Store, *kv.Limited, error) {
	var backend kv.Backend = &kv.Memory{}
	var stored *kv.Limited
	if cfg.ProposalsConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.ProposalsConfigMap)
		if err != nil {
			return nil, nil, fmt.Errorf("the proposed changes' ConfigMap: %w", err)
		}
		copyProposalsOfBefore(ctx, restCfg, cm, o.log)
		stored = limited(cm, objProposals, o.log.With("component", "storage"))
		backend = stored
	} else {
		o.log.Warn("the agents' proposed changes are kept in memory and lost on restart (set -proposals-configmap to keep them)")
	}
	var secrets cards.SecretStore
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(restCfg, ns, prefix+"-cases", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "cases", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return nil, nil, fmt.Errorf("the notification channels' Secret: %w", err)
		}
		secrets = sk
	}
	st := cards.New(cards.Options{
		Backend: backend,
		Secrets: secrets,
		Audit:   o.auditLog,
		Logger:  o.log,
		Limits:  cardLimits(),
		Leads:   o.leads,
		Cluster: func(id string) (cards.ClusterInfo, bool) {
			sum, ok := o.manager.Summary(id)
			if !ok {
				return cards.ClusterInfo{}, false
			}
			prod := sum.Env == clusters.EnvProduction
			return cards.ClusterInfo{Name: sum.Name, Production: prod, TwoPerson: prod && o.shared.Current().WatsonOrDefault().TwoPerson}, true
		},
		ModelOf: func(agent string) string {
			if o.agents == nil {
				return ""
			}
			for _, a := range o.agents.View(context.Background()).Agents {
				if a.ID == agent {
					return a.Model
				}
			}
			return ""
		},
		AgentName: func(agent string) string {
			for _, a := range ai.Catalog {
				if a.ID == agent {
					return a.Name
				}
			}
			return agent
		},
	})
	return st, stored, nil
}

// --- The ConfigMap's new name (CF-104) ---

// proposalKeys are the keys of the proposed changes' ConfigMap, the cards
// last: once they are in the new object, its copy is done.
var proposalKeys = []string{cards.StandingKey, cards.NotifyKey, cards.ChatsKey, cards.CardsKey}

// oldProposalsRef is the name the proposed changes' ConfigMap had before
// CF-104, beside the new one: namespace/<release>-cases for
// namespace/<release>-proposals; "" for a name that does not end so.
func oldProposalsRef(ref string) string {
	base, ok := strings.CutSuffix(ref, "-proposals")
	if !ok || base == "" || strings.HasSuffix(base, "/") {
		return ""
	}
	return base + "-cases"
}

// copyProposalsOfBefore copies, once, the records an install kept in
// <release>-cases before the ConfigMap was <release>-proposals, so a test
// install keeps its cards. Not being able to is logged and does not stop
// the console: it starts with what the new object holds.
func copyProposalsOfBefore(ctx context.Context, restCfg *rest.Config, to *kube.ConfigMapStorage, log *slog.Logger) {
	ref := oldProposalsRef(to.Ref())
	if ref == "" {
		return
	}
	from, err := kube.NewConfigMapStorage(restCfg, ref)
	if err != nil {
		log.Warn("could not copy the agents' proposed changes from the ConfigMap of before", "from", ref, "to", to.Ref(), "err", err)
		return
	}
	copyProposals(ctx, from, to, ref, to.Ref(), log)
}

// copyProposals is copyProposalsOfBefore once both objects are open.
func copyProposals(ctx context.Context, from, to kv.Backend, fromRef, toRef string, log *slog.Logger) {
	keys, err := copyOnce(ctx, from, to, proposalKeys)
	if len(keys) > 0 {
		log.Info("the agents' proposed changes were copied once from the ConfigMap of before", "from", fromRef, "to", toRef, "keys", keys)
	}
	if err != nil {
		log.Warn("could not copy the agents' proposed changes from the ConfigMap of before", "from", fromRef, "to", toRef, "err", err)
	}
}

// copyOnce copies keys from an object of before into the one that
// replaces it, unless the new one holds the last key already: the copy
// is done, or the new object is in use. It copies a key only where the
// new object does not hold it, each save on the version just read, so a
// copy cut short is finished at the next start and two replicas starting
// together copy once between them. It returns the keys it copied.
func copyOnce(ctx context.Context, from, to kv.Backend, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if _, _, err := to.LoadKey(ctx, keys[len(keys)-1]); !errors.Is(err, kv.ErrNotFound) {
		return nil, err
	}
	var copied []string
	for _, k := range keys {
		b, _, err := from.LoadKey(ctx, k)
		if errors.Is(err, kv.ErrNotFound) {
			continue
		}
		if err != nil {
			return copied, err
		}
		for try := 0; ; try++ {
			_, version, err := to.LoadKey(ctx, k)
			if err == nil {
				// Written meanwhile, by another replica's copy or a first record.
				break
			}
			if !errors.Is(err, kv.ErrNotFound) {
				return copied, err
			}
			_, err = to.SaveKey(ctx, k, b, version)
			if err == nil {
				copied = append(copied, k)
				break
			}
			if !errors.Is(err, kv.ErrConflict) || try == 4 {
				return copied, err
			}
		}
	}
	return copied, nil
}

// --- end of the ConfigMap's new name.

// fleetBook is the fleets of the console's settings, for the cards.
type fleetBook struct{ shared *settings.Service }

func (f fleetBook) Fleets(cluster string) []fleets.Fleet { return f.shared.Current().Fleets[cluster] }

func (f fleetBook) SetFleets(ctx context.Context, cluster string, list []fleets.Fleet) error {
	_, err := f.shared.UpdateFleets(ctx, cluster, list)
	return err
}

// cardRoles are the chart's cases.approverRoles.
func cardRoles(cfg config.Config) []roles.ID {
	var out []roles.ID
	for _, r := range cfg.CasesApproverRoles {
		out = append(out, roles.ID(r))
	}
	return out
}

// wireCards opens the Watson cards: the changes the agents propose.
func (w *wiring) wireCards(ctx context.Context) error {
	cfg, log := w.cfg, w.log
	// Watson cards (Phase 12, ADR-99 to ADR-102): the agents'
	// proposals, durable in <release>-cases, with the console actions they
	// may carry; expired on the leader.
	cardStore, cardsStored, err := openCards(ctx, cfg, w.restCfg, cardsOptions{auditLog: w.auditLog, manager: w.manager, shared: w.shared, agents: w.aiSvc.Service, leads: w.elector.IsLeader, log: log.With("component", "cards")})
	if err != nil {
		return err
	}
	if cardsStored != nil {
		w.stored = append(w.stored, cardsStored)
	}
	cardConsole := cards.Console{Fleets: fleetBook{w.shared}, Members: fleetMembers{manager: w.manager, hosts: w.hosts}, Audit: w.auditLog, LoginUser: func() string { return w.toolboxCfg.SSH.LoginUser }}
	if w.autoStore != nil {
		cardConsole.Params = w.autoStore
	}
	if w.autoEngine != nil && w.toolboxCfg.SSH.Enabled {
		cardConsole.Hosts = w.sshSvc
	}
	cardStore.RegisterConsole(cardConsole)
	go cardStore.Run(ctx, 30*time.Second)
	// What the jobs propose waits as cards (JB-15, WC-01).
	w.jobsW.svc.SetProposer(jobsProposer{cardsProposer{cardStore}})
	w.mycroftProposals.cards.Store(&cardsProposer{cardStore})
	registerKnowledgeJobs(ctx, w.knowledgeSvc, w.jobsW.svc)
	w.cardStore, w.cardsStored = cardStore, cardsStored
	return nil
}
