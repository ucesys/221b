package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/sessions"
	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/twoperson"
)

// The ConfigMaps the console keeps its records in, and the most each key
// may hold (#46). Kubernetes keeps at most 1 MiB in one; the limits in each
// add up to less, which a test checks, so a new key cannot go in without
// finding room.
const (
	objSettings       = "settings"
	objAudit          = "audit"
	objSessions       = "sessions"
	objAutomation     = "automation"
	objAutomationRuns = "automation-runs"
	// objectBudget is what the keys of one ConfigMap may add up to, below
	// its 1 MiB with room for the object's own fields.
	objectBudget = 960 << 10
)

// historyMaxBytes bounds the toolbox's session records.
const historyMaxBytes = 480 << 10

var layout = map[string]map[string]kv.Limit{
	objSettings: {
		kube.SettingsKey:    {Bytes: 384 << 10, Hint: "remove fleets or acknowledged alerts you no longer need"},
		nodessh.PairingsKey: {Bytes: 256 << 10, Hint: "unpair nodes no longer in use"},
		sherlock.SpendKey:   {Bytes: 16 << 10},
		twoperson.Key:       {Bytes: 64 << 10},
		sessions.HistoryKey: {Bytes: 0},
		audit.StoreKey:      {Bytes: 0},
	},
	objAudit:    {audit.StoreKey: {Bytes: 640 << 10}},
	objSessions: {sessions.HistoryKey: {Bytes: 512 << 10}},
	objAutomation: {
		automation.ScriptsKey:   {Bytes: 384 << 10, Hint: "delete scripts no longer in use"},
		automation.ChangesKey:   {Bytes: 256 << 10},
		automation.VariablesKey: {Bytes: 64 << 10},
		automation.ProfilesKey:  {Bytes: 32 << 10},
		automation.GroupsKey:    {Bytes: 64 << 10},
		automation.RunsKey:      {Bytes: 0},
		automation.FactsKey:     {Bytes: 0},
	},
	objAutomationRuns: {
		automation.RunsKey:  {Bytes: 512 << 10},
		automation.FactsKey: {Bytes: 384 << 10},
	},
}

// limits are an object's limits, leaving out the keys it holds only until
// they move (Bytes 0), which another object bounds.
func limits(obj string) map[string]kv.Limit {
	out := map[string]kv.Limit{}
	for k, l := range layout[obj] {
		if l.Bytes > 0 {
			out[k] = l
		}
	}
	return out
}

// storedObjects are what the metrics report the sizes of.
type storedObjects []*kv.Limited

// limited wraps a ConfigMap with its object's limits.
func limited(cm *kube.ConfigMapStorage, obj string, log *slog.Logger) *kv.Limited {
	return &kv.Limited{Backend: cm, Name: cm.Ref(), Limits: limits(obj), Logger: log}
}

// openConfigMap opens a ConfigMap the chart makes, trying for a while: a
// name given that cannot be read stops the console rather than leave a
// replica keeping records somewhere its peers do not look.
func openConfigMap(ctx context.Context, cfg *rest.Config, ref string) (*kube.ConfigMapStorage, error) {
	cm, err := kube.NewConfigMapStorage(cfg, ref)
	if err != nil {
		return nil, err
	}
	for try := 0; ; try++ {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = cm.Exists(pctx)
		cancel()
		if err == nil || try == 4 {
			break
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w (check -audit-configmap, -sessions-configmap and -automation-runs-configmap against the chart)", err)
	}
	return cm, nil
}

// settingsBackend is the settings ConfigMap with the audit log and the
// session records routed to ConfigMaps of their own when named.
func settingsBackend(ctx context.Context, cfg *rest.Config, settingsCM *kube.ConfigMapStorage, auditRef, sessionsRef string, log *slog.Logger) (kv.Backend, storedObjects, error) {
	base := limited(settingsCM, objSettings, log)
	objects := storedObjects{base}
	routes := map[string]kv.Backend{}
	for _, o := range []struct{ ref, obj, key string }{{auditRef, objAudit, audit.StoreKey}, {sessionsRef, objSessions, sessions.HistoryKey}} {
		if o.ref == "" {
			continue
		}
		cm, err := openConfigMap(ctx, cfg, o.ref)
		if err != nil {
			return nil, nil, err
		}
		l := limited(cm, o.obj, log)
		objects = append(objects, l)
		routes[o.key] = l
	}
	if len(routes) == 0 {
		return base, objects, nil
	}
	return &kv.Routed{Default: base, Routes: routes}, objects, nil
}
