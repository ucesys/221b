package knowledge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ucesys/221b/server/internal/jobs"
)

// fakeCovers are the known issues and exceptions a test gives: an open
// known issue by object, an active exception by cluster, object and rule.
type fakeCovers struct {
	known      map[string]string
	exceptions map[string]string
	mu         sync.Mutex
	hits       map[string]int
}

func (c *fakeCovers) Cover(context.Context) Cover {
	return func(cluster, object, rule string) (string, string) {
		return c.known[object], c.exceptions[cluster+"|"+object+"|"+rule]
	}
}

func (c *fakeCovers) Matched(_ context.Context, hits map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hits == nil {
		c.hits = map[string]int{}
	}
	for k, n := range hits {
		c.hits[k] += n
	}
}

// The cluster of PC-22: three control-plane nodes and five workers, two
// of them with GPUs, all Linux; the fleets Control Plane and Linux
// Servers.
func pc22(f *fixture) fleetConsole {
	f.console.hosts = nil
	all := []string{}
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("rke2cp%d", i)
		all = append(all, name)
		f.console.hosts = append(f.console.hosts, ConsoleHost{Cluster: "local", Name: name, Kind: "node", Answering: true,
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": "true", "kubernetes.io/os": "linux"}})
	}
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("worker%d", i)
		all = append(all, name)
		labels := map[string]string{"kubernetes.io/os": "linux"}
		if i <= 2 {
			labels["pool"] = "gpu"
		}
		f.console.hosts = append(f.console.hosts, ConsoleHost{Cluster: "local", Name: name, Kind: "node", Answering: true, Labels: labels})
	}
	// A node of another cluster with the same labels selects nothing here.
	f.console.hosts = append(f.console.hosts, ConsoleHost{Cluster: "prod", Name: "prod-1", Kind: "node", Answering: true, Labels: map[string]string{"pool": "burst"}})
	return fleetConsole{console: f.console, fleets: []ConsoleFleet{
		{Name: "Control Plane", Members: []string{"rke2cp3", "rke2cp1", "rke2cp2"}},
		{Name: "Linux Servers", Members: all},
	}}
}

// The inventory checks a document's groups against the cluster before it
// proposes them as fleets (KN-30): a group whose labels select no node is
// not proposed, nor one that selects the same nodes as a fleet the console
// has; each is reported in the sync's report, with why. A proposed fleet
// says its members now.
func TestTheInventoryChecksGroupsAgainstTheClusterKN30(t *testing.T) {
	f := newFixture(t)
	f.s.o.ReadNew = true
	f.s.o.Console = pc22(f)
	f.model.reply = func(text string) string {
		var key string
		if m := sourceTag.FindStringSubmatch(text); m != nil {
			key = m[1]
		}
		return fmt.Sprintf(`{"pages":[],"hosts":[],"fleets":[
			{"name":"burst pool","labels":{"pool":"burst","sleep-policy":"on-demand"},"source":%[1]q},
			{"name":"control-plane nodes","labels":{"node-role.kubernetes.io/control-plane":"true"},"source":%[1]q},
			{"name":"every node","labels":{"kubernetes.io/os":"linux"},"source":%[1]q},
			{"name":"gpu control plane","labels":{"pool":"gpu","node-role.kubernetes.io/control-plane":"true"},"source":%[1]q},
			{"name":"half burst","labels":{"pool":"gpu","sleep-policy":"on-demand"},"source":%[1]q},
			{"name":"GPU workers","labels":{"pool":"gpu"},"source":%[1]q}]}`, key)
	}
	f.space(t, map[string]string{"nodes.md": "<p>The nodes and their pools.</p>"})
	f.s.Wait()
	props := f.props.Proposals()
	if len(props) != 1 || len(props[0].Actions) != 1 {
		t.Fatalf("one card with one fleet: %+v", props)
	}
	a := props[0].Actions[0]
	if a.Kind != "fleet.create" || a.Args["name"] != "GPU workers" || !strings.Contains(a.Impact, "; 2 nodes now: worker1, worker2;") {
		t.Fatalf("the fleet: %+v", a)
	}
	if props[0].Title != "Add 1 fleet from “nodes.md”" {
		t.Errorf("the title: %s", props[0].Title)
	}
	st, _ := f.s.Status(ctx)
	if st.Last == nil {
		t.Fatal("no sync")
	}
	want := []string{
		"“burst pool” in nodes.md: pool=burst and sleep-policy=on-demand are on no node",
		"“control-plane nodes” in nodes.md: same nodes as the fleet Control Plane",
		"“every node” in nodes.md: same nodes as the fleet Linux Servers",
		"“gpu control plane” in nodes.md: no node has node-role.kubernetes.io/control-plane=true and pool=gpu together",
		"“half burst” in nodes.md: sleep-policy=on-demand is on no node",
	}
	if !slices.Equal(st.Last.NotProposed, want) {
		t.Fatalf("not proposed:\n%s", strings.Join(st.Last.NotProposed, "\n"))
	}
	if !strings.Contains(st.Last.Text, "**Not proposed**\n\n- “burst pool” in nodes.md: pool=burst and sleep-policy=on-demand are on no node") {
		t.Errorf("the report: %s", st.Last.Text)
	}
}

// A fleet's members are capped in words.
func TestMembersWords(t *testing.T) {
	for in, want := range map[string]string{
		"":              "no node now",
		"a":             "1 node now: a",
		"a,b,c":         "3 nodes now: a, b, c",
		"a,b,c,d,e,f,g": "7 nodes now: a, b, c, d, e +2 more",
	} {
		var names []string
		if in != "" {
			names = strings.Split(in, ",")
		}
		if got := MembersWords(names); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The drift check and the inventory leave out a difference an open known
// issue on its object covers, or an active exception for its cluster,
// object and rule, and say so in the run's report; an exception's matches
// count. An exception for another rule on the same object does not cover
// (KN-31).
func TestDriftRespectsKnownIssuesAndExceptionsKN31(t *testing.T) {
	f := newFixture(t)
	f.space(t, map[string]string{"Server inventory": "<p>hosts</p>"})
	m := newManifest()
	m.Items["infra:1"] = &ItemState{Source: "infra", ID: "1", Title: "Server inventory", Version: "3", Hosts: []HostFact{{Name: "db-3", Address: "10.0.0.3"}, {Name: "web-1", Address: "10.0.0.4"}}}
	f.store.Put(ManifestPath, m.encode())
	if err := f.s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.console.hosts = []ConsoleHost{
		{Cluster: "local", Name: "db-3", Address: "10.0.9.9", Kind: "host", Answering: true},
		{Cluster: "local", Name: "web-1", Address: "10.0.0.4", Kind: "host", Answering: false},
		{Cluster: "local", Name: "cache-2", Address: "10.0.0.5", Kind: "host", Answering: true},
		{Cluster: "local", Name: "r820", Address: "10.0.0.6", Kind: "host", Answering: true},
	}
	covers := &fakeCovers{
		known: map[string]string{"Host/web-1": "KI-3"},
		exceptions: map[string]string{
			"local|Host/db-3|" + RuleAddressDiffers:          "EX-4",
			"local|Host/cache-2|" + RuleHostUnreachable:      "EX-9",
			"local|Host/r820|" + RuleHostUnlisted:            "EX-5",
			"prod|Host/cache-2|" + RuleHostUnlisted:          "EX-6",
			"local|Host/backup-9|" + RuleHostMissing:         "EX-7",
			"local|Fleet/backup-servers|" + RuleFleetMissing: "EX-8",
		},
	}
	f.s.SetCovers(covers)
	res, err := f.s.Drift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposals != 1 || !slices.Equal(res.Differences, []string{"cache-2 is in no document"}) {
		t.Fatalf("drift: %+v", res)
	}
	want := []string{
		"db-3: the document's address differs: accepted as-is by EX-4",
		"web-1 is listed but not answering: covered by KI-3",
		"r820 is in no document: accepted as-is by EX-5",
	}
	if !slices.Equal(res.NotProposed, want) || !strings.Contains(res.Text, "Not proposed:\n- db-3: the document's address differs: accepted as-is by EX-4") {
		t.Fatalf("not proposed: %+v\n%s", res.NotProposed, res.Text)
	}
	if fmt.Sprint(covers.hits) != "map[EX-4:1 EX-5:1]" {
		t.Errorf("the exceptions' matches: %v", covers.hits)
	}
	if p := f.props.Proposals(); len(p) != 1 || p[0].DedupKey != "drift:unnamed:cache-2" {
		t.Errorf("proposed: %+v", p)
	}

	// The inventory too: a host and a group a document lists that an
	// exception covers are not proposed, and the sync's report says why.
	f2 := newFixture(t)
	f2.s.o.ReadNew = true
	f2.s.o.Console = fleetConsole{console: f2.console}
	covers2 := &fakeCovers{exceptions: covers.exceptions, known: map[string]string{"Host/backup-2": "KI-1"}}
	f2.s.SetCovers(covers2)
	f2.model.reply = func(text string) string {
		var key string
		if m := sourceTag.FindStringSubmatch(text); m != nil {
			key = m[1]
		}
		return fmt.Sprintf(`{"pages":[],"hosts":[
			{"name":"backup-9","address":"10.0.1.9","labels":{"role":"backup"},"source":%[1]q},
			{"name":"backup-2","address":"10.0.1.2","labels":{"role":"backup"},"source":%[1]q},
			{"name":"backup-1","address":"10.0.1.1","labels":{"role":"backup"},"source":%[1]q}],
			"fleets":[{"name":"Backup servers","labels":{"role":"backup"},"source":%[1]q}]}`, key)
	}
	f2.space(t, map[string]string{"Backups": "<p>backup-1, backup-2 and backup-9.</p>"})
	f2.s.Wait()
	props := f2.props.Proposals()
	if len(props) != 1 || len(props[0].Actions) != 1 || props[0].Actions[0].Args["name"] != "backup-1" {
		t.Fatalf("only backup-1 is proposed: %+v", props)
	}
	st, _ := f2.s.Status(ctx)
	want = []string{
		"backup-9 in Backups: accepted as-is by EX-7",
		"backup-2 in Backups: covered by KI-1",
		"“Backup servers” in Backups: accepted as-is by EX-8",
	}
	if st.Last == nil || !slices.Equal(st.Last.NotProposed, want) {
		t.Fatalf("not proposed: %+v", st.Last)
	}
	if fmt.Sprint(covers2.hits) != "map[EX-7:1 EX-8:1]" {
		t.Errorf("the exceptions' matches: %v", covers2.hits)
	}
}

// The rules the differences are keyed by are the catalogue's (package
// jobs' InventoryChecks), under the drift check's kind.
func TestTheInventoryRulesAreTheCataloguesKN31(t *testing.T) {
	var ids []string
	for _, c := range jobs.InventoryChecks {
		ids = append(ids, c.ID)
	}
	for _, r := range []string{RuleHostMissing, RuleHostUnreachable, RuleAddressDiffers, RuleHostUnlisted, RuleFleetMissing} {
		if !slices.Contains(ids, r) {
			t.Errorf("%s is not in the catalogue", r)
		}
	}
	if JobDrift.ID != jobs.KindDrift {
		t.Errorf("the drift check's kind: %s, the catalogue's %s", JobDrift.ID, jobs.KindDrift)
	}
}
