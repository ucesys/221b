package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The AI agents (Phase 12): their ConfigMap, the Secrets of the providers
// added besides the default one, and the chart's baseline instructions.

type aiOptions struct {
	shared        ai.SettingsStore
	keys          *sherlock.Keys
	chartProvider sherlock.Provider
	log           *slog.Logger
}

// aiService is the agents, with the ConfigMap they are kept in, for the
// metrics.
type aiService struct {
	*ai.Service
	stored *kv.Limited
}

// openAI opens the agents: their ConfigMap (memory without one), each other
// provider's credential in a Secret beside Sherlock's
// (<release>-ai-<provider>), and the baselines. It carries Sherlock's
// instructions and notes from before Phase 12 over, once.
func openAI(ctx context.Context, restCfg *rest.Config, cfg config.Config, secretRef string, o aiOptions) (aiService, error) {
	var out aiService
	var backend kv.Backend = &kv.Memory{}
	if cfg.AIConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.AIConfigMap)
		if err != nil {
			return out, fmt.Errorf("the AI agents' ConfigMap: %w", err)
		}
		l := limited(cm, objAI, o.log)
		backend, out.stored = l, l
	} else {
		o.log.Warn("the AI agents' providers, instructions and test cases are kept in memory and lost on restart (set -ai-configmap to keep them)")
	}
	baselines, err := ai.LoadBaselines(cfg.AgentsDir)
	if err != nil {
		return out, fmt.Errorf("the agents' baseline instructions: %w", err)
	}
	var keyBackend func(id string) sherlock.KeyBackend
	if secretRef != "" {
		ns, name, _ := strings.Cut(secretRef, "/")
		keyBackend = func(id string) sherlock.KeyBackend {
			sk, err := kube.NewSecretKey(restCfg, ns, strings.TrimSuffix(name, "-sherlock")+"-ai-"+id, map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "ai-provider", "app.kubernetes.io/managed-by": "221b"})
			if err != nil {
				o.log.Warn("a provider's credential is kept in memory", "provider", id, "err", err)
				return &sherlock.MemoryKey{}
			}
			return sk
		}
	}
	out.Service = ai.New(ai.Options{
		Backend: backend, Settings: o.shared, Keys: o.keys, KeyBackend: keyBackend,
		ChartProvider: o.chartProvider, ProviderLocked: cfg.SherlockLockProvider, DefaultURL: cfg.SherlockAPIURL,
		HTTP: &http.Client{Timeout: 5 * time.Minute}, Baselines: baselines, ToolDefs: api.ToolDefs, Logger: o.log,
	})
	mctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := out.Migrate(mctx); err != nil {
		o.log.Warn("carrying Sherlock's instructions over to the agents failed; it is tried again at the next start", "err", err)
	}
	if err := out.Settle(mctx); err != nil {
		o.log.Warn("settling the agents' instructions with the chart's failed", "err", err)
	}
	return out, nil
}
