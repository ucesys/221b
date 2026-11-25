package casework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- One case for one failure (CF-117 to CF-119) ---

// nodeUnknown is the alert of a node that stopped reporting, as the
// console's alerts give it on November 22, 2026 (r620-worker, 17:08Z).
func nodeUnknown(node string) jobs.Alert {
	return jobs.Alert{Cluster: "prod", Key: "node:" + node, Kind: "node", Severity: jobs.SeverityError, Name: node, Status: "Unknown",
		Reason: "Not ready", Message: "Kubelet stopped posting node status.", Fingerprint: "failed|Unknown"}
}

// podUnknown is the alert of a pod on that node: status Unknown, its node
// named from the console's live view of the pods.
func podUnknown(ns, name, node string) jobs.Alert {
	return jobs.Alert{Cluster: "prod", Key: "pod:" + ns + "/" + name, Kind: "pod", Severity: jobs.SeverityError, Name: name, Namespace: ns, Status: "Unknown",
		Node: node, Fingerprint: "failed|Unknown"}
}

// sixPods are the six pods that were on r620-worker (C-14 to C-19).
func sixPods(node string) []jobs.Alert {
	var out []jobs.Alert
	for i, ns := range []string{"monitoring", "kube-system", "shop", "shop", "longhorn-system", "cert-manager"} {
		out = append(out, podUnknown(ns, fmt.Sprintf("app-%d", i+1), node))
	}
	return out
}

func (w *world) watch(list *[]jobs.Alert, minutes int) {
	for range minutes {
		w.svc.WatchAlerts(context.Background(), func() []jobs.Alert { return *list })
		w.now = w.now.Add(time.Minute)
	}
}

func (w *world) openLines(t *testing.T) []issues.Line {
	t.Helper()
	lines, err := w.iss.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []issues.Line
	for _, l := range lines {
		if l.Open() {
			out = append(out, l)
		}
	}
	return out
}

func signalKeys(v View) []string {
	var out []string
	for _, g := range v.Signals {
		out = append(out, g.Key)
	}
	return out
}

// pendingCount is the Inbox's Pending count of cases: the cases listed
// pending, as the Inbox reads them.
func (w *world) pendingCount(t *testing.T) int {
	t.Helper()
	list, err := w.svc.List(context.Background(), olivia, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range list {
		if b.Pending {
			n++
		}
	}
	return n
}

// The owner's scenario (November 22-23, 2026): a node stops reporting, and
// its six pods go Unknown a minute later. Past the safety net's 15
// minutes it opens ONE case, about the node, with the node's correlation
// key, and attaches the six pods' alerts to it as its evidence: a row
// each, a line of its history each ("Alert … attached: same node"), seen
// once more each, audited. The Pending count for the incident is 1, and
// the pods' alerts never open a case of their own while they go on.
func TestOneNodeFailureIsOneCaseCF117(t *testing.T) {
	w := setup(t)
	node := nodeUnknown("r620-worker")
	list := []jobs.Alert{node}
	w.watch(&list, 1)
	list = append(list, sixPods("r620-worker")...)
	w.watch(&list, 30)
	open := w.openLines(t)
	if len(open) != 1 {
		var titles []string
		for _, l := range open {
			titles = append(titles, l.Title)
		}
		t.Fatalf("%d open cases for one node failure: %v", len(open), titles)
	}
	v := w.get(t, caseID(open[0].Number))
	if v.Resource != "Node/r620-worker" || v.CorrelationKey != "node:prod/r620-worker" || v.Priority != 0 || v.Source.AlertKey != "node:r620-worker" {
		t.Fatalf("the node's case %+v key %q", v.Brief, v.CorrelationKey)
	}
	want := []string{"pod:cert-manager/app-6", "pod:kube-system/app-2", "pod:longhorn-system/app-5", "pod:monitoring/app-1", "pod:shop/app-3", "pod:shop/app-4"}
	if got := signalKeys(v); !slices.Equal(got, want) {
		t.Fatalf("the alerts attached %v", got)
	}
	g := v.Signals[0]
	if g.Cluster != "prod" || g.Why != "same node" || g.By != netCaller.Name || g.ByKey != "system:alert-net" || g.Object != "cert-manager/Pod/app-6" ||
		g.Name != "Pod status Unknown: node r620-worker stopped reporting" || g.At.IsZero() {
		t.Errorf("a signal %+v", g)
	}
	if v.Seen == nil || v.Seen.Count != 7 || v.SignalCount != 6 {
		t.Errorf("seen %+v, %d signals", v.Seen, v.SignalCount)
	}
	attached := 0
	for _, e := range v.History {
		if strings.HasPrefix(e.What, "Alert Pod status Unknown: node r620-worker stopped reporting (") && strings.HasSuffix(e.What, ") attached: same node") {
			attached++
		}
	}
	if attached != 6 {
		t.Errorf("%d history lines of an alert attached: %+v", attached, v.History)
	}
	if out := w.audit.String(); strings.Count(out, `"action":"case.attach"`) != 6 || strings.Count(out, `"action":"case.create"`) != 1 {
		t.Errorf("audited: %s", out)
	}
	if n := w.pendingCount(t); n != 1 {
		t.Errorf("the Pending count for the incident is %d", n)
	}
	// The Alerts tab shows the node's case on each pod's alert (CF-77).
	covers, err := w.svc.AlertCovers(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(covers.For("pod:shop/app-3", "shop/Pod/app-3")); len(got) != 1 || got[0] != v.Number {
		t.Errorf("a pod's alert is covered by %v", got)
	}
	// A seventh pod goes Unknown later: attached, no case of its own.
	late := podUnknown("shop", "app-7", "r620-worker")
	list = append(list, late)
	w.watch(&list, 20)
	if open := w.openLines(t); len(open) != 1 {
		t.Fatalf("a later pod opened a case: %d open", len(open))
	}
	if got := signalKeys(w.get(t, v.ID)); len(got) != 7 || got[6] != late.Key {
		t.Errorf("the later pod %v", got)
	}
}

// A pod's alert on a node that already has an open case never opens a
// case of its own (CF-117): the case a person or the Troubleshooter
// opened about the node (C-13 was the Troubleshooter's) takes it, even
// when the node's own alert is not among the alerts (acknowledged); and
// a new leader, which starts its minutes afresh, does not attach it twice
// nor open a case for it. A pod failing on a healthy node keeps its own
// case though a case about that node is open, as before.
func TestAPodAlertOnANodeWithAnOpenCaseOpensNoneCF117(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	c13, err := w.svc.Create(ctx, NewCase{Title: "r620-worker stopped reporting without being put to sleep by nodepower", Cluster: "prod", Resource: "Node/r620-worker"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := w.svc.Create(ctx, NewCase{Title: "n2 disk pressure", Cluster: "prod", Resource: "Node/n2"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	crash := jobs.Alert{Cluster: "prod", Key: "pod:shop/web-1", Kind: "pod", Severity: jobs.SeverityError, Name: "web-1", Namespace: "shop", Status: "CrashLoopBackOff",
		Message: "back-off restarting failed container", Node: "n2", Fingerprint: "failed|CrashLoopBackOff"}
	list := append(sixPods("r620-worker"), crash)
	w.watch(&list, 16)
	open := w.openLines(t)
	if len(open) != 3 {
		t.Fatalf("%d open cases: %+v", len(open), open)
	}
	v := w.get(t, c13.ID)
	if got := signalKeys(v); len(got) != 6 {
		t.Fatalf("C-13's alerts %v", got)
	}
	if w.get(t, disk.ID).SignalCount != 0 {
		t.Error("a crashloop on a healthy node was attached to the node's unrelated case")
	}
	if c := w.get(t, "C-3"); c.Resource != "shop/Pod/web-1" {
		t.Errorf("the crashloop's own case %+v", c.Brief)
	}
	// A new leader: its memory is let go after netForget without a look.
	w.now = w.now.Add(netForget + time.Minute)
	w.watch(&list, 20)
	if open := w.openLines(t); len(open) != 3 {
		t.Fatalf("a new leader opened %d cases", len(open)-3)
	}
	if got := signalKeys(w.get(t, c13.ID)); len(got) != 6 {
		t.Errorf("attached twice %v", got)
	}
	if n := w.pendingCount(t); n != 3 {
		t.Errorf("pending %d", n)
	}
}

// A node that is down with no alert of its own among the alerts and no
// case: its pods' alerts open a case each, as before (nothing says the
// node is down); and a pod whose node is not known neither.
func TestWithoutTheNodesStateEachPodKeepsItsCaseCF117(t *testing.T) {
	w := setup(t)
	list := []jobs.Alert{podUnknown("shop", "a", "gone"), podUnknown("shop", "b", "")}
	w.watch(&list, 16)
	if open := w.openLines(t); len(open) != 2 || open[0].CorrelationKey != "" {
		t.Fatalf("open %+v", open)
	}
}

// mergeWorld opens a node's case (C-1, by hand) and the safety net's case
// for a pod's alert on it (C-2), before CF-117 made them one.
func mergeWorld(t *testing.T) (*world, View, View) {
	t.Helper()
	w := setup(t)
	ctx := context.Background()
	node, err := w.svc.Create(ctx, NewCase{Title: "r620-worker stopped reporting", Cluster: "prod", Resource: "Node/r620-worker"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.svc.openForAlert(ctx, podUnknown("shop", "app-3", "r620-worker"), 15*time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	return w, node, w.get(t, id)
}

// Merge into case… (CF-119): the case closes as a duplicate of the target,
// "Merged into C-1", pointing at it; its alert moves to the target, seen
// once more there; both histories say so, and its conversation stays on
// it; the Pending count follows the target only; audited as case.merge.
// Not into itself, not into a closed case, not a closed case; for whoever
// may close a case, over HTTP too.
func TestMergeIntoACaseCF119(t *testing.T) {
	w, node, pod := mergeWorld(t)
	ctx := context.Background()
	if _, err := w.svc.Say(ctx, pod.ID, Message{Text: "Looked at it with Sherlock.", Internal: true}, olivia); err != nil {
		t.Fatal(err)
	}
	if n := w.pendingCount(t); n != 2 {
		t.Fatalf("pending before %d", n)
	}
	if _, err := w.svc.Merge(ctx, pod.ID, MergeInput{Into: pod.ID}, olivia); !errors.Is(err, ErrInvalid) {
		t.Errorf("into itself: %v", err)
	}
	if _, err := w.svc.Merge(ctx, pod.ID, MergeInput{Into: "C-99"}, olivia); !errors.Is(err, ErrNotFound) {
		t.Errorf("into no case: %v", err)
	}
	v, err := w.svc.Merge(ctx, pod.ID, MergeInput{Into: "c-1", Explanation: "Same node."}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != issues.StatusClosed || v.Outcome != issues.OutcomeDuplicate || v.MergedInto != node.ID || v.DuplicateOf != node.ID ||
		v.Explanation != "Merged into C-1: Same node." || v.ClosedBy == nil || v.ClosedBy.Name != "Olivia" || len(v.Signals) != 0 {
		t.Fatalf("the merged case %+v", v.Brief)
	}
	if last := v.History[len(v.History)-1].What; last != "Merged into C-1: Same node.; its 1 alert moved there" {
		t.Errorf("its history %q", last)
	}
	if len(v.Messages) == 0 || v.Messages[0].Text != "Looked at it with Sherlock." {
		t.Errorf("its conversation %+v", v.Messages)
	}
	tg := w.get(t, node.ID)
	if got := signalKeys(tg); len(got) != 1 || got[0] != "pod:shop/app-3" || tg.Signals[0].Why != "merged from C-2" || tg.Signals[0].By != "Olivia" || tg.Seen == nil || tg.Seen.Count != 2 {
		t.Fatalf("the target's alerts %+v seen %+v", tg.Signals, tg.Seen)
	}
	if last := tg.History[len(tg.History)-1].What; !strings.HasPrefix(last, "C-2 (Pod status Unknown: node r620-worker stopped reporting (shop/Pod/app-3)) was merged into this case: its 1 alert moved here") {
		t.Errorf("the target's history %q", last)
	}
	if n := w.pendingCount(t); n != 1 {
		t.Errorf("pending after %d", n)
	}
	if out := w.audit.String(); !strings.Contains(out, `"action":"case.merge"`) || !strings.Contains(out, "case C-2 merged into C-1 (r620-worker stopped reporting), 1 alert moved: Same node.") {
		t.Errorf("not audited: %s", out)
	}
	// The pod's alert is covered by the target now (CF-77).
	covers, _ := w.svc.AlertCovers(ctx, "prod")
	if got := numbers(covers.For("pod:shop/app-3", "shop/Pod/app-3")); len(got) != 1 || got[0] != 1 {
		t.Errorf("covered by %v", got)
	}
	// A closed case is not merged, nor merged into.
	if _, err := w.svc.Merge(ctx, pod.ID, MergeInput{Into: node.ID}, olivia); err == nil {
		t.Error("a closed case was merged")
	}
	third, _ := w.svc.Create(ctx, NewCase{Title: "another", Cluster: "prod"}, olivia)
	if _, err := w.svc.Merge(ctx, third.ID, MergeInput{Into: pod.ID}, olivia); !errors.Is(err, ErrInvalid) {
		t.Errorf("into a closed case: %v", err)
	}
	// Reopened, it is no longer merged.
	if r, err := w.svc.Reopen(ctx, pod.ID, "Not the node after all.", olivia); err != nil || r.MergedInto != "" {
		t.Errorf("reopened %+v %v", r.Brief, err)
	}
	// Over HTTP, for whoever may close a case.
	caller := olivia
	mux := http.NewServeMux()
	for _, rt := range w.svc.Routes(func(*http.Request) (Caller, bool) { return caller, true }) {
		mux.Handle(rt.Pattern, rt.Handler)
	}
	post := func(path, body string) (int, View) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		var out View
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	caller = Caller{Person: Person{Name: "Reader"}, ID: "reader", Read: true}
	if code, _ := post(Prefix+"/"+third.ID+"/merge", `{"into": "C-1"}`); code != http.StatusForbidden {
		t.Errorf("one who only reads the cases: %d", code)
	}
	caller = olivia
	if code, _ := post(Prefix+"/"+third.ID+"/merge", `{"into": "C-3"}`); code != http.StatusBadRequest {
		t.Errorf("into itself over HTTP: %d", code)
	}
	if code, out := post(Prefix+"/"+third.ID+"/merge", `{"into": "C-1"}`); code != http.StatusOK || out.MergedInto != "C-1" || out.Status != issues.StatusClosed {
		t.Errorf("merged over HTTP: %d %+v", code, out.Brief)
	}
}

// sameCauseAnswer is the Troubleshooter's analysis of a pod's case on a
// node that stopped reporting, naming the node's case and the other pods'
// (as it did on C-15), with a case that is not there and a closed one.
const sameCauseAnswer = `{"summary": "The pod is Unknown because its node r620-worker stopped reporting.", "cause": "r620-worker stopped reporting", "confidence": "confirmed",
 "evidence": ["Node/r620-worker: Ready Unknown since 17:08Z"], "fix": {"summary": "", "commands": []}, "proposals": [],
 "same_cause_as": ["C-1", "C-99", "C-3", "C-1"], "root_case": "C-1"}`

// The analysis says which open cases share the cause (CF-118): its
// same_cause_as and root_case, kept only for open cases on the case's
// cluster; the case then suggests "Merge into C-1", which a person
// applies (nothing merges by itself). The root's own case suggests
// nothing. The analysis's format asks for both fields, and the run reads
// the other open cases on the cluster.
func TestTheAnalysisNamesTheCasesWithTheSameCauseCF118(t *testing.T) {
	w, node, _ := mergeWorld(t)
	ctx := context.Background()
	closed, _ := w.svc.Create(ctx, NewCase{Title: "an old one", Cluster: "prod"}, olivia)
	if _, err := w.svc.Close(ctx, closed.ID, CloseInput{Outcome: issues.OutcomeNoAction, Explanation: "Old."}, olivia); err != nil {
		t.Fatal(err)
	}
	w.jobOn(t, true)
	w.agents.answer = sameCauseAnswer
	id, err := w.svc.openForAlert(ctx, podUnknown("shop", "app-4", "r620-worker"), 15*time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	v := w.get(t, id)
	if v.Analysis == nil || !slices.Equal(v.Analysis.SameCauseAs, []string{"C-1"}) || v.Analysis.RootCase != "C-1" {
		t.Fatalf("the analysis %+v", v.Analysis)
	}
	if v.MergeSuggestion == nil || v.MergeSuggestion.ID != "C-1" || v.MergeSuggestion.Title != "r620-worker stopped reporting" {
		t.Fatalf("the suggestion %+v", v.MergeSuggestion)
	}
	if v.Status == issues.StatusClosed {
		t.Fatal("the analysis merged the case by itself")
	}
	task := w.agents.last()
	// The other open cases on the cluster are in its task: alike, or not.
	if !strings.Contains(task, `"same_cause_as": [], "root_case": ""`) || !strings.Contains(task, "- C-1 (P2, new)") || !strings.Contains(task, "- C-2 (P0, new)") {
		t.Errorf("the task %s", task)
	}
	// One click applies it.
	m, err := w.svc.Merge(ctx, id, MergeInput{Into: v.MergeSuggestion.ID}, olivia)
	if err != nil || m.MergedInto != "C-1" || m.MergeSuggestion != nil {
		t.Fatalf("applied %+v %v", m.Brief, err)
	}
	if got := signalKeys(w.get(t, node.ID)); !slices.Contains(got, "pod:shop/app-4") {
		t.Errorf("the target's alerts %v", got)
	}
	// The root suggests nothing; camelCase is read too.
	w.agents.answer = strings.Replace(strings.Replace(sameCauseAnswer, `"same_cause_as"`, `"sameCauseAs"`, 1), `"root_case": "C-1"`, `"rootCase": "C-5"`, 1)
	root, err := w.svc.Create(ctx, NewCase{Title: "the root", Cluster: "prod", Resource: "Node/r620-worker"}, olivia)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if r := w.get(t, root.ID); r.Analysis == nil || r.Analysis.RootCase != root.ID || !slices.Equal(r.Analysis.SameCauseAs, []string{"C-1"}) || r.MergeSuggestion != nil {
		t.Errorf("the root %+v %+v", r.Analysis, r.MergeSuggestion)
	}
}
