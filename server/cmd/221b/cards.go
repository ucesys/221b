package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/db"
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
	// wrap routes the keys the database keeps to it.
	wrap     func(kv.Backend) kv.Backend
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

// openCards opens the cards: their records where o.wrap keeps them, the
// database; the channels' addresses in the Secret <release>-cases beside
// the connections. <release>-proposals, when the chart names it, is the
// one-time copy's source and nothing else: where a release before 0.13
// kept the cards, read with <release>-cases of before CF-104
// (proposalsOfBefore). It is returned too, for the metrics of its size.
func openCards(ctx context.Context, cfg config.Config, restCfg *rest.Config, o cardsOptions) (*cards.Store, *kv.Limited, error) {
	var backend kv.Backend
	var stored *kv.Limited
	if cfg.ProposalsConfigMap != "" {
		cm, err := openSource(ctx, restCfg, cfg.ProposalsConfigMap)
		if err != nil {
			return nil, nil, fmt.Errorf("the proposed changes' ConfigMap: %w", err)
		}
		before, err := openProposalsOfBefore(ctx, restCfg, cfg.ProposalsConfigMap)
		if err != nil {
			return nil, nil, fmt.Errorf("the proposed changes' ConfigMap of before CF-104: %w", err)
		}
		var now kv.Backend
		if cm != nil {
			stored = limited(cm, objProposals, o.log.With("component", "storage"))
			now = stored
		}
		switch {
		case before != nil:
			backend = proposalsOfBefore{now: now, before: before}
		case now != nil:
			backend = now
		}
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
	// Everything the store keeps is kept in the database; a caller that
	// gives no wrap (a test) keeps it in memory.
	if o.wrap != nil {
		backend = o.wrap(backend)
	} else if backend == nil {
		backend = &kv.Memory{}
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

// proposalKeys are the keys of the proposed changes' ConfigMap; once the
// cards are in <release>-proposals, the one of before is not read.
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

// openProposalsOfBefore opens <release>-cases, where a build before
// CF-104 kept the proposed changes, as a copy's source beside
// <release>-proposals (openSource: nil when it is not there).
func openProposalsOfBefore(ctx context.Context, restCfg *rest.Config, ref string) (*kube.ConfigMapStorage, error) {
	now, err := kube.NewConfigMapStorage(restCfg, ref)
	if err != nil {
		return nil, err
	}
	old := oldProposalsRef(now.Ref())
	if old == "" {
		return nil, nil
	}
	return openSource(ctx, restCfg, old)
}

// proposalsOfBefore is the proposed changes' copy source when
// <release>-cases of before CF-104 is still there: a key is read from
// <release>-proposals, and from <release>-cases where <release>-proposals
// does not hold it and holds no cards yet (the new object was not in use).
// Builds before 0.13 copied the keys from one ConfigMap into the other at
// start; this reads them in place, so nothing is written to a ConfigMap
// (DB-72), and both are deleted once the cards are in the database.
type proposalsOfBefore struct{ now, before kv.Backend }

func (p proposalsOfBefore) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	if p.now == nil {
		return p.before.LoadKey(ctx, key)
	}
	b, version, err := p.now.LoadKey(ctx, key)
	if !errors.Is(err, kv.ErrNotFound) || !slices.Contains(proposalKeys, key) {
		return b, version, err
	}
	if _, _, cerr := p.now.LoadKey(ctx, cards.CardsKey); !errors.Is(cerr, kv.ErrNotFound) {
		return b, version, err
	}
	return p.before.LoadKey(ctx, key)
}

func (proposalsOfBefore) SaveKey(_ context.Context, key string, _ []byte, _ string) (string, error) {
	return "", fmt.Errorf("the console keeps nothing under %q in a ConfigMap: every record set is kept in the database (DB-69)", key)
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
	cardStore, cardsStored, err := openCards(ctx, cfg, w.restCfg, cardsOptions{wrap: func(b kv.Backend) kv.Backend {
		// Everything the store keeps is rows (Phase 13): the standing
		// approvals and the notification channels (approval_policies,
		// notification_channels), the proposed changes (changes and the
		// tables that hang from it, DB-45) and the waiting changes'
		// conversations with Sherlock (chats, chat_messages, DB-46).
		w.inDatabase = append(w.inDatabase, "changes")
		w.cardsBackend = w.inTables(ctx, b, func(d *db.DB) map[string]movedDoc {
			return map[string]movedDoc{cards.StandingKey: cards.NewStandingPG(d), cards.NotifyKey: cards.NewNotifyPG(d),
				cards.CardsKey: cards.NewCardsPG(d), cards.ChatsKey: cards.NewChatsPG(d)}
		})
		return w.cardsBackend
	}, auditLog: w.auditLog, manager: w.manager, shared: w.shared, agents: w.aiSvc.Service, leads: w.elector.IsLeader, log: log.With("component", "cards")})
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
