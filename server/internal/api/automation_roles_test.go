package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

type noExec struct{}

func (noExec) Exec(context.Context, string, string, string, string, bool, io.Reader, io.Writer) (int, error) {
	return 0, nil
}

type autoEnv struct {
	srv    *httptest.Server
	c      *http.Client
	store  *automation.Store
	engine *automation.Engine
	audit  *bytes.Buffer
}

func autoSetup(t *testing.T, who, env string) autoEnv {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7})
	simSrv := httptest.NewServer(s)
	t.Cleanup(simSrv.Close)
	st := automation.NewStore(&kv.Memory{}, nil)
	eng := automation.NewEngine(automation.Options{Store: st, Exec: noExec{}})
	srv, c, m, auditOut, local, store := multiSetupWith(t, who, nil, func(o *Options) {
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		o.Automation, o.AutomationStore = eng, st
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(context.Background(), clusters.Spec{Name: "Here", Env: env, Mode: clusters.ModeWrite}, local, store); err != nil {
		t.Fatal(err)
	}
	return autoEnv{srv: srv, c: c, store: st, engine: eng, audit: auditOut}
}

func (e autoEnv) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "221b")
	r, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

const autoKube = `#!/bin/sh
# 221b:describe Read the pods
# 221b:target kubernetes
# 221b:privilege none
# 221b:dry-run yes
# 221b:k8s get pods
kubectl get pods -A
`

func approved(t *testing.T, st *automation.Store, name, text string) {
	t.Helper()
	ctx := context.Background()
	ann, sam := automation.Who{Name: "Ann", User: "ann"}, automation.Who{Name: "Sam", User: "sam"}
	if _, err := st.Save(ctx, name, "", text, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Submit(ctx, name, 1, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Approve(ctx, name, 1, sam); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationIsASuperAdminsAlone(t *testing.T) {
	e := autoSetup(t, "viewer@corp.example", clusters.EnvOther)
	approved(t, e.store, "reads", autoKube)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/automation", ""},
		{"GET", "/api/v1/automation/runs", ""},
		{"GET", "/api/v1/automation/run-profiles", ""},
		{"GET", "/api/v1/automation/facts", ""},
		{"GET", "/api/v1/automation/variable-changes", ""},
		{"POST", "/api/v1/automation/runs", `{"script":"reads","fleet":"_cluster","rollout":{"mode":"all"}}`},
		{"POST", "/api/v1/automation/scripts/reads/versions/1/revoke", "{}"},
		{"PUT", "/api/v1/automation/run-profiles/x", "{}"},
	} {
		if code, _ := e.do(t, c.method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("%s %s answered %d for someone who is not a SuperAdmin", c.method, c.path, code)
		}
	}
}

func TestAKubernetesScriptRunsThroughWatson(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	approved(t, e.store, "reads", autoKube)
	code, body := e.do(t, "POST", "/api/v1/automation/runs", `{"script":"reads","fleet":"_cluster","rollout":{"mode":"all"}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var r automation.Run
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400 && e.engine.Running(r.ID); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	got, outs, _, err := e.engine.Get(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != automation.RunSucceeded {
		t.Fatalf("%+v %v", got, outs)
	}
	if out := outs[got.Nodes[0].Node]; !strings.Contains(out, "Watson via reads v1, run #") || !strings.Contains(out, "NAMESPACE") {
		t.Fatalf("output: %q", out)
	}
}

// A hook names an identity of its own, and the dry run of a drain changes
// nothing: both are read through Watson's real command.
func TestAHookLineTakesAServerSideDryRun(t *testing.T) {
	for _, line := range []string{"kubectl cordon n1", "kubectl uncordon n1", "kubectl drain n1 --ignore-daemonsets --delete-emptydir-data --timeout=300s"} {
		c, err := watson.Parse(line + " --dry-run=server")
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if !c.DryRun() {
			t.Fatalf("%s is no dry run", line)
		}
	}
}

func TestProductionHoldsAChangeThatMattersUntilSomeoneElseApprovesIt(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvProduction)
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:dry-run yes\n# 221b:k8s get pods\n# 221b:param MODE optional m\nkubectl get pods -A\n"
	approved(t, e.store, "reads", text)
	code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"MODE":"new"},"sensitive":["MODE"],"reason":"try it"}`)
	if code != http.StatusAccepted || !strings.Contains(body, `"state":"waiting"`) {
		t.Fatalf("%d %s", code, body)
	}
	if params, _ := e.store.Variables(context.Background(), clusters.LocalID); params["_all"].Values["MODE"] != "" {
		t.Fatalf("the value changed before the approval: %+v", params)
	}
	var ans variablesAnswer
	if err := json.Unmarshal([]byte(body), &ans); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do(t, "POST", "/api/v1/automation/variable-changes/1/approve", "{}"); code != http.StatusForbidden {
		t.Fatalf("the person who asked approved it: %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/v1/automation/variable-changes/1/reject", `{"comment":"not this week"}`); code != 200 {
		t.Fatalf("reject: %d", code)
	}
}

func TestOffProductionAChangeAppliesAtOnceAndSaysWhatItTouches(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	text := "#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\n# 221b:target kubernetes\n# 221b:dry-run yes\n# 221b:k8s get pods\n# 221b:param MODE optional m\nkubectl get pods -A\n"
	approved(t, e.store, "reads", text)
	code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"MODE":"new"}}`)
	if code != 200 || !strings.Contains(body, `"state":"applied"`) || !strings.Contains(body, `"script":"reads"`) {
		t.Fatalf("%d %s", code, body)
	}
}

// secretCluster is a cluster with one Secret.
type secretCluster struct {
	Cluster
	sec *corev1.Secret
}

func (c secretCluster) GetSecret(_ context.Context, _ kube.Person, ns, name string) (*corev1.Secret, error) {
	if c.sec == nil || ns != "db" || name != "creds" {
		return nil, errors.New("secrets \"creds\" is forbidden, and the value is hunter2")
	}
	return c.sec, nil
}

func TestASecretParameterTakesAReferenceAndAnErrorHoldsNoValue(t *testing.T) {
	h := automation.Header{Params: []automation.Param{{Name: "DB_PASS", Secret: true}, {Name: "PLAIN"}}}
	cl := secretCluster{sec: &corev1.Secret{Data: map[string][]byte{"password": []byte("hunter2")}}}
	vals := map[string]map[string]string{"a": {"DB_PASS": "secret:db/creds/password", "PLAIN": "x"}, "b": {"DB_PASS": "secret:db/creds/password"}}
	got, err := readSecrets(context.Background(), cl, kube.Person{}, h, vals)
	if err != nil || got["a"]["DB_PASS"] != "hunter2" || got["b"]["DB_PASS"] != "hunter2" || got["a"]["PLAIN"] != "" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := readSecrets(context.Background(), cl, kube.Person{}, h, map[string]map[string]string{"a": {"DB_PASS": "typed in the open"}}); err == nil {
		t.Fatal("a secret parameter took a plain value")
	}
	_, err = readSecrets(context.Background(), secretCluster{}, kube.Person{}, h, vals)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("%v", err)
	}
}

func TestAGroupWithScriptsIsNotDeleted(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	if code, body := e.do(t, "POST", "/api/v1/automation/groups", `{"name":"storage","desc":"disks"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := e.do(t, "GET", "/api/v1/automation", ""); code != 200 || !strings.Contains(body, `"name":"storage"`) {
		t.Fatalf("an empty group is not listed: %d %s", code, body)
	}
	if _, err := e.store.Save(context.Background(), "s", "storage", autoKube, automation.Who{Name: "Ann"}, true); err != nil {
		t.Fatal(err)
	}
	if code, body := e.do(t, "DELETE", "/api/v1/automation/groups/storage", ""); code != http.StatusConflict || !strings.Contains(body, "move s out first") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := e.do(t, "PUT", "/api/v1/automation/groups/storage", `{"name":"disks"}`); code != 200 {
		t.Fatalf("rename: %d", code)
	}
	if sc, _, _ := e.store.Script(context.Background(), "s"); sc.Group != "disks" {
		t.Fatalf("%+v", sc.Group)
	}
}

func TestSherlocksDraftReadsItsOwnFindings(t *testing.T) {
	text, q, notes := scriptFrom("Here it is:\n```sh\n#!/bin/sh\necho hi\n```\nIt says hi.\nQUESTION: which package?\nQUESTION: which port?\n")
	if text != "#!/bin/sh\necho hi\n" || len(q) != 2 || q[1] != "which port?" {
		t.Fatalf("%q %v", text, q)
	}
	// What it says beside the code is kept for the person, less its questions.
	if notes != "Here it is:\n\nIt says hi." {
		t.Fatalf("notes %q", notes)
	}
	if _, _, notes := scriptFrom("#!/bin/sh\necho bare\n"); notes != "" {
		t.Fatalf("a bare script has no notes: %q", notes)
	}
	if got := findingsText([]automation.Finding{{Level: "error", Line: 3, Code: "x", Msg: "bad"}}); got != "error, line 3 (x): bad\n" {
		t.Fatalf("%q", got)
	}
}

func TestSherlockIsSentBackUntilTheChecksPassOrItStopsChanging(t *testing.T) {
	bad := "```sh\n#!/bin/sh\necho no header\n```"
	good := "```sh\n#!/bin/sh\n# 221b:describe x\n# 221b:privilege none\necho ok\n```"
	calls := 0
	d := fixDraft(bad+"\nA first try.", func(ask string) (string, error) {
		calls++
		if !strings.Contains(ask, "describe") || !strings.Contains(ask, "echo no header") {
			t.Fatalf("it was not told what is wrong: %q", ask)
		}
		return good + "\nAdded the header.", nil
	})
	if calls != 1 || len(d.Findings) != 0 || !strings.Contains(d.Text, "echo ok") {
		t.Fatalf("%d %v %q", calls, d.Findings, d.Text)
	}
	if !slices.Equal(d.Notes, []string{"A first try.", "Added the header."}) {
		t.Fatalf("every round's words are kept: %q", d.Notes)
	}

	calls = 0
	d = fixDraft(bad, func(string) (string, error) {
		calls++
		return bad + "\nQUESTION: what should it do?\n", nil
	})
	if calls != 1 || len(d.Findings) == 0 || len(d.Questions) != 1 {
		t.Fatalf("no progress should stop and say so: %d %v %v", calls, d.Findings, d.Questions)
	}

	calls = 0
	d = fixDraft(bad, func(string) (string, error) {
		calls++
		return "```sh\n#!/bin/sh\necho " + strings.Repeat("x", calls) + "\n```", nil
	})
	if calls != fixRounds || len(d.Findings) == 0 {
		t.Fatalf("it should stop at %d rounds: %d", fixRounds, calls)
	}

	d = fixDraft(bad, func(string) (string, error) { return "", errors.New("the model is gone") })
	if len(d.Findings) == 0 {
		t.Fatal("a failed round lost the findings")
	}
}

func TestOnlyASecondAdminUnmarksASensitiveParameter(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	if err := e.store.SetVariables(context.Background(), clusters.LocalID, "_all", automation.FleetVariables{Values: map[string]string{"A": "1"}, Sensitive: []string{"A"}}); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(t, "PUT", "/api/v1/automation/fleets/_all/variables", `{"values":{"A":"1"}}`)
	if code != http.StatusAccepted || !strings.Contains(body, `"state":"waiting"`) {
		t.Fatalf("off production too: %d %s", code, body)
	}
	if p, _ := e.store.Variables(context.Background(), clusters.LocalID); len(p["_all"].Sensitive) != 1 {
		t.Fatalf("it was unmarked before the approval: %+v", p)
	}
	if code, _ := e.do(t, "POST", "/api/v1/automation/variable-changes/1/approve", "{}"); code != http.StatusForbidden {
		t.Fatalf("the same admin approved it: %d", code)
	}
}

func TestApprovalsListWhatWaitsForThePersonThenWhatWasDecided(t *testing.T) {
	e := autoSetup(t, "ops@corp.example", clusters.EnvOther)
	ctx := context.Background()
	ann := automation.Who{Name: "Ann", User: "ann"}
	if _, err := e.store.Save(ctx, "s", "", autoKube, ann, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Submit(ctx, "s", 1, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.NewChange(ctx, automation.VariableChange{Cluster: clusters.LocalID, Fleet: "_all", By: ann, State: automation.ChangeWaiting, Changed: []string{"A"}}); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(t, "GET", "/api/v1/admin/approvals", "")
	if code != 200 || !strings.Contains(body, `"script":"s"`) || !strings.Contains(body, `"what":"_all: A"`) || strings.Contains(body, `"state":"approved"`) {
		t.Fatalf("%d %s", code, body)
	}
	// Decided, the change stays on the list below what waits, with who
	// approved it and when.
	if _, err := e.store.DecideChange(ctx, clusters.LocalID, 1, true, "", automation.Who{Name: "Bea", User: "bea"}); err != nil {
		t.Fatal(err)
	}
	code, body = e.do(t, "GET", "/api/v1/admin/approvals", "")
	var got struct {
		Items []approvalRow `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &got); code != 200 || err != nil {
		t.Fatalf("%d %v %s", code, err, body)
	}
	if len(got.Items) != 2 || got.Items[0].State != approvalPending || got.Items[0].Kind != approvalScript {
		t.Fatalf("pending first: %+v", got.Items)
	}
	if c := got.Items[1]; c.Kind != approvalChange || c.State != approvalApproved || c.ApprovedBy != "Bea" || c.ApprovedAt == nil || c.RequestedBy != "Ann" {
		t.Fatalf("the decided change: %+v", c)
	}
	if got.Items[1].Mine {
		t.Fatalf("Ann asked for the change, not this person: %+v", got.Items[1])
	}
	// Rejected through the API, the version says who rejected it and why.
	if code, body := e.do(t, "POST", "/api/v1/automation/scripts/s/versions/1/reject", `{}`); code != http.StatusBadRequest {
		t.Fatalf("rejected without a reason: %d %s", code, body)
	}
	if code, body := e.do(t, "POST", "/api/v1/automation/scripts/s/versions/1/reject", `{"comment":"use the mirror"}`); code != 200 || !strings.Contains(body, `"state":"rejected"`) {
		t.Fatalf("%d %s", code, body)
	}
	_, body = e.do(t, "GET", "/api/v1/admin/approvals", "")
	got.Items = nil
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	var rejected *approvalRow
	for i := range got.Items {
		if got.Items[i].Kind == approvalScript {
			rejected = &got.Items[i]
		}
	}
	if rejected == nil || rejected.State != approvalRejected || rejected.Comment != "use the mirror" || rejected.ApprovedBy == "" || rejected.ApprovedAt == nil {
		t.Fatalf("the rejected version: %+v", got.Items)
	}
	// What this person asked for, decided by someone else, is theirs to see.
	_, idx := e.do(t, "GET", "/api/v1/automation", "")
	var index struct {
		Me automation.Who `json:"me"`
	}
	if err := json.Unmarshal([]byte(idx), &index); err != nil || index.Me.Name == "" {
		t.Fatalf("%v %s", err, idx)
	}
	mine, err := e.store.NewChange(ctx, automation.VariableChange{Cluster: clusters.LocalID, Fleet: "_all", By: index.Me, State: automation.ChangeWaiting, Changed: []string{"B"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.DecideChange(ctx, clusters.LocalID, mine.ID, false, "not now", automation.Who{Name: "Bea", User: "bea"}); err != nil {
		t.Fatal(err)
	}
	_, body = e.do(t, "GET", "/api/v1/admin/approvals", "")
	got.Items = nil
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	flagged := 0
	for _, it := range got.Items {
		if it.Mine {
			flagged++
			if it.Kind != approvalChange || it.State != approvalRejected || it.Comment != "not now" {
				t.Fatalf("flagged: %+v", it)
			}
		}
	}
	if flagged != 1 {
		t.Fatalf("%d flagged in %+v", flagged, got.Items)
	}
	viewer := autoSetup(t, "viewer@corp.example", clusters.EnvOther)
	if code, _ := viewer.do(t, "GET", "/api/v1/admin/approvals", ""); code != http.StatusForbidden {
		t.Fatalf("%d", code)
	}
}
