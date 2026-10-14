package views_test

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/views"
)

func TestPodSecurityWarningsAsTheAPIServerWordsThem(t *testing.T) {
	src := fake.New("worker-1")
	src.Put(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "edge", Labels: map[string]string{"kubernetes.io/metadata.name": "edge", "pod-security.kubernetes.io/warn": "baseline"}}})
	mk := func(name string, f func(p *corev1.Pod)) {
		p := pod("edge", name, "worker-1")
		f(p)
		src.Put(p)
	}
	for _, n := range []string{"agent-b", "agent-a", "agent-c"} {
		mk(n, func(p *corev1.Pod) {
			p.Spec.HostNetwork = true
			p.Spec.Volumes = []corev1.Volume{{Name: "proc", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/proc"}}}}
		})
	}
	mk("proxy", func(p *corev1.Pod) {
		p.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 80, HostPort: 80}}
	})
	mk("plain", func(p *corev1.Pod) {})
	row, ok := views.BuildPolicy(src, "ns:edge", now)
	if !ok {
		t.Fatal("no row")
	}
	r := row.(views.PolicyNSRow)
	want := []string{
		`existing pods in namespace "edge" violate the new PodSecurity enforce level "baseline:latest"`,
		"agent-a (and 2 other pods): host namespaces, hostPath volumes",
		"proxy: hostPort",
	}
	if strings.Join(r.Baseline.Warnings, "\n") != strings.Join(want, "\n") {
		t.Fatalf("baseline warnings:\n%s", strings.Join(r.Baseline.Warnings, "\n"))
	}
	if len(r.Restricted.Fail) != 5 || r.Warn != "baseline" || r.Enforce != "" {
		t.Fatalf("restricted %d fail, warn %q enforce %q", len(r.Restricted.Fail), r.Warn, r.Enforce)
	}
	if !strings.Contains(r.Restricted.Fail[0].Detail, `must set securityContext.allowPrivilegeEscalation=false`) {
		t.Fatalf("detail %q", r.Restricted.Fail[0].Detail)
	}
}

func TestDrainVerdicts(t *testing.T) {
	src := fake.New("worker-1", "worker-2", "worker-3")
	one := intstr.FromInt32(1)
	for _, n := range []string{"web-1", "web-2"} {
		src.Put(pod("shop", n, "worker-1"))
	}
	src.Put(&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}, Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2}})
	bare := pod("shop", "debug", "worker-2")
	bare.OwnerReferences = nil
	bare.Labels = map[string]string{"run": "debug"}
	src.Put(bare)
	drain := func(n string) views.DrainRow {
		r, ok := views.BuildPolicy(src, "drain:"+n, now)
		if !ok {
			t.Fatalf("no drain row for %s", n)
		}
		return r.(views.DrainRow)
	}
	if d := drain("worker-1"); d.Verdict != "waits" || !strings.Contains(d.Why, "shop/web allows 1 disruption at a time and 2 of its pods run here") {
		t.Fatalf("worker-1 %s: %s", d.Verdict, d.Why)
	}
	if d := drain("worker-2"); d.Verdict != "blocked" || !strings.Contains(d.Why, "shop/debug: it has no controller") {
		t.Fatalf("worker-2 %s: %s", d.Verdict, d.Why)
	}
	if d := drain("worker-3"); d.Verdict != "clean" || d.Why != "Only static and DaemonSet pods run here." {
		t.Fatalf("worker-3 %s: %s", d.Verdict, d.Why)
	}
	// With no disruption left, the eviction is refused.
	src.Put(&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}, Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 2}})
	if d := drain("worker-1"); d.Verdict != "blocked" || d.Why != "shop/web allows no disruption now, and web-1 and web-2 run here." {
		t.Fatalf("worker-1 blocked %s: %s", d.Verdict, d.Why)
	}
}

func TestRolloutWouldStall(t *testing.T) {
	src := fake.New("worker-1")
	src.Put(&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "q"}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"requests.memory": resource.MustParse("1Gi")}},
		Status: corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{"requests.memory": resource.MustParse("1Gi")}, Used: corev1.ResourceList{"requests.memory": resource.MustParse("768Mi")}}})
	one := int32(1)
	tpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	src.Put(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}, Spec: appsv1.DeploymentSpec{Replicas: &one, Template: tpl}})
	two := int32(4)
	src.Put(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "big"}, Spec: appsv1.DeploymentSpec{Replicas: &two, Template: tpl}})
	r, ok := views.BuildPolicy(src, "rollout:shop/api", now)
	if !ok {
		t.Fatal("no rollout row")
	}
	ro := r.(views.RolloutRow)
	if !ro.Stalls || ro.Surge != 1 || ro.Unavailable != 0 || len(ro.Short) != 1 || ro.Short[0].Need != "512Mi" || ro.Short[0].Left != "256Mi" {
		t.Fatalf("api %+v", ro)
	}
	// Four replicas may lose one first, so the update does not stall.
	r, _ = views.BuildPolicy(src, "rollout:shop/big", now)
	if big := r.(views.RolloutRow); big.Stalls || big.Unavailable != 1 {
		t.Fatalf("big %+v", big)
	}
	q, _ := views.BuildPolicy(src, "quota:shop/q", now)
	if it := q.(views.QuotaRow).Items[0]; it.Pct != 75 || it.Tone != "warn" {
		t.Fatalf("quota %+v", it)
	}
}
