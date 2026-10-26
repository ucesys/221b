package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/ucesys/221b/server/internal/kube"
)

// Writes, for Watson (Phase 6): the simulator accepts what kubectl and
// Helm send a real API server — creates, patches of every kind, updates,
// deletes, evictions that respect disruption budgets, the scale
// subresource and server-side dry runs — and plays the controllers that
// react: a cordoned node gets its taint, a scaled or restarted workload
// gets new pods, an evicted pod is replaced on another node.

func (s *Sim) watsonRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api", s.api(s.apiVersions))
	m.HandleFunc("GET /api/v1", s.api(s.coreResources))
	m.HandleFunc("GET /apis", s.api(s.apiGroups))
	m.HandleFunc("GET /apis/{group}/{version}", s.api(s.groupResources))
	m.HandleFunc("GET /apis/metrics.k8s.io/v1beta1/namespaces/{ns}/pods", s.api(s.podMetrics))
	m.HandleFunc("GET /apis/metrics.k8s.io/v1beta1/nodes/{name}", s.api(s.nodeMetrics))
	m.HandleFunc("GET /apis/metrics.k8s.io/v1beta1/namespaces/{ns}/pods/{name}", s.api(s.podMetrics))
	m.HandleFunc("POST /api/v1/namespaces/{ns}/pods/{name}/eviction", s.api(s.evict))
	for _, k := range kube.Kinds {
		coll, obj := k.Path(), k.Path()+"/{name}"
		if k.Namespaced {
			coll = namespacedPath(k) + "/{ns}/" + k.Resource
			obj = coll + "/{name}"
		}
		if k.Name != "secrets" {
			m.HandleFunc("POST "+coll, s.api(s.create(k)))
			m.HandleFunc("PUT "+obj, s.api(s.update(k)))
			m.HandleFunc("DELETE "+obj, s.api(s.remove(k)))
		}
		m.HandleFunc("PATCH "+obj, s.api(s.patch(k)))
		if scalable[k.Resource] {
			m.HandleFunc("GET "+obj+"/scale", s.api(s.scale(k)))
			m.HandleFunc("PUT "+obj+"/scale", s.api(s.scale(k)))
			m.HandleFunc("PATCH "+obj+"/scale", s.api(s.scale(k)))
		}
	}
}

// ---------- field selectors ----------

// fieldsOf are the fields a field selector can match on an object, as
// the API server supports them for each kind.
func fieldsOf(obj runtime.Object) fields.Set {
	set := fields.Set{}
	if m, err := meta.Accessor(obj); err == nil {
		set["metadata.name"], set["metadata.namespace"] = m.GetName(), m.GetNamespace()
	}
	switch o := obj.(type) {
	case *corev1.Pod:
		set["spec.nodeName"], set["status.phase"] = o.Spec.NodeName, string(o.Status.Phase)
		set["spec.restartPolicy"], set["spec.schedulerName"] = string(o.Spec.RestartPolicy), o.Spec.SchedulerName
		set["spec.serviceAccountName"], set["status.podIP"] = o.Spec.ServiceAccountName, o.Status.PodIP
		set["status.nominatedNodeName"], set["spec.hostNetwork"] = o.Status.NominatedNodeName, strconv.FormatBool(o.Spec.HostNetwork)
	case *corev1.Event:
		io := o.InvolvedObject
		set["involvedObject.kind"], set["involvedObject.namespace"], set["involvedObject.name"] = io.Kind, io.Namespace, io.Name
		set["involvedObject.uid"], set["involvedObject.apiVersion"], set["involvedObject.resourceVersion"] = string(io.UID), io.APIVersion, io.ResourceVersion
		set["involvedObject.fieldPath"], set["reason"], set["type"] = io.FieldPath, o.Reason, o.Type
		set["source"], set["reportingComponent"] = o.Source.Component, o.ReportingController
	case *corev1.Node:
		set["spec.unschedulable"] = strconv.FormatBool(o.Spec.Unschedulable)
	case *corev1.Secret:
		set["type"] = string(o.Type)
	case *corev1.Namespace:
		set["status.phase"] = string(o.Status.Phase)
	}
	return set
}

// parseFields reads a fieldSelector; nil for none.
func parseFields(q string) (fields.Selector, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	sel, err := fields.ParseSelector(q)
	if err != nil {
		return nil, fmt.Errorf("invalid fieldSelector: %v", err)
	}
	if sel.Empty() {
		return nil, nil
	}
	return sel, nil
}

// ---------- objects of any kind ----------

// newTyped returns an empty object of a kind, with its type set.
func newTyped(k kube.Kind) runtime.Object {
	gvk := schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind}
	obj, err := scheme.Scheme.New(gvk)
	if err != nil {
		u := &metav1.PartialObjectMetadata{}
		u.SetGroupVersionKind(gvk)
		return u
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	return obj
}

func keyOf(k kube.Kind, r *http.Request) string {
	if k.Namespaced {
		return r.PathValue("ns") + "/" + r.PathValue("name")
	}
	return r.PathValue("name")
}

func dryRun(r *http.Request) bool {
	for _, v := range r.URL.Query()["dryRun"] {
		if v == "All" {
			return true
		}
	}
	return false
}

// deleteDryRun is dryRun for a DELETE, whose options client-go sends in
// the body.
func deleteDryRun(r *http.Request) bool {
	if dryRun(r) {
		return true
	}
	var o metav1.DeleteOptions
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&o) == nil {
		for _, v := range o.DryRun {
			if v == "All" {
				return true
			}
		}
	}
	return false
}

func notFound(w http.ResponseWriter, k kube.Kind, name string) {
	res := k.Resource
	if k.Group != "" {
		res += "." + k.Group
	}
	status(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("%s %q not found", res, name))
}

func respond(w http.ResponseWriter, code int, obj runtime.Object) {
	b, _ := json.Marshal(obj)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(b)
}

// putLocked stores an object of any kind and tells the watchers.
func (s *Sim) putLocked(k kube.Kind, obj runtime.Object, t watch.EventType) {
	m, _ := meta.Accessor(obj)
	key := m.GetName()
	if k.Namespaced {
		key = m.GetNamespace() + "/" + key
	}
	switch k.Name {
	case "nodes":
		n := obj.(*corev1.Node)
		s.rv++
		n.ResourceVersion = strconv.FormatInt(s.rv, 10)
		if t == watch.Deleted {
			delete(s.nodes, key)
			delete(s.usage, key)
		} else {
			s.nodes[key] = n
		}
		s.publishLocked("nodes", t, n)
	case "pods":
		p := obj.(*corev1.Pod)
		if t == watch.Deleted {
			delete(s.pods, key)
			delete(s.podUsage, key)
			s.rv++
			p.ResourceVersion = strconv.FormatInt(s.rv, 10)
			s.publishLocked("pods", watch.Deleted, p)
			return
		}
		s.putPodLocked(p, t)
	default:
		s.storeLocked(obj, t)
	}
}

// commitLocked checks and stores a changed object, then lets the
// simulated controllers react. With dry, nothing is stored.
func (s *Sim) commitLocked(k kube.Kind, old, next runtime.Object, dry bool) (int, error) {
	om, _ := meta.Accessor(old)
	nm, err := meta.Accessor(next)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if rv := nm.GetResourceVersion(); rv != "" && rv != om.GetResourceVersion() {
		return http.StatusConflict, fmt.Errorf("Operation cannot be fulfilled on %s %q: the object has been modified; please apply your changes to the latest version and try again", k.Resource, om.GetName())
	}
	nm.SetName(om.GetName())
	nm.SetNamespace(om.GetNamespace())
	nm.SetUID(om.GetUID())
	nm.SetCreationTimestamp(om.GetCreationTimestamp())
	nm.SetResourceVersion(om.GetResourceVersion())
	nm.SetManagedFields(nil)
	next.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind})
	if k.Name != "pods" && k.Name != "nodes" && !sameSpec(old, next) {
		nm.SetGeneration(om.GetGeneration() + 1)
	} else {
		nm.SetGeneration(om.GetGeneration())
	}
	if n, ok := next.(*corev1.Node); ok {
		// The node controller keeps the unschedulable taint in step.
		if n.Spec.Unschedulable {
			if !hasTaint(n.Spec.Taints, corev1.TaintNodeUnschedulable) {
				added := metav1.NewTime(s.opts.Now().Truncate(time.Second))
				n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule, TimeAdded: &added})
			}
		} else {
			n.Spec.Taints = removeTaint(n.Spec.Taints, corev1.TaintNodeUnschedulable)
		}
	}
	if dry {
		return http.StatusOK, nil
	}
	s.putLocked(k, next, watch.Modified)
	s.reactLocked(k, old, next)
	return http.StatusOK, nil
}

// sameSpec compares the spec of two objects, which a generation counts.
func sameSpec(a, b runtime.Object) bool {
	spec := func(o runtime.Object) string {
		raw, _ := json.Marshal(o)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		return string(m["spec"]) + string(m["data"]) + string(m["rules"]) + string(m["roleRef"]) + string(m["subjects"])
	}
	return spec(a) == spec(b)
}

func hasTaint(ts []corev1.Taint, key string) bool {
	for _, t := range ts {
		if t.Key == key {
			return true
		}
	}
	return false
}

func (s *Sim) create(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		obj := newTyped(k)
		if err := decodeBody(r, obj); err != nil {
			status(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		m, _ := meta.Accessor(obj)
		if k.Namespaced {
			if ns := m.GetNamespace(); ns != "" && ns != r.PathValue("ns") {
				status(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, "the namespace of the provided object does not match the namespace sent on the request")
				return
			}
			m.SetNamespace(r.PathValue("ns"))
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if m.GetName() == "" && m.GetGenerateName() != "" {
			m.SetName(m.GetGenerateName() + s.suffixLocked())
		}
		if m.GetName() == "" {
			status(w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, k.Kind+" is invalid: metadata.name: Required value: name or generateName is required")
			return
		}
		key := m.GetName()
		if k.Namespaced {
			key = m.GetNamespace() + "/" + key
		}
		if s.objectLocked(k.Name, key) != nil {
			res := k.Resource
			if k.Group != "" {
				res += "." + k.Group
			}
			status(w, http.StatusConflict, metav1.StatusReasonAlreadyExists, fmt.Sprintf("%s %q already exists", res, m.GetName()))
			return
		}
		obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind})
		m.SetUID(uid(k.Name + "/" + key + "/" + strconv.FormatInt(s.rv, 10)))
		m.SetCreationTimestamp(metav1.NewTime(s.opts.Now().UTC().Truncate(time.Second)))
		m.SetResourceVersion("")
		m.SetManagedFields(nil)
		if k.Name != "pods" && k.Name != "nodes" {
			m.SetGeneration(1)
		}
		if dryRun(r) {
			respond(w, http.StatusCreated, obj)
			return
		}
		s.putLocked(k, obj, watch.Added)
		s.reactLocked(k, nil, obj)
		respond(w, http.StatusCreated, obj)
	}
}

func decodeBody(r *http.Request, into runtime.Object) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	if strings.Contains(r.Header.Get("Content-Type"), "yaml") {
		if body, err = yaml.YAMLToJSON(body); err != nil {
			return err
		}
	}
	if bytes.HasPrefix(body, []byte("k8s\x00")) {
		// Protobuf, as a clientset sends built-in kinds.
		if _, _, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, into); err != nil {
			return fmt.Errorf("the object could not be read: %v", err)
		}
		return nil
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("the object could not be read: %v", err)
	}
	return nil
}

func (s *Sim) update(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next := newTyped(k)
		if err := decodeBody(r, next); err != nil {
			status(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		old := s.objectLocked(k.Name, keyOf(k, r))
		if old == nil {
			notFound(w, k, r.PathValue("name"))
			return
		}
		if code, err := s.commitLocked(k, old, next, dryRun(r)); err != nil {
			status(w, code, reasonOf(code), err.Error())
			return
		}
		respond(w, http.StatusOK, next)
	}
}

func reasonOf(code int) metav1.StatusReason {
	switch code {
	case http.StatusConflict:
		return metav1.StatusReasonConflict
	case http.StatusUnprocessableEntity:
		return metav1.StatusReasonInvalid
	case http.StatusNotFound:
		return metav1.StatusReasonNotFound
	}
	return metav1.StatusReasonBadRequest
}

func (s *Sim) patch(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			status(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		ct := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
		s.mu.Lock()
		defer s.mu.Unlock()
		old := s.objectLocked(k.Name, keyOf(k, r))
		if old == nil {
			notFound(w, k, r.PathValue("name"))
			return
		}
		cur, _ := json.Marshal(old)
		var out []byte
		switch types.PatchType(ct) {
		case types.JSONPatchType:
			var p jsonpatch.Patch
			if p, err = jsonpatch.DecodePatch(body); err == nil {
				out, err = p.Apply(cur)
			}
		case types.MergePatchType:
			out, err = jsonpatch.MergePatch(cur, body)
		case types.StrategicMergePatchType:
			out, err = strategicpatch.StrategicMergePatch(cur, body, newTyped(k))
		case types.ApplyYAMLPatchType:
			// Server-side apply, approximately: the applied fields win.
			var j []byte
			if j, err = yaml.YAMLToJSON(body); err == nil {
				out, err = jsonpatch.MergePatch(cur, j)
			}
		default:
			status(w, http.StatusUnsupportedMediaType, metav1.StatusReasonUnsupportedMediaType, "the body of the request was in an unknown format - accepted media types include: application/json-patch+json, application/merge-patch+json, application/apply-patch+yaml, application/strategic-merge-patch+json")
			return
		}
		if err != nil {
			status(w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, "the patch could not be applied: "+err.Error())
			return
		}
		next := newTyped(k)
		if err := json.Unmarshal(out, next); err != nil {
			status(w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, err.Error())
			return
		}
		// A patch does not carry the resource version unless it means to.
		if m, err := meta.Accessor(next); err == nil && !strings.Contains(string(body), "resourceVersion") {
			m.SetResourceVersion("")
		}
		if code, err := s.commitLocked(k, old, next, dryRun(r)); err != nil {
			status(w, code, reasonOf(code), err.Error())
			return
		}
		respond(w, http.StatusOK, next)
	}
}

func (s *Sim) remove(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		old := s.objectLocked(k.Name, keyOf(k, r))
		if old == nil {
			notFound(w, k, r.PathValue("name"))
			return
		}
		m, _ := meta.Accessor(old)
		st := &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess,
			Details: &metav1.StatusDetails{Name: m.GetName(), Group: k.Group, Kind: k.Resource, UID: m.GetUID()}}
		if deleteDryRun(r) {
			respond(w, http.StatusOK, st)
			return
		}
		s.putLocked(k, old, watch.Deleted)
		s.reactLocked(k, old, nil)
		respond(w, http.StatusOK, st)
	}
}

// ---------- evictions ----------

func (s *Sim) evict(w http.ResponseWriter, r *http.Request) {
	var ev policyv1.Eviction
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&ev)
	ns, name := r.PathValue("ns"), r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pods[ns+"/"+name]
	if !ok {
		status(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("pods %q not found", name))
		return
	}
	for _, o := range s.objectsLocked("poddisruptionbudgets", ns, nil, nil) {
		b := o.(*policyv1.PodDisruptionBudget)
		sel, err := metav1.LabelSelectorAsSelector(b.Spec.Selector)
		if err != nil || sel.Empty() || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		if b.Status.DisruptionsAllowed < 1 {
			st := metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusFailure, Code: http.StatusTooManyRequests,
				Reason: metav1.StatusReasonTooManyRequests, Message: "Cannot evict pod as it would violate the pod's disruption budget.",
				Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: "DisruptionBudget", Message: fmt.Sprintf("The disruption budget %s needs %d healthy pods and has %d currently", b.Name, b.Status.DesiredHealthy, b.Status.CurrentHealthy)}}}}
			respond(w, http.StatusTooManyRequests, &st)
			return
		}
	}
	ok201 := &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusSuccess, Code: http.StatusCreated}
	evDry := ev.DeleteOptions != nil && len(ev.DeleteOptions.DryRun) > 0 && ev.DeleteOptions.DryRun[0] == "All"
	if dryRun(r) || evDry {
		respond(w, http.StatusCreated, ok201)
		return
	}
	pods, _ := kube.KindByName("pods")
	s.putLocked(pods, p, watch.Deleted)
	s.reactLocked(pods, p, nil)
	respond(w, http.StatusCreated, ok201)
}

// ---------- the scale subresource ----------

func (s *Sim) scale(k kube.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		old := s.objectLocked(k.Name, keyOf(k, r))
		if old == nil {
			notFound(w, k, r.PathValue("name"))
			return
		}
		cur := scaleOf(old)
		if r.Method == http.MethodGet {
			respond(w, http.StatusOK, cur)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		curJSON, _ := json.Marshal(cur)
		var out []byte
		var err error
		switch {
		case r.Method == http.MethodPut:
			out = body
		case strings.HasPrefix(r.Header.Get("Content-Type"), string(types.JSONPatchType)):
			var p jsonpatch.Patch
			if p, err = jsonpatch.DecodePatch(body); err == nil {
				out, err = p.Apply(curJSON)
			}
		default:
			out, err = jsonpatch.MergePatch(curJSON, body)
		}
		var want autoscalingv1.Scale
		if err == nil {
			err = json.Unmarshal(out, &want)
		}
		if err != nil {
			status(w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, err.Error())
			return
		}
		if want.ResourceVersion != "" && want.ResourceVersion != cur.ResourceVersion {
			status(w, http.StatusConflict, metav1.StatusReasonConflict, fmt.Sprintf("Operation cannot be fulfilled on %s.%s %q: the object has been modified; please apply your changes to the latest version and try again", k.Resource, k.Group, cur.Name))
			return
		}
		next := old.DeepCopyObject()
		n := want.Spec.Replicas
		switch o := next.(type) {
		case *appsv1.Deployment:
			o.Spec.Replicas = &n
		case *appsv1.StatefulSet:
			o.Spec.Replicas = &n
		case *appsv1.ReplicaSet:
			o.Spec.Replicas = &n
		}
		if code, err := s.commitLocked(k, old, next, dryRun(r)); err != nil {
			status(w, code, reasonOf(code), err.Error())
			return
		}
		respond(w, http.StatusOK, scaleOf(next))
	}
}

func scaleOf(obj runtime.Object) *autoscalingv1.Scale {
	m, _ := meta.Accessor(obj)
	sc := &autoscalingv1.Scale{TypeMeta: metav1.TypeMeta{Kind: "Scale", APIVersion: "autoscaling/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: m.GetName(), Namespace: m.GetNamespace(), UID: m.GetUID(), ResourceVersion: m.GetResourceVersion(), CreationTimestamp: m.GetCreationTimestamp()}}
	switch o := obj.(type) {
	case *appsv1.Deployment:
		sc.Spec.Replicas, sc.Status.Replicas, sc.Status.Selector = int32Of(o.Spec.Replicas, 1), o.Status.Replicas, metav1.FormatLabelSelector(o.Spec.Selector)
	case *appsv1.StatefulSet:
		sc.Spec.Replicas, sc.Status.Replicas, sc.Status.Selector = int32Of(o.Spec.Replicas, 1), o.Status.Replicas, metav1.FormatLabelSelector(o.Spec.Selector)
	case *appsv1.ReplicaSet:
		sc.Spec.Replicas, sc.Status.Replicas, sc.Status.Selector = int32Of(o.Spec.Replicas, 1), o.Status.Replicas, metav1.FormatLabelSelector(o.Spec.Selector)
	}
	return sc
}

// ---------- the simulated controllers ----------

func (s *Sim) suffixLocked() string {
	b := make([]byte, 5)
	for i := range b {
		b[i] = suffixChars[s.rnd.Intn(len(suffixChars))]
	}
	return string(b)
}

// reactLocked plays the controllers after a change: old is nil for a
// create, next nil for a delete.
func (s *Sim) reactLocked(k kube.Kind, old, next runtime.Object) {
	s.toolboxReactLocked(k.Name, old, next)
	switch k.Name {
	case "deployments":
		// A new Deployment gets its first ReplicaSet; a deleted one takes
		// its ReplicaSets and their pods with it, as the garbage collector would.
		if d, ok := next.(*appsv1.Deployment); ok && old == nil {
			s.rolloutLocked(d)
			s.syncStatusLocked(d.Namespace)
		}
		if od, ok := old.(*appsv1.Deployment); ok && next == nil {
			for _, rs := range s.replicaSetsLocked(od) {
				for _, p := range s.ownedPodsLocked(rs.Namespace, rs.UID) {
					s.putLocked(mustKind("pods"), p, watch.Deleted)
				}
				s.putLocked(mustKind("replicasets"), rs, watch.Deleted)
			}
			s.syncStatusLocked(od.Namespace)
		}
		if d, ok := next.(*appsv1.Deployment); ok && old != nil {
			od := old.(*appsv1.Deployment)
			if !equality.Semantic.DeepEqual(od.Spec.Template, d.Spec.Template) {
				s.rolloutLocked(d)
			} else if int32Of(od.Spec.Replicas, 1) != int32Of(d.Spec.Replicas, 1) {
				if rs := s.currentRSLocked(d); rs != nil {
					cp := rs.DeepCopy()
					cp.Spec.Replicas = d.Spec.Replicas
					s.putLocked(mustKind("replicasets"), cp, watch.Modified)
					s.reconcileRSLocked(cp)
				}
			}
			s.syncStatusLocked(d.Namespace)
		}
	case "replicasets":
		if rs, ok := next.(*appsv1.ReplicaSet); ok && old != nil && int32Of(old.(*appsv1.ReplicaSet).Spec.Replicas, 1) != int32Of(rs.Spec.Replicas, 1) {
			s.reconcileRSLocked(rs)
			s.syncStatusLocked(rs.Namespace)
		}
	case "statefulsets":
		if st, ok := next.(*appsv1.StatefulSet); ok && old != nil {
			os := old.(*appsv1.StatefulSet)
			if !equality.Semantic.DeepEqual(os.Spec.Template, st.Spec.Template) {
				for _, p := range s.ownedPodsLocked(st.Namespace, st.UID) {
					s.recreateLocked(p, st.Spec.Template, p.Spec.NodeName)
				}
			}
			s.reconcileSTSLocked(st)
			s.syncStatusLocked(st.Namespace)
		}
	case "daemonsets":
		if ds, ok := next.(*appsv1.DaemonSet); ok && old != nil && !equality.Semantic.DeepEqual(old.(*appsv1.DaemonSet).Spec.Template, ds.Spec.Template) {
			for _, p := range s.ownedPodsLocked(ds.Namespace, ds.UID) {
				s.recreateLocked(p, ds.Spec.Template, p.Spec.NodeName)
			}
			s.syncStatusLocked(ds.Namespace)
		}
	case "pods":
		if next == nil && old != nil {
			s.replaceLocked(old.(*corev1.Pod))
			s.syncStatusLocked(old.(*corev1.Pod).Namespace)
		}
	case "persistentvolumeclaims":
		s.reactClaimLocked(old, next)
	}
}

// reactClaimLocked does what the volume controllers do (ST-18): a bound
// claim asked for more grows when its class allows expansion, its volume
// with it; a deleted claim's volume is Released, or deleted with the claim
// under the Delete reclaim policy.
func (s *Sim) reactClaimLocked(old, next runtime.Object) {
	pvKind := mustKind("persistentvolumes")
	if c, ok := next.(*corev1.PersistentVolumeClaim); ok && old != nil && c.Status.Phase == corev1.ClaimBound {
		want, have := c.Spec.Resources.Requests[corev1.ResourceStorage], c.Status.Capacity[corev1.ResourceStorage]
		cls, _ := s.objectLocked("storageclasses", ptr.Deref(c.Spec.StorageClassName, "")).(*storagev1.StorageClass)
		if want.Cmp(have) > 0 && cls != nil && cls.AllowVolumeExpansion != nil && *cls.AllowVolumeExpansion {
			cp := c.DeepCopy()
			cp.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: want}
			s.putLocked(mustKind("persistentvolumeclaims"), cp, watch.Modified)
			if pv, ok := s.objectLocked("persistentvolumes", c.Spec.VolumeName).(*corev1.PersistentVolume); ok {
				pcp := pv.DeepCopy()
				pcp.Spec.Capacity = corev1.ResourceList{corev1.ResourceStorage: want}
				s.putLocked(pvKind, pcp, watch.Modified)
			}
			if u, ok := s.volumeUsage[c.Namespace+"/"+c.Name]; ok {
				u[1] = want.Value() * 98 / 100
				s.volumeUsage[c.Namespace+"/"+c.Name] = u
			}
		}
	}
	if c, ok := old.(*corev1.PersistentVolumeClaim); ok && next == nil && c.Spec.VolumeName != "" {
		pv, ok := s.objectLocked("persistentvolumes", c.Spec.VolumeName).(*corev1.PersistentVolume)
		if !ok {
			return
		}
		if pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
			s.putLocked(pvKind, pv, watch.Deleted)
			return
		}
		cp := pv.DeepCopy()
		now := metav1.NewTime(s.opts.Now())
		cp.Status = corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased, LastPhaseTransitionTime: &now}
		s.putLocked(pvKind, cp, watch.Modified)
		delete(s.volumeUsage, c.Namespace+"/"+c.Name)
	}
}

func mustKind(name string) kube.Kind {
	k, ok := kube.KindByName(name)
	if !ok {
		panic("sim: no kind " + name)
	}
	return k
}

func controllerOf(m metav1.Object) *metav1.OwnerReference {
	for i, o := range m.GetOwnerReferences() {
		if o.Controller != nil && *o.Controller {
			return &m.GetOwnerReferences()[i]
		}
	}
	return nil
}

// ownedPodsLocked lists the pods an owner controls, by name.
func (s *Sim) ownedPodsLocked(ns string, owner types.UID) []*corev1.Pod {
	var out []*corev1.Pod
	for _, o := range s.objectsLocked("pods", ns, nil, nil) {
		p := o.(*corev1.Pod)
		if c := controllerOf(p); c != nil && c.UID == owner {
			out = append(out, p)
		}
	}
	return out
}

// replicaSetsLocked lists a Deployment's ReplicaSets.
func (s *Sim) replicaSetsLocked(d *appsv1.Deployment) []*appsv1.ReplicaSet {
	var out []*appsv1.ReplicaSet
	for _, o := range s.objectsLocked("replicasets", d.Namespace, nil, nil) {
		rs := o.(*appsv1.ReplicaSet)
		if c := controllerOf(rs); c != nil && c.UID == d.UID {
			out = append(out, rs)
		}
	}
	return out
}

func revisionOf(m metav1.Object) int {
	n, _ := strconv.Atoi(m.GetAnnotations()["deployment.kubernetes.io/revision"])
	return n
}

// currentRSLocked is the ReplicaSet of a Deployment's newest revision.
func (s *Sim) currentRSLocked(d *appsv1.Deployment) *appsv1.ReplicaSet {
	var cur *appsv1.ReplicaSet
	for _, rs := range s.replicaSetsLocked(d) {
		if cur == nil || revisionOf(rs) > revisionOf(cur) {
			cur = rs
		}
	}
	return cur
}

// sameTemplate compares pod templates as the deployment controller does,
// without the pod-template-hash label.
func sameTemplate(a, b corev1.PodTemplateSpec) bool {
	a, b = *a.DeepCopy(), *b.DeepCopy()
	delete(a.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	delete(b.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return equality.Semantic.DeepEqual(a, b)
}

// rolloutLocked moves a Deployment to its new template: the ReplicaSet
// that has it (an older one, for a rollback) or a new one takes the next
// revision and all the replicas, and the old pods are replaced.
func (s *Sim) rolloutLocked(d *appsv1.Deployment) {
	rsKind := mustKind("replicasets")
	sets := s.replicaSetsLocked(d)
	maxRev := 0
	var target *appsv1.ReplicaSet
	for _, rs := range sets {
		maxRev = max(maxRev, revisionOf(rs))
		if sameTemplate(rs.Spec.Template, d.Spec.Template) {
			target = rs
		}
	}
	replicas := int32Of(d.Spec.Replicas, 1)
	rev := strconv.Itoa(maxRev + 1)
	if target == nil {
		hash := strings.ReplaceAll(string(uid(fmt.Sprintf("%s/%s/%d/%d", d.Namespace, d.Name, maxRev+1, s.rv))), "-", "")[:10]
		hash = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return rune(suffixChars[int(r)%len(suffixChars)])
		}, hash)
		tmpl := *d.Spec.Template.DeepCopy()
		if tmpl.Labels == nil {
			tmpl.Labels = map[string]string{}
		}
		tmpl.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = hash
		sel := d.Spec.Selector.DeepCopy()
		if sel == nil {
			// A Deployment the API server would refuse; the simulator takes
			// it and selects its template's labels.
			sel = &metav1.LabelSelector{MatchLabels: copyLabels(d.Spec.Template.Labels)}
		}
		if sel.MatchLabels == nil {
			sel.MatchLabels = map[string]string{}
		}
		sel.MatchLabels[appsv1.DefaultDeploymentUniqueLabelKey] = hash
		ctrl := true
		target = &appsv1.ReplicaSet{TypeMeta: typeMeta("ReplicaSet"), ObjectMeta: objectMeta(d.Namespace, d.Name+"-"+hash, s.opts.Now(), copyLabels(tmpl.Labels)),
			Spec: appsv1.ReplicaSetSpec{Replicas: &replicas, Selector: sel, Template: tmpl}}
		target.UID = uid("rs/" + d.Namespace + "/" + target.Name + "/" + strconv.FormatInt(s.rv, 10))
		target.Generation = 1
		target.Annotations = map[string]string{"deployment.kubernetes.io/revision": rev}
		target.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: &ctrl, BlockOwnerDeletion: &ctrl}}
		s.putLocked(rsKind, target, watch.Added)
	} else {
		cp := target.DeepCopy()
		cp.Spec.Replicas = &replicas
		if cp.Annotations == nil {
			cp.Annotations = map[string]string{}
		}
		if revisionOf(cp) != maxRev {
			cp.Annotations["deployment.kubernetes.io/revision"] = rev
		} else {
			rev = strconv.Itoa(maxRev)
		}
		cp.Generation++
		s.putLocked(rsKind, cp, watch.Modified)
		target = cp
	}
	for _, rs := range sets {
		if rs.UID == target.UID || int32Of(rs.Spec.Replicas, 1) == 0 {
			continue
		}
		cp := rs.DeepCopy()
		zero := int32(0)
		cp.Spec.Replicas = &zero
		cp.Generation++
		s.putLocked(rsKind, cp, watch.Modified)
		for _, p := range s.ownedPodsLocked(rs.Namespace, rs.UID) {
			s.putLocked(mustKind("pods"), p, watch.Deleted)
		}
	}
	// The Deployment records the revision it now runs.
	if cur, ok := s.objects["deployments"][d.Namespace+"/"+d.Name].(*appsv1.Deployment); ok {
		cp := cur.DeepCopy()
		if cp.Annotations == nil {
			cp.Annotations = map[string]string{}
		}
		cp.Annotations["deployment.kubernetes.io/revision"] = rev
		s.putLocked(mustKind("deployments"), cp, watch.Modified)
	}
	s.reconcileRSLocked(target)
}

// reconcileRSLocked gives a ReplicaSet as many pods as it wants.
func (s *Sim) reconcileRSLocked(rs *appsv1.ReplicaSet) {
	pods := s.ownedPodsLocked(rs.Namespace, rs.UID)
	want := int(int32Of(rs.Spec.Replicas, 1))
	sort.Slice(pods, func(i, j int) bool { return pods[i].CreationTimestamp.After(pods[j].CreationTimestamp.Time) })
	for len(pods) > want {
		s.putLocked(mustKind("pods"), pods[0], watch.Deleted)
		pods = pods[1:]
	}
	for i := len(pods); i < want; i++ {
		s.startPodLocked(rs.Namespace, rs.Name+"-"+s.suffixLocked(), rs.Spec.Template, ownerRef("ReplicaSet", rs.Name, rs.UID), "")
	}
}

// reconcileSTSLocked gives a StatefulSet pods 0 to replicas-1.
func (s *Sim) reconcileSTSLocked(st *appsv1.StatefulSet) {
	want := int(int32Of(st.Spec.Replicas, 1))
	for _, p := range s.ownedPodsLocked(st.Namespace, st.UID) {
		i, err := strconv.Atoi(strings.TrimPrefix(p.Name, st.Name+"-"))
		if err == nil && i >= want {
			s.putLocked(mustKind("pods"), p, watch.Deleted)
		}
	}
	for i := 0; i < want; i++ {
		name := fmt.Sprintf("%s-%d", st.Name, i)
		if _, ok := s.pods[st.Namespace+"/"+name]; !ok {
			s.startPodLocked(st.Namespace, name, st.Spec.Template, ownerRef("StatefulSet", st.Name, st.UID), "")
		}
	}
}

func ownerRef(kind, name string, id types.UID) metav1.OwnerReference {
	ctrl := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, UID: id, Controller: &ctrl, BlockOwnerDeletion: &ctrl}
}

// replaceLocked has a deleted pod's controller replace it.
func (s *Sim) replaceLocked(p *corev1.Pod) {
	c := controllerOf(p)
	if c == nil {
		return
	}
	switch c.Kind {
	case "ReplicaSet":
		if rs, ok := s.objects["replicasets"][p.Namespace+"/"+c.Name].(*appsv1.ReplicaSet); ok {
			s.reconcileRSLocked(rs)
		}
	case "StatefulSet":
		if st, ok := s.objects["statefulsets"][p.Namespace+"/"+c.Name].(*appsv1.StatefulSet); ok {
			s.reconcileSTSLocked(st)
		}
	case "DaemonSet":
		if ds, ok := s.objects["daemonsets"][p.Namespace+"/"+c.Name].(*appsv1.DaemonSet); ok {
			if _, ok := s.nodes[p.Spec.NodeName]; ok {
				s.startPodLocked(p.Namespace, ds.Name+"-"+s.suffixLocked(), ds.Spec.Template, ownerRef("DaemonSet", ds.Name, ds.UID), p.Spec.NodeName)
			}
		}
	}
}

// recreateLocked replaces one pod with one from the template, keeping
// its name when its controller names pods (StatefulSets).
func (s *Sim) recreateLocked(p *corev1.Pod, tmpl corev1.PodTemplateSpec, node string) {
	s.putLocked(mustKind("pods"), p, watch.Deleted)
	c := controllerOf(p)
	name := p.Name
	if c != nil && c.Kind != "StatefulSet" {
		base := c.Name
		if c.Kind == "ReplicaSet" {
			base = strings.TrimSuffix(p.Name, p.Name[strings.LastIndex(p.Name, "-"):])
		}
		name = base + "-" + s.suffixLocked()
	}
	owner := metav1.OwnerReference{}
	if c != nil {
		owner = *c
	}
	if c == nil || c.Kind != "DaemonSet" {
		node = ""
	}
	s.startPodLocked(p.Namespace, name, tmpl, owner, node)
}

// startPodLocked creates a pod from a template, scheduled on a node that
// takes it, ContainerCreating at first and Running a moment later.
func (s *Sim) startPodLocked(ns, name string, tmpl corev1.PodTemplateSpec, owner metav1.OwnerReference, node string) {
	now := s.opts.Now()
	var sibling *corev1.Pod
	if owner.UID != "" {
		if sib := s.ownedPodsLocked(ns, owner.UID); len(sib) > 0 {
			sibling = sib[0]
		}
	}
	var p *corev1.Pod
	if sibling != nil {
		p = sibling.DeepCopy()
		p.ResourceVersion = ""
	} else {
		p = s.newPod(ns, name, "", 100, 128, now)
		p.Spec = *tmpl.Spec.DeepCopy()
	}
	p.Name, p.Namespace = name, ns
	p.UID = uid(ns + "/" + name + "/" + strconv.FormatInt(s.rv, 10))
	p.CreationTimestamp = metav1.NewTime(now.Truncate(time.Second))
	p.DeletionTimestamp = nil
	p.Labels = copyLabels(tmpl.Labels)
	if owner.Kind == "StatefulSet" {
		p.Labels["statefulset.kubernetes.io/pod-name"] = name
	}
	p.Annotations = nil
	for k, v := range tmpl.Annotations {
		if p.Annotations == nil {
			p.Annotations = map[string]string{}
		}
		p.Annotations[k] = v
	}
	if owner.UID != "" {
		p.OwnerReferences = []metav1.OwnerReference{owner}
	}
	for i := range p.Spec.Containers {
		if i < len(tmpl.Spec.Containers) {
			p.Spec.Containers[i].Image = tmpl.Spec.Containers[i].Image
		}
	}
	if node == "" {
		node = s.scheduleLocked(p)
	}
	p.Spec.NodeName = node
	p.Status = corev1.PodStatus{Phase: corev1.PodPending, QOSClass: p.Status.QOSClass}
	if node == "" {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/" + strconv.Itoa(len(s.nodes)) + " nodes are available: no node would take the pod.", LastTransitionTime: metav1.NewTime(now)}}
		s.putLocked(mustKind("pods"), p, watch.Added)
		return
	}
	if n := s.nodes[node]; n != nil {
		p.Status.HostIP = address(n, corev1.NodeInternalIP)
		p.Status.PodIP = podIPOn(n, 100+s.rnd.Intn(150))
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(now)}}
	for _, c := range p.Spec.Containers {
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}})
	}
	if sibling != nil {
		if u, ok := s.podUsage[ns+"/"+sibling.Name]; ok {
			s.podUsage[ns+"/"+name] = u
		}
	}
	s.putLocked(mustKind("pods"), p, watch.Added)
	podUID := p.UID
	time.AfterFunc(1200*time.Millisecond+time.Duration(s.rnd.Intn(800))*time.Millisecond, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		cur, ok := s.pods[ns+"/"+name]
		if !ok || cur.UID != podUID {
			return
		}
		cp := cur.DeepCopy()
		runReady(cp, s.opts.Now())
		s.putPodLocked(cp, watch.Modified)
		s.syncStatusLocked(ns)
	})
}

// scheduleLocked picks a node for a pod: ready, schedulable, with no taint
// the pod does not tolerate, and the fewest pods.
func (s *Sim) scheduleLocked(p *corev1.Pod) string {
	counts := map[string]int{}
	for _, q := range s.pods {
		counts[q.Spec.NodeName]++
	}
	best, bestN := "", 1<<30
	for _, name := range s.sortedNodeNames() {
		n := s.nodes[name]
		if n.Spec.Unschedulable || !isReady(n) {
			continue
		}
		if len(p.Spec.NodeSelector) > 0 && !labels.SelectorFromSet(p.Spec.NodeSelector).Matches(labels.Set(n.Labels)) {
			continue
		}
		fits := true
		for _, t := range n.Spec.Taints {
			if t.Effect == corev1.TaintEffectPreferNoSchedule {
				continue
			}
			tolerated := false
			for _, tol := range p.Spec.Tolerations {
				if tol.ToleratesTaint(klog.Background(), &t, false) {
					tolerated = true
				}
			}
			if !tolerated {
				fits = false
			}
		}
		if fits && counts[name] < bestN {
			best, bestN = name, counts[name]
		}
	}
	return best
}

// syncStatusLocked recomputes the status of a namespace's workloads and
// disruption budgets from their pods, as their controllers do.
func (s *Sim) syncStatusLocked(ns string) {
	type tally struct{ n, ready int32 }
	by := map[types.UID]*tally{}
	for _, o := range s.objectsLocked("pods", ns, nil, nil) {
		p := o.(*corev1.Pod)
		c := controllerOf(p)
		if c == nil {
			continue
		}
		t := by[c.UID]
		if t == nil {
			t = &tally{}
			by[c.UID] = t
		}
		t.n++
		if podReady(p) {
			t.ready++
		}
	}
	get := func(id types.UID) tally {
		if t := by[id]; t != nil {
			return *t
		}
		return tally{}
	}
	for _, o := range s.objectsLocked("replicasets", ns, nil, nil) {
		rs := o.(*appsv1.ReplicaSet)
		t := get(rs.UID)
		if rs.Status.Replicas == t.n && rs.Status.ReadyReplicas == t.ready && rs.Status.ObservedGeneration == rs.Generation {
			continue
		}
		cp := rs.DeepCopy()
		cp.Status.Replicas, cp.Status.ReadyReplicas, cp.Status.AvailableReplicas, cp.Status.FullyLabeledReplicas, cp.Status.ObservedGeneration = t.n, t.ready, t.ready, t.n, rs.Generation
		s.putLocked(mustKind("replicasets"), cp, watch.Modified)
	}
	for _, o := range s.objectsLocked("deployments", ns, nil, nil) {
		d := o.(*appsv1.Deployment)
		var total, ready, updated int32
		cur := s.currentRSLocked(d)
		for _, rs := range s.replicaSetsLocked(d) {
			t := get(rs.UID)
			total += t.n
			ready += t.ready
			if cur != nil && rs.UID == cur.UID {
				updated = t.n
			}
		}
		st := d.Status
		if st.Replicas == total && st.ReadyReplicas == ready && st.UpdatedReplicas == updated && st.ObservedGeneration == d.Generation {
			continue
		}
		cp := d.DeepCopy()
		cp.Status.Replicas, cp.Status.ReadyReplicas, cp.Status.AvailableReplicas, cp.Status.UpdatedReplicas, cp.Status.ObservedGeneration = total, ready, ready, updated, d.Generation
		cp.Status.UnavailableReplicas = max(0, int32Of(d.Spec.Replicas, 1)-ready)
		s.putLocked(mustKind("deployments"), cp, watch.Modified)
	}
	for _, o := range s.objectsLocked("statefulsets", ns, nil, nil) {
		st := o.(*appsv1.StatefulSet)
		t := get(st.UID)
		if st.Status.Replicas == t.n && st.Status.ReadyReplicas == t.ready && st.Status.ObservedGeneration == st.Generation {
			continue
		}
		cp := st.DeepCopy()
		cp.Status.Replicas, cp.Status.ReadyReplicas, cp.Status.AvailableReplicas, cp.Status.CurrentReplicas, cp.Status.UpdatedReplicas, cp.Status.ObservedGeneration = t.n, t.ready, t.ready, t.n, t.n, st.Generation
		s.putLocked(mustKind("statefulsets"), cp, watch.Modified)
	}
	for _, o := range s.objectsLocked("daemonsets", ns, nil, nil) {
		ds := o.(*appsv1.DaemonSet)
		t := get(ds.UID)
		if ds.Status.CurrentNumberScheduled == t.n && ds.Status.NumberReady == t.ready && ds.Status.ObservedGeneration == ds.Generation {
			continue
		}
		cp := ds.DeepCopy()
		cp.Status.CurrentNumberScheduled, cp.Status.NumberReady, cp.Status.NumberAvailable, cp.Status.UpdatedNumberScheduled, cp.Status.ObservedGeneration = t.n, t.ready, t.ready, t.n, ds.Generation
		s.putLocked(mustKind("daemonsets"), cp, watch.Modified)
	}
	for _, o := range s.objectsLocked("poddisruptionbudgets", ns, nil, nil) {
		b := o.(*policyv1.PodDisruptionBudget)
		cp := b.DeepCopy()
		s.pdbStatus(cp)
		if !equality.Semantic.DeepEqual(cp.Status, b.Status) {
			s.storeLocked(cp, watch.Modified)
		}
	}
}

// Node returns a copy of a node, for tests.
func (s *Sim) Node(name string) *corev1.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[name]; ok {
		return n.DeepCopy()
	}
	return nil
}

// LastImpersonated is the person the last change was made as: the
// Impersonate-User of the last write, "" for the console's own account.
func (s *Sim) LastImpersonated() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastWriter
}
