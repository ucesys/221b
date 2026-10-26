package watson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

type auditBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (a *auditBuf) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b.Write(p)
}
func (a *auditBuf) events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audit.Event
	for _, l := range strings.Split(strings.TrimSpace(a.b.String()), "\n") {
		var e audit.Event
		if json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func setup(t *testing.T, o Options) (*Service, Cluster, *sim.Sim, *httptest.Server, *auditBuf) {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7, Metrics: true})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	buf := &auditBuf{}
	o.Audit = audit.New(audit.Options{Out: buf})
	if o.CheckEvery == 0 {
		o.CheckEvery = 50 * time.Millisecond
	}
	svc := New(o)
	cl := Cluster{ID: "local", Name: "test", Env: "development", Config: &rest.Config{Host: srv.URL}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go svc.Watch(ctx, func(id string) (Cluster, bool) { return cl, id == cl.ID })
	return svc, cl, s, srv, buf
}

func alice() Who {
	return Who{Session: "s-alice", User: "alice@corp.example", Name: "Alice", Person: kube.Person{User: "alice@corp.example"}, Audit: audit.Who{Subject: "alice", Email: "alice@corp.example", Name: "Alice"}}
}

func bob() Who {
	return Who{Session: "s-bob", User: "bob@corp.example", Name: "Bob", Person: kube.Person{User: "bob@corp.example"}, Audit: audit.Who{Subject: "bob", Email: "bob@corp.example", Name: "Bob"}}
}

func wait(t *testing.T, svc *Service, id string, ok func(it Item) bool) Item {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if it, _, found := svc.Snapshot(id, 0); found && ok(it) {
			return it
		}
		time.Sleep(20 * time.Millisecond)
	}
	it, _, _ := svc.Snapshot(id, 0)
	t.Fatalf("item %s never got there: status %s, reason %q", id, it.Status, it.Reason)
	return it
}

func outputOf(svc *Service, id string) string {
	_, lines, _ := svc.Snapshot(id, 0)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.T + "\n")
	}
	return b.String()
}

func TestReadsRunAtOnce(t *testing.T) {
	svc, cl, _, _, buf := setup(t, Options{})
	it := svc.Submit(context.Background(), cl, alice(), "kubectl get nodes", "typed", "", true)[0]
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusDone || done.Change {
		t.Fatalf("status %s change %v", done.Status, done.Change)
	}
	if out := outputOf(svc, it.ID); !strings.Contains(out, "NAME") || !strings.Contains(out, "worker-001") {
		t.Fatalf("output %q", out)
	}
	evs := buf.events()
	if len(evs) != 2 || evs[0].Action != ActionCommand || evs[1].Action != ActionResult || evs[1].Who.Email != "alice@corp.example" || evs[1].Cluster != "local" {
		t.Fatalf("audit %+v", evs)
	}
}

func TestShellCharactersAreRefused(t *testing.T) {
	svc, cl, s, _, buf := setup(t, Options{})
	for _, line := range []string{
		"kubectl get pods; kubectl delete pod cache-0 -n web",
		"kubectl get pods | grep api",
		"kubectl get pods && kubectl cordon worker-001",
		"kubectl get pods $(kubectl cordon worker-001)",
		"kubectl get pods `kubectl cordon worker-001`",
		"kubectl get pods > /tmp/x",
		"kubectl get pods -n \"$HOME\"",
		"kubectl get pods --kubeconfig /etc/passwd",
		"kubectl get pods --as system:admin",
		"kubectl exec -it api -- sh",
		"bash -c 'kubectl cordon worker-001'",
	} {
		it := svc.Submit(context.Background(), cl, alice(), line, "typed", "", true)[0]
		if it.Status != StatusRefused || it.Reason == "" || it.Rule == "" {
			t.Errorf("%s: status %s", line, it.Status)
		}
	}
	// Quoted, the characters are literal and harmless: a name with a
	// semicolon is simply not found.
	it := svc.Submit(context.Background(), cl, alice(), "kubectl get pod 'cache-0; kubectl cordon worker-001' -n web", "typed", "", true)[0]
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusFailed || !strings.Contains(outputOf(svc, it.ID), "not found") {
		t.Fatalf("quoted: %s %q", done.Status, outputOf(svc, it.ID))
	}
	if n := s.Node("worker-001"); n.Spec.Unschedulable {
		t.Fatal("a refused command cordoned the node")
	}
	refusals := 0
	for _, e := range buf.events() {
		if e.Action == ActionRefuse {
			refusals++
		}
	}
	if refusals != 11 {
		t.Fatalf("%d refusals audited", refusals)
	}
}

func TestChangesWaitForApprovalAndRunAsTheApprover(t *testing.T) {
	svc, cl, s, _, buf := setup(t, Options{})
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-002", "Cordon button on node worker-002", "", true)[0]
	if it.Status != StatusPending || !it.Change {
		t.Fatalf("status %s", it.Status)
	}
	card := wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if len(card.Plan.Targets) != 1 || card.Plan.Targets[0] != "node worker-002" {
		t.Fatalf("targets %v", card.Plan.Targets)
	}
	if !strings.Contains(strings.Join(card.Plan.Impact, " "), "No new pods will be scheduled on worker-002") {
		t.Fatalf("impact %v", card.Plan.Impact)
	}
	if len(card.Plan.DryRun) == 0 || !strings.Contains(card.Plan.DryRun[0].T, "cordoned (server dry run)") {
		t.Fatalf("dry run %+v", card.Plan.DryRun)
	}
	if s.Node("worker-002").Spec.Unschedulable {
		t.Fatal("cordoned before the approval")
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusDone || !s.Node("worker-002").Spec.Unschedulable {
		t.Fatalf("status %s", done.Status)
	}
	var actions []string
	for _, e := range buf.events() {
		actions = append(actions, e.Action)
	}
	if strings.Join(actions, ",") != "watson.command,watson.approve,watson.result" {
		t.Fatalf("audit %v", actions)
	}
	// It ran as the approver, impersonated.
	if s.LastImpersonated() != "alice@corp.example" {
		t.Fatalf("ran as %q", s.LastImpersonated())
	}
}

// The Storage tab's three changes run once approved, with cards that say
// what each does (ST-18): a claim grows when its class allows it, a
// deleted claim's volume follows its reclaim policy, and the default class
// moves by its annotation.
func TestStorageChangesWaitForApprovalAndRun(t *testing.T) {
	svc, cl, _, srv, buf := setup(t, Options{})
	get := func(path string, v any) {
		t.Helper()
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	run := func(line string, wants ...string) {
		t.Helper()
		it := svc.Submit(context.Background(), cl, alice(), line, "Storage", "", true)[0]
		if it.Status != StatusPending {
			t.Fatalf("%s: %s %s", line, it.Status, it.Reason)
		}
		card := wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
		im := strings.Join(card.Plan.Impact, " ")
		for _, w := range wants {
			if !strings.Contains(im, w) {
				t.Fatalf("%s: impact %q lacks %q", line, im, w)
			}
		}
		if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
			t.Fatal(err)
		}
		if done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) }); done.Status != StatusDone {
			t.Fatalf("%s: %s %q", line, done.Status, outputOf(svc, it.ID))
		}
	}

	run(`kubectl patch pvc data-postgres-1 -n storage --type merge -p '{"spec":{"resources":{"requests":{"storage":"150Gi"}}}}'`, "Grows claim storage/data-postgres-1 from 100Gi to 150Gi.", "Its class longhorn allows expansion.")
	var c corev1.PersistentVolumeClaim
	get("/api/v1/namespaces/storage/persistentvolumeclaims/data-postgres-1", &c)
	if q := c.Status.Capacity[corev1.ResourceStorage]; q.String() != "150Gi" {
		t.Errorf("the claim has %s", q.String())
	}

	run("kubectl delete pvc data-kafka-2 -n ingest", "Deletes claim ingest/data-kafka-2 and, under its reclaim policy Delete, the volume pvc-d2f84b19-7c6e-4a35-b1d8-9e0a4c6f2b71 and all its data.", "kafka-2 mount it")
	run("kubectl delete pvc hdfs-export -n ingest", "Its volume legacy-hdfs-export and the data stay, Released (reclaim policy Retain)")
	var pv corev1.PersistentVolume
	get("/api/v1/persistentvolumes/legacy-hdfs-export", &pv)
	if pv.Status.Phase != corev1.VolumeReleased {
		t.Errorf("the volume is %s", pv.Status.Phase)
	}

	run("kubectl annotate storageclass longhorn storageclass.kubernetes.io/is-default-class=false --overwrite", "longhorn is no longer the default class")
	run("kubectl annotate storageclass nfs-shared storageclass.kubernetes.io/is-default-class=true --overwrite", "nfs-shared becomes the default class")
	var sc storagev1.StorageClass
	get("/apis/storage.k8s.io/v1/storageclasses/nfs-shared", &sc)
	if sc.Annotations["storageclass.kubernetes.io/is-default-class"] != "true" {
		t.Errorf("annotations %v", sc.Annotations)
	}
	approvals := 0
	for _, e := range buf.events() {
		if e.Action == ActionApprove {
			approvals++
		}
	}
	if approvals != 5 {
		t.Errorf("%d approvals audited, want one per change", approvals)
	}
}

func TestAnApprovalLapsesWhenItsObjectsChange(t *testing.T) {
	svc, cl, _, srv, _ := setup(t, Options{})
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-003", "typed", "", true)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	// Someone cordons it by other means.
	resp, err := http.Post(srv.URL+"/sim/nodes/worker-003/cordon", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	lapsed := wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusLapsed })
	if !strings.Contains(lapsed.Reason, "node worker-003 changed after this card appeared") {
		t.Fatalf("reason %q", lapsed.Reason)
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err == nil {
		t.Fatal("a lapsed card was approved")
	}
}

func TestAnApprovalLapsesAfterItsTime(t *testing.T) {
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	svc, cl, _, _, buf := setup(t, Options{Now: clock})
	it := svc.Submit(context.Background(), cl, alice(), "kubectl uncordon worker-001", "typed", "", true)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	mu.Lock()
	now = now.Add(10*time.Minute + time.Second)
	mu.Unlock()
	lapsed := wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusLapsed })
	if !strings.Contains(lapsed.Reason, "10 minutes") {
		t.Fatalf("reason %q", lapsed.Reason)
	}
	found := false
	for _, e := range buf.events() {
		found = found || e.Action == ActionLapse
	}
	if !found {
		t.Fatal("no lapse record")
	}
}

func TestReadOnlyClustersRefuseChanges(t *testing.T) {
	svc, cl, _, _, buf := setup(t, Options{})
	cl.ReadOnly = true
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-001", "typed", "", true)[0]
	if it.Status != StatusRefused || it.Rule != "read-only cluster" || !strings.Contains(it.Reason, "connected read-only") {
		t.Fatalf("%s %s %q", it.Status, it.Rule, it.Reason)
	}
	// Reads still run.
	r := svc.Submit(context.Background(), cl, alice(), "kubectl get nodes", "typed", "", true)[0]
	if wait(t, svc, r.ID, func(i Item) bool { return finished(i.Status) }).Status != StatusDone {
		t.Fatal("a read failed on a read-only cluster")
	}
	last := buf.events()
	if e := last[1]; e.Action != ActionRefuse || !strings.Contains(e.Detail, "read-only cluster") {
		t.Fatalf("audit %+v", e)
	}
}

func TestProductionNeedsASecondPerson(t *testing.T) {
	svc, cl, s, _, _ := setup(t, Options{TwoPerson: func(c Cluster) bool { return c.Env == "production" }})
	cl.Env = "production"
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-001", "typed", "", true)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	_, err := svc.Approve(context.Background(), cl, alice(), it.ID)
	var r *Refusal
	if err == nil || !errorsAs(err, &r) || r.Rule != "two people" {
		t.Fatalf("self-approval: %v", err)
	}
	// Bob sees Alice's card and approves it; it runs as Bob.
	seen := false
	for _, x := range svc.Items(cl, bob()) {
		seen = seen || x.ID == it.ID
	}
	if !seen {
		t.Fatal("the second person cannot see the card")
	}
	if _, err := svc.Approve(context.Background(), cl, bob(), it.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusDone })
	if s.LastImpersonated() != "bob@corp.example" || !s.Node("worker-001").Spec.Unschedulable {
		t.Fatalf("ran as %q", s.LastImpersonated())
	}
	// With sign-in off nobody can be told apart: changes are refused.
	anon := Who{Session: "s-anon", Name: "Anonymous"}
	if it := svc.Submit(context.Background(), cl, anon, "kubectl uncordon worker-001", "typed", "", true)[0]; it.Status != StatusRefused || it.Rule != "two people" {
		t.Fatalf("anonymous: %s %s", it.Status, it.Rule)
	}
}

func TestDrainCardNamesPodsAndBudgets(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	it := svc.Submit(context.Background(), cl, alice(), "kubectl drain worker-001 --ignore-daemonsets --delete-emptydir-data", "typed", "", true)[0]
	card := wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	im := strings.Join(card.Plan.Impact, "\n")
	if !strings.Contains(im, "Cordons worker-001 and evicts") {
		t.Fatalf("impact %s", im)
	}
	dry := ""
	for _, l := range card.Plan.DryRun {
		dry += l.T + "\n"
	}
	if !strings.Contains(dry, "(server dry run)") {
		t.Fatalf("dry run %s", dry)
	}
	// The simulator's budgets hold some of these pods: the card says so,
	// and the dry run shows the evictions it refuses.
	if strings.Contains(dry, "disruption budget") && !strings.Contains(im, "allows no disruption now") {
		t.Fatalf("impact misses the budget: %s", im)
	}
	t.Log(im)
}

func TestStopAndQueue(t *testing.T) {
	block := make(chan struct{})
	var mu sync.Mutex
	order := []string{}
	run := func(ctx context.Context, c *Command, tg Target, out, errOut ioWriter, extra ...string) int {
		if len(extra) > 0 {
			return 0
		}
		mu.Lock()
		order = append(order, c.Line)
		mu.Unlock()
		if strings.Contains(c.Line, "nodes") {
			select {
			case <-ctx.Done():
				return 130
			case <-block:
			}
		}
		return 0
	}
	svc, cl, _, _, _ := setup(t, Options{Run: run})
	a := svc.Submit(context.Background(), cl, alice(), "kubectl get nodes", "typed", "", true)[0]
	b := svc.Submit(context.Background(), cl, alice(), "kubectl get pods -A", "typed", "", true)[0]
	wait(t, svc, a.ID, func(i Item) bool { return i.Status == StatusRunning })
	if it, _, _ := svc.Snapshot(b.ID, 0); it.Status != StatusQueued {
		t.Fatalf("second command %s, want queued", it.Status)
	}
	if _, err := svc.Stop(cl, alice(), a.ID); err != nil {
		t.Fatal(err)
	}
	if st := wait(t, svc, a.ID, func(i Item) bool { return finished(i.Status) }).Status; st != StatusStopped {
		t.Fatalf("stopped command: %s", st)
	}
	if st := wait(t, svc, b.ID, func(i Item) bool { return finished(i.Status) }).Status; st != StatusDone {
		t.Fatalf("queued command: %s", st)
	}
	close(block)
}

type ioWriter = io.Writer

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// A card belongs to the cluster it was planned for: approved, stopped or
// discarded through another cluster's address, it is not found there.
func TestACardActsOnlyOnItsCluster(t *testing.T) {
	svc, cl, s, _, _ := setup(t, Options{})
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-001", "typed", "", true)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	other := cl
	other.ID, other.Name = "prod", "prod"
	if _, err := svc.Approve(context.Background(), other, alice(), it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve on another cluster: %v", err)
	}
	if _, err := svc.Discard(other, alice(), it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("discard on another cluster: %v", err)
	}
	if _, err := svc.Stop(other, alice(), it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop on another cluster: %v", err)
	}
	if len(svc.Items(other, alice())) != 0 {
		t.Fatal("listed on another cluster")
	}
	if s.Node("worker-001").Spec.Unschedulable {
		t.Fatal("cordoned through another cluster")
	}
	if got, _, _ := svc.Snapshot(it.ID, 0); got.Status != StatusPending {
		t.Fatalf("status %s", got.Status)
	}
}

// A card for a selector shows the objects it matched; one that joins the
// set before the approval voids the card rather than change unseen.
func TestASelectorCardLapsesWhenItsSetChanges(t *testing.T) {
	svc, cl, s, srv, _ := setup(t, Options{})
	k := kubernetes.NewForConfigOrDie(&rest.Config{Host: srv.URL})
	label := func(node string) {
		t.Helper()
		patch := []byte(`{"metadata":{"labels":{"test-pool":"gpu"}}}`)
		if _, err := k.CoreV1().Nodes().Patch(context.Background(), node, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	label("worker-002")
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon -l test-pool=gpu", "typed", "", true)[0]
	card := wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if len(card.Plan.Targets) != 1 || card.Plan.Targets[0] != "node worker-002" {
		t.Fatalf("targets %v", card.Plan.Targets)
	}
	label("worker-003")
	lapsed := wait(t, svc, it.ID, func(i Item) bool { return i.Status == StatusLapsed })
	if !strings.Contains(lapsed.Reason, "the set of nodes matching test-pool=gpu changed") {
		t.Fatalf("reason %q", lapsed.Reason)
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err == nil {
		t.Fatal("a lapsed card was approved")
	}
	if s.Node("worker-003").Spec.Unschedulable {
		t.Fatal("the node that joined the set was cordoned")
	}
}

// A change approved while another of the approver's commands runs waits
// in the queue; if its objects change meanwhile, it lapses instead of
// running against what the card never showed.
func TestAQueuedChangeLapsesWhenItsObjectsChange(t *testing.T) {
	block := make(chan struct{})
	var mu sync.Mutex
	var ran []string
	run := func(ctx context.Context, c *Command, tg Target, out, errOut ioWriter, extra ...string) int {
		if len(extra) > 0 {
			return 0
		}
		mu.Lock()
		ran = append(ran, c.Line)
		mu.Unlock()
		if strings.Contains(c.Line, "get nodes") {
			select {
			case <-ctx.Done():
				return 130
			case <-block:
			}
		}
		return 0
	}
	svc, cl, _, srv, _ := setup(t, Options{Run: run})
	first := svc.Submit(context.Background(), cl, alice(), "kubectl get nodes", "typed", "", true)[0]
	wait(t, svc, first.ID, func(i Item) bool { return i.Status == StatusRunning })
	it := svc.Submit(context.Background(), cl, alice(), "kubectl cordon worker-002", "typed", "", true)[0]
	wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := svc.Snapshot(it.ID, 0); got.Status != StatusQueued {
		t.Fatalf("status %s, want queued", got.Status)
	}
	resp, err := http.Post(srv.URL+"/sim/nodes/worker-002/cordon", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	close(block)
	lapsed := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if lapsed.Status != StatusLapsed || !strings.Contains(lapsed.Reason, "node worker-002 changed after this card appeared") {
		t.Fatalf("status %s reason %q", lapsed.Status, lapsed.Reason)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, l := range ran {
		if strings.Contains(l, "cordon") {
			t.Fatalf("ran %q", l)
		}
	}
}

func TestTaintsWaitForApprovalAndRun(t *testing.T) {
	svc, cl, s, _, _ := setup(t, Options{})
	has := func() bool {
		for _, x := range s.Node("worker-002").Spec.Taints {
			if x.Key == "team" && x.Value == "db" && x.Effect == "NoSchedule" {
				return true
			}
		}
		return false
	}
	it := svc.Submit(context.Background(), cl, alice(), "kubectl taint nodes worker-002 team=db:NoSchedule --overwrite", "Fleets", "", true)[0]
	card := wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	im := strings.Join(card.Plan.Impact, " ")
	if len(card.Plan.Targets) != 1 || card.Plan.Targets[0] != "node worker-002" || !strings.Contains(im, "adds team=db:NoSchedule") || !strings.Contains(im, "do not tolerate team will not be scheduled on worker-002") {
		t.Fatalf("card %v %s", card.Plan.Targets, im)
	}
	if has() {
		t.Fatal("tainted before the approval")
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	if done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) }); done.Status != StatusDone || !has() {
		t.Fatalf("status %s, output %q", done.Status, outputOf(svc, it.ID))
	}

	it = svc.Submit(context.Background(), cl, alice(), "kubectl taint nodes worker-002 team:NoSchedule-", "Fleets", "", true)[0]
	card = wait(t, svc, it.ID, func(i Item) bool { return i.Plan != nil && i.Plan.Ready })
	if im := strings.Join(card.Plan.Impact, " "); !strings.Contains(im, "removes team=db:NoSchedule") {
		t.Fatalf("impact %s", im)
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	if done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) }); done.Status != StatusDone || has() {
		t.Fatalf("status %s, output %q", done.Status, outputOf(svc, it.ID))
	}
}
