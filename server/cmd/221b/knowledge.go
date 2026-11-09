package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/config"
	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/fixes"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Mycroft's knowledge (Phase 12, ADR-94 to ADR-98): its sources, drafts
// and the Confluence publisher in the ConfigMap <release>-knowledge, its
// credentials in the Secret <release>-knowledge, and the wiki's local copy
// in the chart's emptyDir.

type knowledgeOptions struct {
	auditLog *audit.Log
	leads    func() bool
	model    knowledge.Completer
	console  knowledge.Console
	issues   *issues.Service
	proposer knowledge.Proposer
	log      *slog.Logger
}

// openKnowledge builds the knowledge service and its publisher. It
// returns the ConfigMaps too, the knowledge's and the temporary wiki
// store's, for the metrics of their size.
func openKnowledge(ctx context.Context, cfg config.Config, restCfg *rest.Config, o knowledgeOptions) (*knowledge.Service, *kv.Limited, *kv.Limited, error) {
	var backend kv.Backend = &kv.Memory{}
	var stored, wikiStored *kv.Limited
	if cfg.KnowledgeConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.KnowledgeConfigMap)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the knowledge ConfigMap: %w", err)
		}
		stored = limited(cm, objKnowledge, o.log.With("component", "storage"))
		backend = stored
	} else {
		o.log.Warn("Mycroft's sources and drafts are kept in memory and lost on restart (set -knowledge-configmap to keep them)")
	}
	var secrets knowledge.SecretStore = &knowledge.MemorySecret{}
	if cfg.ConnectionsSecrets != "" {
		ns, prefix, _ := strings.Cut(cfg.ConnectionsSecrets, "/")
		sk, err := kube.NewSecretKey(restCfg, ns, prefix+"-knowledge", map[string]string{"app.kubernetes.io/name": "221b", "app.kubernetes.io/component": "knowledge", "app.kubernetes.io/managed-by": "221b"})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the knowledge Secret: %w", err)
		}
		secrets = sk
	}
	// The temporary wiki store (CF-57): the wiki in <release>-wiki while
	// Settings › Knowledge names no store.
	var wiki kv.Backend
	if cfg.WikiConfigMap != "" {
		cm, err := openConfigMap(ctx, restCfg, cfg.WikiConfigMap)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the wiki ConfigMap: %w", err)
		}
		wikiStored = limited(cm, objWiki, o.log.With("component", "storage"))
		wiki = wikiStored
	}
	record := func(e audit.Event) { o.auditLog.Record(e) }
	l := layout[objKnowledge]
	var pub *confluence.Publisher
	if o.issues != nil {
		pub = confluence.NewPublisher(confluence.PublisherOptions{
			Backend: backend, Limit: l[knowledge.PublisherKey].Bytes, Secrets: secrets, Issues: confluence.IssueSource(o.issues),
			Hidden:     func(ctx context.Context) []string { return hiddenNames(ctx, o.console) },
			ConsoleURL: cfg.ConsoleURL, HTTP: &http.Client{Timeout: time.Minute}, Logger: o.log.With("component", "confluence"),
		})
		o.issues.OnChange(pub.OnIssue)
	}
	svc := knowledge.New(knowledge.Options{
		Backend: backend, Secrets: secrets, Dir: cfg.KnowledgeDir, Model: o.model, Console: o.console, Publisher: pub, Proposer: o.proposer,
		Limits: knowledge.Limits{SettingsBytes: l[knowledge.SettingsKey].Bytes, DraftsBytes: l[knowledge.DraftsKey].Bytes, StatusBytes: l[knowledge.StatusKey].Bytes},
		Leads:  o.leads, Audit: record, RefreshGit: cfg.KnowledgeRefreshGit, RefreshS3: cfg.KnowledgeRefreshS3, ConsoleURL: cfg.ConsoleURL,
		Logger:        o.log.With("component", "knowledge"),
		WikiConfigMap: wiki, WikiBytes: layout[objWiki][knowledge.WikiKey].Bytes,
		// Mycroft's first import (Phase 12, GS-11): a source is read once
		// it is added.
		ReadNew: true,
	})
	if pub != nil {
		pub.SetWiki(svc)
	}
	return svc, stored, wikiStored, nil
}

// hiddenNames are what the console knows by name that never reaches
// Confluence: its nodes and hosts, and their addresses.
func hiddenNames(ctx context.Context, c knowledge.Console) []string {
	if c == nil {
		return nil
	}
	hosts, err := c.Hosts(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range hosts {
		out = append(out, h.Name)
		if h.Address != "" {
			out = append(out, h.Address)
		}
	}
	return out
}

// knowledgeHits is ask_knowledge's lookup, for Sherlock's agents.
func knowledgeHits(svc *knowledge.Service) func(ctx context.Context, q string, limit int) ([]sherlock.KnowledgeHit, error) {
	return func(ctx context.Context, q string, limit int) ([]sherlock.KnowledgeHit, error) {
		hits, err := svc.Ask(ctx, q, limit)
		if err != nil {
			return nil, err
		}
		out := make([]sherlock.KnowledgeHit, 0, len(hits))
		for _, h := range hits {
			out = append(out, sherlock.KnowledgeHit{Kind: h.Kind, Title: h.Title, Path: h.Path, Date: h.Date, Stale: h.Stale, Version: h.Version, URL: h.URL, Text: h.Text})
		}
		return out, nil
	}
}

// --- Known issues in ask_knowledge (Phase 12, wave 6e, CF-43) ---

// knownForAgents is the least confidence, and the most known issues, that
// ask_knowledge gives beside the wiki's pages and excerpts.
const (
	knownForAgentsScore = 40
	knownForAgents      = 3
)

// knownHits are the open known issues a question matches, by the issue
// service's matcher (IS-10), as ask_knowledge gives them.
func knownHits(ctx context.Context, svc *issues.Service, q string) []sherlock.KnowledgeHit {
	m, err := svc.MatchText(ctx, q)
	if err != nil {
		return nil
	}
	var out []sherlock.KnowledgeHit
	reader := issues.Caller{Person: issues.Person{ID: "system:agents", Name: "Agents", System: true}, Operator: true}
	for _, cand := range m.Known {
		if cand.Confidence < knownForAgentsScore || len(out) >= knownForAgents {
			continue
		}
		k, err := svc.KnownIssue(ctx, reader, cand.ID)
		if err != nil {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Status: %s.", k.Status)
		if len(k.Symptoms) > 0 {
			b.WriteString(" Symptoms: " + strings.Join(k.Symptoms, "; ") + ".")
		}
		if len(k.Services) > 0 {
			b.WriteString(" Services: " + strings.Join(k.Services, ", ") + ".")
		}
		if k.Workaround != "" {
			b.WriteString(" Workaround: " + k.Workaround)
		}
		if k.Expected != "" {
			b.WriteString(" Fix expected: " + k.Expected + ".")
		}
		out = append(out, sherlock.KnowledgeHit{Kind: sherlock.KindKnownIssue, Title: k.Title, Path: k.ID, Date: k.Updated, Text: b.String()})
	}
	return out
}

// consoleKnowledge is what the console knows, as Mycroft reads it: its
// clusters, nodes and hosts with their facts, fleets, Helm applications
// and Automation scripts, read through the console's own services. No
// Secret's value, no credential, no host key, no values file and no
// script's text is in it.
type consoleKnowledge struct {
	manager  *clusters.Manager
	hosts    *nodessh.Hosts
	settings *settings.Service
	apps     apps.Store
	auto     *automation.Store
}

// maxConsoleItems bounds what the console offers Mycroft.
const maxConsoleItems = 3000

type nodeLister interface {
	NodeNames() []string
	Node(string) *corev1.Node
}

func nodeAddress(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func factLines(f automation.NodeFacts) string {
	keys := make([]string, 0, len(f.Facts))
	for k := range f.Facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %s\n", k, f.Facts[k])
	}
	return b.String()
}

func (c consoleKnowledge) facts(ctx context.Context, cluster string) map[string]automation.NodeFacts {
	out := map[string]automation.NodeFacts{}
	if c.auto == nil {
		return out
	}
	list, err := c.auto.Facts(ctx, cluster)
	if err != nil {
		return out
	}
	for _, f := range list {
		out[f.Node] = f
	}
	return out
}

func (c consoleKnowledge) Items(ctx context.Context) ([]knowledge.ConsoleItem, error) {
	var out []knowledge.ConsoleItem
	add := func(it knowledge.ConsoleItem) bool {
		out = append(out, it)
		return len(out) < maxConsoleItems
	}
	if c.settings != nil {
		for cluster, list := range c.settings.Current().Fleets {
			for _, f := range list {
				text := fmt.Sprintf("Fleet %s in the cluster %s.\n%s\nLabels: %s\nTaints: %s\n", f.Name, cluster, f.Desc, strings.Join(f.Labels, ", "), strings.Join(f.Taints, ", "))
				if !add(knowledge.ConsoleItem{ID: "fleet/" + cluster + "/" + f.ID, Title: "Fleet " + f.Name + " (" + cluster + ")", Text: text, URL: "#/c/" + cluster + "/fleets"}) {
					return out, nil
				}
			}
		}
	}
	for _, sum := range c.manager.List() {
		text := fmt.Sprintf("Cluster %s (id %s), environment %s, Kubernetes %s, %d nodes, %d pods, %s.\n%s\n", sum.Name, sum.ID, sum.Env, sum.Version, sum.Nodes, sum.Pods, sum.State, sum.Description)
		if !add(knowledge.ConsoleItem{ID: "cluster/" + sum.ID, Title: "Cluster " + sum.Name, Text: text, URL: "#/c/" + sum.ID}) {
			return out, nil
		}
		facts := c.facts(ctx, sum.ID)
		if _, client, ok := c.manager.Lookup(sum.ID); ok && client != nil {
			if nl, ok := client.(nodeLister); ok {
				for _, name := range nl.NodeNames() {
					n := nl.Node(name)
					if n == nil {
						continue
					}
					var b strings.Builder
					fmt.Fprintf(&b, "Node %s of the cluster %s. Ready: %v. Address: %s. Kubelet %s, %s, kernel %s.\nLabels:\n", name, sum.Name, nodeReady(n), nodeAddress(n), n.Status.NodeInfo.KubeletVersion, n.Status.NodeInfo.OSImage, n.Status.NodeInfo.KernelVersion)
					keys := make([]string, 0, len(n.Labels))
					for k := range n.Labels {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						fmt.Fprintf(&b, "- %s=%s\n", k, n.Labels[k])
					}
					if f, ok := facts[name]; ok {
						b.WriteString("Facts, as read on " + f.At.UTC().Format("2006-01-02") + ":\n" + factLines(f))
					}
					if !add(knowledge.ConsoleItem{ID: "node/" + sum.ID + "/" + name, Title: "Node " + name + " (" + sum.Name + ")", Text: b.String(), URL: "#/c/" + sum.ID + "/nodes/" + name}) {
						return out, nil
					}
				}
			}
		}
		if c.hosts != nil {
			for _, h := range c.hosts.Cached(sum.ID) {
				var b strings.Builder
				fmt.Fprintf(&b, "Host %s outside Kubernetes, in the cluster %s. Address %s, port %d. State: %s.\n", h.Name, sum.Name, h.Address, h.Port, h.State)
				for k, v := range h.Labels {
					fmt.Fprintf(&b, "- label %s=%s\n", k, v)
				}
				if f, ok := facts[h.Name]; ok {
					b.WriteString("Facts, as read on " + f.At.UTC().Format("2006-01-02") + ":\n" + factLines(f))
				}
				if !add(knowledge.ConsoleItem{ID: "host/" + sum.ID + "/" + h.Name, Title: "Host " + h.Name + " (" + sum.Name + ")", Text: b.String(), URL: "#/c/" + sum.ID + "/nodes/" + h.Name}) {
					return out, nil
				}
			}
		}
	}
	if c.apps != nil {
		if list, err := c.apps.List(ctx); err == nil {
			for _, a := range list {
				text := fmt.Sprintf("Helm application %s in the namespace %s of the cluster %s, from %s. Chart version %s; commit %s of %s: %s.\n", a.Name, a.Namespace, a.Cluster, a.Source.String(), a.ChartVersion, shortCommit(a.Commit), a.CommitDate.UTC().Format("2006-01-02"), a.CommitSubject)
				if !add(knowledge.ConsoleItem{ID: "app/" + a.Cluster + "/" + a.Namespace + "/" + a.Name, Title: "Application " + a.Name + " (" + a.Cluster + ")", Text: text, URL: "#/c/" + a.Cluster + "/helm"}) {
					return out, nil
				}
			}
		}
	}
	if c.auto != nil {
		if list, err := c.auto.Scripts(ctx); err == nil {
			for _, s := range list {
				v := s.Latest()
				text := fmt.Sprintf("Automation script %s (group %s), version %d, %s: %s\nIt runs with %s privilege.\n", s.Name, s.Group, v.N, v.State, v.Header.Describe, v.Header.Privilege)
				var names []string
				for _, p := range v.Header.Params {
					names = append(names, p.Name)
				}
				if len(names) > 0 {
					text += "Its parameters: " + strings.Join(names, ", ") + ".\n"
				}
				if !add(knowledge.ConsoleItem{ID: "script/" + s.Name, Title: "Automation script " + s.Name, Text: text, URL: "#/automation/scripts/" + s.Name}) {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// Fleets are a cluster's fleets by name, so Mycroft proposes only those
// it lacks (Phase 12, GS-11).
func (c consoleKnowledge) Fleets(_ context.Context, cluster string) ([]string, error) {
	var out []string
	for _, f := range c.settings.Current().Fleets[cluster] {
		out = append(out, f.Name)
	}
	return out, nil
}

func (c consoleKnowledge) Hosts(ctx context.Context) ([]knowledge.ConsoleHost, error) {
	var out []knowledge.ConsoleHost
	for _, sum := range c.manager.List() {
		if _, client, ok := c.manager.Lookup(sum.ID); ok && client != nil {
			if nl, ok := client.(nodeLister); ok {
				for _, name := range nl.NodeNames() {
					if n := nl.Node(name); n != nil {
						out = append(out, knowledge.ConsoleHost{Cluster: sum.ID, Name: name, Address: nodeAddress(n), Kind: "node", Answering: nodeReady(n)})
					}
				}
			}
		}
		if c.hosts != nil {
			for _, h := range c.hosts.Cached(sum.ID) {
				out = append(out, knowledge.ConsoleHost{Cluster: sum.ID, Name: h.Name, Address: h.Address, Kind: "host", Answering: h.State == nodessh.HostPaired && h.Mismatch == ""})
			}
		}
	}
	return out, nil
}

// fixHits are the active fixes a question is about (wave 6g, CF-52), as
// ask_knowledge gives them.
func fixHits(ctx context.Context, svc *fixes.Service, q string) []sherlock.KnowledgeHit {
	hits, err := svc.Match(ctx, q, knownForAgents)
	if err != nil {
		return nil
	}
	var out []sherlock.KnowledgeHit
	for _, h := range hits {
		out = append(out, sherlock.KnowledgeHit{Kind: sherlock.KindFix, Title: h.Fix.Title, Path: h.Fix.ID, Date: h.Fix.Updated, Text: fixText(h.Fix)})
	}
	return out
}

// fixText is a fix in a few lines, for the agents.
func fixText(f fixes.Fix) string {
	var b strings.Builder
	if f.Cause != "" {
		b.WriteString("Cause: " + f.Cause + " ")
	}
	if f.Steps != "" {
		b.WriteString("What fixed it: " + f.Steps + " ")
	}
	if len(f.Commands) > 0 {
		b.WriteString("Changes that worked: " + strings.Join(f.Commands, "; ") + ". ")
	}
	if f.Prevention != "" {
		b.WriteString("Prevention: " + f.Prevention + " ")
	}
	fmt.Fprintf(&b, "It fixed %d case(s).", len(f.Cases))
	return b.String()
}
