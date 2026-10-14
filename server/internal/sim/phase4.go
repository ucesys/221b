package sim

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/ucesys/221b/server/internal/kube"
)

// Phase 4's admin API: any object the console watches can be put into the
// simulated cluster or taken out of it, so the tests can stage the faults
// and the policies the analysis tabs read.
func (s *Sim) phase4Routes(m *http.ServeMux) {
	m.HandleFunc("POST /sim/objects", s.admin(s.putObject))
	m.HandleFunc("DELETE /sim/objects/{kind}/{ns}/{name}", s.admin(s.deleteObject))
}

// putObject creates or replaces an object: POST /sim/objects with its JSON
// (apiVersion and kind included). Missing UIDs and creation times are
// filled in.
func (s *Sim) putObject(r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	obj, gvk, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
	if err != nil {
		return fmt.Errorf("not a Kubernetes object: %w", err)
	}
	obj.GetObjectKind().SetGroupVersionKind(*gvk)
	k, ok := kube.KindOf(gvk.Kind)
	if !ok {
		return fmt.Errorf("the simulator does not serve %s", gvk.Kind)
	}
	m, err := meta.Accessor(obj)
	if err != nil {
		return err
	}
	if m.GetName() == "" || (k.Namespaced && m.GetNamespace() == "") {
		return fmt.Errorf("give the object a name%s", map[bool]string{true: " and a namespace"}[k.Namespaced])
	}
	key := m.GetName()
	if k.Namespaced {
		key = m.GetNamespace() + "/" + key
	}
	if m.GetUID() == "" {
		m.SetUID(uid(k.Name + "/" + key))
	}
	now := s.opts.Now()
	if ct := m.GetCreationTimestamp(); ct.IsZero() {
		m.SetCreationTimestamp(metaTime(now))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch o := obj.(type) {
	case *corev1.Pod:
		t := watch.Added
		if _, ok := s.pods[key]; ok {
			t = watch.Modified
		}
		s.putPodLocked(o, t)
	case *corev1.Node:
		t := watch.Added
		if _, ok := s.nodes[key]; ok {
			t = watch.Modified
		} else {
			s.usage[key] = [2]int64{o.Status.Allocatable.Cpu().MilliValue() / 10, o.Status.Allocatable.Memory().Value() / 10}
		}
		s.rv++
		o.ResourceVersion = strconv.FormatInt(s.rv, 10)
		s.nodes[key] = o
		s.publishLocked("nodes", t, o)
	default:
		t := watch.Added
		if s.objects[k.Name] != nil && s.objects[k.Name][key] != nil {
			t = watch.Modified
		}
		s.storeLocked(obj, t)
	}
	return nil
}

// deleteObject removes an object: DELETE /sim/objects/{kind}/{ns}/{name},
// kind as the console names it ("roles"), ns "_" for a cluster-scoped one.
func (s *Sim) deleteObject(r *http.Request) error {
	k, ok := kube.KindByName(r.PathValue("kind"))
	if !ok {
		return fmt.Errorf("no kind %s", r.PathValue("kind"))
	}
	key := r.PathValue("name")
	if k.Namespaced {
		key = r.PathValue("ns") + "/" + key
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var obj runtime.Object
	switch k.Name {
	case "pods":
		p, ok := s.pods[key]
		if !ok {
			return fmt.Errorf("no pod %s", key)
		}
		delete(s.pods, key)
		delete(s.podUsage, key)
		s.rv++
		p.ResourceVersion = strconv.FormatInt(s.rv, 10)
		s.publishLocked("pods", watch.Deleted, p)
		return nil
	case "nodes":
		n, ok := s.nodes[key]
		if !ok {
			return fmt.Errorf("no node %s", key)
		}
		delete(s.nodes, key)
		delete(s.usage, key)
		s.rv++
		n.ResourceVersion = strconv.FormatInt(s.rv, 10)
		s.publishLocked("nodes", watch.Deleted, n)
		return nil
	}
	if s.objects[k.Name] != nil {
		obj = s.objects[k.Name][key]
	}
	if obj == nil {
		return fmt.Errorf("no %s %s", k.Name, key)
	}
	s.storeLocked(obj, watch.Deleted)
	return nil
}

// oomKilled makes a pod's container one the kernel killed for running out
// of memory, now running again.
func oomKilled(p *corev1.Pod, now time.Time) {
	runReady(p, now.Add(-4*time.Minute))
	if len(p.Status.ContainerStatuses) == 0 {
		return
	}
	cs := &p.Status.ContainerStatuses[0]
	cs.RestartCount++
	cs.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		ExitCode: 137, Reason: "OOMKilled", StartedAt: metaTime(now.Add(-52 * time.Minute)), FinishedAt: metaTime(now.Add(-4 * time.Minute)),
	}}
}

func metaTime(t time.Time) metav1.Time { return metav1.NewTime(t.Truncate(time.Second)) }
