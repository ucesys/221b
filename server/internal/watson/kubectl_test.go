package watson

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/sim"
)

// simTarget starts a simulated cluster and returns where to run commands.
func simTarget(t *testing.T) (Target, *sim.Sim) {
	t.Helper()
	s := sim.New(sim.Options{Nodes: 6, Pods: 60, Seed: 7, Metrics: true, Problems: true, Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	var d Discovery
	disc, err := d.For("test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return Target{Config: cfg, Discovery: disc}, s
}

func run(t *testing.T, tg Target, line string, extra ...string) (string, int) {
	t.Helper()
	c, err := Parse(line)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	var out bytes.Buffer
	code := runKubectl(context.Background(), c, tg, &out, &out, extra...)
	return out.String(), code
}

func TestKubectlReadsAgainstTheSimulator(t *testing.T) {
	tg, _ := simTarget(t)
	for _, line := range []string{
		"kubectl get nodes",
		"kubectl get nodes -o wide",
		"kubectl get pods -n web",
		"kubectl get deploy -A",
		"kubectl get pods -A --field-selector status.phase=Pending",
		"kubectl describe node " + "cp-1",
		"kubectl top nodes",
		"kubectl events -n web",
		"kubectl auth can-i delete pods -n web",
		"kubectl get secrets -n web",
	} {
		out, code := run(t, tg, line)
		t.Logf("$ %s  (exit %d)\n%s", line, code, out)
		if code != 0 {
			t.Errorf("%s: exit %d\n%s", line, code, out)
		}
	}
	// Every kind describe and logs meet, with flags before the verb too.
	debugPanics = true
	defer func() { debugPanics = false }()
	names, _ := run(t, tg, "kubectl get pods -n web -o name")
	pod := strings.TrimPrefix(strings.SplitN(strings.TrimSpace(names), "\n", 2)[0], "pod/")
	for _, line := range []string{
		"kubectl describe pod " + pod + " -n web",
		"kubectl -n web describe pod " + pod,
		"kubectl describe pods -n web",
		"kubectl describe deployment -n web",
		"kubectl describe service -n web",
		"kubectl describe namespace web",
		"kubectl describe node worker-001",
		"kubectl describe pvc -A",
		"kubectl describe ingress -A",
		"kubectl describe statefulset -A",
		"kubectl describe daemonset -A",
		"kubectl describe job -A",
		"kubectl describe cronjob -A",
		"kubectl describe configmap -n web",
		"kubectl describe secret -n web",
		"kubectl describe pdb -A",
		"kubectl describe hpa -A",
		"kubectl describe networkpolicy -A",
		"kubectl describe serviceaccount -n web",
		"kubectl describe clusterrole view",
		"kubectl logs " + pod + " -n web --tail=5",
		"kubectl get pod " + pod + " -n web -o yaml",
	} {
		out, code := run(t, tg, line)
		if code != 0 || strings.Contains(out, "runtime error") {
			t.Errorf("$ %s  (exit %d)\n%s", line, code, out)
		}
	}
	_ = kube.Person{}
	_ = strings.TrimSpace
}

func TestKubectlChangesAgainstTheSimulator(t *testing.T) {
	tg, _ := simTarget(t)
	for _, line := range []string{
		"kubectl cordon worker-002 --dry-run=server",
		"kubectl cordon worker-002",
		"kubectl get nodes worker-002",
		"kubectl uncordon worker-002",
		"kubectl drain worker-003 --ignore-daemonsets --delete-emptydir-data --dry-run=server",
		"kubectl drain worker-003 --ignore-daemonsets --delete-emptydir-data --timeout=20s",
		"kubectl get nodes worker-003",
		"kubectl scale deploy/api -n web --replicas=3",
		"kubectl rollout restart deployment/frontend -n web",
		"kubectl rollout undo deployment/worker -n web",
		"kubectl get rs -n web",
		"kubectl delete pod cache-0 -n web",
		"kubectl label node worker-001 team=data",
		"kubectl label node worker-001 team=ml",
		"kubectl label node worker-001 team=ml --overwrite",
		"kubectl annotate deploy api -n web note='checked by watson'",
		"kubectl get pods -n web",
	} {
		out, code := run(t, tg, line)
		t.Logf("$ %s  (exit %d)\n%s", line, code, out)
	}
}

func TestHelmAgainstTheSimulator(t *testing.T) {
	debugPanics = true
	tg, _ := simTarget(t)
	for _, line := range []string{
		"helm list -A",
		"helm list -n web",
		"helm status shop -n web",
		"helm history shop -n web",
		"helm get values shop -n web",
		"helm rollback superset -n analytics --dry-run=server",
		"helm rollback superset 2 -n analytics",
		"helm history superset -n analytics",
	} {
		c, err := Parse(line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		var out bytes.Buffer
		code := runHelm(context.Background(), c, tg, &out, &out)
		t.Logf("$ %s  (exit %d)\n%s", line, code, out.String())
	}
}
