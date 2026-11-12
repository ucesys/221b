// Package ai keeps the console's AI agents (Phase 12): the providers their
// models come from, the agents themselves (Sherlock at the front desk, and
// one agent for each other purpose), their instructions in layers, the
// versions of those instructions with their tests and pilots, and the
// spend each may make. Package sherlock remains the conversation engine;
// this package tells it, for each question and hand-over, who answers and
// how.
package ai

import "github.com/ucesys/221b/server/internal/sherlock"

// Info is an agent of the catalog: what it is for and the tools that
// purpose takes. An administrator may give it fewer, never more.
type Info struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	// Tools are its purpose's tools.
	Tools []string `json:"tools"`
	// On says whether it is on in a new installation.
	On bool `json:"on"`
	// ViewControl says it may change the person's view: Sherlock only.
	ViewControl bool `json:"viewControl,omitempty"`
}

// The agents' ids, which are also their keys under agents. in the chart's
// values.
const (
	Sherlock         = sherlock.SherlockID
	Troubleshooter   = "troubleshooter"
	ChangePlanner    = "changePlanner"
	Auditor          = "auditor"
	HostDoctor       = "hostDoctor"
	AutomationAuthor = "automationAuthor"
	ScriptReviewer   = "scriptReviewer"
	Mycroft          = "mycroft"
	Summarizer       = "summarizer"
)

// readTools are the toolbox's tools that read the cluster, Sherlock's own.
var readTools = []string{
	"cluster_overview", "list_alerts", "list_nodes", "get_node", "list_pods", "get_pod", "pod_logs", "list_events",
	"list_objects", "get_object", "get_yaml", "list_services", "get_service", "list_helm_releases", "get_helm_release",
	"top", "list_fleets", "list_storage_classes", "list_volumes", "trace_ingress", "check_connection", "list_policies",
	"who_can", "list_access", "review_cluster", "node_facts", "automation_runs",
}

// Catalog is every agent, in the order the settings list them. Every
// agent may ask Mycroft's wiki (ask_knowledge, Phase 12).
var Catalog = []Info{
	{ID: Sherlock, Name: "Sherlock", Purpose: "Front desk: answers, routes to the other agents, writes the answer", Tools: append(append([]string(nil), readTools...), sherlock.ToolAskKnowledge), On: true, ViewControl: true},
	{ID: Troubleshooter, Name: "Troubleshooter", Purpose: "Root cause of a failing pod, node, ingress, volume or alert", On: true, Tools: []string{
		"cluster_overview", "list_alerts", "list_nodes", "get_node", "list_pods", "get_pod", "pod_logs", "list_events",
		"list_objects", "get_object", "get_yaml", "list_services", "get_service", "trace_ingress", "check_connection",
		"top", "list_volumes", "list_storage_classes", sherlock.ToolCheckCommand, sherlock.ToolAskKnowledge}},
	{ID: ChangePlanner, Name: "Change planner", Purpose: "Turns an intent into exact Watson actions, with their impact and risk", On: true, Tools: []string{
		"cluster_overview", "list_nodes", "get_node", "list_pods", "get_pod", "list_objects", "get_object", "get_yaml",
		"list_services", "get_service", "list_helm_releases", "get_helm_release", "list_policies", "list_fleets", sherlock.ToolCheckCommand, sherlock.ToolAskKnowledge}},
	{ID: Auditor, Name: "Auditor", Purpose: "Finds problems against best practices: RBAC, network policies, Pod Security, quotas, drift", On: true, Tools: []string{
		"review_cluster", "list_access", "who_can", "list_policies", "cluster_overview", "list_nodes", "list_pods",
		"list_objects", "get_object", "get_yaml", "list_helm_releases", "get_helm_release", "list_storage_classes", sherlock.ToolAskKnowledge}},
	{ID: HostDoctor, Name: "Host doctor", Purpose: "Operating system checks on nodes and hosts", On: true, Tools: []string{
		"list_nodes", "get_node", "list_events", "top", "list_fleets", "node_facts", "automation_runs", sherlock.ToolAskKnowledge}},
	{ID: AutomationAuthor, Name: "Automation author", Purpose: "Drafts Automation scripts and their parameters", On: true, Tools: []string{
		"node_facts", "list_nodes", "get_node", "list_fleets", "automation_runs", sherlock.ToolAskKnowledge}},
	{ID: ScriptReviewer, Name: "Script reviewer", Purpose: "An independent second opinion on a script version", On: true, Tools: []string{"node_facts", sherlock.ToolAskKnowledge}},
	{ID: Mycroft, Name: "Mycroft", Purpose: "Keeps the infrastructure's knowledge: wiki, runbooks, best practices", On: true, Tools: []string{sherlock.ToolAskKnowledge}},
	{ID: Summarizer, Name: "Summarizer", Purpose: "Digests of changes, alerts and runs, on the cheapest model", On: false, Tools: []string{"cluster_overview", "list_alerts", "list_events", "automation_runs", sherlock.ToolAskKnowledge}},
}

// InfoOf finds an agent of the catalog.
func InfoOf(id string) (Info, bool) {
	for _, a := range Catalog {
		if a.ID == id {
			return a, true
		}
	}
	return Info{}, false
}

// ValuesKey is where the chart takes an agent's baseline instructions.
func ValuesKey(id string) string { return "agents." + id + ".instructions" }
