package knowledge

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// Inventory and drift (ADR-98): what the documents list is compared with
// what the console sees. A host a document lists and the console lacks is
// proposed for adding (inventory); a host listed but not answering, an
// address that differs, and a host no document names are each proposed as
// a record of the difference (drift). Every proposal waits for a person;
// none carries a credential or a host key: an approved host is paired in
// the Add host flow.

// proposalTTL is how long a proposal waits.
const proposalTTL = 24 * time.Hour

// DefaultCluster is where hosts outside Kubernetes are proposed.
const DefaultCluster = "local"

// DriftResult is what a drift check found.
type DriftResult struct {
	Differences []string `json:"differences"`
	Proposals   int      `json:"proposals"`
	Text        string   `json:"text"`
}

func knownHosts(hosts []ConsoleHost) (byName map[string]ConsoleHost, byAddr map[string]ConsoleHost) {
	byName, byAddr = map[string]ConsoleHost{}, map[string]ConsoleHost{}
	for _, h := range hosts {
		byName[strings.ToLower(h.Name)] = h
		if h.Address != "" {
			byAddr[h.Address] = h
		}
	}
	return byName, byAddr
}

// proposeInventory proposes, for each changed document that lists hosts
// or groups of them the console lacks, adding them: the hosts with the
// labels the document gives them, and the groups as fleets of those
// labels (Mycroft's first import, GS-11), in one card per document.
func (s *Service) proposeInventory(ctx context.Context, changed []readItem, m *Manifest, req SyncRequest) int {
	if s.o.Console == nil {
		return 0
	}
	hosts, err := s.o.Console.Hosts(ctx)
	if err != nil {
		s.o.Logger.Warn("the console's hosts could not be read for the inventory", "err", err)
		return 0
	}
	byName, byAddr := knownHosts(hosts)
	// The fleets the console has, when it says; without them no fleet is
	// proposed, rather than one that may be there.
	fleets, listsFleets := map[string]bool{}, false
	if cf, ok := s.o.Console.(ConsoleFleets); ok {
		if names, err := cf.Fleets(ctx, DefaultCluster); err == nil {
			listsFleets = true
			for _, n := range names {
				fleets[strings.ToLower(n)] = true
			}
		} else {
			s.o.Logger.Warn("the console's fleets could not be read for the inventory", "err", err)
		}
	}
	now := s.o.Now().UTC()
	n := 0
	for _, ri := range changed {
		st := m.Items[ri.key]
		if st == nil || ri.source.Kind == SourceConsole {
			continue
		}
		var actions []ProposalAction
		var names, groups []string
		for _, h := range st.Hosts {
			if _, ok := byName[strings.ToLower(h.Name)]; ok {
				continue
			}
			if _, ok := byAddr[h.Address]; ok && h.Address != "" {
				continue
			}
			args := map[string]any{"cluster": DefaultCluster, "name": h.Name}
			if h.Address != "" {
				args["address"] = h.Address
			}
			impact := "Adds " + h.Name + " as a pending host"
			if len(h.Labels) > 0 {
				args["labels"] = h.Labels
				impact += ", labelled " + strings.Join(labelList(h.Labels), ", ")
			}
			actions = append(actions, ProposalAction{Kind: "host.add", Args: args, Impact: impact + "; it is paired afterwards in the Add host flow, with its own credential and key approval.", Risk: "low"})
			names = append(names, h.Name)
		}
		for _, f := range st.Fleets {
			if !listsFleets || fleets[strings.ToLower(f.Name)] {
				continue
			}
			labels := labelList(f.Labels)
			actions = append(actions, ProposalAction{Kind: "fleet.create", Args: map[string]any{"name": f.Name, "labels": labels, "taints": []string{}},
				Impact: "Adds the fleet " + f.Name + " of the nodes and hosts labelled " + strings.Join(labels, ", ") + "; nothing on them changes.", Risk: "low"})
			groups = append(groups, f.Name)
		}
		if len(actions) == 0 {
			continue
		}
		var what, why []string
		if len(names) > 0 {
			what = append(what, fmt.Sprintf("%d %s", len(names), plural(len(names), "host", "hosts")))
			why = append(why, fmt.Sprintf("lists %s, which the console does not know", strings.Join(names, ", ")))
		}
		if len(groups) > 0 {
			what = append(what, fmt.Sprintf("%d %s", len(groups), plural(len(groups), "fleet", "fleets")))
			why = append(why, fmt.Sprintf("names the %s %s, which the console does not have", plural(len(groups), "group", "groups"), strings.Join(groups, ", ")))
		}
		ref := st.ref()
		p := Proposal{Agent: MycroftID, Source: ProposalSource{Kind: "knowledge", ID: ri.key, Label: fmt.Sprintf("%s “%s”%s", ri.source.Name, st.Title, versionWords(st.Version)), SetBy: req.By},
			Cluster: DefaultCluster, Title: fmt.Sprintf("Add %s from “%s”", strings.Join(what, " and "), st.Title),
			Reasoning: fmt.Sprintf("“%s” %s.", st.Title, strings.Join(why, ", and ")),
			Evidence:  []Evidence{{Kind: "page", Text: fmt.Sprintf("“%s”%s, %s", st.Title, versionWords(st.Version), ref.Date.Format("2006-01-02")), Ref: st.URL, At: ref.Date}},
			Actions:   actions, Condition: Condition{Kind: "none", Args: map[string]any{}}, DedupKey: "inventory:" + ri.key, Expires: now.Add(proposalTTL)}
		if _, err := s.o.Proposer.Propose(ctx, p); err != nil {
			s.o.Logger.Warn("an inventory proposal could not be made", "item", ri.key, "err", err)
			continue
		}
		n++
	}
	return n
}

// labelList is labels as fleets write them: "key=value", sorted.
func labelList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

var (
	labelKey   = regexp.MustCompile(`^([a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)
	labelValue = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)
)

// cleanLabels keeps the labels a document gives that Kubernetes would
// take, at most eight; nil for none.
func cleanLabels(in map[string]string) map[string]string {
	var out map[string]string
	for _, k := range slices.Sorted(maps.Keys(in)) {
		k2, v := strings.TrimSpace(k), strings.TrimSpace(in[k])
		if !labelKey.MatchString(k2) || !labelValue.MatchString(v) || len(out) >= 8 {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k2] = v
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Drift compares what the documents list with what the console sees and
// proposes a record of each difference. It runs on the leader.
func (s *Service) Drift(ctx context.Context) (DriftResult, error) {
	if err := s.leads(); err != nil {
		return DriftResult{}, err
	}
	if s.o.Console == nil {
		return DriftResult{Text: "The console's hosts are not offered here; nothing was compared."}, nil
	}
	hosts, err := s.o.Console.Hosts(ctx)
	if err != nil {
		return DriftResult{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return DriftResult{}, err
	}
	s.mu.RLock()
	m := s.manifest
	s.mu.RUnlock()
	byName, _ := knownHosts(hosts)
	now := s.o.Now().UTC()
	var res DriftResult
	propose := func(key, title, reasoning string, st *ItemState, args map[string]any) {
		res.Differences = append(res.Differences, title)
		ev := []Evidence{{Kind: "fact", Text: reasoning, At: now}}
		label := "the console"
		if st != nil {
			ref := st.ref()
			label = fmt.Sprintf("“%s”%s", st.Title, versionWords(st.Version))
			ev = append(ev, Evidence{Kind: "page", Text: fmt.Sprintf("“%s”%s, %s", st.Title, versionWords(st.Version), ref.Date.Format("2006-01-02")), Ref: st.URL, At: ref.Date})
		}
		p := Proposal{Agent: MycroftID, Source: ProposalSource{Kind: "knowledge", ID: "drift", Label: "Drift check against " + label}, Cluster: DefaultCluster, Title: title, Reasoning: reasoning,
			Evidence: ev, Actions: []ProposalAction{{Kind: "record", Args: args, Impact: "Records the difference for a person to settle: in the document, or in the console.", Risk: "low"}},
			Condition: Condition{Kind: "none", Args: map[string]any{}}, DedupKey: "drift:" + key, Expires: now.Add(proposalTTL)}
		if _, err := s.o.Proposer.Propose(ctx, p); err == nil {
			res.Proposals++
		} else {
			s.o.Logger.Warn("a drift proposal could not be made", "err", err)
		}
	}
	named := map[string]bool{}
	keys := make([]string, 0, len(m.Items))
	for k := range m.Items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	listing := false
	for _, k := range keys {
		st := m.Items[k]
		if st.Gone || st.Source == ConsoleSourceID {
			continue
		}
		if x, ok := set.source(st.Source); !ok || x.Off {
			continue
		}
		for _, h := range st.Hosts {
			listing = true
			name := strings.ToLower(h.Name)
			named[name] = true
			ch, ok := byName[name]
			if !ok {
				continue
			}
			if h.Address != "" && ch.Address != "" && h.Address != ch.Address {
				propose("address:"+name, fmt.Sprintf("%s: the document's address differs", h.Name),
					fmt.Sprintf("“%s” gives %s the address %s; the console reaches it at %s.", st.Title, h.Name, h.Address, ch.Address), st,
					map[string]any{"record": "host", "name": h.Name, "cluster": ch.Cluster, "field": "address", "document": h.Address, "console": ch.Address})
			}
			if !ch.Answering {
				propose("answering:"+name, fmt.Sprintf("%s is listed but not answering", h.Name),
					fmt.Sprintf("“%s” lists %s, and the console's last check of it got no answer.", st.Title, h.Name), st,
					map[string]any{"record": "host", "name": h.Name, "cluster": ch.Cluster, "field": "answering", "document": "listed", "console": "not answering"})
			}
		}
	}
	// A host no document names, once any document lists hosts at all.
	if listing {
		for _, h := range hosts {
			if named[strings.ToLower(h.Name)] || h.Kind != "host" {
				continue
			}
			propose("unnamed:"+strings.ToLower(h.Name), fmt.Sprintf("%s is in no document", h.Name),
				fmt.Sprintf("The console knows %s in %s, and no document Mycroft reads names it.", h.Name, h.Cluster), nil,
				map[string]any{"record": "host", "name": h.Name, "cluster": h.Cluster, "field": "documented", "document": "not named", "console": "known"})
		}
	}
	if len(res.Differences) == 0 {
		res.Text = "The documents and the console agree."
	} else {
		res.Text = fmt.Sprintf("%d differences between the documents and the console:\n- %s", len(res.Differences), strings.Join(res.Differences, "\n- "))
	}
	s.audit(audit.Event{Action: ActionSync, Who: audit.Who{Name: "drift check"}, Target: "drift", Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("drift check: %d differences, %d proposals", len(res.Differences), res.Proposals)})
	return res, nil
}
