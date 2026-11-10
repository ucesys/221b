package cards

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/nodessh"
)

// Watson's console actions (ADR-101): hosts outside Kubernetes, fleets of
// either kind and Automation's parameters, each with its own validation,
// impact and fingerprint, applied as the SuperAdmin who approves the card
// and audited as if they had made the change on its page, which names the
// card. Two things never go on a card: a host key to trust and a
// credential. A host an approved card adds waits as pending until a person
// pairs it in the Add host flow, with one credential and one approval of
// every key.

// HostBook keeps the hosts outside Kubernetes; *nodessh.Service is one.
type HostBook interface {
	Hosts(ctx context.Context, cluster string) ([]nodessh.Host, error)
	AddHost(ctx context.Context, h nodessh.Host, by string) (nodessh.Host, error)
	LabelHost(ctx context.Context, cluster, name string, labels map[string]string) (nodessh.Host, error)
	RemoveHost(ctx context.Context, cluster, name string) (nodessh.Host, string, error)
}

// FleetBook keeps the fleets, with the console's settings.
type FleetBook interface {
	Fleets(cluster string) []fleets.Fleet
	SetFleets(ctx context.Context, cluster string, list []fleets.Fleet) error
}

// MemberBook says which nodes and hosts of a cluster a fleet takes now
// (KN-30): what a fleet's plan names.
type MemberBook interface {
	Members(ctx context.Context, cluster string, f fleets.Fleet) ([]string, error)
}

// ParamBook keeps Automation's parameters; *automation.Store is one.
type ParamBook interface {
	Variables(ctx context.Context, cluster string) (map[string]automation.FleetVariables, error)
	SetVariables(ctx context.Context, cluster, fleet string, fp automation.FleetVariables) error
}

// Console is what the console actions work on; a nil part leaves its
// actions out.
type Console struct {
	Hosts  HostBook
	Fleets FleetBook
	Params ParamBook
	// Members names a new fleet's members in its plan; nil leaves them
	// out.
	Members MemberBook
	// LoginUser is the login user a host gets when its card names none.
	LoginUser func() string
	Audit     *audit.Log
}

// RegisterConsole registers the console actions whose parts are given.
func (s *Store) RegisterConsole(c Console) {
	if c.Hosts != nil {
		s.Register(KindHostAdd, hostAdd{c})
		s.Register(KindHostLabel, hostLabel{c})
		s.Register(KindHostRemove, hostRemove{c})
	}
	if c.Fleets != nil {
		s.Register(KindFleetCreate, fleetCreate{c})
		s.Register(KindFleetUpdate, fleetUpdate{c})
		if c.Params != nil {
			s.Register(KindAutomationParams, params{c})
		}
	}
}

// digest is a fingerprint of what a value says.
func digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// by names who made a change from a card in the records it changes.
func byOf(c Card, by Approver) string {
	return fmt.Sprintf("%s (proposal %s by %s)", by.Name, c.ID, c.Agent)
}

func (c Console) record(action string, card Card, by Approver, target, outcome, detail string) {
	if c.Audit == nil {
		return
	}
	c.Audit.Record(audit.Event{Action: action, Who: audit.Who{Subject: by.Key, Name: by.Name}, Cluster: card.Cluster, Target: target, Outcome: outcome, Detail: detail + " (approved proposal " + card.ID + ")"})
}

func labelsText(m map[string]string) string {
	keys := slices.Sorted(maps.Keys(m))
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + m[k]
	}
	return strings.Join(out, ", ")
}

// ---------- hosts ----------

type hostAddArgs struct {
	Name      string            `json:"name"`
	Address   string            `json:"address"`
	Port      int               `json:"port,omitempty"`
	LoginUser string            `json:"loginUser,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type hostAdd struct{ c Console }

func (h hostAdd) Admin() bool { return true }

func (h hostAdd) host(cluster string, a Action) (nodessh.Host, error) {
	var in hostAddArgs
	if err := decodeArgs(a.Args, &in); err != nil {
		return nodessh.Host{}, fmt.Errorf("%w: host.add takes name, address, port, loginUser and labels: %v", ErrInvalid, err)
	}
	x := nodessh.Host{Cluster: cluster, Name: in.Name, Address: in.Address, Port: in.Port, LoginUser: in.LoginUser, Labels: in.Labels}
	if x.LoginUser == "" && h.c.LoginUser != nil {
		x.LoginUser = h.c.LoginUser()
	}
	if x.LoginUser == "" {
		x.LoginUser = "root"
	}
	if err := x.Validate(); err != nil {
		return x, fmt.Errorf("%w: %v", ErrInvalid, strings.TrimPrefix(err.Error(), nodessh.ErrInvalid.Error()+": "))
	}
	return x, nil
}

func (h hostAdd) Check(a Action) error {
	_, err := h.host("check", a)
	return err
}

func findHost(ctx context.Context, b HostBook, cluster, name string) (nodessh.Host, bool, error) {
	list, err := b.Hosts(ctx, cluster)
	if err != nil {
		return nodessh.Host{}, false, err
	}
	for _, x := range list {
		if x.Name == name {
			return x, true, nil
		}
	}
	return nodessh.Host{}, false, nil
}

// hostPrint is what a host's fingerprint covers: never its key, which is
// only hashed with the rest.
func hostPrint(x nodessh.Host) string {
	return digest([]any{x.Name, x.Address, x.Port, x.LoginUser, x.Labels, x.State, x.Fingerprint})
}

func (h hostAdd) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	x, err := h.host(cluster, a)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Summary: fmt.Sprintf("Add host %s (%s)", x.Name, x.Address), Targets: []string{"host " + x.Name}}
	old, found, err := findHost(ctx, h.c.Hosts, cluster, x.Name)
	if err != nil {
		return p, err
	}
	if found {
		p.Fingerprint = "present:" + hostPrint(old)
		p.Impact = append(p.Impact, fmt.Sprintf("A host is already called %s, at %s (%s): approving takes it up again only if it is pending at the same address.", old.Name, old.Address, old.State))
	} else {
		p.Fingerprint = "absent"
	}
	line := fmt.Sprintf("Adds host %s at %s, port %d, login user %s", x.Name, x.Address, x.Port, x.LoginUser)
	if len(x.Labels) > 0 {
		line += ", labels " + labelsText(x.Labels)
	}
	p.Impact = append(p.Impact, line+".", "It stays pending until a person pairs it in Add host, with one credential and one approval of its key; no key and no credential comes with this card.")
	return p, nil
}

func (h hostAdd) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	x, err := h.host(c.Cluster, c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	got, err := h.c.Hosts.AddHost(ctx, x, byOf(c, by))
	if err != nil {
		h.c.record(audit.ActionHostAdd, c, by, "host "+x.Name, audit.OutcomeFailed, "adding host "+x.Name+" failed: "+err.Error())
		return Result{}, err
	}
	h.c.record(audit.ActionHostAdd, c, by, "host "+got.Name, audit.OutcomeOK, fmt.Sprintf("host %s added at %s port %d, login user %s; pending until paired", got.Name, got.Address, got.Port, got.LoginUser))
	return Result{OK: true, Summary: fmt.Sprintf("host %s added, pending: pair it in Add host on its cluster's Nodes tab", got.Name), Ref: "host:" + got.Name}, nil
}

type hostLabelArgs struct {
	Name   string            `json:"name"`
	Set    map[string]string `json:"set,omitempty"`
	Remove []string          `json:"remove,omitempty"`
}

type hostLabel struct{ c Console }

func (h hostLabel) Admin() bool { return true }

func (h hostLabel) args(a Action) (hostLabelArgs, error) {
	var in hostLabelArgs
	if err := decodeArgs(a.Args, &in); err != nil {
		return in, fmt.Errorf("%w: host.label takes name, set and remove: %v", ErrInvalid, err)
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return in, fmt.Errorf("%w: name the host", ErrInvalid)
	case len(in.Set) == 0 && len(in.Remove) == 0:
		return in, fmt.Errorf("%w: set or remove a label", ErrInvalid)
	}
	probe := nodessh.Host{Name: "probe", Address: "10.0.0.1", LoginUser: "root", Labels: in.Set}
	if err := probe.Validate(); err != nil {
		return in, fmt.Errorf("%w: %v", ErrInvalid, strings.TrimPrefix(err.Error(), nodessh.ErrInvalid.Error()+": "))
	}
	return in, nil
}

func (h hostLabel) Check(a Action) error { _, err := h.args(a); return err }

func (in hostLabelArgs) next(cur map[string]string) map[string]string {
	out := maps.Clone(cur)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, in.Set)
	for _, k := range in.Remove {
		delete(out, k)
	}
	return out
}

func (h hostLabel) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	in, err := h.args(a)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Summary: "Label host " + in.Name, Targets: []string{"host " + in.Name}}
	x, found, err := findHost(ctx, h.c.Hosts, cluster, in.Name)
	if err != nil {
		return p, err
	}
	if !found {
		return p, fmt.Errorf("there is no host %s", in.Name)
	}
	p.Fingerprint = hostPrint(x)
	for _, k := range slices.Sorted(maps.Keys(in.Set)) {
		if old, ok := x.Labels[k]; ok {
			p.Impact = append(p.Impact, fmt.Sprintf("%s: %s → %s", k, old, in.Set[k]))
		} else {
			p.Impact = append(p.Impact, fmt.Sprintf("adds %s=%s", k, in.Set[k]))
		}
	}
	for _, k := range in.Remove {
		if old, ok := x.Labels[k]; ok {
			p.Impact = append(p.Impact, fmt.Sprintf("removes %s=%s", k, old))
		}
	}
	p.Impact = append(p.Impact, "Fleets that select hosts by these labels gain or lose "+in.Name+".")
	return p, nil
}

func (h hostLabel) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	in, err := h.args(c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	x, found, err := findHost(ctx, h.c.Hosts, c.Cluster, in.Name)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, fmt.Errorf("there is no host %s", in.Name)
	}
	got, err := h.c.Hosts.LabelHost(ctx, c.Cluster, in.Name, in.next(x.Labels))
	if err != nil {
		h.c.record(audit.ActionHostLabel, c, by, "host "+in.Name, audit.OutcomeFailed, "labelling host "+in.Name+" failed: "+err.Error())
		return Result{}, err
	}
	h.c.record(audit.ActionHostLabel, c, by, "host "+got.Name, audit.OutcomeOK, fmt.Sprintf("host %s labelled: %s", got.Name, labelsText(got.Labels)))
	return Result{OK: true, Summary: fmt.Sprintf("host %s now has labels %s", got.Name, labelsText(got.Labels))}, nil
}

type hostRemoveArgs struct {
	Name string `json:"name"`
}

type hostRemove struct{ c Console }

func (h hostRemove) Admin() bool { return true }

func (h hostRemove) args(a Action) (hostRemoveArgs, error) {
	var in hostRemoveArgs
	if err := decodeArgs(a.Args, &in); err != nil {
		return in, fmt.Errorf("%w: host.remove takes name: %v", ErrInvalid, err)
	}
	if in.Name = strings.TrimSpace(in.Name); in.Name == "" {
		return in, fmt.Errorf("%w: name the host", ErrInvalid)
	}
	return in, nil
}

func (h hostRemove) Check(a Action) error { _, err := h.args(a); return err }

func (h hostRemove) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	in, err := h.args(a)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Summary: "Remove host " + in.Name, Targets: []string{"host " + in.Name}}
	x, found, err := findHost(ctx, h.c.Hosts, cluster, in.Name)
	if err != nil {
		return p, err
	}
	if !found {
		return p, fmt.Errorf("there is no host %s", in.Name)
	}
	p.Fingerprint = hostPrint(x)
	p.Impact = []string{fmt.Sprintf("Removes host %s at %s (%s), with its pinned key and its pairing: Automation stops reaching it.", x.Name, x.Address, x.State),
		"The console's login stays on the host until the cleanup command runs there; the result gives it."}
	return p, nil
}

func (h hostRemove) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	in, err := h.args(c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	x, cleanup, err := h.c.Hosts.RemoveHost(ctx, c.Cluster, in.Name)
	if err != nil {
		h.c.record(audit.ActionHostRemove, c, by, "host "+in.Name, audit.OutcomeFailed, "removing host "+in.Name+" failed: "+err.Error())
		return Result{}, err
	}
	h.c.record(audit.ActionHostRemove, c, by, "host "+x.Name, audit.OutcomeOK, fmt.Sprintf("host %s removed, with its pinned key and pairing (was %s)", x.Name, x.State))
	return Result{OK: true, Summary: fmt.Sprintf("host %s removed; run the cleanup command on it to take the console's login off", x.Name), Detail: cleanup}, nil
}

// ---------- fleets ----------

type fleetCreate struct{ c Console }

func (f fleetCreate) Admin() bool { return true }

func (f fleetCreate) fleet(a Action) (fleets.Fleet, error) {
	var in fleets.Fleet
	if err := decodeArgs(a.Args, &in); err != nil {
		return in, fmt.Errorf("%w: fleet.create takes a fleet: id, name, color, desc, labels, taints, kind, nodes, deploy: %v", ErrInvalid, err)
	}
	in = fleets.Clean([]fleets.Fleet{in})[0]
	if in.Labels == nil {
		in.Labels = []string{}
	}
	if in.Taints == nil {
		in.Taints = []string{}
	}
	if in.ID = strings.TrimSpace(in.ID); in.ID == "" {
		in.ID = fleetID(in.Name)
	}
	if err := fleets.Validate([]fleets.Fleet{in}); err != nil {
		return in, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return in, nil
}

// fleetID makes an ID from a name, as the page does.
func fleetID(name string) string {
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

func (f fleetCreate) Check(a Action) error { _, err := f.fleet(a); return err }

func fleetText(x fleets.Fleet) string {
	if x.Kind == fleets.KindNodes {
		return fmt.Sprintf("a list of %d nodes: %s", len(x.Nodes), strings.Join(x.Nodes, ", "))
	}
	parts := []string{}
	if len(x.Labels) > 0 {
		parts = append(parts, "labels "+strings.Join(x.Labels, ", "))
	}
	if len(x.Taints) > 0 {
		parts = append(parts, "taints "+strings.Join(x.Taints, ", "))
	}
	if len(parts) == 0 {
		return "every node"
	}
	return "the nodes with " + strings.Join(parts, " and ")
}

func (f fleetCreate) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	x, err := f.fleet(a)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Summary: "Create fleet " + x.Name, Targets: []string{"fleet " + x.Name}, Fingerprint: "absent"}
	for _, o := range f.c.Fleets.Fleets(cluster) {
		if o.ID == x.ID || strings.EqualFold(o.Name, x.Name) {
			p.Fingerprint = "present:" + digest(o)
			p.Impact = append(p.Impact, fmt.Sprintf("A fleet %s is there already: approving fails.", o.Name))
		}
	}
	p.Impact = append(p.Impact, fmt.Sprintf("Creates the fleet %s: %s.", x.Name, fleetText(x)))
	if x.Deploy != "" {
		p.Impact = append(p.Impact, "Applications deployed to it are pinned to nodes labelled "+x.Deploy+".")
	}
	// Its members now (KN-30), which are not part of what Check again
	// compares: a node may join or leave it meanwhile. None is a warning.
	if f.c.Members != nil {
		names, err := f.c.Members.Members(ctx, cluster, x)
		switch {
		case err != nil:
			p.Impact = append(p.Impact, "Its nodes now could not be read: "+err.Error())
		case len(names) == 0:
			p.Impact = append(p.Impact, "0 nodes now.")
			p.Warning = "No node or host has its labels now: the fleet would be empty."
			if x.Kind == fleets.KindNodes {
				p.Warning = "None of the nodes it lists is on the cluster now: the fleet would be empty."
			}
		default:
			p.Impact = append(p.Impact, membersWords(names)+".")
		}
	}
	return p, nil
}

// maxMembers is how many members a plan names.
const maxMembers = 5

// membersWords is a fleet's members now: "3 nodes now: a, b, c", the
// list capped ("+N more").
func membersWords(names []string) string {
	names = slices.Clone(names)
	sort.Strings(names)
	shown := names
	if len(shown) > maxMembers {
		shown = shown[:maxMembers]
	}
	unit := "nodes"
	if len(names) == 1 {
		unit = "node"
	}
	out := fmt.Sprintf("%d %s now: %s", len(names), unit, strings.Join(shown, ", "))
	if len(names) > len(shown) {
		out += fmt.Sprintf(" +%d more", len(names)-len(shown))
	}
	return out
}

func (f fleetCreate) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	x, err := f.fleet(c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	list := f.c.Fleets.Fleets(c.Cluster)
	for _, o := range list {
		if o.ID == x.ID || strings.EqualFold(o.Name, x.Name) {
			return Result{}, fmt.Errorf("a fleet %s is there already", o.Name)
		}
	}
	list = append(slices.Clone(list), x)
	if err := fleets.Validate(list); err != nil {
		return Result{}, err
	}
	if err := f.c.Fleets.SetFleets(ctx, c.Cluster, list); err != nil {
		f.c.record(audit.ActionFleets, c, by, "fleets", audit.OutcomeFailed, "creating fleet "+x.Name+" failed: "+err.Error())
		return Result{}, err
	}
	f.c.record(audit.ActionFleets, c, by, "fleets", audit.OutcomeOK, fmt.Sprintf("fleet %s created: %s", x.Name, fleetText(x)))
	return Result{OK: true, Summary: "fleet " + x.Name + " created"}, nil
}

type fleetUpdateArgs struct {
	ID          string    `json:"id"`
	Name        *string   `json:"name,omitempty"`
	Desc        *string   `json:"desc,omitempty"`
	Color       *int      `json:"color,omitempty"`
	Labels      *[]string `json:"labels,omitempty"`
	Taints      *[]string `json:"taints,omitempty"`
	Nodes       *[]string `json:"nodes,omitempty"`
	AddNodes    []string  `json:"addNodes,omitempty"`
	RemoveNodes []string  `json:"removeNodes,omitempty"`
	Deploy      *string   `json:"deploy,omitempty"`
}

type fleetUpdate struct{ c Console }

func (f fleetUpdate) Admin() bool { return true }

func (f fleetUpdate) args(a Action) (fleetUpdateArgs, error) {
	var in fleetUpdateArgs
	if err := decodeArgs(a.Args, &in); err != nil {
		return in, fmt.Errorf("%w: fleet.update takes id and name, desc, color, labels, taints, nodes, addNodes, removeNodes, deploy: %v", ErrInvalid, err)
	}
	if in.ID = strings.TrimSpace(in.ID); in.ID == "" {
		return in, fmt.Errorf("%w: name the fleet by its id", ErrInvalid)
	}
	if in.Name == nil && in.Desc == nil && in.Color == nil && in.Labels == nil && in.Taints == nil && in.Nodes == nil && len(in.AddNodes) == 0 && len(in.RemoveNodes) == 0 && in.Deploy == nil {
		return in, fmt.Errorf("%w: change something", ErrInvalid)
	}
	return in, nil
}

func (f fleetUpdate) Check(a Action) error { _, err := f.args(a); return err }

// patch is the fleet as the card changes it.
func (in fleetUpdateArgs) patch(x fleets.Fleet) (fleets.Fleet, error) {
	if in.Name != nil {
		x.Name = *in.Name
	}
	if in.Desc != nil {
		x.Desc = *in.Desc
	}
	if in.Color != nil {
		x.Color = *in.Color
	}
	if in.Labels != nil {
		x.Labels = *in.Labels
	}
	if in.Taints != nil {
		x.Taints = *in.Taints
	}
	if in.Deploy != nil {
		x.Deploy = *in.Deploy
	}
	if in.Nodes != nil || len(in.AddNodes) > 0 || len(in.RemoveNodes) > 0 {
		if x.Kind != fleets.KindNodes {
			return x, fmt.Errorf("%w: the fleet %s selects nodes by labels and taints; it has no list of nodes", ErrInvalid, x.Name)
		}
		if in.Nodes != nil {
			x.Nodes = *in.Nodes
		}
		x.Nodes = append(slices.Clone(x.Nodes), in.AddNodes...)
		x.Nodes = slices.DeleteFunc(x.Nodes, func(n string) bool { return slices.Contains(in.RemoveNodes, n) })
	}
	return fleets.Clean([]fleets.Fleet{x})[0], nil
}

func diffList(what string, a, b []string) []string {
	var out []string
	for _, x := range b {
		if !slices.Contains(a, x) {
			out = append(out, "adds "+what+" "+x)
		}
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, "removes "+what+" "+x)
		}
	}
	return out
}

func (f fleetUpdate) find(cluster, id string) (fleets.Fleet, []fleets.Fleet, int, error) {
	list := f.c.Fleets.Fleets(cluster)
	i := slices.IndexFunc(list, func(x fleets.Fleet) bool { return x.ID == id })
	if i < 0 {
		return fleets.Fleet{}, list, -1, fmt.Errorf("there is no fleet %s", id)
	}
	return list[i], list, i, nil
}

func (f fleetUpdate) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	in, err := f.args(a)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Summary: "Change fleet " + in.ID, Targets: []string{"fleet " + in.ID}}
	x, _, _, err := f.find(cluster, in.ID)
	if err != nil {
		return p, err
	}
	p.Summary, p.Targets, p.Fingerprint = "Change fleet "+x.Name, []string{"fleet " + x.Name}, digest(x)
	next, err := in.patch(x)
	if err != nil {
		return p, err
	}
	if next.Name != x.Name {
		p.Impact = append(p.Impact, fmt.Sprintf("renames it %s", next.Name))
	}
	if next.Desc != x.Desc {
		p.Impact = append(p.Impact, "changes its description")
	}
	if next.Color != x.Color {
		p.Impact = append(p.Impact, "changes its colour")
	}
	p.Impact = append(p.Impact, diffList("label", x.Labels, next.Labels)...)
	p.Impact = append(p.Impact, diffList("taint", x.Taints, next.Taints)...)
	p.Impact = append(p.Impact, diffList("node", x.Nodes, next.Nodes)...)
	if next.Deploy != x.Deploy {
		p.Impact = append(p.Impact, fmt.Sprintf("pins its applications to %q instead of %q", next.Deploy, x.Deploy))
	}
	p.Impact = append(p.Impact, fmt.Sprintf("Afterwards it holds %s.", fleetText(next)))
	return p, nil
}

func (f fleetUpdate) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	in, err := f.args(c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	x, list, at, err := f.find(c.Cluster, in.ID)
	if err != nil {
		return Result{}, err
	}
	next, err := in.patch(x)
	if err != nil {
		return Result{}, err
	}
	list = slices.Clone(list)
	list[at] = next
	if err := fleets.Validate(list); err != nil {
		return Result{}, err
	}
	if err := f.c.Fleets.SetFleets(ctx, c.Cluster, list); err != nil {
		f.c.record(audit.ActionFleets, c, by, "fleets", audit.OutcomeFailed, "changing fleet "+x.Name+" failed: "+err.Error())
		return Result{}, err
	}
	f.c.record(audit.ActionFleets, c, by, "fleets", audit.OutcomeOK, fmt.Sprintf("fleet %s changed: %s", next.Name, fleetText(next)))
	return Result{OK: true, Summary: "fleet " + next.Name + " changed"}, nil
}

// ---------- Automation's parameters ----------

type paramsArgs struct {
	Fleet      string                       `json:"fleet"`
	Values     map[string]string            `json:"values,omitempty"`
	Unset      []string                     `json:"unset,omitempty"`
	Nodes      map[string]map[string]string `json:"nodes,omitempty"`
	UnsetNodes map[string][]string          `json:"unsetNodes,omitempty"`
}

type params struct{ c Console }

func (p params) Admin() bool { return true }

func (p params) args(a Action) (paramsArgs, error) {
	var in paramsArgs
	if err := decodeArgs(a.Args, &in); err != nil {
		return in, fmt.Errorf("%w: automation.params takes fleet, values, unset, nodes and unsetNodes: %v", ErrInvalid, err)
	}
	if in.Fleet = strings.TrimSpace(in.Fleet); in.Fleet == "" {
		return in, fmt.Errorf("%w: name the fleet by its id", ErrInvalid)
	}
	if len(in.Values) == 0 && len(in.Unset) == 0 && len(in.Nodes) == 0 && len(in.UnsetNodes) == 0 {
		return in, fmt.Errorf("%w: set or unset a parameter", ErrInvalid)
	}
	if err := automation.ValidateVariables(automation.FleetVariables{Values: in.Values, Nodes: in.Nodes}); err != nil {
		return in, err
	}
	return in, nil
}

func (p params) Check(a Action) error { _, err := p.args(a); return err }

// next is the fleet's parameters as the card leaves them: only values
// and per-node settings; a card never raises a privilege ceiling or
// unmarks a sensitive parameter.
func (in paramsArgs) next(cur automation.FleetVariables) automation.FleetVariables {
	out := cur
	out.Values = maps.Clone(cur.Values)
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	maps.Copy(out.Values, in.Values)
	for _, k := range in.Unset {
		delete(out.Values, k)
	}
	out.Nodes = map[string]map[string]string{}
	for n, m := range cur.Nodes {
		out.Nodes[n] = maps.Clone(m)
	}
	for n, m := range in.Nodes {
		if out.Nodes[n] == nil {
			out.Nodes[n] = map[string]string{}
		}
		maps.Copy(out.Nodes[n], m)
	}
	for n, ks := range in.UnsetNodes {
		for _, k := range ks {
			delete(out.Nodes[n], k)
		}
		if len(out.Nodes[n]) == 0 {
			delete(out.Nodes, n)
		}
	}
	return out
}

func (p params) fleetName(cluster, id string) (string, bool) {
	for _, f := range p.c.Fleets.Fleets(cluster) {
		if f.ID == id {
			return f.Name, true
		}
	}
	return id, false
}

func changes(prefix string, a, b map[string]string) []string {
	var out []string
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	names := slices.Collect(maps.Keys(keys))
	sort.Strings(names)
	for _, k := range names {
		old, had := a[k]
		now, has := b[k]
		switch {
		case had && !has:
			out = append(out, fmt.Sprintf("%sunsets %s (was %q)", prefix, k, old))
		case !had && has:
			out = append(out, fmt.Sprintf("%ssets %s = %q", prefix, k, now))
		case old != now:
			out = append(out, fmt.Sprintf("%s%s: %q → %q", prefix, k, old, now))
		}
	}
	return out
}

func (p params) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	in, err := p.args(a)
	if err != nil {
		return Plan{}, err
	}
	name, ok := p.fleetName(cluster, in.Fleet)
	pl := Plan{Summary: "Set Automation parameters of fleet " + name, Targets: []string{"parameters of fleet " + name}}
	if !ok {
		return pl, fmt.Errorf("there is no fleet %s", in.Fleet)
	}
	all, err := p.c.Params.Variables(ctx, cluster)
	if err != nil {
		return pl, err
	}
	cur := all[in.Fleet]
	next := in.next(cur)
	pl.Fingerprint = digest(cur)
	pl.Impact = changes("", cur.Values, next.Values)
	nodes := map[string]bool{}
	for n := range cur.Nodes {
		nodes[n] = true
	}
	for n := range next.Nodes {
		nodes[n] = true
	}
	for _, n := range slices.Sorted(maps.Keys(nodes)) {
		pl.Impact = append(pl.Impact, changes("node "+n+": ", cur.Nodes[n], next.Nodes[n])...)
	}
	if len(pl.Impact) == 0 {
		pl.Impact = []string{"No parameter changes: they are as the card asks already."}
	}
	pl.Impact = append(pl.Impact, "The fleet's next runs use them; nothing runs now.")
	return pl, nil
}

func (p params) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	in, err := p.args(c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	name, ok := p.fleetName(c.Cluster, in.Fleet)
	if !ok {
		return Result{}, fmt.Errorf("there is no fleet %s", in.Fleet)
	}
	all, err := p.c.Params.Variables(ctx, c.Cluster)
	if err != nil {
		return Result{}, err
	}
	next := in.next(all[in.Fleet])
	if err := p.c.Params.SetVariables(ctx, c.Cluster, in.Fleet, next); err != nil {
		p.c.record(audit.ActionAutomationParams, c, by, "fleet "+in.Fleet, audit.OutcomeFailed, err.Error())
		return Result{}, err
	}
	changed := changes("", all[in.Fleet].Values, next.Values)
	p.c.record(audit.ActionAutomationParams, c, by, "fleet "+in.Fleet, audit.OutcomeOK, "parameters set: "+strings.Join(changed, ", "))
	return Result{OK: true, Summary: "parameters of fleet " + name + " set"}, nil
}

// ---------- other console records ----------

// Record is one more kind of console record a card may change, each with
// its own card shape: an action of kind record whose args name its Type.
// The console adds them one by one (design: "Other console records").
type Record struct {
	Type  string
	Label string
	// Admin says only a SuperAdmin approves it.
	Admin    bool
	Check    func(args map[string]any) error
	Describe func(ctx context.Context, cluster string, args map[string]any) (Plan, error)
	Apply    func(ctx context.Context, c Card, args map[string]any, by Approver) (Result, error)
}

// RegisterRecord adds a kind of record.
func (s *Store) RegisterRecord(r Record) {
	s.emu.Lock()
	defer s.emu.Unlock()
	s.records[r.Type] = r
}

func (s *Store) recordOf(a Action) (Record, map[string]any, error) {
	t, _ := a.Args["type"].(string)
	s.emu.RLock()
	r, ok := s.records[t]
	s.emu.RUnlock()
	if !ok {
		return r, nil, fmt.Errorf("%w: Watson keeps no record of type %q", ErrInvalid, t)
	}
	rest := maps.Clone(a.Args)
	delete(rest, "type")
	return r, rest, nil
}

func (s *Store) recordAdmin(a Action) bool {
	r, _, err := s.recordOf(a)
	return err != nil || r.Admin
}

// recordExecutor runs a record action by its type.
type recordExecutor struct{ s *Store }

func (e recordExecutor) Admin() bool { return false }

func (e recordExecutor) Check(a Action) error {
	r, args, err := e.s.recordOf(a)
	if err != nil {
		return err
	}
	if r.Check == nil || r.Describe == nil || r.Apply == nil {
		return errors.New("a record type is incomplete")
	}
	return r.Check(args)
}

func (e recordExecutor) Describe(ctx context.Context, cluster string, a Action) (Plan, error) {
	r, args, err := e.s.recordOf(a)
	if err != nil {
		return Plan{}, err
	}
	return r.Describe(ctx, cluster, args)
}

func (e recordExecutor) Apply(ctx context.Context, c Card, i int, by Approver) (Result, error) {
	r, args, err := e.s.recordOf(c.Actions[i])
	if err != nil {
		return Result{}, err
	}
	return r.Apply(ctx, c, args, by)
}
