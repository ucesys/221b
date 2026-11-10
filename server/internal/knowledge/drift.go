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
	// NotProposed are the differences an open known issue or an active
	// exception covers, each with which (KN-31).
	NotProposed []string `json:"notProposed,omitempty"`
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
//
// A group is checked against the cluster first (KN-30): one whose labels
// select no node or host, counting the hosts the same card adds, is not
// proposed, nor one that selects the same nodes as a fleet the console
// has; a proposed fleet says its members now. A host or a group an open
// known issue or an active exception covers is not proposed either
// (KN-31). It returns how many cards it proposed, and what it left out
// and why, for the sync's report.
func (s *Service) proposeInventory(ctx context.Context, changed []readItem, m *Manifest, req SyncRequest) (int, []string) {
	if s.o.Console == nil {
		return 0, nil
	}
	hosts, err := s.o.Console.Hosts(ctx)
	if err != nil {
		s.o.Logger.Warn("the console's hosts could not be read for the inventory", "err", err)
		return 0, nil
	}
	byName, byAddr := knownHosts(hosts)
	labelled := labelsOf(hosts, DefaultCluster)
	// The fleets the console has, when it says; without them no fleet is
	// proposed, rather than one that may be there.
	fleets, listsFleets := map[string]bool{}, false
	var have []ConsoleFleet
	if cf, ok := s.o.Console.(ConsoleFleets); ok {
		if list, err := cf.Fleets(ctx, DefaultCluster); err == nil {
			listsFleets, have = true, list
			for _, f := range list {
				fleets[strings.ToLower(f.Name)] = true
			}
		} else {
			s.o.Logger.Warn("the console's fleets could not be read for the inventory", "err", err)
		}
	}
	settle := s.settler(ctx)
	now := s.o.Now().UTC()
	n := 0
	var left []string
	for _, ri := range changed {
		st := m.Items[ri.key]
		if st == nil || ri.source.Kind == SourceConsole {
			continue
		}
		var actions []ProposalAction
		var names, groups []string
		// added are the labels of the hosts this card adds, by name.
		added := map[string]map[string]string{}
		for _, h := range st.Hosts {
			if _, ok := byName[strings.ToLower(h.Name)]; ok {
				continue
			}
			if _, ok := byAddr[h.Address]; ok && h.Address != "" {
				continue
			}
			if why := settle.why(DefaultCluster, "Host/"+h.Name, RuleHostMissing); why != "" {
				left = append(left, fmt.Sprintf("%s in %s: %s", h.Name, st.Title, why))
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
			added[h.Name] = h.Labels
		}
		for _, f := range st.Fleets {
			if !listsFleets || fleets[strings.ToLower(f.Name)] {
				continue
			}
			labels := labelList(f.Labels)
			where := fmt.Sprintf("“%s” in %s", f.Name, st.Title)
			members, more := selected(labelled, f.Labels), selected(added, f.Labels)
			if len(members) == 0 && len(more) == 0 {
				left = append(left, where+": "+absentWords(labelled, f.Labels))
				continue
			}
			if o := sameFleet(have, members); o != "" && len(more) == 0 {
				left = append(left, fmt.Sprintf("%s: same nodes as the fleet %s", where, o))
				continue
			}
			if why := settle.why(DefaultCluster, "Fleet/"+fleetSlug(f.Name), RuleFleetMissing); why != "" {
				left = append(left, where+": "+why)
				continue
			}
			actions = append(actions, ProposalAction{Kind: "fleet.create", Args: map[string]any{"name": f.Name, "labels": labels, "taints": []string{}},
				Impact: "Adds the fleet " + f.Name + " of the nodes and hosts labelled " + strings.Join(labels, ", ") + "; " + nowWords(members, more) + "; nothing on them changes.", Risk: "low"})
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
	settle.done(ctx)
	return n, left
}

// ---------- the cluster's members (KN-30) ----------

// labelsOf are the labels of a cluster's nodes and hosts by name: a
// node's when a host has its name too.
func labelsOf(hosts []ConsoleHost, cluster string) map[string]map[string]string {
	out := map[string]map[string]string{}
	node := map[string]bool{}
	for _, h := range hosts {
		if h.Cluster != cluster || node[h.Name] {
			continue
		}
		if _, ok := out[h.Name]; ok && h.Kind != "node" {
			continue
		}
		out[h.Name], node[h.Name] = h.Labels, h.Kind == "node"
	}
	return out
}

// selected are the names, sorted, of those that carry every label.
func selected(labelled map[string]map[string]string, want map[string]string) []string {
	var out []string
	for name, labels := range labelled {
		if hasAll(labels, want) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func hasAll(labels, want map[string]string) bool {
	for k, v := range want {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// absentWords says why labels select nothing: the labels no node or host
// carries, or that none carries them together.
func absentWords(labelled map[string]map[string]string, want map[string]string) string {
	var absent []string
	for _, l := range labelList(want) {
		k, v, _ := strings.Cut(l, "=")
		if len(selected(labelled, map[string]string{k: v})) == 0 {
			absent = append(absent, l)
		}
	}
	switch {
	case len(absent) == 1:
		return absent[0] + " is on no node"
	case len(absent) > 1:
		return andList(absent) + " are on no node"
	}
	return "no node has " + andList(labelList(want)) + " together"
}

// sameFleet is the name of the fleet the console has with these members,
// if any.
func sameFleet(have []ConsoleFleet, members []string) string {
	if len(members) == 0 {
		return ""
	}
	for _, f := range have {
		m := slices.Clone(f.Members)
		sort.Strings(m)
		if slices.Equal(slices.Compact(m), members) {
			return f.Name
		}
	}
	return ""
}

// maxMembers is how many members a fleet's impact names.
const maxMembers = 5

// MembersWords says a fleet's members now: "3 nodes now: a, b, c", the
// list capped ("+N more"); "no node now" for none.
func MembersWords(names []string) string {
	if len(names) == 0 {
		return "no node now"
	}
	shown := names
	if len(shown) > maxMembers {
		shown = shown[:maxMembers]
	}
	out := fmt.Sprintf("%d %s now: %s", len(names), plural(len(names), "node", "nodes"), strings.Join(shown, ", "))
	if len(names) > len(shown) {
		out += fmt.Sprintf(" +%d more", len(names)-len(shown))
	}
	return out
}

// nowWords is a proposed fleet's members: now, and with the hosts its
// card adds.
func nowWords(members, more []string) string {
	if len(more) == 0 {
		return MembersWords(members)
	}
	all := slices.Concat(members, more)
	sort.Strings(all)
	all = slices.Compact(all)
	shown := all
	if len(shown) > maxMembers {
		shown = shown[:maxMembers]
	}
	out := fmt.Sprintf("%s; %d with the hosts this card adds: %s", MembersWords(members), len(all), strings.Join(shown, ", "))
	if len(all) > len(shown) {
		out += fmt.Sprintf(" +%d more", len(all)-len(shown))
	}
	return out
}

func andList(list []string) string {
	if len(list) < 2 {
		return strings.Join(list, "")
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}

// fleetSlug is a fleet's name as an object's name: "Fleet/burst-pool".
func fleetSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ---------- known issues and exceptions (KN-31) ----------

// The validation rules the inventory's and the drift check's differences
// are keyed by (package jobs' InventoryChecks): with the cluster and the
// object (Host/<name>, Node/<name>, Fleet/<name>), what an exception
// names, as it does a finding's.
const (
	RuleHostMissing     = "inventory.host-missing"
	RuleHostUnreachable = "inventory.host-unreachable"
	RuleAddressDiffers  = "inventory.address-differs"
	RuleHostUnlisted    = "inventory.host-unlisted"
	RuleFleetMissing    = "inventory.fleet-missing"
)

// Covers say what keeps a difference from being proposed (KN-31): an open
// known issue on its object, or an active exception for its cluster,
// object and rule, as an exception keeps a finding from a case (FX-04,
// CF-05).
type Covers interface {
	// Cover reads the open known issues and the active exceptions once,
	// for one inventory or drift check.
	Cover(ctx context.Context) Cover
	// Matched counts what each exception kept from being proposed, by id.
	Matched(ctx context.Context, hits map[string]int)
}

// Cover says what covers a difference: the id of an open known issue on
// the object ("KI-3"), or of an active exception for the cluster, object
// and rule ("EX-4"); both empty for none.
type Cover func(cluster, object, rule string) (known, exception string)

// SetCovers gives the service the known issues and the exceptions (they
// open after it); nil proposes every difference.
func (s *Service) SetCovers(c Covers) {
	s.covers.Store(&c)
}

// settle checks one run's differences against the known issues and the
// exceptions, and counts the exceptions' matches.
type settle struct {
	covers Covers
	cover  Cover
	hits   map[string]int
}

func (s *Service) settler(ctx context.Context) *settle {
	p := s.covers.Load()
	if p == nil || *p == nil {
		return &settle{}
	}
	return &settle{covers: *p, cover: (*p).Cover(ctx), hits: map[string]int{}}
}

// why says why a difference is not proposed ("accepted as-is by EX-4",
// "covered by KI-3"); "" proposes it. An exception is looked at first,
// as for a finding, and counts the match.
func (x *settle) why(cluster, object, rule string) string {
	if x.cover == nil {
		return ""
	}
	known, exception := x.cover(cluster, object, rule)
	switch {
	case exception != "":
		x.hits[exception]++
		return "accepted as-is by " + exception
	case known != "":
		return "covered by " + known
	}
	return ""
}

// done counts the exceptions' matches.
func (x *settle) done(ctx context.Context) {
	if len(x.hits) > 0 {
		x.covers.Matched(ctx, x.hits)
	}
}

// hostObject is a node's or a host's object, as a key names it.
func hostObject(h ConsoleHost) string {
	if h.Kind == "node" {
		return "Node/" + h.Name
	}
	return "Host/" + h.Name
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
	settle := s.settler(ctx)
	propose := func(key, title, reasoning string, st *ItemState, args map[string]any, h ConsoleHost, rule string) {
		// A difference an open known issue or an active exception covers
		// is reported, not proposed (KN-31).
		if why := settle.why(h.Cluster, hostObject(h), rule); why != "" {
			res.NotProposed = append(res.NotProposed, title+": "+why)
			return
		}
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
					map[string]any{"record": "host", "name": h.Name, "cluster": ch.Cluster, "field": "address", "document": h.Address, "console": ch.Address}, ch, RuleAddressDiffers)
			}
			if !ch.Answering {
				propose("answering:"+name, fmt.Sprintf("%s is listed but not answering", h.Name),
					fmt.Sprintf("“%s” lists %s, and the console's last check of it got no answer.", st.Title, h.Name), st,
					map[string]any{"record": "host", "name": h.Name, "cluster": ch.Cluster, "field": "answering", "document": "listed", "console": "not answering"}, ch, RuleHostUnreachable)
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
				map[string]any{"record": "host", "name": h.Name, "cluster": h.Cluster, "field": "documented", "document": "not named", "console": "known"}, h, RuleHostUnlisted)
		}
	}
	settle.done(ctx)
	switch {
	case len(res.Differences) == 0 && len(res.NotProposed) == 0:
		res.Text = "The documents and the console agree."
	case len(res.Differences) == 0:
		res.Text = "No new difference between the documents and the console."
	default:
		res.Text = fmt.Sprintf("%d differences between the documents and the console:\n- %s", len(res.Differences), strings.Join(res.Differences, "\n- "))
	}
	if len(res.NotProposed) > 0 {
		res.Text += "\n\nNot proposed:\n- " + strings.Join(res.NotProposed, "\n- ")
	}
	s.audit(audit.Event{Action: ActionSync, Who: audit.Who{Name: "drift check"}, Target: "drift", Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("drift check: %d differences, %d proposals, %d not proposed (a known issue or an exception covers them)", len(res.Differences), res.Proposals, len(res.NotProposed))})
	return res, nil
}
