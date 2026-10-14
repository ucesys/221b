package sim

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The simulated cluster's RBAC (P4-03): Kubernetes' and kubeadm's own
// roles and bindings, as a real cluster has them (fewer of the
// controllers'), and the ones a team adds, with something to review in
// each kind of finding.

// rule builds a policy rule: "groups|resources|verbs", each a comma list
// ("" for the core group), and optional resource names.
func rule(spec string, names ...string) rbacv1.PolicyRule {
	parts := strings.SplitN(spec, "|", 3)
	split := func(s string) []string { return strings.Split(s, ",") }
	r := rbacv1.PolicyRule{APIGroups: split(parts[0]), Resources: split(parts[1]), Verbs: split(parts[2])}
	if parts[0] == "" {
		r.APIGroups = []string{""}
	}
	if len(names) > 0 {
		r.ResourceNames = names
	}
	return r
}

func urls(verbs string, paths ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{Verbs: strings.Split(verbs, ","), NonResourceURLs: paths}
}

const (
	rw = "create,delete,deletecollection,get,list,patch,update,watch"
	ro = "get,list,watch"
)

var (
	viewRules = []rbacv1.PolicyRule{
		rule("|configmaps,endpoints,persistentvolumeclaims,persistentvolumeclaims/status,pods,replicationcontrollers,replicationcontrollers/scale,serviceaccounts,services,services/status|" + ro),
		rule("|bindings,events,limitranges,namespaces/status,pods/log,pods/status,replicationcontrollers/status,resourcequotas,resourcequotas/status|" + ro),
		rule("|namespaces|" + ro),
		rule("discovery.k8s.io|endpointslices|" + ro),
		rule("apps|controllerrevisions,daemonsets,daemonsets/status,deployments,deployments/scale,deployments/status,replicasets,replicasets/scale,replicasets/status,statefulsets,statefulsets/scale,statefulsets/status|" + ro),
		rule("autoscaling|horizontalpodautoscalers,horizontalpodautoscalers/status|" + ro),
		rule("batch|cronjobs,cronjobs/status,jobs,jobs/status|" + ro),
		rule("networking.k8s.io|ingresses,ingresses/status,networkpolicies|" + ro),
		rule("policy|poddisruptionbudgets,poddisruptionbudgets/status|" + ro),
	}
	editRules = append([]rbacv1.PolicyRule{
		rule("|pods/attach,pods/exec,pods/portforward,pods/proxy,secrets,services/proxy|" + ro),
		rule("|serviceaccounts|impersonate"),
		rule("|pods,pods/attach,pods/exec,pods/portforward,pods/proxy|create,delete,deletecollection,patch,update"),
		rule("|pods/eviction|create"),
		rule("|configmaps,events,persistentvolumeclaims,replicationcontrollers,replicationcontrollers/scale,secrets,serviceaccounts,services,services/proxy|create,delete,deletecollection,patch,update"),
		rule("|serviceaccounts/token|create"),
		rule("apps|daemonsets,deployments,deployments/rollback,deployments/scale,replicasets,replicasets/scale,statefulsets,statefulsets/scale|create,delete,deletecollection,patch,update"),
		rule("autoscaling|horizontalpodautoscalers|create,delete,deletecollection,patch,update"),
		rule("batch|cronjobs,jobs|create,delete,deletecollection,patch,update"),
		rule("networking.k8s.io|ingresses,networkpolicies|create,delete,deletecollection,patch,update"),
		rule("policy|poddisruptionbudgets|create,delete,deletecollection,patch,update"),
	}, viewRules...)
	adminRules = append([]rbacv1.PolicyRule{
		rule("rbac.authorization.k8s.io|rolebindings,roles|" + rw),
		rule("authorization.k8s.io|localsubjectaccessreviews|create"),
	}, editRules...)
)

func (s *Sim) rbacSeed(now time.Time) {
	day := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }
	defaults := map[string]string{"kubernetes.io/bootstrapping": "rbac-defaults"}
	helm := func(release string) map[string]string {
		return map[string]string{"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": release}
	}
	clusterRole := func(name string, labels map[string]string, age int, rules ...rbacv1.PolicyRule) *rbacv1.ClusterRole {
		r := &rbacv1.ClusterRole{TypeMeta: typeMeta("ClusterRole"), ObjectMeta: objectMeta("", name, day(age), copyLabels(labels)), Rules: rules}
		s.addLocked(r)
		return r
	}
	role := func(ns, name string, labels map[string]string, age int, rules ...rbacv1.PolicyRule) {
		s.addLocked(&rbacv1.Role{TypeMeta: typeMeta("Role"), ObjectMeta: objectMeta(ns, name, day(age), copyLabels(labels)), Rules: rules})
	}
	ref := func(kind, name string) rbacv1.RoleRef {
		return rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kind, Name: name}
	}
	crb := func(name, roleName string, labels map[string]string, age int, subjects ...rbacv1.Subject) {
		s.addLocked(&rbacv1.ClusterRoleBinding{TypeMeta: typeMeta("ClusterRoleBinding"), ObjectMeta: objectMeta("", name, day(age), copyLabels(labels)), RoleRef: ref("ClusterRole", roleName), Subjects: subjects})
	}
	rb := func(ns, name, roleKind, roleName string, labels map[string]string, age int, subjects ...rbacv1.Subject) {
		s.addLocked(&rbacv1.RoleBinding{TypeMeta: typeMeta("RoleBinding"), ObjectMeta: objectMeta(ns, name, day(age), copyLabels(labels)), RoleRef: ref(roleKind, roleName), Subjects: subjects})
	}
	group := func(n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: n}
	}
	user := func(n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: n}
	}
	sa := func(ns, n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: ns, Name: n}
	}
	serviceAccount := func(ns, name string, age int) {
		if s.objects["serviceaccounts"] != nil && s.objects["serviceaccounts"][ns+"/"+name] != nil {
			return
		}
		s.addLocked(&corev1.ServiceAccount{TypeMeta: typeMeta("ServiceAccount"), ObjectMeta: objectMeta(ns, name, day(age), nil)})
	}

	// Kubernetes' own.
	agg := func(r *rbacv1.ClusterRole, to string) {
		r.AggregationRule = &rbacv1.AggregationRule{ClusterRoleSelectors: []metav1.LabelSelector{{MatchLabels: map[string]string{"rbac.authorization.k8s.io/aggregate-to-" + to: "true"}}}}
	}
	clusterRole("cluster-admin", defaults, 214, rule("*|*|*"), urls("*", "*"))
	agg(clusterRole("admin", defaults, 214, adminRules...), "admin")
	agg(clusterRole("edit", withLabel(defaults, "rbac.authorization.k8s.io/aggregate-to-admin", "true"), 214, editRules...), "edit")
	agg(clusterRole("view", withLabel(defaults, "rbac.authorization.k8s.io/aggregate-to-edit", "true"), 214, viewRules...), "view")
	clusterRole("system:basic-user", defaults, 214, rule("authorization.k8s.io|selfsubjectaccessreviews,selfsubjectrulesreviews|create"), rule("authentication.k8s.io|selfsubjectreviews|create"))
	clusterRole("system:discovery", defaults, 214, urls("get", "/api", "/api/*", "/apis", "/apis/*", "/healthz", "/livez", "/openapi", "/openapi/*", "/readyz", "/version", "/version/"))
	clusterRole("system:public-info-viewer", defaults, 214, urls("get", "/healthz", "/livez", "/readyz", "/version", "/version/"))
	clusterRole("system:monitoring", defaults, 214, urls("get", "/healthz", "/healthz/*", "/livez", "/livez/*", "/metrics", "/metrics/slis", "/readyz", "/readyz/*"))
	clusterRole("system:auth-delegator", defaults, 214, rule("authentication.k8s.io|tokenreviews|create"), rule("authorization.k8s.io|subjectaccessreviews|create"))
	clusterRole("system:node-proxier", defaults, 214, rule("|endpoints,services|list,watch"), rule("|nodes|get,list,watch"), rule("discovery.k8s.io|endpointslices|list,watch"), rule(",events.k8s.io|events|create,patch,update"))
	clusterRole("system:kube-scheduler", defaults, 214, rule("|pods|delete,get,list,watch"), rule("|bindings,pods/binding|create"), rule("|pods/status|patch,update"), rule("|nodes,namespaces,persistentvolumeclaims,persistentvolumes,replicationcontrollers,services|get,list,watch"), rule("coordination.k8s.io|leases|create,get,update"), rule(",events.k8s.io|events|create,patch,update"))
	clusterRole("system:kube-controller-manager", defaults, 214, rule("|secrets,serviceaccounts|create"), rule("|secrets|delete"), rule("|configmaps,namespaces,secrets,serviceaccounts|get"), rule("|secrets,serviceaccounts|update"), rule("authentication.k8s.io|tokenreviews|create"), rule("authorization.k8s.io|subjectaccessreviews|create"), rule("*|*|list,watch"), rule("|serviceaccounts/token|create"), rule("coordination.k8s.io|leases|create,get,update"))
	clusterRole("system:node", defaults, 214, rule("|nodes,nodes/status|create,get,list,watch,patch,update"), rule("|pods|get,list,watch,create,delete"), rule("|pods/status|patch,update"), rule("|pods/eviction|create"), rule("|configmaps,secrets|get,list,watch"), rule("|persistentvolumeclaims,persistentvolumes|get"), rule("|serviceaccounts/token|create"), rule("coordination.k8s.io|leases|create,delete,get,patch,update"))
	controllers := map[string][]rbacv1.PolicyRule{
		"deployment-controller":              {rule("apps,extensions|deployments|get,list,update,watch"), rule("apps,extensions|deployments/status|update"), rule("apps,extensions|replicasets|create,delete,get,list,patch,update,watch"), rule("|pods|get,list,update,watch")},
		"replicaset-controller":              {rule("apps,extensions|replicasets|get,list,update,watch"), rule("apps,extensions|replicasets/status|update"), rule("|pods|create,delete,list,patch,watch")},
		"daemon-set-controller":              {rule("apps,extensions|daemonsets|get,list,watch"), rule("apps,extensions|daemonsets/status|update"), rule("|pods|create,delete,list,patch,watch"), rule("|pods/binding|create"), rule("apps|controllerrevisions|create,delete,get,list,patch,update,watch")},
		"statefulset-controller":             {rule("|pods|create,delete,get,list,patch,update,watch"), rule("apps|statefulsets|list,watch"), rule("apps|statefulsets/status|update"), rule("|persistentvolumeclaims|create,get"), rule("apps|controllerrevisions|create,delete,get,list,patch,update,watch")},
		"job-controller":                     {rule("batch|jobs|get,list,patch,update,watch"), rule("batch|jobs/status|update"), rule("|pods|create,delete,list,patch,watch")},
		"namespace-controller":               {rule("|namespaces|delete,get,list,watch"), rule("|namespaces/finalize,namespaces/status|update"), rule("*|*|delete,deletecollection,get,list")},
		"node-controller":                    {rule("|nodes|delete,get,list,patch,update"), rule("|nodes/status|patch,update"), rule("|pods/status|patch,update"), rule("|pods|delete,list")},
		"disruption-controller":              {rule("apps,extensions|deployments,replicasets,statefulsets|get,list,watch"), rule("policy|poddisruptionbudgets|get,list,watch"), rule("policy|poddisruptionbudgets/status|update"), rule("*|*/scale|get")},
		"endpointslice-controller":           {rule("|nodes,pods,services|get,list,watch"), rule("discovery.k8s.io|endpointslices|create,delete,get,list,update")},
		"generic-garbage-collector":          {rule("*|*|delete,get,list,patch,update,watch")},
		"clusterrole-aggregation-controller": {rule("rbac.authorization.k8s.io|clusterroles|escalate,get,list,patch,update,watch")},
		"persistent-volume-binder":           {rule("|persistentvolumes|create,delete,get,list,update,watch"), rule("|persistentvolumeclaims|get,list,update,watch"), rule("|pods|create,delete,get,list,watch")},
		"horizontal-pod-autoscaler":          {rule("autoscaling|horizontalpodautoscalers|get,list,watch"), rule("autoscaling|horizontalpodautoscalers/status|update"), rule("*|*/scale|get,update")},
		"service-account-controller":         {rule("|serviceaccounts|create")},
	}
	for name, rules := range controllers {
		clusterRole("system:controller:"+name, defaults, 214, append(rules, rule(",events.k8s.io|events|create,patch,update"))...)
		crb("system:controller:"+name, "system:controller:"+name, defaults, 214, sa("kube-system", name))
		serviceAccount("kube-system", name, 214)
	}
	crb("cluster-admin", "cluster-admin", defaults, 214, group("system:masters"))
	crb("system:basic-user", "system:basic-user", defaults, 214, group("system:authenticated"))
	crb("system:discovery", "system:discovery", defaults, 214, group("system:authenticated"))
	crb("system:public-info-viewer", "system:public-info-viewer", defaults, 214, group("system:authenticated"), group("system:unauthenticated"))
	crb("system:monitoring", "system:monitoring", defaults, 214, group("system:monitoring"))
	crb("system:node-proxier", "system:node-proxier", defaults, 214, user("system:kube-proxy"))
	crb("system:kube-scheduler", "system:kube-scheduler", defaults, 214, user("system:kube-scheduler"))
	crb("system:kube-controller-manager", "system:kube-controller-manager", defaults, 214, user("system:kube-controller-manager"))
	role("kube-system", "extension-apiserver-authentication-reader", defaults, 214, rule("|configmaps|get,list,watch", "extension-apiserver-authentication"))
	role("kube-system", "system:controller:bootstrap-signer", defaults, 214, rule("|secrets|get,list,watch"))
	rb("kube-system", "system:controller:bootstrap-signer", "Role", "system:controller:bootstrap-signer", defaults, 214, sa("kube-system", "bootstrap-signer"))
	serviceAccount("kube-system", "bootstrap-signer", 214)

	// kubeadm's.
	crb("kubeadm:cluster-admins", "cluster-admin", nil, 214, group("kubeadm:cluster-admins"))
	clusterRole("kubeadm:get-nodes", nil, 214, rule("|nodes|get"))
	crb("kubeadm:get-nodes", "kubeadm:get-nodes", nil, 214, group("system:bootstrappers:kubeadm:default-node-token"))
	crb("kubeadm:node-proxier", "system:node-proxier", nil, 214, sa("kube-system", "kube-proxy"))
	serviceAccount("kube-system", "kube-proxy", 214)
	role("kube-public", "kubeadm:bootstrap-signer-clusterinfo", nil, 214, rule("|configmaps|get", "cluster-info"))
	rb("kube-public", "kubeadm:bootstrap-signer-clusterinfo", "Role", "kubeadm:bootstrap-signer-clusterinfo", nil, 214, user("system:anonymous"))

	// Helm releases'.
	clusterRole("system:metrics-server", helm("metrics-server"), 120, rule("|nodes/metrics|get"), rule("|nodes,pods|get,list,watch"))
	crb("system:metrics-server", "system:metrics-server", helm("metrics-server"), 120, sa("kube-system", "metrics-server"))
	crb("metrics-server:system:auth-delegator", "system:auth-delegator", helm("metrics-server"), 120, sa("kube-system", "metrics-server"))
	rb("kube-system", "metrics-server-auth-reader", "Role", "extension-apiserver-authentication-reader", helm("metrics-server"), 120, sa("kube-system", "metrics-server"))
	serviceAccount("kube-system", "metrics-server", 120)
	clusterRole("node-agent", helm("node-agent"), 90, rule("|nodes,nodes/proxy,nodes/stats|get,list,watch"), rule("|pods|get,list,watch"))
	crb("node-agent", "node-agent", helm("node-agent"), 90, sa("kube-system", "node-agent"))
	// The monitoring exporter reads every Secret in the cluster (a
	// medium finding).
	clusterRole("exporter-reader", nil, 75, rule("|configmaps,secrets|get,list,watch"), rule("|nodes,pods,services,endpoints|get,list,watch"))
	crb("exporter-reader", "exporter-reader", nil, 75, sa("monitoring", "exporter"))
	serviceAccount("monitoring", "exporter", 75)
	for _, p := range s.pods {
		if p.Namespace == "monitoring" && strings.HasPrefix(p.Name, "exporter-") {
			p.Spec.ServiceAccountName = "exporter"
		}
	}

	// What the platform team added.
	crb("platform-admins", "cluster-admin", nil, 200, group("oidc:platform-admins"))
	// A deploy pipeline with cluster-admin and a token that never
	// expires (high).
	crb("ci-deployer-admin", "cluster-admin", nil, 187, sa("default", "ci-deployer"))
	serviceAccount("default", "ci-deployer", 187)
	s.addLocked(&corev1.Secret{TypeMeta: typeMeta("Secret"), ObjectMeta: objectMetaWith("default", "ci-deployer-token", day(187), nil, map[string]string{corev1.ServiceAccountNameKey: "ci-deployer"}), Type: corev1.SecretTypeServiceAccountToken,
		Data: map[string][]byte{"token": []byte("eyJhbGciOiJSUzI1NiJ9.ci-deployer." + PlantedSecretValue), "ca.crt": []byte(fakeCert("cluster-ca")), "namespace": []byte("default")}})
	clusterRole("node-operator", nil, 160, rule("|nodes|get,list,watch,patch,update"), rule("|pods|"+ro), rule("|pods/eviction|create"), rule("apps|daemonsets|get,list"))
	crb("sre-oncall-view", "view", nil, 160, group("oidc:sre-oncall"))
	crb("sre-oncall-node-operator", "node-operator", nil, 160, group("oidc:sre-oncall"))
	rb("monitoring", "sre-oncall-edit", "ClusterRole", "edit", nil, 160, group("oidc:sre-oncall"))
	rb("analytics", "data-engineers-edit", "ClusterRole", "edit", nil, 140, group("oidc:data-engineers"))
	rb("ingest", "data-engineers-edit", "ClusterRole", "edit", nil, 140, group("oidc:data-engineers"))
	rb("storage", "data-engineers-view", "ClusterRole", "view", nil, 140, group("oidc:data-engineers"))
	rb("analytics", "bi-analysts-view", "ClusterRole", "view", nil, 110, group("oidc:bi-analysts"))
	// A person with admin directly, not through a group (low).
	rb("web", "dana-admin", "ClusterRole", "admin", nil, 64, user("dana@corp.example"))
	// A binding for a ServiceAccount nobody created (medium).
	rb("batch", "report-jobs", "ClusterRole", "edit", nil, 52, sa("batch", "report-runner"))
	// An operator that manages roles of its own (info: it can change RBAC).
	clusterRole("queue-operator", nil, 45, rule("rbac.authorization.k8s.io|roles,rolebindings|*"), rule("queue.example.io|*|*"), rule("|pods,services,configmaps|"+rw))
	crb("queue-operator", "queue-operator", nil, 45, sa("ingest", "queue-operator"))
	serviceAccount("ingest", "queue-operator", 45)
	// Narrow grants by name.
	role("web", "tls-reader", nil, 30, rule("|secrets|get", "shop-tls"))
	rb("web", "tls-reader", "Role", "tls-reader", nil, 30, sa("web", "gateway"))
	serviceAccount("web", "gateway", 30)
}

func copyLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func withLabel(in map[string]string, k, v string) map[string]string {
	out := copyLabels(in)
	if out == nil {
		out = map[string]string{}
	}
	out[k] = v
	return out
}

func objectMetaWith(ns, name string, created time.Time, labels, annotations map[string]string) metav1.ObjectMeta {
	m := objectMeta(ns, name, created, labels)
	m.Annotations = annotations
	return m
}
