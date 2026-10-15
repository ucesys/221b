package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/netpol"
	"github.com/ucesys/221b/server/internal/views"
)

// netCheckResult answers "can it connect?" (P4-04): for a pod, or for each
// pod behind a Service, what the NetworkPolicies on both sides decide.
type netCheckResult struct {
	Checks []views.NetCheck `json:"checks"`
	// Service is set when the destination was a Service: the port asked
	// for and what it forwards to.
	Service *netCheckService `json:"service,omitempty"`
	Allowed int              `json:"allowed"`
}

type netCheckService struct {
	Name       string `json:"name"`
	Port       int32  `json:"port"`
	PortName   string `json:"portName,omitempty"`
	TargetPort string `json:"targetPort"`
	Protocol   string `json:"protocol"`
}

// netCheck: GET netcheck?from=ns/pod&to=ns/pod&port=8080&protocol=TCP, or
// with toService=ns/name and the Service's port.
func (s *Server) netCheck(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	fromNS, fromName, ok1 := strings.Cut(q.Get("from"), "/")
	port64, err := strconv.ParseInt(q.Get("port"), 10, 32)
	if !ok1 || err != nil || port64 <= 0 || port64 > 65535 {
		jsonError(w, http.StatusBadRequest, "give from=namespace/pod, a destination and a port from 1 to 65535")
		return
	}
	proto := corev1.Protocol(strings.ToUpper(q.Get("protocol")))
	if proto == "" {
		proto = corev1.ProtocolTCP
	}
	if proto != corev1.ProtocolTCP && proto != corev1.ProtocolUDP && proto != corev1.ProtocolSCTP {
		jsonError(w, http.StatusBadRequest, "the protocol is TCP, UDP or SCTP")
		return
	}
	toSvc := q.Get("toService") != ""
	dest := q.Get("to")
	if toSvc {
		dest = q.Get("toService")
	}
	toNS, toName, ok2 := strings.Cut(dest, "/")
	if !ok2 || toName == "" {
		jsonError(w, http.StatusBadRequest, "give to=namespace/pod or toService=namespace/name")
		return
	}
	for _, ns := range []string{fromNS, toNS} {
		if !s.mayList(w, r, cl, "pods", ns) || !s.mayList(w, r, cl, "networkpolicies", ns) {
			return
		}
	}
	from, _ := cl.Object("pods", fromNS, fromName).(*corev1.Pod)
	if from == nil {
		jsonError(w, http.StatusNotFound, fmt.Sprintf("no pod %s/%s", fromNS, fromName))
		return
	}
	out := netCheckResult{Checks: []views.NetCheck{}}
	if !toSvc {
		to, _ := cl.Object("pods", toNS, toName).(*corev1.Pod)
		if to == nil {
			jsonError(w, http.StatusNotFound, fmt.Sprintf("no pod %s/%s", toNS, toName))
			return
		}
		out.Checks = append(out.Checks, views.CheckConnection(cl, from, to, netpol.Port{Number: int32(port64), Protocol: proto}))
	} else {
		if !s.mayList(w, r, cl, "services", toNS) {
			return
		}
		svc, _ := cl.Object("services", toNS, toName).(*corev1.Service)
		if svc == nil {
			jsonError(w, http.StatusNotFound, fmt.Sprintf("no Service %s/%s", toNS, toName))
			return
		}
		var sp *corev1.ServicePort
		for i := range svc.Spec.Ports {
			if svc.Spec.Ports[i].Port == int32(port64) {
				sp = &svc.Spec.Ports[i]
			}
		}
		if sp == nil {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("the Service %s/%s has no port %d", toNS, toName, port64))
			return
		}
		p := sp.Protocol
		if p == "" {
			p = corev1.ProtocolTCP
		}
		out.Service = &netCheckService{Name: toNS + "/" + toName, Port: sp.Port, PortName: sp.Name, TargetPort: sp.TargetPort.String(), Protocol: string(p)}
		for _, pod := range backends(cl, svc, sp) {
			target := targetPort(sp.TargetPort, sp.Port, pod)
			if target == 0 {
				continue
			}
			out.Checks = append(out.Checks, views.CheckConnection(cl, from, pod, netpol.Port{Number: target, Protocol: p}))
			if len(out.Checks) == 20 {
				break
			}
		}
	}
	for _, c := range out.Checks {
		if c.Allowed {
			out.Allowed++
		}
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// backends are the pods behind a Service's port, from its EndpointSlices.
func backends(cl Cluster, svc *corev1.Service, sp *corev1.ServicePort) []*corev1.Pod {
	var out []*corev1.Pod
	seen := map[string]bool{}
	for _, o := range cl.ByIndex("endpointslices", kube.IndexSliceService, svc.Namespace+"/"+svc.Name) {
		s, ok := o.(*discoveryv1.EndpointSlice)
		if !ok {
			continue
		}
		for _, e := range s.Endpoints {
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" || seen[e.TargetRef.Name] {
				continue
			}
			seen[e.TargetRef.Name] = true
			if p, _ := cl.Object("pods", svc.Namespace, e.TargetRef.Name).(*corev1.Pod); p != nil {
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// targetPort resolves a Service's targetPort on a pod: a number, a
// container port's name, or the Service port when unset.
func targetPort(tp intstr.IntOrString, port int32, pod *corev1.Pod) int32 {
	if tp.Type == intstr.Int {
		if tp.IntVal == 0 {
			return port
		}
		return tp.IntVal
	}
	for _, c := range pod.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == tp.StrVal {
				return cp.ContainerPort
			}
		}
	}
	return 0
}
