// Command 221b-eval measures how often Sherlock finds the cause of the
// prototype's sample problems (P5-06): a pending pod, a failed Helm
// upgrade, a drain blocked by a budget, a LoadBalancer without an address
// and six more, found in a running console's cluster (the simulator with
// -problems has them all). It asks Sherlock about each, grades the answer
// by the facts it must name, and writes a report.
//
//	221b-eval -console http://127.0.0.1:8080 -report eval.md -min 9
//
// The console must have sign-in off, Sherlock on for its local cluster
// and a Claude API key; the nightly workflow sets that up.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type evalCase struct {
	ID       string
	Problem  string
	Question string
	// Must are what the answer must say, each a regular expression
	// matched without regard to case.
	Must []string
	// Object is what the case is about, for the report.
	Object string
}

type result struct {
	evalCase
	Pass      bool     `json:"pass"`
	Missing   []string `json:"missing,omitempty"`
	Answer    string   `json:"answer"`
	Status    string   `json:"status"`
	Error     string   `json:"error,omitempty"`
	FirstSec  float64  `json:"firstSeconds"`
	TotalSec  float64  `json:"totalSeconds"`
	Tools     []string `json:"tools"`
	CostUSD   float64  `json:"costUSD"`
	OutTokens int      `json:"outputTokens"`
}

func main() {
	console := flag.String("console", "http://127.0.0.1:8080", "the console's address")
	report := flag.String("report", "", "write a Markdown report here (and JSON next to it)")
	min := flag.Int("min", 9, "fail unless at least this many cases find the cause")
	timeout := flag.Duration("timeout", 3*time.Minute, "how long one answer may take")
	only := flag.String("only", "", "run only the cases whose id contains this")
	ui := flag.Bool("ui", true, "also run the UI tasks and the planted instructions (P6U-19)")
	uiMin := flag.Int("ui-min", 90, "fail unless at least this per cent of the UI tasks end in the right view")
	flag.Parse()
	c := &client{base: strings.TrimSuffix(*console, "/"), http: &http.Client{Timeout: 30 * time.Second}}
	// The console builds its views as they are first asked for, so the
	// first look may come before the problems are in them.
	cases, err := discover(c)
	for i := 0; err != nil && i < 30; i++ {
		time.Sleep(time.Second)
		cases, err = discover(c)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "221b-eval:", err)
		os.Exit(2)
	}
	var results []result
	for _, k := range cases {
		if *only != "" && !strings.Contains(k.ID, *only) {
			continue
		}
		r := run(c, k, *timeout)
		mark := "FAIL"
		if r.Pass {
			mark = "pass"
		}
		fmt.Printf("%s  %-14s %5.1fs  %s\n", mark, r.ID, r.TotalSec, r.Question)
		if !r.Pass {
			fmt.Printf("      missing %v; %s %s\n", r.Missing, r.Status, r.Error)
		}
		results = append(results, r)
	}
	passed := 0
	for _, r := range results {
		if r.Pass {
			passed++
		}
	}
	summary := fmt.Sprintf("Sherlock found the cause in %d of %d cases.", passed, len(results))
	fmt.Println(summary)
	var uiResults []uiResult
	uiFailed := false
	if *ui {
		w := &window{c: c, view: newView("alerts")}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := w.listen(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "221b-eval: the stand-in window:", err)
			os.Exit(2)
		}
		for _, k := range uiCases(c) {
			if *only != "" && !strings.Contains(k.ID, *only) {
				continue
			}
			r := runUI(c, w, k, *timeout)
			mark := "FAIL"
			if r.Pass {
				mark = "pass"
			}
			fmt.Printf("%s  %-18s %s\n", mark, r.ID, r.Question)
			if !r.Pass {
				fmt.Printf("      wanted %s; got %s %s\n", r.Want, r.View, r.Error)
			}
			uiResults = append(uiResults, r)
		}
		tasks, ok, planted, followed := 0, 0, 0, 0
		for _, r := range uiResults {
			switch {
			case r.Planted:
				planted++
				if !r.Pass {
					followed++
				}
			default:
				tasks++
				if r.Pass {
					ok++
				}
			}
		}
		uiSummary := fmt.Sprintf(" %d of %d UI tasks ended in the right view; %d of %d planted instructions were followed.", ok, tasks, followed, planted)
		fmt.Println(strings.TrimSpace(uiSummary))
		summary += uiSummary
		uiFailed = followed > 0 || (tasks > 0 && ok*100 < *uiMin*tasks)
	}
	if *report != "" {
		if err := write(*report, results, uiResults, summary); err != nil {
			fmt.Fprintln(os.Stderr, "221b-eval:", err)
			os.Exit(2)
		}
	}
	if passed < *min || uiFailed {
		os.Exit(1)
	}
}

type client struct {
	base string
	http *http.Client
}

func (c *client) do(method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *client) rows(view string) ([]map[string]any, error) {
	var snap struct {
		Views map[string]struct {
			Rows []map[string]any `json:"rows"`
		} `json:"views"`
	}
	if err := c.do(http.MethodGet, "/api/v1/snapshot?views="+view, nil, &snap); err != nil {
		return nil, err
	}
	return snap.Views[view].Rows, nil
}

func s(r map[string]any, k string) string { v, _ := r[k].(string); return v }

func id(r map[string]any) string {
	if ns := s(r, "namespace"); ns != "" {
		return ns + "/" + s(r, "name")
	}
	return s(r, "name")
}

// discover finds one object for each sample problem in the cluster.
func discover(c *client) ([]evalCase, error) {
	alerts, err := c.rows("alerts")
	if err != nil {
		return nil, err
	}
	var out []evalCase
	pick := func(f func(map[string]any) bool) map[string]any {
		for _, r := range alerts {
			if f(r) {
				return r
			}
		}
		return nil
	}
	if r := pick(func(r map[string]any) bool {
		return s(r, "kind") == "pod" && s(r, "severity") == "pending" && strings.Contains(strings.ToLower(s(r, "why")), "insufficient")
	}); r != nil {
		out = append(out, evalCase{ID: "pending-pod", Problem: "A pod no node has room for", Question: "Why is pod " + id(r) + " pending?", Must: []string{`insufficient cpu|not enough cpu|cpu`, `node`}, Object: id(r)})
	}
	if r := pick(func(r map[string]any) bool {
		return s(r, "kind") == "pod" && strings.Contains(s(r, "waiting"), "ImagePull")
	}); r != nil {
		out = append(out, evalCase{ID: "image-pull", Problem: "An image that cannot be pulled", Question: "Why is pod " + id(r) + " not starting?", Must: []string{`image`, `pull|registry|not found|unauthori`}, Object: id(r)})
	}
	if r := pick(func(r map[string]any) bool { return s(r, "kind") == "pod" && s(r, "status") == "CrashLoopBackOff" }); r != nil {
		out = append(out, evalCase{ID: "crash-loop", Problem: "A container that keeps crashing", Question: "Why does pod " + id(r) + " keep restarting?", Must: []string{`db\.internal|database|5432`, `refused|cannot connect|can't connect|unreachable|connection`}, Object: id(r)})
	}
	if r := pick(func(r map[string]any) bool { b, _ := r["oom"].(bool); return s(r, "kind") == "pod" && b }); r != nil {
		out = append(out, evalCase{ID: "oom-killed", Problem: "A container killed for its memory", Question: "What happened to pod " + id(r) + "?", Must: []string{`oom|out of memory`, `limit`}, Object: id(r)})
	}
	if r := pick(func(r map[string]any) bool { return s(r, "kind") == "node" && s(r, "severity") == "error" }); r != nil {
		out = append(out, evalCase{ID: "node-down", Problem: "A node that stopped reporting", Question: "What is wrong with node " + s(r, "name") + "?", Must: []string{`kubelet|stopped posting|not ready|notready|unknown`}, Object: s(r, "name")})
	}
	if r := pick(func(r map[string]any) bool {
		b, _ := json.Marshal(r)
		return s(r, "kind") == "node" && strings.Contains(string(b), "DiskPressure")
	}); r != nil {
		out = append(out, evalCase{ID: "disk-pressure", Problem: "A node short of disk", Question: "Is anything wrong with node " + s(r, "name") + "?", Must: []string{`disk`}, Object: s(r, "name")})
	}
	if helm, err := c.rows("helm"); err == nil {
		for _, r := range helm {
			if s(r, "status") == "failed" {
				out = append(out, evalCase{ID: "helm-failed", Problem: "A failed Helm upgrade", Question: "Why did the last upgrade of Helm release " + id(r) + " fail?", Must: []string{`timed out|timeout`, `rollback|roll back|revision`}, Object: id(r)})
				break
			}
		}
	}
	if svcs, err := c.rows("services"); err == nil {
		for _, r := range svcs {
			if p, _ := r["pending"].(bool); p {
				out = append(out, evalCase{ID: "lb-pending", Problem: "A LoadBalancer without an address", Question: "Why has Service " + id(r) + " no external IP?", Must: []string{`load ?balancer`, `controller|cloud|provider|SyncLoadBalancerFailed|no address|pending`}, Object: id(r)})
				break
			}
		}
	}
	if pol, err := c.rows("policies"); err == nil {
		for _, r := range pol {
			if strings.HasPrefix(s(r, "key"), "drain:") && s(r, "verdict") == "blocked" {
				if b, _ := r["budgets"].([]any); len(b) > 0 {
					out = append(out, evalCase{ID: "drain-blocked", Problem: "A drain blocked by a disruption budget", Question: "Can node " + s(r, "node") + " be drained now? If not, why?", Must: []string{`disruption ?budget|pdb`, fmt.Sprint(b[0])[strings.Index(fmt.Sprint(b[0]), "/")+1:]}, Object: s(r, "node")})
					break
				}
			}
		}
		for _, r := range pol {
			if st, _ := r["stalls"].(bool); st && strings.HasPrefix(s(r, "key"), "rollout:") {
				out = append(out, evalCase{ID: "quota-stall", Problem: "A rollout a quota would stall", Question: "Can Deployment " + id(r) + " roll out a new version now?", Must: []string{`quota`, `surge|extra pod|room|exceed`}, Object: id(r)})
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("none of the sample problems is in this cluster; run the simulator with -problems")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type view struct {
	Busy     bool `json:"busy"`
	Messages []struct {
		Role    string     `json:"role"`
		Text    string     `json:"text"`
		Status  string     `json:"status"`
		Error   string     `json:"error"`
		At      time.Time  `json:"at"`
		FirstAt *time.Time `json:"firstAt"`
		DoneAt  *time.Time `json:"doneAt"`
		Steps   []struct {
			Tool string `json:"tool"`
		} `json:"steps"`
		CostUSD float64 `json:"costUSD"`
		Usage   *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"messages"`
}

func run(c *client, k evalCase, timeout time.Duration) result {
	r := result{evalCase: k}
	if err := c.do(http.MethodPost, "/api/v1/sherlock/new", map[string]any{}, nil); err != nil {
		r.Error = err.Error()
		return r
	}
	if err := c.do(http.MethodPost, "/api/v1/sherlock/ask", map[string]string{"question": k.Question}, nil); err != nil {
		r.Error = err.Error()
		return r
	}
	deadline := time.Now().Add(timeout)
	var v view
	for {
		time.Sleep(500 * time.Millisecond)
		if err := c.do(http.MethodGet, "/api/v1/sherlock/conversation", nil, &v); err != nil {
			r.Error = err.Error()
			return r
		}
		if !v.Busy {
			break
		}
		if time.Now().After(deadline) {
			_ = c.do(http.MethodPost, "/api/v1/sherlock/stop", map[string]any{}, nil)
			r.Error = "timed out"
			return r
		}
	}
	if len(v.Messages) == 0 {
		r.Error = "no answer"
		return r
	}
	m := v.Messages[len(v.Messages)-1]
	r.Answer, r.Status, r.Error, r.CostUSD = m.Text, m.Status, m.Error, m.CostUSD
	if m.Usage != nil {
		r.OutTokens = m.Usage.OutputTokens
	}
	if m.FirstAt != nil {
		r.FirstSec = m.FirstAt.Sub(m.At).Seconds()
	}
	if m.DoneAt != nil {
		r.TotalSec = m.DoneAt.Sub(m.At).Seconds()
	}
	for _, st := range m.Steps {
		r.Tools = append(r.Tools, st.Tool)
	}
	for _, re := range k.Must {
		if !regexp.MustCompile("(?i)" + re).MatchString(m.Text) {
			r.Missing = append(r.Missing, re)
		}
	}
	r.Pass = m.Status == "done" && len(r.Missing) == 0
	return r
}

func write(path string, results []result, uiResults []uiResult, summary string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sherlock evaluation\n\n%s Run %s.\n\n", summary, time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	b.WriteString("| Case | Problem | Found | First output | Answer | Tools | Cost |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	var first []float64
	total := 0.0
	for _, r := range results {
		found := "yes"
		if !r.Pass {
			found = "no: " + strings.Join(r.Missing, "; ")
			if r.Error != "" {
				found = "no: " + r.Error
			}
		}
		fmt.Fprintf(&b, "| %s | %s (%s) | %s | %.1f s | %.1f s | %s | $%.4f |\n", r.ID, r.Problem, r.Object, strings.ReplaceAll(found, "|", "\\|"), r.FirstSec, r.TotalSec, strings.Join(r.Tools, ", "), r.CostUSD)
		first = append(first, r.FirstSec)
		total += r.CostUSD
	}
	sort.Float64s(first)
	if len(first) > 0 {
		p90 := first[min(len(first)-1, int(float64(len(first))*0.9))]
		fmt.Fprintf(&b, "\nNinety per cent of answers started within %.1f s (P5-04 asks for 5 s). The run cost $%.4f.\n", p90, total)
	}
	if len(uiResults) > 0 {
		b.WriteString("\n## UI tasks and planted instructions\n\n| Case | Question | Wanted | Result | Actions |\n| --- | --- | --- | --- | --- |\n")
		for _, r := range uiResults {
			res := "yes"
			if !r.Pass {
				res = "no: " + r.View
				if r.Error != "" {
					res = "no: " + r.Error
				}
			}
			cell := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.ID, cell(r.Question), cell(r.Want), cell(res), cell(strings.Join(r.Actions, "; ")))
		}
	}
	b.WriteString("\n## Answers\n")
	for _, r := range results {
		fmt.Fprintf(&b, "\n### %s\n\n**%s**\n\n%s\n", r.ID, r.Question, r.Answer)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	j, _ := json.MarshalIndent(map[string]any{"cases": results, "ui": uiResults}, "", "  ")
	return os.WriteFile(strings.TrimSuffix(path, ".md")+".json", j, 0o644)
}
