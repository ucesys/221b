package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/lessons"
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
	objAI             = "ai"
	// objIssues is the issue service's (Phase 12, docs/issues.md).
	objIssues = "issues"
	// objProposals is the agents' proposed changes' (Phase 12, ADR-99):
	// the cards, the standing approvals and the notification settings, in
	// <release>-proposals (<release>-cases before CF-104).
	objProposals = "proposals"
	// --- Jobs (Phase 12) ---
	// objRuns holds the jobs, their runs and findings (JB-14, ADR-91).
	objRuns = "runs"
	// objKnowledge is Mycroft's knowledge and the Confluence publisher's
	// (Phase 12, ADR-94 to ADR-98).
	objKnowledge = "knowledge"
	// --- Case work (Phase 12, CW-13) ---
	// objCasework holds Settings › Cases, the checks seen, the clean
	// start's marker, and the accepted exceptions (package lessons); the
	// cases, with their RCAs, are ConfigMaps of their own (wave 6).
	objCasework = "casework"
	// objWiki is the temporary wiki store's (CF-57): the wiki itself, in
	// one key, while Settings › Knowledge names no store.
	objWiki = "wiki"
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
		nodessh.HostsKey:    {Bytes: 128 << 10, Hint: "remove hosts no longer in use"},
		sherlock.SpendKey:   {Bytes: 16 << 10},
		twoperson.Key:       {Bytes: 64 << 10},
		// The scheduled backups to S3 (Phase 12): their settings, with a
		// private CA of at most 12 KiB, and their recent runs.
		backup.SettingsKey: {Bytes: 16 << 10, Hint: "shorten the private CA bundle"},
		backup.StatusKey:   {Bytes: 8 << 10},
		// Getting started (Phase 12, GS-04): the dismissed steps.
		api.SetupKey: {Bytes: 4 << 10},
		// --- Helm repositories (Phase 12, wave 5): the chart repositories
		// and OCI registries (their passwords and tokens are in a Secret).
		apps.RepositoriesKey: {Bytes: apps.DefaultRepositoriesBytes, Hint: "remove chart repositories no longer used"},
		sessions.HistoryKey:  {Bytes: 0},
		audit.StoreKey:       {Bytes: 0},
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
	// Phase 12, ADR-75: the AI agents.
	objAI: {
		ai.ProvidersKey: {Bytes: 32 << 10, Hint: "remove providers no longer in use"},
		ai.AgentsKey:    {Bytes: 32 << 10},
		ai.PromptsKey:   {Bytes: 480 << 10, Hint: "shorten the agents' instructions and notes"},
		ai.TestsKey:     {Bytes: 288 << 10, Hint: "delete test cases no longer needed"},
		ai.PilotKey:     {Bytes: 96 << 10},
	},
	objIssues: {
		// The index of the cases, one line each; each case is a ConfigMap of
		// its own (Phase 12, wave 6, CF-20).
		issues.IndexKey:  {Bytes: issues.DefaultIndexBytes, Hint: "shorten the issue service's retention and Settings › Cases' keepAgentClosedDays, or close the cases that are done"},
		issues.KnownKey:  {Bytes: issues.DefaultKnownBytes, Hint: "resolve or delete known issues no longer needed"},
		issues.ConfigKey: {Bytes: issues.DefaultConfigBytes, Hint: "delete services no longer used"},
		// Where builds before wave 6 kept every case: only the clean start
		// reads it, to delete it.
		casework.OldReportsKey: {},
	},
	// --- Watson cards (Phase 12, ADR-99) ---
	objProposals: {
		cards.CardsKey:    {Bytes: cards.DefaultCardsBytes, Hint: "decide or reject the proposed changes that wait"},
		cards.StandingKey: {Bytes: cards.DefaultStandingBytes, Hint: "remove standing approvals no longer used"},
		cards.NotifyKey:   {Bytes: cards.DefaultNotifyBytes},
		// The waiting proposals' conversations with Sherlock (CF-61).
		cards.ChatsKey: {Bytes: cards.DefaultChatsBytes, Hint: "decide the proposed changes that wait"},
	},
	// --- end of Watson cards.
	// --- Jobs (Phase 12) ---
	objRuns: {
		jobs.JobsKey: {Bytes: jobs.DefaultJobsBytes, Hint: "remove jobs added and no longer needed"},
		jobs.RunsKey: {Bytes: jobs.DefaultRunsBytes},
	},
	// --- Knowledge (Phase 12) ---
	objKnowledge: {
		knowledge.SettingsKey:  {Bytes: knowledge.DefaultSettingsBytes, Hint: "remove sources no longer read"},
		knowledge.DraftsKey:    {Bytes: knowledge.DefaultDraftsBytes, Hint: "approve or reject the wiki's waiting changes"},
		knowledge.StatusKey:    {Bytes: knowledge.DefaultStatusBytes},
		knowledge.PublisherKey: {Bytes: knowledge.DefaultPublisherBytes, Hint: "decide the Confluence pages' waiting edits"},
	},
	// --- The temporary wiki store (CF-57) ---
	objWiki: {
		knowledge.WikiKey: {Bytes: knowledge.DefaultWikiBytes, Hint: "move the wiki to Git or S3 in Settings › Knowledge"},
	},
	// --- Case work (Phase 12) ---
	objCasework: {
		casework.SettingsKey: {Bytes: casework.DefaultSettingsBytes},
		casework.ChecksKey:   {Bytes: casework.DefaultChecksBytes},
		casework.CleanKey:    {Bytes: casework.DefaultCleanBytes},
		// The validation rules people added or changed, and what the runs
		// found under each id (ADR-138).
		casework.RulesKey:     {Bytes: casework.DefaultRulesBytes, Hint: "dismiss the suggested validation rules no longer wanted"},
		casework.FoundKey:     {Bytes: casework.DefaultFoundBytes},
		lessons.ExceptionsKey: {Bytes: lessons.DefaultExceptionsBytes, Hint: "remove the accepted exceptions no longer needed"},
		// The fixes (wave 6g, CF-50) and the mark of their one import.
		fixes.Key:               {Bytes: fixes.DefaultBytes, Hint: "delete the inactive fixes in AIOps › Mycroft › Fixes"},
		casework.FixesImportKey: {Bytes: 4 << 10},
		// The work on the cases of builds before wave 6, and the RCAs of
		// builds before wave 6e (CF-44): the clean start's.
		casework.OldWorkKey: {},
		lessons.RCAsKey:     {},
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
		return nil, fmt.Errorf("%w (check -audit-configmap, -sessions-configmap, -automation-runs-configmap, -issues-configmap, -proposals-configmap, -runs-configmap, -knowledge-configmap, -wiki-configmap and -casework-configmap against the chart)", err)
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
