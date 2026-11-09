package casework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/kv"
)

// --- The clean start (Phase 12, wave 6, CF-23) ---
//
// 0.12.0 changed what a case and a finding are, with no migration: a
// console that ran a build before starts clean once, when the chart's
// cases.cleanStart.token is set and differs from the marker the last
// clean start left (CleanKey in the casework ConfigMap). It deletes the
// Cases area's records ONLY:
//
//   - the cases: the issue service's index and every case's ConfigMap,
//     and the known issues;
//   - the cases' work beside them (analyses, drafts, history: in the case
//     ConfigMaps), the exceptions, the RCAs and the check ids seen; the
//     analyses under way and the counter;
//   - the jobs' runs and their findings (the jobs, their schedules and
//     instructions stay);
//   - the proposed changes (cards) agents made for cases or runs (a
//     job's, an alert's, a report's or a case's; Watson's own, Mycroft's
//     and the standing approvals stay);
//   - Mycroft's pages, drafts and excerpts, with the manifest and the
//     syncs' state (the sources and their credentials stay).
//
// It keeps the issue service's settings, services and tokens, the SLA
// targets (renumbered "P1".."P5" → "0".."4" once, values kept), Settings
// › Cases, and everything outside Cases. With dryRun it logs, one line
// per item, what it would delete and deletes nothing, and writes no
// marker.

// CleanStores are the raw records a clean start reads: each a ConfigMap
// (or the case ConfigMaps). One left nil is skipped.
type CleanStores struct {
	// Issues is <release>-issues; Cases the case ConfigMaps.
	Issues kv.Backend
	Cases  kv.Objects
	// Casework is <release>-casework: the cases' settings, the checks,
	// the exceptions, the RCAs and the marker.
	Casework kv.Backend
	// Runs is <release>-runs; Cards <release>-cases (the cards);
	// Knowledge <release>-knowledge.
	Runs      kv.Backend
	Cards     kv.Backend
	Knowledge kv.Backend
	// Wiki deletes Mycroft's pages and excerpts from the wiki store and
	// says which (or would, with dryRun).
	Wiki func(ctx context.Context, dryRun bool) ([]string, error)
}

// The keys a clean start touches outside this package's (their owners'
// names, kept here so the test can name every key it may touch).
const (
	cleanExceptionsKey = "exceptions.json"
	cleanRCAsKey       = "rcas.json"
	cleanRunsKey       = "runs.json"
	cleanCardsKey      = "cards.json"
	cleanDraftsKey     = "knowledge-drafts.json"
	cleanStatusKey     = "knowledge-status.json"
	cleanFixesKey      = "fixes.json"
)

// agentCardKinds are the sources of the cards agents made for cases or
// runs.
var agentCardKinds = map[string]bool{"job": true, "alert": true, "report": true, "case": true}

// CleanItem is one thing a clean start deletes (or would).
type CleanItem struct {
	Object string `json:"object"`
	Key    string `json:"key,omitempty"`
	What   string `json:"what"`
	Count  int    `json:"count"`
}

// CleanResult is what a clean start did.
type CleanResult struct {
	// Done says it ran: the token was set and differed from the marker.
	Done   bool        `json:"done"`
	DryRun bool        `json:"dryRun,omitempty"`
	Items  []CleanItem `json:"items"`
}

// The keys builds before wave 6 kept the cases in: the reports (every
// case and its conversation) and the work on them. The storage layout
// names them so the clean start reaches them in their ConfigMaps.
const (
	OldReportsKey = "issues-reports.json"
	OldWorkKey    = "cases.json"
)

// cleanMarker is CleanKey.
type cleanMarker struct {
	Token string      `json:"token"`
	At    time.Time   `json:"at"`
	Items []CleanItem `json:"items,omitempty"`
}

// CleanStart runs the clean start when token is set and differs from the
// marker (see above).
func CleanStart(ctx context.Context, st CleanStores, token string, dryRun bool, now time.Time, log *slog.Logger, auditLog *audit.Log) (CleanResult, error) {
	out := CleanResult{DryRun: dryRun, Items: []CleanItem{}}
	token = strings.TrimSpace(token)
	if token == "" || st.Casework == nil {
		return out, nil
	}
	var marker cleanMarker
	if _, err := loadJSON(ctx, st.Casework, CleanKey, &marker); err != nil {
		return out, err
	}
	if marker.Token == token {
		return out, nil
	}
	out.Done = true
	c := cleaner{ctx: ctx, dry: dryRun, log: log, out: &out}
	// The cases and the known issues.
	c.drop(st.Issues, "issues", issues.IndexKey, "cases in the index", countOf("cases"))
	c.drop(st.Issues, "issues", issues.KnownKey, "known issues", countOf("known"))
	c.drop(st.Issues, "issues", OldReportsKey, "cases kept before wave 6 (the reports)", countOf("reports"))
	c.drop(st.Casework, "casework", OldWorkKey, "the work on cases kept before wave 6", countOf("work"))
	c.renumberTargets(st.Issues)
	if st.Cases != nil {
		names, err := st.Cases.List(ctx)
		if err != nil {
			return out, err
		}
		c.item(CleanItem{Object: "case ConfigMaps", What: "cases' ConfigMaps (the issue service's record and the work on each)", Count: len(names)})
		if !dryRun {
			for _, n := range names {
				if err := st.Cases.Delete(ctx, n); err != nil {
					return out, err
				}
			}
		}
	}
	// The cases' records beside them.
	c.drop(st.Casework, "casework", cleanExceptionsKey, "exceptions", countOf("items"))
	c.drop(st.Casework, "casework", cleanRCAsKey, "RCAs", countOf("items"))
	c.drop(st.Casework, "casework", cleanFixesKey, "fixes", countOf("items"))
	c.drop(st.Casework, "casework", ChecksKey, "clusters' check ids seen", countOf("clusters"))
	c.keepSettings(st.Casework)
	// The runs and their findings; the jobs stay.
	c.drop(st.Runs, "runs", cleanRunsKey, "runs (and their findings)", countOf("runs"))
	// The agents' cards for cases and runs.
	c.filterCards(st.Cards)
	// Mycroft's drafts and syncs' state, and its pages in the wiki.
	c.drop(st.Knowledge, "knowledge", cleanDraftsKey, "Mycroft's drafts", countOf("runs"))
	c.drop(st.Knowledge, "knowledge", cleanStatusKey, "the syncs' state", func([]byte) int { return 1 })
	if st.Wiki != nil && c.err == nil {
		gone, err := st.Wiki(ctx, dryRun)
		if err != nil {
			log.Warn("the clean start could not delete Mycroft's pages from the wiki", "err", err)
		} else {
			c.item(CleanItem{Object: "wiki", What: "Mycroft's pages, the excerpts and the manifest", Count: len(gone)})
		}
	}
	if c.err != nil {
		return out, c.err
	}
	if dryRun {
		return out, nil
	}
	marker = cleanMarker{Token: token, At: now.UTC(), Items: out.Items}
	b, _ := json.Marshal(marker)
	if _, err := st.Casework.SaveKey(ctx, CleanKey, b, ""); err != nil {
		return out, err
	}
	if auditLog != nil {
		var parts []string
		for _, it := range out.Items {
			parts = append(parts, fmt.Sprintf("%d %s", it.Count, it.What))
		}
		auditLog.Record(audit.Event{Action: ActionClean, Who: audit.Who{Subject: "221b", Name: "clean start"}, Target: "cases", Outcome: audit.OutcomeOK,
			Detail: "the Cases area started clean: " + strings.Join(parts, ", ")})
	}
	return out, nil
}

// cleaner does a clean start's steps, the first error stopping the rest.
type cleaner struct {
	ctx context.Context
	dry bool
	log *slog.Logger
	out *CleanResult
	err error
}

func (c *cleaner) item(it CleanItem) {
	c.out.Items = append(c.out.Items, it)
	verb := "clean start: deleted"
	if c.dry {
		verb = "clean start (dry run): would delete"
	}
	c.log.Info(verb, "object", it.Object, "key", it.Key, "what", it.What, "count", it.Count)
}

// countOf counts the entries of one list or map field of a record.
func countOf(field string) func([]byte) int {
	return func(b []byte) int {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			return 0
		}
		var list []json.RawMessage
		if json.Unmarshal(m[field], &list) == nil {
			return len(list)
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(m[field], &obj) == nil {
			return len(obj)
		}
		return 0
	}
}

// drop deletes one key, saying how many entries it held.
func (c *cleaner) drop(b kv.Backend, obj, key, what string, count func([]byte) int) {
	if b == nil || c.err != nil {
		return
	}
	raw, _, err := b.LoadKey(c.ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return
	}
	if err != nil {
		c.err = err
		return
	}
	c.item(CleanItem{Object: obj, Key: key, What: what, Count: count(raw)})
	if c.dry {
		return
	}
	if d, ok := b.(kv.Deleter); ok {
		c.err = d.DeleteKey(c.ctx, key)
		return
	}
	_, c.err = b.SaveKey(c.ctx, key, []byte("{}"), "")
}

// renumberTargets renumbers the SLA targets kept by "P1".."P5" to
// "0".."4", values kept, once: nothing else of the issue service's
// settings changes.
func (c *cleaner) renumberTargets(b kv.Backend) {
	if b == nil || c.err != nil {
		return
	}
	raw, version, err := b.LoadKey(c.ctx, issues.ConfigKey)
	if errors.Is(err, kv.ErrNotFound) {
		return
	}
	if err != nil {
		c.err = err
		return
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc["settings"] == nil {
		return
	}
	var set map[string]json.RawMessage
	if json.Unmarshal(doc["settings"], &set) != nil {
		return
	}
	var targets map[string]json.RawMessage
	if json.Unmarshal(set["targets"], &targets) != nil {
		return
	}
	moved := 0
	out := map[string]json.RawMessage{}
	for k, v := range targets {
		if n, ok := strings.CutPrefix(k, "P"); ok && len(n) == 1 && n >= "1" && n <= "5" {
			out[string(rune(n[0]-1))] = v
			moved++
			continue
		}
		if _, taken := out[k]; !taken {
			out[k] = v
		}
	}
	if moved == 0 {
		return
	}
	c.item(CleanItem{Object: "issues", Key: issues.ConfigKey, What: "SLA targets renumbered P1-P5 to 0-4 (values kept; nothing deleted)", Count: moved})
	if c.dry {
		return
	}
	set["targets"], _ = json.Marshal(out)
	doc["settings"], _ = json.Marshal(set)
	b2, _ := json.Marshal(doc)
	_, c.err = b.SaveKey(c.ctx, issues.ConfigKey, b2, version)
}

// keepSettings keeps Settings › Cases alone in their key: the analyses
// under way, the counter and the retention's day go.
func (c *cleaner) keepSettings(b kv.Backend) {
	if b == nil || c.err != nil {
		return
	}
	var rec record
	raw, version, err := b.LoadKey(c.ctx, SettingsKey)
	if errors.Is(err, kv.ErrNotFound) {
		return
	}
	if err != nil || json.Unmarshal(raw, &rec) != nil {
		c.err = err
		return
	}
	if len(rec.Analyzing) == 0 && rec.Duplicates == 0 && rec.Retained.IsZero() {
		return
	}
	c.item(CleanItem{Object: "casework", Key: SettingsKey, What: "analyses under way and the counter of duplicates (Settings › Cases kept)", Count: len(rec.Analyzing) + rec.Duplicates})
	if c.dry {
		return
	}
	out, _ := json.Marshal(record{Settings: rec.Settings})
	_, c.err = b.SaveKey(c.ctx, SettingsKey, out, version)
}

// filterCards deletes the cards agents made for cases or runs, keeping
// the rest of the record as it is.
func (c *cleaner) filterCards(b kv.Backend) {
	if b == nil || c.err != nil {
		return
	}
	raw, version, err := b.LoadKey(c.ctx, cleanCardsKey)
	if errors.Is(err, kv.ErrNotFound) {
		return
	}
	if err != nil {
		c.err = err
		return
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return
	}
	var list []json.RawMessage
	if json.Unmarshal(doc["cards"], &list) != nil {
		return
	}
	var kept []json.RawMessage
	for _, card := range list {
		var x struct {
			Source struct {
				Kind string `json:"kind"`
			} `json:"source"`
		}
		if json.Unmarshal(card, &x) == nil && agentCardKinds[x.Source.Kind] {
			continue
		}
		kept = append(kept, card)
	}
	gone := len(list) - len(kept)
	if gone == 0 {
		return
	}
	c.item(CleanItem{Object: "cases", Key: cleanCardsKey, What: "proposed changes agents made for cases or runs", Count: gone})
	if c.dry {
		return
	}
	if kept == nil {
		kept = []json.RawMessage{}
	}
	doc["cards"], _ = json.Marshal(kept)
	out, _ := json.Marshal(doc)
	_, c.err = b.SaveKey(c.ctx, cleanCardsKey, out, version)
}
