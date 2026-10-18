package views

import (
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/netpol"
)

// The Networks tab (P4-02): for each Ingress host and path, the path of a
// request hop by hop (address, Ingress controller, TLS certificate,
// Ingress rule, Service, endpoints, network policy, pods), each hop with
// its state; when a hop fails, its description names the cause. Also the
// nodes' addresses and pod ranges, and facts about the cluster's network.

// Keys of the networks view.
const (
	NetRoute = "route"
	NetNode  = "node"
	NetFacts = "facts"
)

// NetworkKeys lists every row: one per (Ingress, rule, path), one per
// node, and the facts.
func NetworkKeys(src Source) []string {
	out := []string{NetFacts + ":cluster"}
	for _, o := range src.Objects("ingresses", "") {
		ing, ok := o.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		for ri, r := range ing.Spec.Rules {
			if r.HTTP == nil {
				out = append(out, fmt.Sprintf("%s:%s/%s/%d/-", NetRoute, ing.Namespace, ing.Name, ri))
				continue
			}
			for pi := range r.HTTP.Paths {
				out = append(out, fmt.Sprintf("%s:%s/%s/%d/%d", NetRoute, ing.Namespace, ing.Name, ri, pi))
			}
		}
		if ing.Spec.DefaultBackend != nil {
			out = append(out, fmt.Sprintf("%s:%s/%s/default/-", NetRoute, ing.Namespace, ing.Name))
		}
	}
	for _, o := range src.Objects("nodes", "") {
		if n, ok := o.(*corev1.Node); ok {
			out = append(out, NetNode+":"+n.Name)
		}
	}
	return out
}

// SplitNetworkKey reads a key: its prefix and the rest.
func SplitNetworkKey(key string) (prefix, rest string, ok bool) {
	prefix, rest, ok = strings.Cut(key, ":")
	return prefix, rest, ok && rest != ""
}

// BuildNetwork builds one row of the networks view.
func BuildNetwork(src Source, key string, now time.Time) (any, bool) {
	prefix, rest, ok := SplitNetworkKey(key)
	if !ok {
		return nil, false
	}
	switch prefix {
	case NetFacts:
		return netFacts(src), true
	case NetNode:
		return nodeNet(src, rest)
	case NetRoute:
		return traceRoute(src, key, rest, now)
	}
	return nil, false
}

// HopLink is what a hop's target opens.
type HopLink struct {
	// Kind is ingress, ingressclass, service, pod, secret, netpol, release or node.
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Hop is one step of a request.
type Hop struct {
	K    string   `json:"k"`
	T    string   `json:"t"`
	D    string   `json:"d"`
	S    string   `json:"s"`
	Tone string   `json:"tone"`
	Link *HopLink `json:"link,omitempty"`
}

// Health is a short state with its tone, for the routes table.
type Health struct {
	Text string `json:"text"`
	Tone string `json:"tone"`
	Tip  string `json:"tip,omitempty"`
}

// TraceRow is the path of a request to one host and path.
type TraceRow struct {
	Key       string     `json:"key"`
	Host      string     `json:"host"`
	Path      string     `json:"path"`
	PathType  string     `json:"pathType,omitempty"`
	URL       string     `json:"url"`
	Namespace string     `json:"namespace"`
	Ingress   string     `json:"ingress"`
	Class     string     `json:"class,omitempty"`
	Backend   string     `json:"backend"`
	Verdict   string     `json:"verdict"`
	Tone      string     `json:"tone"`
	Text      string     `json:"text"`
	Hops      []Hop      `json:"hops"`
	Endpoints Health     `json:"endpoints"`
	Policy    Health     `json:"policy"`
	Cert      Health     `json:"cert"`
	NotAfter  *time.Time `json:"notAfter,omitempty"`
	Created   *time.Time `json:"created,omitempty"`
}

// controllers maps an IngressClass's controller to the labels of its pods.
// Distributions rename the charts they bundle, so a controller can have
// more than one name: RKE2's are rke2-ingress-nginx and rke2-traefik.
var controllers = map[string][]labels.Set{
	"k8s.io/ingress-nginx": {
		{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"},
		{"app.kubernetes.io/name": "rke2-ingress-nginx", "app.kubernetes.io/component": "controller"},
	},
	"traefik.io/ingress-controller":  {{"app.kubernetes.io/name": "traefik"}, {"app.kubernetes.io/name": "rke2-traefik"}},
	"nginx.org/ingress-controller":   {{"app.kubernetes.io/name": "nginx-ingress"}},
	"haproxy.org/ingress-controller": {{"app.kubernetes.io/name": "kubernetes-ingress"}},
	"projectcontour.io/contour":      {{"app.kubernetes.io/name": "contour"}},
}

// cloudControllers serve Ingresses from outside the cluster.
var cloudControllers = map[string]string{
	"k8s.io/ingress-gce":        "Google Cloud load balancers",
	"ingress.k8s.aws/alb":       "AWS Application Load Balancers",
	"azure/application-gateway": "Azure Application Gateway",
}

// ingressClass finds the class that serves an Ingress: the one it names,
// or the default. With neither, a controller started with
// --watch-ingress-without-class serves it, as RKE2's ingress-nginx is:
// unclassed says so.
func ingressClass(src Source, ing *networkingv1.Ingress) (name string, cls *networkingv1.IngressClass, isDefault, unclassed bool) {
	if ing.Spec.IngressClassName != nil {
		name = *ing.Spec.IngressClassName
	} else if a := ing.Annotations["kubernetes.io/ingress.class"]; a != "" {
		name = a
	}
	if name != "" {
		cls, _ = src.Object("ingressclasses", "", name).(*networkingv1.IngressClass)
		if cls != nil {
			isDefault = cls.Annotations[networkingv1.AnnotationIsDefaultIngressClass] == "true"
		}
		return name, cls, isDefault, false
	}
	var classes []*networkingv1.IngressClass
	for _, o := range src.Objects("ingressclasses", "") {
		if c, ok := o.(*networkingv1.IngressClass); ok {
			if c.Annotations[networkingv1.AnnotationIsDefaultIngressClass] == "true" {
				return c.Name, c, true, false
			}
			classes = append(classes, c)
		}
	}
	sort.Slice(classes, func(i, j int) bool { return classes[i].Name < classes[j].Name })
	for _, c := range classes {
		for _, p := range controllerPods(src, c) {
			if watchesUnclassed(p) {
				return c.Name, c, false, true
			}
		}
	}
	return "", nil, false, false
}

// watchesUnclassed says whether an ingress-nginx pod serves Ingresses that
// name no class.
func watchesUnclassed(p *corev1.Pod) bool {
	for _, c := range p.Spec.Containers {
		for _, a := range append(append([]string{}, c.Command...), c.Args...) {
			if a == "--watch-ingress-without-class" || a == "--watch-ingress-without-class=true" {
				return true
			}
		}
	}
	return false
}

// controllerPods are the pods of an IngressClass's controller: the pods
// started with --controller-class set to it, or, for a controller that is
// started without the flag, the pods with its labels.
func controllerPods(src Source, cls *networkingv1.IngressClass) []*corev1.Pod {
	sets, ok := controllers[cls.Spec.Controller]
	if !ok {
		return nil
	}
	var out []*corev1.Pod
	for _, o := range src.Objects("pods", "") {
		p, ok := o.(*corev1.Pod)
		if !ok || !live(p) {
			continue
		}
		if class, set := controllerClass(p); set {
			if class == cls.Spec.Controller {
				out = append(out, p)
			}
			continue
		}
		for _, s := range sets {
			if labels.SelectorFromSet(s).Matches(labels.Set(p.Labels)) {
				out = append(out, p)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name })
	return out
}

// controllerClass reads a pod's --controller-class flag, which
// ingress-nginx is started with.
func controllerClass(p *corev1.Pod) (string, bool) {
	for _, c := range p.Spec.Containers {
		for _, a := range append(append([]string{}, c.Command...), c.Args...) {
			if v, ok := strings.CutPrefix(a, "--controller-class="); ok {
				return v, true
			}
		}
	}
	return "", false
}

func hostMatches(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if rest, ok := strings.CutPrefix(pattern, "*."); ok {
		_, after, found := strings.Cut(host, ".")
		return found && after == rest
	}
	return false
}

func traceRoute(src Source, key, rest string, now time.Time) (any, bool) {
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return nil, false
	}
	ns, name, ri, pi := parts[0], parts[1], parts[2], parts[3]
	ing, _ := src.Object("ingresses", ns, name).(*networkingv1.Ingress)
	if ing == nil {
		return nil, false
	}
	r := TraceRow{Key: key, Namespace: ns, Ingress: name, Hops: []Hop{}, Created: created(ing)}
	var backend *networkingv1.IngressBackend
	if ri == "default" {
		backend = ing.Spec.DefaultBackend
		r.Path = "(default backend)"
	} else {
		var i, j int
		if _, err := fmt.Sscanf(ri, "%d", &i); err != nil || i >= len(ing.Spec.Rules) {
			return nil, false
		}
		rule := ing.Spec.Rules[i]
		r.Host = rule.Host
		if pi != "-" {
			if _, err := fmt.Sscanf(pi, "%d", &j); err != nil || rule.HTTP == nil || j >= len(rule.HTTP.Paths) {
				return nil, false
			}
			p := rule.HTTP.Paths[j]
			r.Path = p.Path
			if r.Path == "" {
				r.Path = "/"
			}
			if p.PathType != nil {
				r.PathType = string(*p.PathType)
			}
			backend = &p.Backend
		}
	}
	host := r.Host
	if host == "" {
		host = "*"
	}
	// TLS: the entry that covers the host.
	var tls *networkingv1.IngressTLS
	for i := range ing.Spec.TLS {
		for _, h := range ing.Spec.TLS[i].Hosts {
			if hostMatches(h, r.Host) || (r.Host == "" && h == "") {
				tls = &ing.Spec.TLS[i]
			}
		}
		if len(ing.Spec.TLS[i].Hosts) == 0 && tls == nil {
			tls = &ing.Spec.TLS[i]
		}
	}
	scheme := "http://"
	if tls != nil {
		scheme = "https://"
	}
	r.URL = scheme + host + strings.TrimPrefix(r.Path, "(default backend)")

	// 1. Address.
	var addrs []string
	for _, a := range ing.Status.LoadBalancer.Ingress {
		if a.IP != "" {
			addrs = append(addrs, a.IP)
		} else if a.Hostname != "" {
			addrs = append(addrs, a.Hostname)
		}
	}
	if len(addrs) == 0 {
		r.Hops = append(r.Hops, Hop{K: "Address", T: host, S: "no address", Tone: "err", D: "The Ingress has no address yet: no controller has admitted it."})
	} else {
		r.Hops = append(r.Hops, Hop{K: "Address", T: host, S: plural(len(addrs), "address", "addresses"), Tone: "ok", D: fmt.Sprintf("The Ingress publishes %s; DNS for %s is kept outside the cluster.", joinList(addrs), host)})
	}

	// 2. Ingress controller.
	clsName, cls, isDefault, unclassed := ingressClass(src, ing)
	r.Class = clsName
	var ctl []*corev1.Pod
	switch {
	case clsName == "":
		r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: "no class", S: "no class", Tone: "err", D: "The Ingress names no class and none is the default."})
	case cls == nil:
		r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: clsName, S: "no class", Tone: "err", D: fmt.Sprintf("No IngressClass %s exists, so no controller serves this Ingress.", clsName)})
	default:
		link := &HopLink{Kind: "ingressclass", Name: cls.Name}
		def := ""
		if isDefault {
			def = " (default)"
		} else if unclassed {
			def = " (it serves Ingresses that name no class)"
		}
		if where, ok := cloudControllers[cls.Spec.Controller]; ok {
			r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: cls.Name, S: "outside the cluster", Tone: "ok", Link: link, D: fmt.Sprintf("%s serve class %s%s.", where, cls.Name, def)})
			break
		}
		if _, known := controllers[cls.Spec.Controller]; !known {
			r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: cls.Name, S: "not checked", Tone: "muted", Link: link, D: fmt.Sprintf("Class %s%s is served by %s, whose pods 221B doesn’t know how to find.", cls.Name, def, cls.Spec.Controller)})
			break
		}
		ctl = controllerPods(src, cls)
		ready := 0
		var notReady []string
		nodes := map[string]bool{}
		for _, p := range ctl {
			if podReady(p) {
				ready++
			} else {
				st := BuildPodRow(src, p, now).Status
				if st == "Running" {
					st = "running, not ready"
				}
				notReady = append(notReady, p.Name+": "+st)
			}
			if p.Spec.NodeName != "" {
				nodes[p.Spec.NodeName] = true
			}
		}
		owner := "pods"
		if len(ctl) > 0 {
			if o := controllerOf(src, ctl[0].Namespace, ctl[0].OwnerReferences); o != nil {
				owner = o.Kind + " " + ctl[0].Namespace + "/" + o.Name
				link = &HopLink{Kind: "pod", Namespace: ctl[0].Namespace, Name: ctl[0].Name}
			}
		}
		nodeList := make([]string, 0, len(nodes))
		for n := range nodes {
			nodeList = append(nodeList, n)
		}
		sort.Strings(nodeList)
		switch {
		case len(ctl) == 0:
			r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: cls.Name, S: "no controller", Tone: "err", Link: &HopLink{Kind: "ingressclass", Name: cls.Name}, D: fmt.Sprintf("Nothing runs the %s controller for class %s.", cls.Spec.Controller, cls.Name)})
		case ready == 0:
			r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: owner, S: fmt.Sprintf("0 of %d ready", len(ctl)), Tone: "err", Link: link, D: "No controller pod is ready: " + strings.Join(notReady, "; ") + "."})
		case ready < len(ctl):
			r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: owner, S: fmt.Sprintf("%d of %d ready", ready, len(ctl)), Tone: "warn", Link: link, D: fmt.Sprintf("%s on %s · class %s%s · %s", owner, joinList(nodeList), cls.Name, def, strings.Join(notReady, "; "))})
		default:
			r.Hops = append(r.Hops, Hop{K: "Ingress controller", T: owner, S: fmt.Sprintf("%d of %d ready", ready, len(ctl)), Tone: "ok", Link: link, D: fmt.Sprintf("%s on %s · class %s%s", owner, joinList(nodeList), cls.Name, def)})
		}
	}

	// 3. TLS certificate.
	r.Cert = Health{Text: "—", Tone: "muted"}
	switch {
	case tls == nil:
		r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: "none", S: "plain HTTP", Tone: "muted", D: fmt.Sprintf("No TLS entry covers %s, so it is served over plain HTTP, or with the controller’s default certificate.", host)})
		r.Cert = Health{Text: "plain HTTP", Tone: "muted"}
	case tls.SecretName == "":
		r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: "default", S: "controller’s", Tone: "muted", D: "The TLS entry names no Secret: the controller serves its default certificate."})
	default:
		link := &HopLink{Kind: "secret", Namespace: ns, Name: tls.SecretName}
		sec, _ := src.Object("secrets", ns, tls.SecretName).(*kube.SecretSummary)
		switch {
		case !src.SecretsRead():
			r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: "not checked", Tone: "muted", D: "221B doesn’t read Secrets here (secrets.read is off), so it can’t check the certificate."})
		case sec == nil:
			r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: "secret missing", Tone: "err", D: fmt.Sprintf("Secret %s/%s doesn’t exist, so the controller serves its default certificate.", ns, tls.SecretName)})
			r.Cert = Health{Text: "secret missing", Tone: "err"}
		case sec.Cert == nil:
			r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: "not a TLS secret", Tone: "err", Link: link, D: fmt.Sprintf("Secret %s has no tls.crt and tls.key.", tls.SecretName)})
			r.Cert = Health{Text: "not a TLS secret", Tone: "err"}
		case sec.Cert.Error != "":
			r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: "unreadable", Tone: "err", Link: link, D: fmt.Sprintf("Secret %s: %s.", tls.SecretName, sec.Cert.Error)})
			r.Cert = Health{Text: "unreadable", Tone: "err"}
		default:
			c := sec.Cert
			days := int(math.Floor(c.NotAfter.Sub(now).Hours() / 24))
			na := c.NotAfter
			r.NotAfter = &na
			covers := r.Host == ""
			for _, d := range c.DNSNames {
				if hostMatches(d, r.Host) {
					covers = true
				}
			}
			issuer := ""
			if c.Issuer != "" {
				issuer = " from " + c.Issuer
			}
			date := c.NotAfter.Format("2006-01-02")
			switch {
			case days < 0:
				r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: "expired", Tone: "err", Link: link, D: fmt.Sprintf("The certificate expired on %s.", date)})
				r.Cert = Health{Text: "expired", Tone: "err", Tip: "Valid until " + date}
			case !covers:
				r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: "doesn’t cover host", Tone: "err", Link: link, D: fmt.Sprintf("The certificate in %s covers %s, not %s.", tls.SecretName, joinList(c.DNSNames), r.Host)})
				r.Cert = Health{Text: "wrong host", Tone: "err", Tip: "Covers " + strings.Join(c.DNSNames, ", ")}
			case days < 21:
				tone := "warn"
				if days < 7 {
					tone = "err"
				}
				r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: fmt.Sprintf("%d days left", days), Tone: tone, Link: link, D: fmt.Sprintf("Certificate%s expires on %s.", issuer, date)})
				r.Cert = Health{Text: fmt.Sprintf("%d days left", days), Tone: tone, Tip: "Valid until " + date}
			default:
				r.Hops = append(r.Hops, Hop{K: "TLS certificate", T: tls.SecretName, S: fmt.Sprintf("%d days left", days), Tone: "ok", Link: link, D: fmt.Sprintf("Certificate%s for %s, valid until %s.", issuer, joinList(c.DNSNames), date)})
				r.Cert = Health{Text: fmt.Sprintf("%d days left", days), Tone: "ok", Tip: "Valid until " + date}
			}
		}
	}

	// 4. Ingress rule.
	ruleLink := &HopLink{Kind: "ingress", Namespace: ns, Name: name}
	pathText := r.Path
	if r.PathType != "" {
		pathText += " (" + r.PathType + ")"
	}
	var svcName, portText string
	var portNum int32
	var portName string
	switch {
	case backend == nil:
		r.Hops = append(r.Hops, Hop{K: "Ingress rule", T: ns + "/" + name, S: "no paths", Tone: "err", Link: ruleLink, D: fmt.Sprintf("The rule for %s has no paths, so nothing is routed.", host)})
	case backend.Service == nil:
		r.Hops = append(r.Hops, Hop{K: "Ingress rule", T: ns + "/" + name, S: "no backend", Tone: "err", Link: ruleLink, D: fmt.Sprintf("The rule for %s has no Service backend; 221B traces Services only.", pathText)})
	default:
		svcName = backend.Service.Name
		portNum, portName = backend.Service.Port.Number, backend.Service.Port.Name
		portText = fmt.Sprint(portNum)
		if portName != "" {
			portText = portName
		}
		r.Backend = svcName + ":" + portText
		r.Hops = append(r.Hops, Hop{K: "Ingress rule", T: ns + "/" + name, S: "matches", Tone: "ok", Link: ruleLink, D: fmt.Sprintf("%s %s → Service %s:%s", host, pathText, svcName, portText)})
	}

	// 5. Service, 6. endpoints, 7. network policy, 8. pods.
	r.Endpoints = Health{Text: "—", Tone: "muted"}
	r.Policy = Health{Text: "—", Tone: "muted"}
	stop := func(after string) {
		for _, k := range []string{"Service", "Endpoints", "Pods"} {
			if k == after {
				continue
			}
			found := false
			for _, h := range r.Hops {
				if h.K == k {
					found = true
				}
			}
			if !found {
				r.Hops = append(r.Hops, Hop{K: k, T: "—", S: "not checked", Tone: "muted", D: "Not checked: the request stops before it."})
			}
		}
	}
	if svcName != "" {
		svc, _ := src.Object("services", ns, svcName).(*corev1.Service)
		svcLink := &HopLink{Kind: "service", Namespace: ns, Name: svcName}
		var sp *corev1.ServicePort
		if svc != nil {
			for i := range svc.Spec.Ports {
				p := &svc.Spec.Ports[i]
				if (portName != "" && p.Name == portName) || (portName == "" && p.Port == portNum) {
					sp = p
				}
			}
		}
		switch {
		case svc == nil:
			r.Hops = append(r.Hops, Hop{K: "Service", T: ns + "/" + svcName, S: "missing", Tone: "err", D: "The Service doesn’t exist, so the controller answers 503."})
			r.Endpoints = Health{Text: "no service", Tone: "err"}
			stop("Service")
		case svc.Spec.Type == corev1.ServiceTypeExternalName:
			r.Hops = append(r.Hops, Hop{K: "Service", T: ns + "/" + svcName, S: "ExternalName", Tone: "muted", Link: svcLink, D: fmt.Sprintf("%s is an ExternalName for %s; the trace ends here.", svcName, svc.Spec.ExternalName)})
		case sp == nil:
			var have []string
			for _, p := range svc.Spec.Ports {
				have = append(have, servicePortText(p))
			}
			r.Hops = append(r.Hops, Hop{K: "Service", T: ns + "/" + svcName, S: "no such port", Tone: "err", Link: svcLink, D: fmt.Sprintf("Service %s has no port %s; it serves %s.", svcName, portText, joinList(have))})
			r.Endpoints = Health{Text: "no such port", Tone: "err"}
			stop("Service")
		default:
			r.Hops = append(r.Hops, Hop{K: "Service", T: ns + "/" + svcName, S: "port " + fmt.Sprint(sp.Port), Tone: "ok", Link: svcLink, D: fmt.Sprintf("Port %d → target port %s. The controller sends requests to the endpoint addresses, not through the cluster IP %s.", sp.Port, sp.TargetPort.String(), svc.Spec.ClusterIP)})
			eps := endpointsOf(src, svc, sp)
			r.Endpoints = epHealth(eps, svc)
			epHop := Hop{K: "Endpoints", T: fmt.Sprintf("%d of %d ready", eps.ready, eps.total), S: r.Endpoints.Text, Tone: r.Endpoints.Tone}
			switch {
			case eps.total == 0 && len(svc.Spec.Selector) == 0:
				epHop.D = "The Service has no selector and nothing fills in its endpoints, so the controller answers 503."
			case eps.total == 0:
				epHop.D = fmt.Sprintf("The selector %s matches no ready pod in %s, so the controller answers 503.", labels.SelectorFromSet(svc.Spec.Selector).String(), ns)
			case eps.ready == 0:
				epHop.D = fmt.Sprintf("None of the %d endpoints is ready, so the controller answers 503.", eps.total)
			case eps.ready < eps.total:
				epHop.D = fmt.Sprintf("%d of %d ready; requests go to the ready ones.", eps.ready, eps.total)
			default:
				epHop.D = fmt.Sprintf("%s on target port %s.", plural(eps.ready, "ready endpoint", "ready endpoints"), sp.TargetPort.String())
			}
			r.Hops = append(r.Hops, epHop)
			// 7. Network policy: from a ready controller pod to the first
			// backend pod, on its target port.
			var src0 *corev1.Pod
			for _, p := range ctl {
				if podReady(p) {
					src0 = p
					break
				}
			}
			var dst0 *corev1.Pod
			for _, p := range eps.pods {
				if podReady(p) {
					dst0 = p
					break
				}
			}
			if dst0 == nil && len(eps.pods) > 0 {
				dst0 = eps.pods[0]
			}
			if src0 != nil && dst0 != nil {
				if tp := resolveTarget(sp, dst0); tp > 0 {
					c := CheckConnection(src, src0, dst0, netpol.Port{Number: tp, Protocol: corev1.ProtocolTCP})
					r.Hops = append(r.Hops, policyHop(c, src0, ns, tp))
					switch {
					case !c.Allowed:
						r.Policy = Health{Text: "blocked", Tone: "err"}
					case c.Ingress.Isolated || c.Egress.Isolated:
						r.Policy = Health{Text: strings.Join(append(c.Egress.AllowedBy, c.Ingress.AllowedBy...), ", "), Tone: "ok"}
					default:
						r.Policy = Health{Text: "not restricted", Tone: "muted"}
					}
				}
			}
			// 8. Pods.
			var lines []string
			ready := 0
			for _, p := range eps.pods {
				node := p.Spec.NodeName
				if node == "" {
					node = "no node"
				}
				line := p.Name + " on " + node
				if podReady(p) {
					ready++
				} else {
					row := BuildPodRow(src, p, now)
					line += " — " + row.Status
					if row.StatusTip != "" {
						line += " (" + row.StatusTip + ")"
					}
				}
				lines = append(lines, line)
			}
			pods := Hop{K: "Pods", T: plural(len(eps.pods), "pod", "pods"), S: fmt.Sprintf("%d ready", ready), D: strings.Join(lines, "\n")}
			if len(eps.pods) > 0 {
				pods.Link = &HopLink{Kind: "pod", Namespace: ns, Name: eps.pods[0].Name}
			}
			switch {
			case len(eps.pods) == 0:
				pods.Tone, pods.D = "err", "No pod is behind the Service."
			case ready == 0:
				pods.Tone = "err"
			case ready < len(eps.pods):
				pods.Tone = "warn"
			default:
				pods.Tone = "ok"
			}
			r.Hops = append(r.Hops, pods)
		}
	} else {
		stop("")
	}

	// The verdict: the first failing hop names the cause.
	r.Verdict, r.Tone = "Works", "ok"
	for _, h := range r.Hops {
		if h.Tone == "err" {
			r.Verdict, r.Tone, r.Text = "Fails", "err", "Fails at "+strings.ToLower(h.K)+": "+h.D
			break
		}
	}
	if r.Tone == "ok" {
		for _, h := range r.Hops {
			if h.Tone == "warn" {
				r.Verdict, r.Tone, r.Text = "Works, degraded", "warn", h.K+": "+strings.Split(h.D, "\n")[0]
				break
			}
		}
	}
	if r.Tone == "ok" {
		r.Text = fmt.Sprintf("Requests reach %s.", r.Endpoints.Text)
	}
	return r, true
}

func servicePortText(p corev1.ServicePort) string {
	if p.Name != "" {
		return fmt.Sprintf("%d (%s)", p.Port, p.Name)
	}
	return fmt.Sprint(p.Port)
}

type endpoints struct {
	ready, total int
	pods         []*corev1.Pod
}

// endpointsOf counts a Service port's endpoints from its EndpointSlices,
// and finds their pods.
func endpointsOf(src Source, svc *corev1.Service, sp *corev1.ServicePort) endpoints {
	var out endpoints
	seen := map[string]bool{}
	for _, o := range src.ByIndex("endpointslices", kube.IndexSliceService, svc.Namespace+"/"+svc.Name) {
		s, ok := o.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		has := len(s.Ports) == 0
		for _, p := range s.Ports {
			if (p.Name == nil && sp.Name == "") || (p.Name != nil && *p.Name == sp.Name) {
				has = true
			}
		}
		if !has {
			continue
		}
		for _, e := range s.Endpoints {
			k := strings.Join(e.Addresses, ",")
			if seen[k] {
				continue
			}
			seen[k] = true
			out.total++
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				out.ready++
			}
			if e.TargetRef != nil && e.TargetRef.Kind == "Pod" {
				if p, _ := src.Object("pods", svc.Namespace, e.TargetRef.Name).(*corev1.Pod); p != nil {
					out.pods = append(out.pods, p)
				}
			}
		}
	}
	sort.Slice(out.pods, func(i, j int) bool { return out.pods[i].Name < out.pods[j].Name })
	return out
}

func epHealth(e endpoints, svc *corev1.Service) Health {
	switch {
	case e.total == 0:
		return Health{Text: "no endpoints", Tone: "err"}
	case e.ready == 0:
		return Health{Text: "none ready", Tone: "err"}
	case e.ready < e.total:
		return Health{Text: fmt.Sprintf("%d of %d ready", e.ready, e.total), Tone: "warn"}
	}
	return Health{Text: plural(e.total, "endpoint", "endpoints"), Tone: "ok"}
}

func resolveTarget(sp *corev1.ServicePort, p *corev1.Pod) int32 {
	tp := sp.TargetPort
	if tp.Type == 0 {
		if tp.IntVal == 0 {
			return sp.Port
		}
		return tp.IntVal
	}
	for _, c := range p.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == tp.StrVal {
				return cp.ContainerPort
			}
		}
	}
	return 0
}

func policyHop(c NetCheck, from *corev1.Pod, ns string, tp int32) Hop {
	who := from.Namespace + "/" + workloadNameOf(from)
	h := Hop{K: "Network policy"}
	switch {
	case c.ToHost:
		h.T, h.S, h.Tone, h.D = "none", "not restricted", "muted", "The pods use the node’s network; policies don’t apply."
	case !c.Ingress.Allowed:
		sel := c.Ingress.Selecting
		h.T, h.S, h.Tone = "no rule admits it", "blocked", "err"
		h.Link = &HopLink{Kind: "netpol", Namespace: ns, Name: sel[0]}
		h.D = fmt.Sprintf("%s %s these pods and none admits %s on TCP %d, so the connection times out and the controller answers 504.", joinList(sel), verbFor(len(sel), "selects", "select"), who, tp)
	case !c.Egress.Allowed:
		sel := c.Egress.Selecting
		h.T, h.S, h.Tone = "no rule admits it", "blocked", "err"
		h.Link = &HopLink{Kind: "netpol", Namespace: from.Namespace, Name: sel[0]}
		h.D = fmt.Sprintf("%s %s the controller’s pods and none lets them connect to these pods on TCP %d, so the controller answers 504.", joinList(sel), verbFor(len(sel), "selects", "select"), tp)
	case c.Ingress.Isolated:
		h.T, h.S, h.Tone = c.Ingress.AllowedBy[0], "allowed", "ok"
		h.Link = &HopLink{Kind: "netpol", Namespace: ns, Name: c.Ingress.AllowedBy[0]}
		h.D = fmt.Sprintf("Admits %s on TCP %d (%s).", who, tp, c.Ingress.Rules[c.Ingress.AllowedBy[0]])
	default:
		h.T, h.S, h.Tone, h.D = "none", "not restricted", "muted", "No NetworkPolicy selects these pods, so any pod may connect."
	}
	return h
}

func verbFor(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func workloadNameOf(p *corev1.Pod) string {
	for _, r := range p.OwnerReferences {
		if r.Controller != nil && *r.Controller {
			if r.Kind == "ReplicaSet" {
				if i := strings.LastIndex(r.Name, "-"); i > 0 {
					return r.Name[:i]
				}
			}
			return r.Name
		}
	}
	return p.Name
}

// NodeNetRow is a node's addresses and pod range.
type NodeNetRow struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	InternalIP  string `json:"internalIP"`
	ExternalIP  string `json:"externalIP,omitempty"`
	PodCIDR     string `json:"podCIDR"`
	Capacity    int    `json:"capacity"`
	PodIPs      int    `json:"podIPs"`
	HostNetwork int    `json:"hostNetwork"`
	Zone        string `json:"zone,omitempty"`
}

func nodeNet(src Source, name string) (any, bool) {
	n, _ := src.Object("nodes", "", name).(*corev1.Node)
	if n == nil {
		return nil, false
	}
	r := NodeNetRow{Key: NetNode + ":" + name, Name: name, Role: "worker", PodCIDR: n.Spec.PodCIDR, Zone: n.Labels[corev1.LabelTopologyZone]}
	if IsControlPlane(n.Labels) {
		r.Role = "control-plane"
	}
	for _, a := range n.Status.Addresses {
		switch a.Type {
		case corev1.NodeInternalIP:
			if r.InternalIP == "" {
				r.InternalIP = a.Address
			}
		case corev1.NodeExternalIP:
			if r.ExternalIP == "" {
				r.ExternalIP = a.Address
			}
		}
	}
	if p, err := netip.ParsePrefix(n.Spec.PodCIDR); err == nil && p.Addr().Is4() {
		r.Capacity = max(0, 1<<(32-p.Bits())-2)
	}
	for _, o := range src.ByIndex("pods", kube.IndexPodNode, name) {
		p, ok := o.(*corev1.Pod)
		if !ok || !live(p) {
			continue
		}
		if p.Spec.HostNetwork {
			r.HostNetwork++
		} else if p.Status.PodIP != "" {
			r.PodIPs++
		}
	}
	return r, true
}

// NetFactsRow is what the cluster's network is made of.
type NetFactsRow struct {
	Key string `json:"key"`
	CNI CNIRow `json:"cni"`
	// PodCIDRs are the nodes' pod ranges; ServiceIP is the API server's
	// Service address, which starts the Service range.
	PodCIDRs   []string   `json:"podCIDRs"`
	ServiceIP  string     `json:"serviceIP,omitempty"`
	ClusterIPs int        `json:"clusterIPs"`
	DNS        *NetDNS    `json:"dns,omitempty"`
	KubeProxy  string     `json:"kubeProxy,omitempty"`
	Classes    []NetClass `json:"classes"`
	PendingLBs []string   `json:"pendingLBs"`
	Policies   int        `json:"policies"`
	PolicyNS   []string   `json:"policyNamespaces"`
}

// NetDNS is the cluster DNS Service and its pods.
type NetDNS struct {
	Service string   `json:"service"`
	IP      string   `json:"ip"`
	Ready   int      `json:"ready"`
	Total   int      `json:"total"`
	Nodes   []string `json:"nodes"`
}

// NetClass is an IngressClass and its controller's pods.
type NetClass struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
	Default    bool   `json:"default,omitempty"`
	Ready      int    `json:"ready"`
	Total      int    `json:"total"`
	Hosts      int    `json:"hosts"`
}

func netFacts(src Source) NetFactsRow {
	r := NetFactsRow{Key: NetFacts + ":cluster", CNI: cniRow(src), PodCIDRs: []string{}, Classes: []NetClass{}, PendingLBs: []string{}, PolicyNS: []string{}}
	for _, o := range src.Objects("nodes", "") {
		if n, ok := o.(*corev1.Node); ok && n.Spec.PodCIDR != "" {
			r.PodCIDRs = append(r.PodCIDRs, n.Spec.PodCIDR)
		}
	}
	sort.Strings(r.PodCIDRs)
	if k, _ := src.Object("services", "default", "kubernetes").(*corev1.Service); k != nil {
		r.ServiceIP = k.Spec.ClusterIP
	}
	for _, o := range src.Objects("services", "") {
		s, ok := o.(*corev1.Service)
		if !ok {
			continue
		}
		if s.Spec.ClusterIP != "" && s.Spec.ClusterIP != corev1.ClusterIPNone {
			r.ClusterIPs++
		}
		if s.Spec.Type == corev1.ServiceTypeLoadBalancer && len(s.Status.LoadBalancer.Ingress) == 0 {
			r.PendingLBs = append(r.PendingLBs, s.Namespace+"/"+s.Name)
		}
	}
	sort.Strings(r.PendingLBs)
	if dns, _ := src.Object("services", "kube-system", "kube-dns").(*corev1.Service); dns != nil {
		d := &NetDNS{Service: "kube-system/kube-dns", IP: dns.Spec.ClusterIP, Nodes: []string{}}
		sel := labels.SelectorFromSet(dns.Spec.Selector)
		nodes := map[string]bool{}
		for _, o := range src.Objects("pods", "kube-system") {
			p, ok := o.(*corev1.Pod)
			if !ok || !live(p) || len(dns.Spec.Selector) == 0 || !sel.Matches(labels.Set(p.Labels)) {
				continue
			}
			d.Total++
			if podReady(p) {
				d.Ready++
			}
			if p.Spec.NodeName != "" && !nodes[p.Spec.NodeName] {
				nodes[p.Spec.NodeName] = true
				d.Nodes = append(d.Nodes, p.Spec.NodeName)
			}
		}
		sort.Strings(d.Nodes)
		r.DNS = d
	}
	if ds, _ := src.Object("daemonsets", "kube-system", "kube-proxy").(*appsv1.DaemonSet); ds != nil {
		r.KubeProxy = fmt.Sprintf("a DaemonSet, %d of %d ready", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
	}
	hosts := map[string]int{}
	for _, o := range src.Objects("ingresses", "") {
		if ing, ok := o.(*networkingv1.Ingress); ok {
			name, _, _, _ := ingressClass(src, ing)
			hosts[name] += len(ing.Spec.Rules)
		}
	}
	for _, o := range src.Objects("ingressclasses", "") {
		c, ok := o.(*networkingv1.IngressClass)
		if !ok {
			continue
		}
		nc := NetClass{Name: c.Name, Controller: c.Spec.Controller, Default: c.Annotations[networkingv1.AnnotationIsDefaultIngressClass] == "true", Hosts: hosts[c.Name]}
		for _, p := range controllerPods(src, c) {
			nc.Total++
			if podReady(p) {
				nc.Ready++
			}
		}
		r.Classes = append(r.Classes, nc)
	}
	sort.Slice(r.Classes, func(i, j int) bool { return r.Classes[i].Name < r.Classes[j].Name })
	nsSeen := map[string]bool{}
	for _, o := range src.Objects("networkpolicies", "") {
		if m, ok := metaOf(o); ok {
			r.Policies++
			if !nsSeen[m.GetNamespace()] {
				nsSeen[m.GetNamespace()] = true
				r.PolicyNS = append(r.PolicyNS, m.GetNamespace())
			}
		}
	}
	sort.Strings(r.PolicyNS)
	return r
}
