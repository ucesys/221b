package sherlocktools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/scrub"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// The console's own records, read as the person asking (Phase 12.1, SR-01
// to SR-08): an Automation run's output, Automation's scripts and
// variables, Watson's history, the audit log, the cases and the jobs'
// runs. Each is the API of the screen it mirrors, so the same people may
// read it: Automation and the audit log are a SuperAdmin's, the cases
// and runs are for those who may see AIOps, and Watson's history is the
// commands the person's own Watson panel lists. Text that people or
// nodes wrote is scrubbed of credentials and cut to a bound.

const (
	// maxRunNodes bounds the nodes of one Automation run a result shows.
	maxRunNodes = 20
	// maxTail bounds the lines of output a result shows of one node or
	// one command, as pod_logs does.
	maxTail = 200
	// maxText bounds a long text in a result: a run's report, a case's
	// message, a script.
	maxText = 8000
	// secretShown stands for a secret parameter's value, which is never
	// shown: only that it is set.
	secretShown = "(set: a secret parameter's value is not shown)"
)

// tailLines is the last n lines of text, scrubbed, and how many it left
// out.
func tailLines(text string, n int) (string, int) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return "", 0
	}
	lines := strings.Split(text, "\n")
	cut := 0
	if len(lines) > n {
		cut = len(lines) - n
		lines = lines[cut:]
	}
	return scrub.Text(strings.Join(lines, "\n")), cut
}

// clip cuts text to at most n bytes, on a rune, saying so.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(" … (%d more bytes not shown)", len(s)-cut)
}

// getJSON reads a GET path of the console into v; a result is set when it
// could not.
func (tb *Toolbox) getJSON(ctx context.Context, p string, q url.Values, v any, what string) (sherlock.ToolResult, bool) {
	code, b := tb.get(ctx, p, q)
	if code != http.StatusOK {
		return failure(code, b), false
	}
	if err := json.Unmarshal(b, v); err != nil {
		return sherlock.ToolResult{Text: what + " could not be read.", Error: true}, false
	}
	return sherlock.ToolResult{}, true
}

// jsonResult writes a value as a result, scrubbed.
func jsonResult(v any) sherlock.ToolResult {
	b, _ := json.Marshal(v)
	return sherlock.ToolResult{Text: string(scrub.JSON(b))}
}

// ---------- Automation ----------

// nodeRank puts the nodes whose part failed first.
func nodeRank(state string) int {
	switch state {
	case "failed", "unreachable":
		return 0
	case "cancelled", "skipped":
		return 1
	case "running", "rebooting", "waiting":
		return 2
	}
	return 3
}

func (tb *Toolbox) automationRun(ctx context.Context, in map[string]any) sherlock.ToolResult {
	id := num(in, "id", 0, 0, 1<<30)
	if id < 1 {
		return sherlock.ToolResult{Text: "Give the run's number, from automation_runs.", Error: true}
	}
	var out struct {
		Run    map[string]any    `json:"run"`
		Output map[string]string `json:"output"`
		Live   bool              `json:"live"`
	}
	if r, ok := tb.getJSON(ctx, "/automation/runs/"+strconv.Itoa(id), nil, &out, "The run"); !ok {
		return r
	}
	tail := num(in, "tail", 60, 1, maxTail)
	only := str(in, "node")
	run := out.Run
	// The values each node got and the overrides the run started with are
	// parameters: a secret one is a reference, still not for the model.
	drop(run, "overrides", "request", "beat", "hookRefs")
	nodes, _ := run["nodes"].([]any)
	var rows []map[string]any
	for _, x := range nodes {
		n, ok := x.(map[string]any)
		if !ok || only != "" && sget(n, "node") != only {
			continue
		}
		drop(n, "values", "batch")
		text, ok := out.Output[sget(n, "node")]
		if !ok {
			text = sget(n, "tail")
		}
		delete(n, "tail")
		if t, cut := tailLines(text, tail); t != "" {
			n["output"] = t
			if cut > 0 {
				n["outputNote"] = fmt.Sprintf("Only the last %d lines; %d before them are not shown.", tail, cut)
			}
		}
		if e := sget(n, "error"); e != "" {
			n["error"] = clip(scrub.Text(e), 2000)
		}
		rows = append(rows, n)
	}
	sort.SliceStable(rows, func(i, j int) bool { return nodeRank(sget(rows[i], "state")) < nodeRank(sget(rows[j], "state")) })
	total := len(rows)
	if len(rows) > maxRunNodes {
		rows = rows[:maxRunNodes]
		run["nodesNote"] = fmt.Sprintf("Only %d of its %d nodes, those whose part failed first; name a node to read another.", maxRunNodes, total)
	}
	if only != "" && total == 0 {
		return sherlock.ToolResult{Text: "Run " + strconv.Itoa(id) + " did not run on " + only + ".", Error: true}
	}
	run["nodes"] = rows
	run["live"] = out.Live
	return jsonResult(run)
}

// secretParams are the parameters some script declares secret, by name.
func (tb *Toolbox) secretParams(ctx context.Context) (map[string]bool, sherlock.ToolResult, bool) {
	var out struct {
		Scripts []struct {
			Params []struct {
				Name   string `json:"name"`
				Secret bool   `json:"secret"`
			} `json:"params"`
		} `json:"scripts"`
	}
	if r, ok := tb.getJSON(ctx, "/automation", nil, &out, "Automation"); !ok {
		return nil, r, false
	}
	secret := map[string]bool{}
	for _, s := range out.Scripts {
		for _, p := range s.Params {
			if p.Secret {
				secret[p.Name] = true
			}
		}
	}
	return secret, sherlock.ToolResult{}, true
}

// hideSecrets replaces the value of every secret parameter, and of every
// value that names a Secret, by a word that says it is set.
func hideSecrets(values map[string]any, secret map[string]bool) {
	for k, v := range values {
		s, _ := v.(string)
		if secret[k] || strings.HasPrefix(s, "secret:") {
			values[k] = secretShown
		}
	}
}

func (tb *Toolbox) scripts(ctx context.Context, in map[string]any) sherlock.ToolResult {
	var out struct {
		Off     string           `json:"off"`
		Scripts []map[string]any `json:"scripts"`
	}
	if r, ok := tb.getJSON(ctx, "/automation", nil, &out, "Automation"); !ok {
		return r
	}
	var rows []map[string]any
	for _, s := range out.Scripts {
		if matches(s, str(in, "query")) {
			rows = append(rows, s)
		}
	}
	r := listResult("scripts", rows, num(in, "limit", 40, 1, 200), nil)
	if out.Off != "" {
		r.Text = `{"off":` + strconv.Quote(out.Off) + `,"list":` + r.Text + `}`
	}
	return r
}

func (tb *Toolbox) script(ctx context.Context, in map[string]any) sherlock.ToolResult {
	name := str(in, "name")
	if name == "" {
		return sherlock.ToolResult{Text: "Name the script, from list_scripts.", Error: true}
	}
	var sc struct {
		Name     string           `json:"name"`
		Group    string           `json:"group"`
		Created  any              `json:"created"`
		Updated  any              `json:"updated"`
		Versions []map[string]any `json:"versions"`
	}
	if r, ok := tb.getJSON(ctx, "/automation/scripts/"+url.PathEscape(name), nil, &sc, "The script"); !ok {
		return r
	}
	if len(sc.Versions) == 0 {
		return sherlock.ToolResult{Text: "Script " + name + " has no versions.", Error: true}
	}
	want := num(in, "version", 0, 0, 1<<30)
	var chosen map[string]any
	var list []map[string]any
	for _, v := range sc.Versions {
		n := int(int64Of(v["n"]))
		brief := map[string]any{"n": n, "state": v["state"], "created": v["created"]}
		if a, ok := v["author"].(map[string]any); ok {
			brief["author"] = a["name"]
		}
		list = append(list, brief)
		if want == 0 || n == want {
			chosen = v
		}
	}
	if chosen == nil {
		return sherlock.ToolResult{Text: fmt.Sprintf("Script %s has no version %d.", name, want), Error: true}
	}
	// What the person may do with it is the screen's, not the answer's.
	delete(chosen, "may")
	if t, ok := chosen["text"].(string); ok {
		chosen["text"] = clip(scrub.Text(t), maxText)
	}
	return jsonResult(map[string]any{"name": sc.Name, "group": sc.Group, "created": sc.Created, "updated": sc.Updated, "version": chosen, "versions": list})
}

func (tb *Toolbox) variables(ctx context.Context, in map[string]any) sherlock.ToolResult {
	secret, r, ok := tb.secretParams(ctx)
	if !ok {
		return r
	}
	var out struct {
		Cluster   string           `json:"cluster"`
		Off       string           `json:"off"`
		Fleets    []map[string]any `json:"fleets"`
		Conflicts []map[string]any `json:"conflicts"`
	}
	if r, ok := tb.getJSON(ctx, "automation/fleets", nil, &out, "The variables"); !ok {
		return r
	}
	fleet, node := str(in, "fleet"), str(in, "node")
	var rows []map[string]any
	for _, f := range out.Fleets {
		if fleet != "" && sget(f, "id") != fleet && !strings.EqualFold(sget(f, "name"), fleet) {
			continue
		}
		var members []string
		member := node == "" || sget(f, "id") == "_all"
		if list, ok := f["nodes"].([]any); ok {
			for _, x := range list {
				if n, ok := x.(map[string]any); ok {
					members = append(members, sget(n, "name"))
					member = member || sget(n, "name") == node
				}
			}
		}
		if !member {
			continue
		}
		p, _ := f["params"].(map[string]any)
		if p == nil {
			p = map[string]any{}
		}
		if v, ok := p["values"].(map[string]any); ok {
			hideSecrets(v, secret)
		}
		if byNode, ok := p["nodes"].(map[string]any); ok {
			for n, x := range byNode {
				if node != "" && n != node {
					delete(byNode, n)
					continue
				}
				if v, ok := x.(map[string]any); ok {
					hideSecrets(v, secret)
				}
			}
		}
		rows = append(rows, map[string]any{"id": f["id"], "name": f["name"], "nodes": members, "params": p})
	}
	if len(rows) == 0 {
		return sherlock.ToolResult{Text: "No fleet matches" + suffixIf(" ", fleet) + suffixIf(" with node ", node) + ".", Error: true}
	}
	return jsonResult(map[string]any{"cluster": out.Cluster, "off": out.Off, "fleets": rows, "conflicts": out.Conflicts})
}

// ---------- Watson ----------

func (tb *Toolbox) watsonHistory(ctx context.Context, in map[string]any) sherlock.ToolResult {
	lines := num(in, "lines", 10, 0, 50)
	var out struct {
		Commands []map[string]any `json:"commands"`
	}
	if r, ok := tb.getJSON(ctx, "watson/history", url.Values{"lines": {strconv.Itoa(lines)}}, &out, "Watson's history"); !ok {
		return r
	}
	status, q := strings.ToLower(str(in, "status")), str(in, "query")
	var rows []map[string]any
	for _, c := range out.Commands {
		if status != "" && strings.ToLower(sget(c, "status")) != status || !matches(c, q) {
			continue
		}
		var text []string
		if list, ok := c["lines"].([]any); ok {
			for _, x := range list {
				if l, ok := x.(map[string]any); ok {
					text = append(text, sget(l, "t"))
				}
			}
		}
		// The card's plan is read on its card; the rest is the screen's.
		drop(c, "lines", "lineStart", "rev", "plan", "canApprove", "mine")
		if len(text) > 0 {
			c["output"] = scrub.Text(strings.Join(text, "\n"))
		}
		rows = append(rows, c)
	}
	return listResult("commands", rows, num(in, "limit", 20, 1, 100), nil)
}

// ---------- the audit log ----------

func (tb *Toolbox) auditLog(ctx context.Context, in map[string]any) sherlock.ToolResult {
	limit := num(in, "limit", 50, 1, 200)
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	for k, v := range map[string]string{"q": str(in, "query"), "action": str(in, "action"), "outcome": str(in, "outcome")} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var out struct {
		Events  []map[string]any `json:"events"`
		Total   int              `json:"total"`
		Actions []string         `json:"actions"`
	}
	if r, ok := tb.getJSON(ctx, "/admin/audit", q, &out, "The audit log"); !ok {
		return r
	}
	for _, e := range out.Events {
		if d := sget(e, "detail"); d != "" {
			e["detail"] = clip(d, 1000)
		}
	}
	res := map[string]any{"events": out.Events, "kept": out.Total, "actions": out.Actions}
	switch {
	case len(out.Events) == 0:
		res["note"] = "None: nothing matches."
	case len(out.Events) == limit:
		res["note"] = fmt.Sprintf("Only the newest %d that match; narrow the request.", limit)
	}
	return jsonResult(res)
}

// ---------- cases and the jobs' runs ----------

func (tb *Toolbox) cases(ctx context.Context, in map[string]any) sherlock.ToolResult {
	q := url.Values{}
	for k, v := range map[string]string{"status": str(in, "status"), "q": str(in, "query"), "priority": str(in, "priority"), "cluster": str(in, "cluster")} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if flag(in, "open") {
		q.Set("open", "1")
	}
	var out struct {
		Cases []map[string]any `json:"cases"`
	}
	if r, ok := tb.getJSON(ctx, "/aiops/cases", q, &out, "The cases"); !ok {
		return r
	}
	for _, c := range out.Cases {
		drop(c, "clusterConnected", "key", "createdBy", "lastBy", "slaApplies", "analysisQueued")
	}
	return listResult("cases", out.Cases, num(in, "limit", 30, 1, 100), nil)
}

func (tb *Toolbox) getCase(ctx context.Context, in map[string]any) sherlock.ToolResult {
	id := str(in, "id")
	if id == "" {
		return sherlock.ToolResult{Text: "Give the case's id, such as C-12, from list_cases.", Error: true}
	}
	var c map[string]any
	if r, ok := tb.getJSON(ctx, "/aiops/cases/"+url.PathEscape(id), nil, &c, "The case"); !ok {
		return r
	}
	// What the person may do with it, and the clusters New Case offers,
	// are the screen's.
	drop(c, "mayChange", "mayApprove", "clusters", "allowedOutcomes", "mayReopen")
	if d := sget(c, "detail"); d != "" {
		c["detail"] = clip(d, maxText)
	}
	if msgs, ok := c["messages"].([]any); ok {
		const keep = 20
		if len(msgs) > keep {
			c["messagesNote"] = fmt.Sprintf("Only the last %d of %d messages.", keep, len(msgs))
			msgs = msgs[len(msgs)-keep:]
		}
		for _, x := range msgs {
			if m, ok := x.(map[string]any); ok {
				if t := sget(m, "text"); t != "" {
					m["text"] = clip(t, 2000)
				}
			}
		}
		c["messages"] = msgs
	}
	return jsonResult(c)
}

func (tb *Toolbox) jobRuns(ctx context.Context, in map[string]any) sherlock.ToolResult {
	limit := num(in, "limit", 20, 1, 100)
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	for k, v := range map[string]string{"job": str(in, "job"), "status": str(in, "status"), "ref": str(in, "ref")} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var out struct {
		Runs  []map[string]any `json:"runs"`
		Total int              `json:"total"`
	}
	if r, ok := tb.getJSON(ctx, "/runs", q, &out, "The runs"); !ok {
		return r
	}
	res := listResult("runs", out.Runs, limit, nil)
	if out.Total > len(out.Runs) {
		res.Text = `{"matching":` + strconv.Itoa(out.Total) + `,"list":` + res.Text + `}`
	}
	return res
}

func (tb *Toolbox) jobRun(ctx context.Context, in map[string]any) sherlock.ToolResult {
	id := str(in, "id")
	if id == "" {
		return sherlock.ToolResult{Text: "Give the run's id, such as run-12, from list_job_runs.", Error: true}
	}
	var run map[string]any
	if r, ok := tb.getJSON(ctx, "/runs/"+url.PathEscape(id), nil, &run, "The run"); !ok {
		return r
	}
	drop(run, "mayCancel", "instance", "reroute")
	if rep := sget(run, "report"); rep != "" {
		run["report"] = clip(rep, maxText)
	}
	if fs, ok := run["findings"].([]any); ok {
		const keep = 50
		if len(fs) > keep {
			run["findingsNote"] = fmt.Sprintf("Only the first %d of %d findings.", keep, len(fs))
			run["findings"] = fs[:keep]
		}
	}
	return jsonResult(run)
}
