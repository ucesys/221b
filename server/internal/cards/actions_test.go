package cards

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/nodessh"
)

type fakeHosts struct {
	mu    sync.Mutex
	hosts []nodessh.Host
	by    []string
}

func (f *fakeHosts) Hosts(_ context.Context, cluster string) ([]nodessh.Host, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []nodessh.Host
	for _, h := range f.hosts {
		if h.Cluster == cluster {
			out = append(out, h)
		}
	}
	return out, nil
}

func (f *fakeHosts) AddHost(_ context.Context, h nodessh.Host, by string) (nodessh.Host, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.hosts {
		if x.Cluster == h.Cluster && x.Name == h.Name {
			return h, fmt.Errorf("%w: %s", nodessh.ErrConflict, h.Name)
		}
	}
	h.State, h.AddedBy = nodessh.HostPending, by
	f.hosts = append(f.hosts, h)
	f.by = append(f.by, by)
	return h, nil
}

func (f *fakeHosts) LabelHost(_ context.Context, cluster, name string, labels map[string]string) (nodessh.Host, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.hosts {
		if x.Cluster == cluster && x.Name == name {
			f.hosts[i].Labels = labels
			return f.hosts[i], nil
		}
	}
	return nodessh.Host{}, nodessh.ErrNoHost
}

func (f *fakeHosts) RemoveHost(_ context.Context, cluster, name string) (nodessh.Host, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.hosts {
		if x.Cluster == cluster && x.Name == name {
			f.hosts = append(f.hosts[:i], f.hosts[i+1:]...)
			return x, "sudo sh -c 'remove the console lines'", nil
		}
	}
	return nodessh.Host{}, "", nodessh.ErrNoHost
}

type fakeFleets struct {
	mu   sync.Mutex
	list map[string][]fleets.Fleet
}

func (f *fakeFleets) Fleets(cluster string) []fleets.Fleet {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fleets.Fleet(nil), f.list[cluster]...)
}

func (f *fakeFleets) SetFleets(_ context.Context, cluster string, list []fleets.Fleet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list[cluster] = list
	return nil
}

type fakeParams struct {
	mu   sync.Mutex
	vars map[string]automation.FleetVariables
}

func (f *fakeParams) Variables(context.Context, string) (map[string]automation.FleetVariables, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.vars), nil
}

func (f *fakeParams) SetVariables(_ context.Context, _, fleet string, fp automation.FleetVariables) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vars[fleet] = fp
	return nil
}

type console struct {
	hosts  *fakeHosts
	fleets *fakeFleets
	params *fakeParams
}

func withConsole(w *world) console {
	c := console{hosts: &fakeHosts{}, fleets: &fakeFleets{list: map[string][]fleets.Fleet{"dev-1": {{ID: "db", Name: "Databases", Kind: fleets.KindNodes, Nodes: []string{"db-0"}, Labels: []string{}, Taints: []string{}}}}}, params: &fakeParams{vars: map[string]automation.FleetVariables{"db": {Values: map[string]string{"SWAPPINESS": "60"}}}}}
	w.st.RegisterConsole(Console{Hosts: c.hosts, Fleets: c.fleets, Params: c.params, LoginUser: func() string { return "ops" }, Audit: w.st.opts.Audit})
	return c
}

func proposal(actions ...Action) Proposal {
	return Proposal{Agent: "mycroft", Source: Source{Kind: SourceKnowledge, Label: "Confluence: Server inventory, version 12", SetBy: "Sam"}, Cluster: "dev-1", Title: "Inventory from Server inventory", Actions: actions}
}

var admin = Approver{Person: Person{Key: "root", Name: "Root"}, Admin: true}

// Each console action runs as the SuperAdmin who approves it; a card of
// them is a SuperAdmin's to approve, as the records are today.
func TestTheConsoleActions(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	c := withConsole(w)
	// host.add: pending, with no key and no credential.
	id, err := w.st.Propose(ctx, proposal(
		Action{Kind: KindHostAdd, Args: map[string]any{"name": "backup-1", "address": "10.0.0.9", "labels": map[string]any{"role": "backup"}}},
		Action{Kind: KindHostAdd, Args: map[string]any{"name": "backup-2", "address": "10.0.0.10", "port": 2222}},
	))
	if err != nil {
		t.Fatal(err)
	}
	card, _ := w.st.Get(ctx, id)
	if card.Plans[0].Fingerprint != "absent" || !strings.Contains(strings.Join(card.Plans[0].Impact, " "), "pending until a person pairs it") {
		t.Fatalf("plan %+v", card.Plans[0])
	}
	if _, err := w.st.Approve(ctx, id, person("pat"), ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-SuperAdmin: %v", err)
	}
	if _, err := w.st.Approve(ctx, id, admin, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, id, Applied)
	hosts, _ := c.hosts.Hosts(ctx, "dev-1")
	if len(hosts) != 2 || hosts[0].State != nodessh.HostPending || hosts[0].LoginUser != "ops" || hosts[1].Port != 2222 || !strings.Contains(c.hosts.by[0], "proposal "+id) {
		t.Fatalf("hosts %+v by %v", hosts, c.hosts.by)
	}
	// A bad host is refused as it is proposed.
	if _, err := w.st.Propose(ctx, proposal(Action{Kind: KindHostAdd, Args: map[string]any{"name": "Bad Name", "address": "10.0.0.1"}})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad name: %v", err)
	}
	// host.label, re-checked: the host changed since.
	id, _ = w.st.Propose(ctx, proposal(Action{Kind: KindHostLabel, Args: map[string]any{"name": "backup-1", "set": map[string]any{"zone": "a"}, "remove": []any{"role"}}}))
	card, _ = w.st.Get(ctx, id)
	if strings.Join(card.Plans[0].Impact, "; ") != "adds zone=a; removes role=backup; Fleets that select hosts by these labels gain or lose backup-1." {
		t.Fatalf("label impact %q", card.Plans[0].Impact)
	}
	_, _ = c.hosts.LabelHost(ctx, "dev-1", "backup-1", map[string]string{"role": "archive"})
	if _, err := w.st.Approve(ctx, id, admin, ""); !errors.Is(err, ErrStale) {
		t.Fatalf("a host changed since: %v", err)
	}
	id, _ = w.st.Propose(ctx, proposal(Action{Kind: KindHostLabel, Args: map[string]any{"name": "backup-1", "set": map[string]any{"zone": "a"}}}))
	if _, err := w.st.Approve(ctx, id, admin, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, id, Applied)
	if hosts, _ := c.hosts.Hosts(ctx, "dev-1"); hosts[0].Labels["zone"] != "a" || hosts[0].Labels["role"] != "archive" {
		t.Fatalf("labels %+v", hosts[0].Labels)
	}
	// host.remove gives the cleanup command.
	id, _ = w.st.Propose(ctx, proposal(Action{Kind: KindHostRemove, Args: map[string]any{"name": "backup-2"}}))
	_, _ = w.st.Approve(ctx, id, admin, "")
	card = waitState(t, w.st, id, Applied)
	if card.Results[0].Detail == "" {
		t.Fatalf("no cleanup: %+v", card.Results)
	}
	// fleet.create, of labels; then fleet.update of the list one.
	id, err = w.st.Propose(ctx, proposal(Action{Kind: KindFleetCreate, Args: map[string]any{"name": "Backup hosts", "labels": []any{"role=backup"}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.st.Approve(ctx, id, admin, "")
	waitState(t, w.st, id, Applied)
	if got := c.fleets.Fleets("dev-1"); len(got) != 2 || got[1].ID != "backup-hosts" || got[1].Labels[0] != "role=backup" {
		t.Fatalf("fleets %+v", got)
	}
	if _, err := w.st.Propose(ctx, proposal(Action{Kind: KindFleetCreate, Args: map[string]any{"name": "X", "labels": []any{"bad key!=x"}}})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad fleet: %v", err)
	}
	id, _ = w.st.Propose(ctx, proposal(Action{Kind: KindFleetUpdate, Args: map[string]any{"id": "db", "addNodes": []any{"db-1"}, "name": "DB"}}))
	card, _ = w.st.Get(ctx, id)
	if !strings.Contains(strings.Join(card.Plans[0].Impact, "; "), "adds node db-1") {
		t.Fatalf("update impact %q", card.Plans[0].Impact)
	}
	_, _ = w.st.Approve(ctx, id, admin, "")
	waitState(t, w.st, id, Applied)
	if got := c.fleets.Fleets("dev-1"); got[0].Name != "DB" || len(got[0].Nodes) != 2 {
		t.Fatalf("updated %+v", got[0])
	}
	// automation.params: values and a node's, never the ceiling.
	id, err = w.st.Propose(ctx, proposal(Action{Kind: KindAutomationParams, Args: map[string]any{"fleet": "db", "values": map[string]any{"SWAPPINESS": "10"}, "nodes": map[string]any{"db-0": map[string]any{"HUGEPAGES": "512"}}}}))
	if err != nil {
		t.Fatal(err)
	}
	card, _ = w.st.Get(ctx, id)
	if !strings.Contains(strings.Join(card.Plans[0].Impact, "; "), `SWAPPINESS: "60" → "10"`) {
		t.Fatalf("params impact %q", card.Plans[0].Impact)
	}
	_, _ = w.st.Approve(ctx, id, admin, "")
	waitState(t, w.st, id, Applied)
	if v := c.params.vars["db"]; v.Values["SWAPPINESS"] != "10" || v.Nodes["db-0"]["HUGEPAGES"] != "512" {
		t.Fatalf("params %+v", v)
	}
	if _, err := w.st.Propose(ctx, proposal(Action{Kind: KindAutomationParams, Args: map[string]any{"fleet": "db", "ceiling": "full"}})); !errors.Is(err, ErrInvalid) {
		t.Fatalf("raising the ceiling: %v", err)
	}
	if log := w.audit.String(); !strings.Contains(log, `"host.add"`) || !strings.Contains(log, `"host.label"`) || !strings.Contains(log, `"host.remove"`) || !strings.Contains(log, `"fleets.update"`) || !strings.Contains(log, `"automation.params"`) {
		t.Fatalf("audit: %s", log)
	}
}

// Another console record is added one type at a time, with its own card
// shape.
func TestARecordTypeIsAddedOneByOne(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := proposal(Action{Kind: KindRecord, Args: map[string]any{"type": "owner", "service": "shop", "owner": "team-a"}})
	if _, err := w.st.Propose(ctx, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown record: %v", err)
	}
	owners := map[string]string{}
	w.st.RegisterRecord(Record{Type: "owner", Label: "Service owner",
		Check: func(args map[string]any) error {
			if args["service"] == nil || args["owner"] == nil {
				return ErrInvalid
			}
			return nil
		},
		Describe: func(_ context.Context, _ string, args map[string]any) (Plan, error) {
			s := args["service"].(string)
			return Plan{Summary: "Owner of " + s, Impact: []string{s + " → " + args["owner"].(string)}, Fingerprint: "was:" + owners[s]}, nil
		},
		Apply: func(_ context.Context, _ Card, args map[string]any, _ Approver) (Result, error) {
			owners[args["service"].(string)] = args["owner"].(string)
			return Result{OK: true, Summary: "owner set"}, nil
		},
	})
	id, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Approve(ctx, id, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, id, Applied)
	if owners["shop"] != "team-a" {
		t.Fatalf("owners %v", owners)
	}
}
