package sherlocktools

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Sherlock's toolset contains no tool that writes to a cluster or to
// settings shared with others; its UI tools change only the asking
// person's own view (P5-07 as reworded by P6U-16). The cluster tools are
// listed here, so that a new one is a reviewed change, and they reach the
// console only through its API, called with GET, and the two reads of
// Console, which the compiler holds them to. The UI tools are listed in TestSherlockUIActionsList and run in
// the browser, which has no way to change a cluster but Watson's card.
func TestSherlockToolsOnlyRead(t *testing.T) {
	var names []string
	for _, tl := range tools {
		names = append(names, tl.def.Name)
		var schema map[string]any
		if err := json.Unmarshal(tl.def.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Errorf("%s: the input schema is not an object: %s", tl.def.Name, tl.def.InputSchema)
		}
		for _, w := range []string{"create", "update", "patch", "delete", "apply", "exec", "scale", "cordon", "drain_node", "evict", "restart", "rollback", "label", "annotate", "write", "set_"} {
			if strings.HasPrefix(tl.def.Name, w) {
				t.Errorf("%s looks like a tool that writes", tl.def.Name)
			}
		}
	}
	sort.Strings(names)
	want := []string{"automation_runs", "check_connection", "cluster_overview", "get_helm_release", "get_node", "get_object", "get_pod", "get_service", "get_yaml", "list_access", "list_alerts", "list_events", "list_fleets", "list_helm_releases", "list_nodes", "list_objects", "list_pods", "list_policies", "list_services", "list_storage_classes", "list_volumes", "node_facts", "pod_logs", "review_cluster", "top", "trace_ingress", "who_can"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("the toolset changed; review that each new tool only reads, then update this list:\n got %v\nwant %v", names, want)
	}
}

// Every tool an agent's purpose takes is one of the toolbox's, which only
// read, or one of the agents' own.
func TestEveryAgentToolExists(t *testing.T) {
	names := map[string]bool{}
	for _, d := range Defs() {
		names[d.Name] = true
	}
	for _, info := range ai.Catalog {
		for _, tl := range info.Tools {
			if !names[tl] && !sherlock.IsAgentTool(tl) {
				t.Errorf("%s's %s is no tool", info.ID, tl)
			}
		}
	}
}
