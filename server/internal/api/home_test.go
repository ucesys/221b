package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
