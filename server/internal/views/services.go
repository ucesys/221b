package views

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/ucesys/221b/server/internal/kube"
)

// ServiceRow is one Service as the Services tab shows it.
type ServiceRow struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
	Type      string `json:"type"`
	// Headless is true for a ClusterIP Service without a cluster IP.
	Headless  bool     `json:"headless"`
	ClusterIP string   `json:"clusterIP,omitempty"`
	Ports     []string `json:"ports"`
	// Endpoints ready, of the total the Service selects; Selector is
	// false for Services without one.
	Ready    int  `json:"ready"`
	Total    int  `json:"total"`
	Selector bool `json:"selector"`
	// External: the addresses a LoadBalancer got, or Pending with why; node
	// ports; Ingress hostnames that publish it.
	ExternalIPs []string  `json:"externalIPs"`
	Pending     bool      `json:"pending"`
	PendingWhy  string    `json:"pendingWhy,omitempty"`
	NodePorts   []int32   `json:"nodePorts"`
	Hostnames   []string  `json:"hostnames"`
	Release     string    `json:"release,omitempty"`
	Created     time.Time `json:"created"`
	Status      string    `json:"status"`
	Tone        string    `json:"tone"`
}

// BuildServiceRow computes a Service's row.
func BuildServiceRow(src Source, s *corev1.Service, now time.Time) ServiceRow {
	r := ServiceRow{
		Key: s.Namespace + "/" + s.Name, Name: s.Name, Namespace: s.Namespace, UID: string(s.UID),
		Type: string(s.Spec.Type), ClusterIP: s.Spec.ClusterIP, Ports: []string{}, ExternalIPs: []string{},
		NodePorts: []int32{}, Hostnames: []string{}, Created: s.CreationTimestamp.UTC(),
		Selector: len(s.Spec.Selector) > 0,
	}
	if s.Spec.ClusterIP == corev1.ClusterIPNone {
		r.Headless, r.ClusterIP = true, ""
	}
	if s.Spec.Type == corev1.ServiceTypeExternalName {
		r.ClusterIP = ""
		r.ExternalIPs = append(r.ExternalIPs, s.Spec.ExternalName)
	}
	for _, p := range s.Spec.Ports {
		port := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
		if p.Name != "" && len(s.Spec.Ports) > 1 {
			port = p.Name + " " + port
		}
		r.Ports = append(r.Ports, port)
		if p.NodePort != 0 {
			r.NodePorts = append(r.NodePorts, p.NodePort)
		}
	}
	for _, ing := range s.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			r.ExternalIPs = append(r.ExternalIPs, ing.IP)
		} else if ing.Hostname != "" {
			r.ExternalIPs = append(r.ExternalIPs, ing.Hostname)
		}
	}
	r.ExternalIPs = append(r.ExternalIPs, s.Spec.ExternalIPs...)
	if s.Spec.Type == corev1.ServiceTypeLoadBalancer && len(s.Status.LoadBalancer.Ingress) == 0 {
		r.Pending = true
		r.PendingWhy = pendingReason(src, s, now)
	}
	r.Ready, r.Total = endpointCounts(src, s)
	r.Hostnames = ingressHosts(src, s)
	r.Release = s.Labels["app.kubernetes.io/instance"]
	if s.Labels["app.kubernetes.io/managed-by"] != "Helm" {
		r.Release = s.Annotations["meta.helm.sh/release-name"]
	}
	switch {
	case !r.Selector:
		r.Status, r.Tone = "no selector", "muted"
	case r.Total == 0:
		r.Status, r.Tone = "no endpoints", "warn"
	case r.Ready == r.Total:
		r.Status, r.Tone = fmt.Sprintf("%d ready", r.Ready), "ok"
	case r.Ready == 0:
		r.Status, r.Tone = fmt.Sprintf("0 of %d ready", r.Total), "err"
	default:
		r.Status, r.Tone = fmt.Sprintf("%d of %d ready", r.Ready, r.Total), "warn"
	}
	if r.Pending && r.Tone != "err" {
		r.Tone = "warn"
	}
	return r
}

// pendingReason says why a LoadBalancer has no address, from the
// Service's events when they say, otherwise from how long it has waited.
func pendingReason(src Source, s *corev1.Service, now time.Time) string {
	for _, e := range eventsFor(src, string(s.UID)) {
		if e.Type == corev1.EventTypeWarning && e.Message != "" {
			return e.Reason + ": " + e.Message
		}
	}
	waited := now.Sub(s.CreationTimestamp.Time)
	if waited < 2*time.Minute {
		return "Waiting for a load balancer controller to give it an address."
	}
	return fmt.Sprintf("No load balancer controller has given it an address in %s; the cluster may have none (no MetalLB, kube-vip or cloud provider). Its node ports still work on every node.", dur(waited))
}

// endpointCounts counts the endpoints a Service's slices hold, ready and
// in total.
func endpointCounts(src Source, s *corev1.Service) (ready, total int) {
	for _, o := range src.ByIndex("endpointslices", kube.IndexSliceService, s.Namespace+"/"+s.Name) {
		slice, ok := o.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		for _, e := range slice.Endpoints {
			total++
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				ready++
			}
		}
	}
	return ready, total
}

// ingressHosts lists the hostnames of Ingresses that send traffic to the
// Service.
func ingressHosts(src Source, s *corev1.Service) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, o := range src.Objects("ingresses", s.Namespace) {
		ing, ok := o.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service != nil && p.Backend.Service.Name == s.Name && rule.Host != "" && !seen[rule.Host] {
					seen[rule.Host] = true
					out = append(out, rule.Host)
				}
			}
		}
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil && ing.Spec.DefaultBackend.Service.Name == s.Name {
			for _, rule := range ing.Spec.Rules {
				if rule.Host != "" && !seen[rule.Host] {
					seen[rule.Host] = true
					out = append(out, rule.Host)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// ServiceDetails is what the Service drawer shows.
type ServiceDetails struct {
	ServiceRow
	DNSName         string            `json:"dnsName"`
	SelectorText    []string          `json:"selectorText"`
	SessionAffinity string            `json:"sessionAffinity"`
	TrafficPolicy   string            `json:"trafficPolicy,omitempty"`
	PortDetails     []ServicePort     `json:"portDetails"`
	Endpoints       []Endpoint        `json:"endpoints"`
	Ingresses       []IngressRef      `json:"ingresses"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	Events          []Event           `json:"events"`
}

// ServicePort is one port of a Service.
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port"`
	Protocol   string `json:"protocol"`
	TargetPort string `json:"targetPort"`
	NodePort   int32  `json:"nodePort,omitempty"`
}

// Endpoint is one address behind a Service and the pod it belongs to.
type Endpoint struct {
	Address   string `json:"address"`
	Ready     bool   `json:"ready"`
	Pod       string `json:"pod,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Node      string `json:"node,omitempty"`
	PodStatus string `json:"podStatus,omitempty"`
	Tone      string `json:"tone"`
}

// IngressRef is an Ingress rule that reaches the Service.
type IngressRef struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  bool   `json:"tls"`
}

// BuildServiceDetails computes the drawer's data for a Service.
func BuildServiceDetails(src Source, s *corev1.Service, now time.Time) ServiceDetails {
	d := ServiceDetails{
		ServiceRow:      BuildServiceRow(src, s, now),
		DNSName:         fmt.Sprintf("%s.%s.svc.cluster.local", s.Name, s.Namespace),
		SelectorText:    []string{},
		SessionAffinity: string(s.Spec.SessionAffinity),
		TrafficPolicy:   string(s.Spec.ExternalTrafficPolicy),
		PortDetails:     []ServicePort{},
		Endpoints:       []Endpoint{},
		Ingresses:       []IngressRef{},
		Labels:          s.Labels,
		Annotations:     s.Annotations,
	}
	if d.Labels == nil {
		d.Labels = map[string]string{}
	}
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	for _, k := range sortedKeys(s.Spec.Selector) {
		d.SelectorText = append(d.SelectorText, k+"="+s.Spec.Selector[k])
	}
	for _, p := range s.Spec.Ports {
		d.PortDetails = append(d.PortDetails, ServicePort{Name: p.Name, Port: p.Port, Protocol: string(p.Protocol), TargetPort: p.TargetPort.String(), NodePort: p.NodePort})
	}
	for _, o := range src.ByIndex("endpointslices", kube.IndexSliceService, s.Namespace+"/"+s.Name) {
		slice, ok := o.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		for _, e := range slice.Endpoints {
			ep := Endpoint{Ready: e.Conditions.Ready == nil || *e.Conditions.Ready, Tone: "err"}
			if ep.Ready {
				ep.Tone = "ok"
			}
			if len(e.Addresses) > 0 {
				ep.Address = e.Addresses[0]
			}
			if len(slice.Ports) > 0 && slice.Ports[0].Port != nil {
				ep.Address = fmt.Sprintf("%s:%d", ep.Address, *slice.Ports[0].Port)
			}
			if e.NodeName != nil {
				ep.Node = *e.NodeName
			}
			if e.TargetRef != nil && e.TargetRef.Kind == "Pod" {
				ep.Pod, ep.Namespace = e.TargetRef.Name, e.TargetRef.Namespace
				if p, ok := src.Object("pods", e.TargetRef.Namespace, e.TargetRef.Name).(*corev1.Pod); ok {
					ep.PodStatus, _, _ = podStatus(p, now)
					if ep.Node == "" {
						ep.Node = p.Spec.NodeName
					}
				}
			}
			d.Endpoints = append(d.Endpoints, ep)
		}
	}
	sort.Slice(d.Endpoints, func(i, j int) bool { return d.Endpoints[i].Address < d.Endpoints[j].Address })
	for _, o := range src.Objects("ingresses", s.Namespace) {
		ing, ok := o.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		tlsHosts := map[string]bool{}
		for _, t := range ing.Spec.TLS {
			for _, h := range t.Hosts {
				tlsHosts[h] = true
			}
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service != nil && p.Backend.Service.Name == s.Name {
					d.Ingresses = append(d.Ingresses, IngressRef{Name: ing.Name, Host: rule.Host, Path: p.Path, TLS: tlsHosts[rule.Host]})
				}
			}
		}
	}
	d.Events = eventsFor(src, string(s.UID))
	return d
}

// PodsBehind lists the pods a Service's slices point at.
func PodsBehind(src Source, s *corev1.Service) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, o := range src.ByIndex("endpointslices", kube.IndexSliceService, s.Namespace+"/"+s.Name) {
		slice, ok := o.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		for _, e := range slice.Endpoints {
			if e.TargetRef != nil && e.TargetRef.Kind == "Pod" && !seen[e.TargetRef.Name] {
				seen[e.TargetRef.Name] = true
				out = append(out, e.TargetRef.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}
