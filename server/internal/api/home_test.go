package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// The home page sums each cluster up as the person may see it (asked at
// the Phase 4 review).
func TestHome(t *testing.T) {
	srv, ops, _, _, local, store := multiSetupWith(t, "ops@corp.example", nil)
	local.Update("nodes", "", "n1", func(o runtime.Object) {
		o.(*corev1.Node).Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi"), corev1.ResourcePods: resource.MustParse("110")}
	})
	store.Notify(kube.Change{Views: map[string][]string{live.ViewNodes: {kube.AllRows}}})
	store.Process()
	read := func(srv *httptest.Server, c *http.Client) homeCluster {
		t.Helper()
		r, b := send(t, c, http.MethodGet, srv.URL+"/api/v1/home", "")
		if r.StatusCode != 200 {
			t.Fatalf("home %d %s", r.StatusCode, b)
		}
		var out struct {
			Clusters []homeCluster `json:"clusters"`
		}
		if err := json.Unmarshal([]byte(b), &out); err != nil || len(out.Clusters) == 0 {
			t.Fatalf("home %v %s", err, b)
		}
		return out.Clusters[0]
	}
	all := read(srv, ops)
	if all.ID != "local" || !all.Local || all.Nodes.Total != 2 || all.Pods.Total != 2 || all.CPU.Allocatable != 4000 || all.Memory.Allocatable != 8<<30 || all.Namespaces != 3 {
		t.Fatalf("ops sees %+v", all)
	}
	// The pods by state add up to all of them, as the tile shows them.
	if p := all.Pods; p.Ready+p.Unready+p.Pending+p.Failing+p.Completed != p.Total {
		t.Fatalf("the pods' states do not add up: %+v", p)
	}
	// Someone who may not list kube-system's pods does not count them.
	limited, viewer, _, _, _, _ := multiSetupWith(t, "viewer@corp.example", map[string]bool{"pods/kube-system": true})
	if mine := read(limited, viewer); mine.Pods.Total != 1 {
		t.Fatalf("the viewer sees pods they may not list: %+v", mine.Pods)
	}
	anon := &http.Client{}
	if r, err := anon.Get(srv.URL + "/api/v1/home"); err != nil || r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %v %v", r.StatusCode, err)
	}
}

// A node that stopped reporting counts once, as not ready, and its pods as
// Unknown, not failing; a cordoned node is still ready; the storage tile
// sums up the claims (asked at the second Toolbox review).
func TestHomeCountsANodeThatStoppedReportingOnce(t *testing.T) {
	srv, ops, _, _, local, store := multiSetupWith(t, "ops@corp.example", nil)
	now := metav1.Now()
	local.Update("nodes", "", "n1", func(o runtime.Object) {
		n := o.(*corev1.Node)
		n.Spec.Unschedulable = true
		n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastTransitionTime: now}}
	})
	local.Update("nodes", "", "n2", func(o runtime.Object) {
		o.(*corev1.Node).Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown, LastTransitionTime: now}}
	})
	putStorage(local, store)
	ch := everything()
	ch.Mark(live.ViewAlerts, kube.AllRows)
	store.Notify(ch)
	store.Process()
	r, b := send(t, ops, http.MethodGet, srv.URL+"/api/v1/home", "")
	var out struct {
		Clusters []homeCluster `json:"clusters"`
	}
	if r.StatusCode != 200 || json.Unmarshal([]byte(b), &out) != nil || len(out.Clusters) == 0 {
		t.Fatalf("home %d %s", r.StatusCode, b)
	}
	h := out.Clusters[0]
	if h.Nodes.Total != 2 || h.Nodes.Ready != 1 || h.Nodes.Cordoned != 1 {
		t.Fatalf("nodes %+v", h.Nodes)
	}
	if a := h.Alerts; a.NodesDown != 1 || a.NodeErrors != 0 || a.NodeWarnings != 1 || a.Unknown != 1 || a.Failed != 0 {
		t.Fatalf("alerts %+v", a)
	}
	if h.Pods.Unknown != 1 {
		t.Fatalf("pods %+v", h.Pods)
	}
	if s := h.Storage; s.Claims != 2 || s.Bound != 2 || s.Attention != 1 || s.Errors != 0 {
		t.Fatalf("storage %+v", s)
	}
}
