package views

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/netpol"
)

// Admission webhooks and priority classes (P4-06).

func hookKeys(src Source) []string {
	var out []string
	for _, o := range src.Objects("validatingwebhookconfigurations", "") {
		if c, ok := o.(*admissionv1.ValidatingWebhookConfiguration); ok {
			for _, w := range c.Webhooks {
				out = append(out, PolHook+":Validating/"+c.Name+"/"+w.Name)
			}
		}
	}
	for _, o := range src.Objects("mutatingwebhookconfigurations", "") {
		if c, ok := o.(*admissionv1.MutatingWebhookConfiguration); ok {
			for _, w := range c.Webhooks {
				out = append(out, PolHook+":Mutating/"+c.Name+"/"+w.Name)
			}
		}
	}
	return out
}

// HookPod is a pod behind a webhook's Service.
type HookPod struct {
	Name  string `json:"name"`
	Node  string `json:"node,omitempty"`
	Ready bool   `json:"ready"`
}

// Reach is whether the API server can reach a webhook: its Service, the
// port, and ready endpoints behind it. The console never calls webhooks.
type Reach struct {
	// State is missing, port, noEndpoints, noneReady, partial, ready or url.
	State string    `json:"state"`
	Ready int       `json:"ready"`
	Total int       `json:"total"`
	Text  string    `json:"text"`
	Tone  string    `json:"tone"`
	Pods  []HookPod `json:"pods"`
}

// HookRow is one webhook of a configuration.
type HookRow struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"`
	Config  string   `json:"config"`
	Webhook string   `json:"webhook"`
	Rules   []string `json:"rules"`
	// Service is "ns/name:port/path", or URL the webhook's URL host.
	Service          string     `json:"service,omitempty"`
	ServiceNamespace string     `json:"serviceNamespace,omitempty"`
	ServiceName      string     `json:"serviceName,omitempty"`
	URL              string     `json:"url,omitempty"`
	FailurePolicy    string     `json:"failurePolicy"`
	Timeout          int32      `json:"timeout"`
	SideEffects      string     `json:"sideEffects,omitempty"`
	MatchPolicy      string     `json:"matchPolicy,omitempty"`
	Namespaces       string     `json:"namespaces"`
	Objects          string     `json:"objects"`
	Reinvocation     string     `json:"reinvocation,omitempty"`
	CABundle         bool       `json:"caBundle"`
	Reach            Reach      `json:"reach"`
	Release          string     `json:"release,omitempty"`
	Created          *time.Time `json:"created,omitempty"`
}

func ruleWords(rs []admissionv1.RuleWithOperations) []string {
	out := []string{}
	for _, r := range rs {
		ops := make([]string, len(r.Operations))
		for i, o := range r.Operations {
			ops[i] = string(o)
		}
		groups := make([]string, len(r.APIGroups))
		for i, g := range r.APIGroups {
			if g == "" {
				g = "core"
			}
			groups[i] = g
		}
		res := strings.Join(r.Resources, ", ")
		what := res + " (" + strings.Join(groups, ", ") + ")"
		if res == "*" || res == "*/*" {
			what = strings.Join(groups, ", ")
		}
		scope := ""
		if r.Scope != nil && *r.Scope != admissionv1.AllScopes {
			scope = " · " + strings.ToLower(string(*r.Scope))
		}
		out = append(out, what+" · "+strings.Join(ops, ", ")+scope)
	}
	return out
}

func hookRow(src Source, rest string, now time.Time) (any, bool) {
	kind, rest, _ := strings.Cut(rest, "/")
	config, name, _ := strings.Cut(rest, "/")
	r := HookRow{Key: PolHook + ":" + kind + "/" + config + "/" + name, Kind: kind, Config: config, Webhook: name}
	var cc admissionv1.WebhookClientConfig
	var fail *admissionv1.FailurePolicyType
	var timeout *int32
	var side *admissionv1.SideEffectClass
	var match *admissionv1.MatchPolicyType
	found := false
	switch kind {
	case "Validating":
		c, _ := src.Object("validatingwebhookconfigurations", "", config).(*admissionv1.ValidatingWebhookConfiguration)
		if c == nil {
			return nil, false
		}
		r.Release, r.Created = objectRelease(c), created(c)
		for _, w := range c.Webhooks {
			if w.Name == name {
				found = true
				cc, fail, timeout, side, match = w.ClientConfig, w.FailurePolicy, w.TimeoutSeconds, w.SideEffects, w.MatchPolicy
				r.Rules, r.Namespaces, r.Objects = ruleWords(w.Rules), selectorScope(w.NamespaceSelector, "all namespaces"), selectorScope(w.ObjectSelector, "every object")
			}
		}
	case "Mutating":
		c, _ := src.Object("mutatingwebhookconfigurations", "", config).(*admissionv1.MutatingWebhookConfiguration)
		if c == nil {
			return nil, false
		}
		r.Release, r.Created = objectRelease(c), created(c)
		for _, w := range c.Webhooks {
			if w.Name == name {
				found = true
				cc, fail, timeout, side, match = w.ClientConfig, w.FailurePolicy, w.TimeoutSeconds, w.SideEffects, w.MatchPolicy
				r.Rules, r.Namespaces, r.Objects = ruleWords(w.Rules), selectorScope(w.NamespaceSelector, "all namespaces"), selectorScope(w.ObjectSelector, "every object")
				if w.ReinvocationPolicy != nil {
					r.Reinvocation = string(*w.ReinvocationPolicy)
				}
			}
		}
	}
	if !found {
		return nil, false
	}
	// The API's defaults for v1 webhooks.
	r.FailurePolicy, r.Timeout = string(admissionv1.Fail), 10
	if fail != nil {
		r.FailurePolicy = string(*fail)
	}
	if timeout != nil {
		r.Timeout = *timeout
	}
	if side != nil {
		r.SideEffects = string(*side)
	}
	if match != nil {
		r.MatchPolicy = string(*match)
	}
	r.CABundle = len(cc.CABundle) > 0
	if cc.URL != nil {
		r.URL = *cc.URL
		host := *cc.URL
		if u, err := url.Parse(*cc.URL); err == nil {
			host = u.Host
		}
		r.Reach = Reach{State: "url", Text: "URL " + host + ": not checked", Tone: "muted", Pods: []HookPod{}}
		return r, true
	}
	if cc.Service == nil {
		r.Reach = Reach{State: "missing", Text: "no backend configured", Tone: "err", Pods: []HookPod{}}
		return r, true
	}
	port := int32(443)
	if cc.Service.Port != nil {
		port = *cc.Service.Port
	}
	path := ""
	if cc.Service.Path != nil {
		path = *cc.Service.Path
	}
	r.ServiceNamespace, r.ServiceName = cc.Service.Namespace, cc.Service.Name
	r.Service = fmt.Sprintf("%s/%s:%d%s", cc.Service.Namespace, cc.Service.Name, port, path)
	r.Reach = reach(src, cc.Service.Namespace, cc.Service.Name, port, r.FailurePolicy == string(admissionv1.Fail))
	_ = now
	return r, true
}

func selectorScope(s *metav1.LabelSelector, all string) string {
	if s == nil || (len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0) {
		return all
	}
	return netpol.SelectorText(s)
}

// reach looks the webhook's Service up, checks it has the port, and counts
// the ready endpoints behind that port.
func reach(src Source, ns, name string, port int32, failClosed bool) Reach {
	r := Reach{Pods: []HookPod{}}
	bad := "warn"
	if failClosed {
		bad = "err"
	}
	svc, _ := src.Object("services", ns, name).(*corev1.Service)
	if svc == nil {
		r.State, r.Text, r.Tone = "missing", "Service not found", bad
		return r
	}
	var sp *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Port == port {
			sp = &svc.Spec.Ports[i]
		}
	}
	if sp == nil {
		r.State, r.Text, r.Tone = "port", fmt.Sprintf("the Service has no port %d", port), bad
		return r
	}
	seen := map[string]bool{}
	for _, o := range src.ByIndex("endpointslices", kube.IndexSliceService, ns+"/"+name) {
		s, ok := o.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		hasPort := false
		for _, p := range s.Ports {
			if p.Name != nil && *p.Name == sp.Name {
				hasPort = true
			}
		}
		if !hasPort {
			continue
		}
		for _, e := range s.Endpoints {
			key := strings.Join(e.Addresses, ",")
			if seen[key] {
				continue
			}
			seen[key] = true
			r.Total++
			ready := e.Conditions.Ready == nil || *e.Conditions.Ready
			if ready {
				r.Ready++
			}
			hp := HookPod{Ready: ready}
			if e.TargetRef != nil {
				hp.Name = e.TargetRef.Name
			} else {
				hp.Name = key
			}
			if e.NodeName != nil {
				hp.Node = *e.NodeName
			}
			r.Pods = append(r.Pods, hp)
		}
	}
	sort.Slice(r.Pods, func(i, j int) bool { return r.Pods[i].Name < r.Pods[j].Name })
	switch {
	case r.Total == 0:
		r.State, r.Text, r.Tone = "noEndpoints", "no endpoints", bad
	case r.Ready == 0:
		r.State, r.Text, r.Tone = "noneReady", fmt.Sprintf("none of %d ready", r.Total), bad
	case r.Ready < r.Total:
		r.State, r.Text, r.Tone = "partial", fmt.Sprintf("%d of %d ready", r.Ready, r.Total), "warn"
	default:
		r.State, r.Text, r.Tone = "ready", plural(r.Ready, "pod", "pods")+" ready", "ok"
		if r.Ready == 1 && failClosed {
			r.Tone = "warn"
		}
	}
	return r
}

// PriorityRow is a PriorityClass and the pods that use it.
type PriorityRow struct {
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	Value         int32      `json:"value"`
	GlobalDefault bool       `json:"globalDefault,omitempty"`
	Preemption    string     `json:"preemption"`
	Description   string     `json:"description,omitempty"`
	BuiltIn       bool       `json:"builtIn,omitempty"`
	Pods          int        `json:"pods"`
	Workloads     []string   `json:"workloads"`
	Created       *time.Time `json:"created,omitempty"`
}

func priorityRow(src Source, name string, now time.Time) (any, bool) {
	c, _ := src.Object("priorityclasses", "", name).(*schedulingv1.PriorityClass)
	if c == nil {
		return nil, false
	}
	r := PriorityRow{Key: PolPriority + ":" + name, Name: name, Value: c.Value, GlobalDefault: c.GlobalDefault, Preemption: "PreemptLowerPriority", Description: c.Description, BuiltIn: strings.HasPrefix(name, "system-"), Workloads: []string{}, Created: created(c)}
	if c.PreemptionPolicy != nil {
		r.Preemption = string(*c.PreemptionPolicy)
	}
	seen := map[string]bool{}
	for _, o := range src.Objects("pods", "") {
		p, ok := o.(*corev1.Pod)
		if !ok || !live(p) || p.Spec.PriorityClassName != name {
			continue
		}
		r.Pods++
		w := p.Namespace + "/" + workloadName(src, p)
		if !seen[w] && len(r.Workloads) < 24 {
			seen[w] = true
			r.Workloads = append(r.Workloads, w)
		}
	}
	sort.Strings(r.Workloads)
	_ = now
	return r, true
}

// NoClassRow counts the pods that run without a PriorityClass, and names
// one that waits for room, which it cannot make by preempting others.
type NoClassRow struct {
	Key     string `json:"key"`
	Pods    int    `json:"pods"`
	Waiting string `json:"waiting,omitempty"`
	// Default is the class marked globalDefault, whose value such pods
	// get instead of 0.
	Default string `json:"default,omitempty"`
}

func noClassRow(src Source) NoClassRow {
	r := NoClassRow{Key: PolNoClass + ":cluster"}
	for _, o := range src.Objects("priorityclasses", "") {
		if c, ok := o.(*schedulingv1.PriorityClass); ok && c.GlobalDefault {
			r.Default = c.Name
		}
	}
	var waiting []string
	for _, o := range src.Objects("pods", "") {
		p, ok := o.(*corev1.Pod)
		if !ok || !live(p) || p.Spec.PriorityClassName != "" {
			continue
		}
		r.Pods++
		if p.Spec.NodeName == "" {
			waiting = append(waiting, p.Namespace+"/"+p.Name)
		}
	}
	sort.Strings(waiting)
	if len(waiting) > 0 {
		r.Waiting = waiting[0]
	}
	return r
}
