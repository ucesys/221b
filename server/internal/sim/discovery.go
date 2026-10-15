package sim

import (
	"net/http"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube"
)

// Discovery: the API groups and resources the simulator serves, in the
// legacy form client-go falls back to. kubectl and Helm read it to turn
// "deploy" into deployments.apps and to find subresources such as
// pods/eviction and deployments/scale (Phase 6).

// resourceNames are the short and singular names kubectl knows a resource
// by, as a real API server reports them.
var shortNames = map[string][]string{
	"pods": {"po"}, "services": {"svc"}, "namespaces": {"ns"}, "nodes": {"no"}, "deployments": {"deploy"},
	"replicasets": {"rs"}, "statefulsets": {"sts"}, "daemonsets": {"ds"}, "configmaps": {"cm"},
	"persistentvolumeclaims": {"pvc"}, "events": {"ev"}, "serviceaccounts": {"sa"}, "ingresses": {"ing"},
	"networkpolicies": {"netpol"}, "poddisruptionbudgets": {"pdb"}, "resourcequotas": {"quota"},
	"limitranges": {"limits"}, "priorityclasses": {"pc"}, "cronjobs": {"cj"}, "endpoints": {"ep"},
	"horizontalpodautoscalers": {"hpa"},
}

// allCategory is what "kubectl get all" lists.
var allCategory = map[string]bool{"pods": true, "services": true, "deployments": true, "replicasets": true, "statefulsets": true, "daemonsets": true, "jobs": true, "cronjobs": true, "horizontalpodautoscalers": true}

// scalable kinds have a scale subresource.
var scalable = map[string]bool{"deployments": true, "statefulsets": true, "replicasets": true}

func groupVersion(k kube.Kind) string { return k.APIVersion() }

// resourcesOf lists the resources of one group version.
func (s *Sim) resourcesOf(gv string) []metav1.APIResource {
	var out []metav1.APIResource
	verbs := metav1.Verbs{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"}
	for _, k := range kube.Kinds {
		if groupVersion(k) != gv {
			continue
		}
		r := metav1.APIResource{Name: k.Resource, SingularName: strings.ToLower(k.Kind), Namespaced: k.Namespaced, Kind: k.Kind, Verbs: verbs, ShortNames: shortNames[k.Resource]}
		if allCategory[k.Resource] {
			r.Categories = []string{"all"}
		}
		out = append(out, r)
		switch {
		case k.Resource == "pods":
			out = append(out,
				metav1.APIResource{Name: "pods/eviction", Namespaced: true, Group: "policy", Version: "v1", Kind: "Eviction", Verbs: metav1.Verbs{"create"}},
				metav1.APIResource{Name: "pods/log", Namespaced: true, Kind: "Pod", Verbs: metav1.Verbs{"get"}},
				metav1.APIResource{Name: "pods/status", Namespaced: true, Kind: "Pod", Verbs: metav1.Verbs{"get", "patch", "update"}})
		case k.Resource == "nodes":
			out = append(out, metav1.APIResource{Name: "nodes/proxy", Kind: "NodeProxyOptions", Verbs: metav1.Verbs{"create", "delete", "get", "patch", "update"}})
		case scalable[k.Resource]:
			out = append(out, metav1.APIResource{Name: k.Resource + "/scale", Namespaced: true, Group: "autoscaling", Version: "v1", Kind: "Scale", Verbs: metav1.Verbs{"get", "patch", "update"}})
		}
	}
	switch gv {
	case "authorization.k8s.io/v1":
		out = append(out,
			metav1.APIResource{Name: "selfsubjectaccessreviews", SingularName: "selfsubjectaccessreview", Kind: "SelfSubjectAccessReview", Verbs: metav1.Verbs{"create"}},
			metav1.APIResource{Name: "subjectaccessreviews", SingularName: "subjectaccessreview", Kind: "SubjectAccessReview", Verbs: metav1.Verbs{"create"}},
			metav1.APIResource{Name: "selfsubjectrulesreviews", SingularName: "selfsubjectrulesreview", Kind: "SelfSubjectRulesReview", Verbs: metav1.Verbs{"create"}})
	case "authentication.k8s.io/v1":
		out = append(out, metav1.APIResource{Name: "selfsubjectreviews", SingularName: "selfsubjectreview", Kind: "SelfSubjectReview", Verbs: metav1.Verbs{"create"}})
	case "metrics.k8s.io/v1beta1":
		out = append(out,
			metav1.APIResource{Name: "nodes", SingularName: "", Kind: "NodeMetrics", Verbs: metav1.Verbs{"get", "list"}},
			metav1.APIResource{Name: "pods", SingularName: "", Namespaced: true, Kind: "PodMetrics", Verbs: metav1.Verbs{"get", "list"}})
	}
	return out
}

// groups lists every API group but the core one, with its versions.
func (s *Sim) groups() []metav1.APIGroup {
	seen := map[string]bool{}
	var names []string
	add := func(g string) {
		if g != "" && !seen[g] {
			seen[g] = true
			names = append(names, g)
		}
	}
	for _, k := range kube.Kinds {
		add(k.Group)
	}
	add("authorization.k8s.io")
	add("authentication.k8s.io")
	s.mu.Lock()
	metrics := s.metrics
	s.mu.Unlock()
	if metrics {
		add("metrics.k8s.io")
	}
	sort.Strings(names)
	out := make([]metav1.APIGroup, 0, len(names))
	for _, g := range names {
		v := "v1"
		for _, k := range kube.Kinds {
			if k.Group == g {
				v = k.Version
				break
			}
		}
		if g == "metrics.k8s.io" {
			v = "v1beta1"
		}
		gv := metav1.GroupVersionForDiscovery{GroupVersion: g + "/" + v, Version: v}
		out = append(out, metav1.APIGroup{Name: g, Versions: []metav1.GroupVersionForDiscovery{gv}, PreferredVersion: gv})
	}
	return out
}

func (s *Sim) apiVersions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"},
		ServerAddressByClientCIDRs: []metav1.ServerAddressByClientCIDR{{ClientCIDR: "0.0.0.0/0", ServerAddress: "127.0.0.1:6443"}}})
}

func (s *Sim) apiGroups(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}, Groups: s.groups()})
}

func (s *Sim) coreResources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: "v1", APIResources: s.resourcesOf("v1")})
}

func (s *Sim) groupResources(w http.ResponseWriter, r *http.Request) {
	gv := r.PathValue("group") + "/" + r.PathValue("version")
	for _, g := range s.groups() {
		if g.PreferredVersion.GroupVersion == gv {
			writeJSON(w, metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gv, APIResources: s.resourcesOf(gv)})
			return
		}
	}
	s.notFound(w, r)
}

// groupFor finds the group of a "group/version" path pair, for routing.
func kindFor(group, version, resource string) (kube.Kind, bool) {
	for _, k := range kube.Kinds {
		if k.Group == group && k.Version == version && k.Resource == resource {
			return k, true
		}
	}
	return kube.Kind{}, false
}
