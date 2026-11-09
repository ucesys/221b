package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
// cases ConfigMap's keys.
func cardLimits() cards.Limits {
	l := layout[objCases]
	return cards.Limits{CardsBytes: l[cards.CardsKey].Bytes, StandingBytes: l[cards.StandingKey].Bytes, NotifyBytes: l[cards.NotifyKey].Bytes, ChatsBytes: l[cards.ChatsKey].Bytes}
}

// openCards opens the cards: in <release>-cases when the chart names it,
// in memory otherwise; the channels' addresses in the Secret
// <release>-cases beside the connections. It returns the ConfigMap too,
// for the metrics of its size.
func openCards(ctx context.Context, cfg config.Config, restCfg *rest.Config, o cardsOptions) (*cards.Store, *kv.Limited, error) {
	var backend kv.Backend = &kv.Memory{}
	var stored *kv.Limited
	if cfg.CasesConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.CasesConfigMap)
		if err != nil {
			return nil, nil, fmt.Errorf("the proposals' ConfigMap: %w", err)
		}
		stored = limited(cm, objCases, o.log.With("component", "storage"))
		backend = stored
	} else {
		o.log.Warn("the agents' proposals are kept in memory and lost on restart (set -cases-configmap to keep them)")
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
