package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fleetConsole is the console with its fleets.
type fleetConsole struct {
	*console
	fleets []ConsoleFleet
}

func (c fleetConsole) Fleets(context.Context, string) ([]ConsoleFleet, error) { return c.fleets, nil }

// Mycroft's first import (GS-11): a source just added is read at once,
// and the hosts and groups of hosts it lists that the console lacks wait
// as one card, the hosts with their labels and the groups as fleets of
// those labels; what the console has is not proposed again, and nothing
// carries a credential.
func TestTheFirstImportProposesTheInventory(t *testing.T) {
	f := newFixture(t)
	f.s.o.ReadNew = true
	f.s.o.Console = fleetConsole{console: f.console, fleets: []ConsoleFleet{{Name: "Web servers", Members: []string{"web-1"}}}}
	f.console.hosts = []ConsoleHost{{Cluster: "local", Name: "web-1", Address: "10.0.0.4", Kind: "host", Answering: true}}
	f.model.reply = func(text string) string {
		var key string
		if m := sourceTag.FindStringSubmatch(text); m != nil {
			key = m[1]
		}
		return fmt.Sprintf(`{"pages":[],"hosts":[
			{"name":"backup-1","address":"10.0.1.1","labels":{"role":"backup","site":"frankfurt-2","bad key!":"x"},"source":%[1]q},
			{"name":"backup-2","address":"10.0.1.2","labels":{"role":"backup"},"source":%[1]q},
			{"name":"web-1","address":"10.0.0.4","labels":{"role":"web"},"source":%[1]q}],
			"fleets":[{"name":"Backup servers","labels":{"role":"backup"},"source":%[1]q},{"name":"Web servers","labels":{"role":"web"},"source":%[1]q},{"name":"No labels","labels":{},"source":%[1]q}]}`, key)
	}
	f.space(t, map[string]string{"Server inventory": "<p>backup-1, backup-2 and web-1, with their roles.</p>"})
	f.s.Wait()
	props := f.props.Proposals()
	if len(props) != 1 {
		t.Fatalf("one card for the document: %+v", props)
	}
	p := props[0]
	if p.Agent != MycroftID || p.Source.Kind != "knowledge" || !strings.Contains(p.Source.SetBy, "first read of Confluence space INFRA") || p.Title != "Add 2 hosts and 1 fleet from “Server inventory”" {
		t.Fatalf("the card: %+v", p)
	}
	var kinds []string
	for _, a := range p.Actions {
		kinds = append(kinds, a.Kind)
	}
	if !slices.Equal(kinds, []string{"host.add", "host.add", "fleet.create"}) {
		t.Fatalf("actions: %v", kinds)
	}
	labels, _ := p.Actions[0].Args["labels"].(map[string]string)
	if p.Actions[0].Args["name"] != "backup-1" || labels["role"] != "backup" || labels["site"] != "frankfurt-2" || len(labels) != 2 {
		t.Fatalf("the first host and its labels: %+v", p.Actions[0].Args)
	}
	fl := p.Actions[2].Args
	if fl["name"] != "Backup servers" || !slices.Equal(fl["labels"].([]string), []string{"role=backup"}) {
		t.Fatalf("the fleet: %+v", fl)
	}
	// Its members count the hosts the same card adds (KN-30).
	if !strings.Contains(p.Actions[2].Impact, "no node now; 2 with the hosts this card adds: backup-1, backup-2") {
		t.Fatalf("the fleet's members: %s", p.Actions[2].Impact)
	}
	b, _ := json.Marshal(p)
	for _, never := range []string{"read-token-123456", "password", "hostKey"} {
		if strings.Contains(string(b), never) {
			t.Fatalf("the card holds %q", never)
		}
	}
	// The sync is the first read's, audited like every sync.
	if st, _ := f.s.Status(ctx); st.Last == nil || !strings.Contains(st.Last.By, "first read") || st.Last.Proposals != 1 {
		t.Fatalf("status: %+v", st.Last)
	}
}

// Without a store chosen for the wiki, a new source is not read at once:
// it waits for the first sync after one is.
func TestNoFirstReadWithoutAStore(t *testing.T) {
	f := newFixture(t)
	f.s.o.ReadNew = true
	f.s.o.Store = nil
	tok := "read-token-123456"
	if _, err := f.s.SaveSource(ctx, admin, Source{ID: "infra", Name: "Confluence space INFRA", Kind: SourceConfluence, BaseURL: f.conf.URL, Flavor: "datacenter", Space: "INFRA"}, &tok, true); err != nil {
		t.Fatal(err)
	}
	f.s.Wait()
	if len(f.model.calls()) != 0 || len(f.props.Proposals()) != 0 {
		t.Fatal("a source was read with nowhere to keep the wiki")
	}
}
