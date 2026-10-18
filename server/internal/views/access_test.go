package views_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/fake"
	"github.com/ucesys/221b/server/internal/views"
)

func accessRows(t *testing.T, src views.Source) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, k := range views.AccessKeys(src) {
		if r, ok := views.BuildAccess(src, k, now); ok {
			out[k] = r
		}
	}
	return out
}

func TestAccessRowsAndFindings(t *testing.T) {
	src := fake.New("worker-1")
	meta := func(ns, name string, labels map[string]string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, CreationTimestamp: ts(0)}
	}
	defaults := map[string]string{"kubernetes.io/bootstrapping": "rbac-defaults"}
	all := rbacv1.PolicyRule{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}
	src.Put(&rbacv1.ClusterRole{ObjectMeta: meta("", "cluster-admin", defaults), Rules: []rbacv1.PolicyRule{all}})
	src.Put(&rbacv1.ClusterRole{ObjectMeta: meta("", "secret-reader", map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "mon"}), Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"list"}}}})
	sa := func(ns, name string) rbacv1.Subject {
		return rbacv1.Subject{Kind: "ServiceAccount", Namespace: ns, Name: name}
	}
	ref := func(kind, name string) rbacv1.RoleRef {
		return rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kind, Name: name}
	}
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: meta("", "cluster-admin", defaults), RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{{Kind: "Group", Name: "system:masters"}}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: meta("", "ci", nil), RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{sa("default", "ci")}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: meta("", "mon", nil), RoleRef: ref("ClusterRole", "secret-reader"), Subjects: []rbacv1.Subject{sa("mon", "exporter")}})
	src.Put(&rbacv1.RoleBinding{ObjectMeta: meta("web", "dana", nil), RoleRef: ref("ClusterRole", "admin"), Subjects: []rbacv1.Subject{{Kind: "User", Name: "dana@corp.example"}, {Kind: "ServiceAccount", Name: "ghost"}}})
	src.Put(&corev1.ServiceAccount{ObjectMeta: meta("default", "ci", nil)})
	src.Put(&corev1.ServiceAccount{ObjectMeta: meta("mon", "exporter", nil)})
	src.Put(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ci-token", Annotations: map[string]string{corev1.ServiceAccountNameKey: "ci"}}, Type: corev1.SecretTypeServiceAccountToken, Data: map[string][]byte{"token": []byte("x")}})
	p := pod("default", "deploy-1", "worker-1")
	p.Spec.ServiceAccountName = "ci"
	src.Put(p)

	// Rancher, its charts and RKE2's Helm controller need cluster-admin:
	// info, with no command to delete what they would put back. A chart
	// that is not the platform's is still high, fixed in its values.
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "rancher", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"}, Annotations: map[string]string{"meta.helm.sh/release-name": "rancher", "meta.helm.sh/release-namespace": "cattle-system"}}, RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{sa("cattle-system", "rancher")}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "globaladmin-u-1", Annotations: map[string]string{"authz.cluster.cattle.io/globalrole": "admin"}}, RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{{Kind: "User", Name: "u-1"}}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "helm-kube-system-rke2-canal", Annotations: map[string]string{"objectset.rio.cattle.io/owner-gvk": "helm.cattle.io/v1, Kind=HelmChart", "objectset.rio.cattle.io/owner-namespace": "kube-system", "objectset.rio.cattle.io/owner-name": "rke2-canal"}}, RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{sa("kube-system", "helm-rke2-canal")}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "argo", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "argo"}}, RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{sa("argo", "argo")}})
	// Rancher's per-user bindings name only their owner; RKE2 marks the
	// bindings it adds to Kubernetes' defaults; clusterctl labels its own.
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "grb-u-1-view", OwnerReferences: []metav1.OwnerReference{{APIVersion: "management.cattle.io/v3", Kind: "User", Name: "u-1"}}}, RoleRef: ref("ClusterRole", "view"), Subjects: []rbacv1.Subject{{Kind: "User", Name: "u-1"}}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-kubelet-admin", Labels: map[string]string{"rke2.io/bootstrapping": "rbac-defaults"}}, RoleRef: ref("ClusterRole", "system:kubelet-api-admin"), Subjects: []rbacv1.Subject{{Kind: "User", Name: "kube-apiserver"}}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "capi", Labels: map[string]string{"clusterctl.cluster.x-k8s.io": ""}}, RoleRef: ref("ClusterRole", "secret-reader"), Subjects: []rbacv1.Subject{sa("capi", "manager")}})
	src.Put(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "turtles", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "manager-rolebinding"}, Annotations: map[string]string{"meta.helm.sh/release-name": "rancher-turtles", "meta.helm.sh/release-namespace": "cattle-turtles-system"}}, RoleRef: ref("ClusterRole", "cluster-admin"), Subjects: []rbacv1.Subject{sa("cattle-turtles-system", "manager")}})
	for _, n := range []string{"cattle-system/rancher", "kube-system/helm-rke2-canal", "argo/argo", "capi/manager", "cattle-turtles-system/manager"} {
		ns, name, _ := strings.Cut(n, "/")
		src.Put(&corev1.ServiceAccount{ObjectMeta: meta(ns, name, nil)})
	}

	rows := accessRows(t, src)
	find := func(key string) views.BindingRow {
		b, ok := rows[key].(views.BindingRow)
		if !ok {
			t.Fatalf("no binding %s in %v", key, keys(rows))
		}
		return b
	}
	sev := func(b views.BindingRow) string {
		var out []string
		for _, f := range b.Findings {
			out = append(out, f.Severity+": "+f.Title)
		}
		return strings.Join(out, " | ")
	}
	if got := sev(find("binding:ClusterRoleBinding/cluster-admin")); got != "info: system:masters bypasses RBAC" {
		t.Errorf("masters: %s", got)
	}
	if got := sev(find("binding:ClusterRoleBinding/ci")); got != "high: ServiceAccount default/ci has cluster-admin" {
		t.Errorf("ci: %s", got)
	}
	if got := sev(find("binding:ClusterRoleBinding/mon")); got != "medium: ServiceAccount mon/exporter can read every Secret in the cluster" {
		t.Errorf("mon: %s", got)
	}
	for key, want := range map[string]string{
		"rancher":                     "info: ServiceAccount cattle-system/rancher has cluster-admin, as Rancher needs",
		"globaladmin-u-1":             "info: User u-1 has cluster-admin, through Rancher",
		"helm-kube-system-rke2-canal": "info: ServiceAccount kube-system/helm-rke2-canal has cluster-admin, as RKE2 needs",
		"argo":                        "high: ServiceAccount argo/argo has cluster-admin",
	} {
		b := find("binding:ClusterRoleBinding/" + key)
		if got := sev(b); got != want {
			t.Errorf("%s: %s", key, got)
		}
		if b.Findings[0].Command != "" {
			t.Errorf("%s: command %q", key, b.Findings[0].Command)
		}
	}
	if b := find("binding:ClusterRoleBinding/grb-u-1-view"); b.Source.Platform != views.PlatformRancher || len(b.Findings) != 0 {
		t.Errorf("grb-u-1-view: %+v %s", b.Source, sev(b))
	}
	if b := find("binding:ClusterRoleBinding/kube-apiserver-kubelet-admin"); !b.Source.Default() || b.Source.Platform != views.PlatformRKE2 || len(b.Findings) != 0 {
		t.Errorf("kubelet-admin: %+v %s", b.Source, sev(b))
	}
	if f := find("binding:ClusterRoleBinding/capi").Findings[0]; f.Severity != "medium" || f.Command != "" || !strings.Contains(f.Text, "clusterctl manages this binding") {
		t.Errorf("capi: %+v", f)
	}
	if f := find("binding:ClusterRoleBinding/turtles").Findings[0]; !strings.Contains(f.Text, "Rancher’s chart rancher-turtles made") {
		t.Errorf("turtles: %s", f.Text)
	}
	if f := find("binding:ClusterRoleBinding/argo").Findings[0]; !strings.Contains(f.Text, "Helm release argo made this binding, so change it in the release’s values") {
		t.Errorf("argo: %s", f.Text)
	}
	if c := find("binding:ClusterRoleBinding/ci").Findings[0].Command; c != "kubectl delete clusterrolebinding ci" {
		t.Errorf("ci command %q", c)
	}
	dana := find("binding:RoleBinding/web/dana")
	if got := sev(dana); got != "medium: RoleBinding web/dana names a ServiceAccount that doesn’t exist | low: dana@corp.example has admin in web directly" {
		t.Errorf("dana: %s", got)
	}
	// The ServiceAccount without a namespace is the binding's; the
	// ClusterRole admin does not exist here.
	if dana.Subjects[1].Namespace != "web" || !dana.Subjects[1].Missing || !dana.RoleMissing {
		t.Errorf("dana subjects %+v missing role %v", dana.Subjects, dana.RoleMissing)
	}
	role := rows["role:ClusterRole/secret-reader"].(views.RoleRow)
	if role.Source.Kind != "helm" || role.Source.Release != "mon" || strings.Join(role.Powers, ",") != "reads Secrets" {
		t.Errorf("role %+v", role)
	}
	ci := rows["sa:default/ci"].(views.SARow)
	if len(ci.Pods) != 1 || ci.Pods[0].Name != "deploy-1" || len(ci.Tokens) != 1 || ci.Tokens[0].Name != "ci-token" {
		t.Errorf("sa %+v", ci)
	}
	for _, k := range []string{"role:ClusterRole/x", "role:Role/ns/x", "binding:RoleBinding/ns/x", "sa:ns/x"} {
		if _, _, _, _, ok := views.SplitAccessKey(k); !ok {
			t.Errorf("%s does not split", k)
		}
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
