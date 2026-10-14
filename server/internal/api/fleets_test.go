package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// P4-01: administrators replace a cluster's fleets; the fleets view puts
// the nodes that have every label and taint in them.
func TestFleets(t *testing.T) {
	srv, ops, _, auditOut, local, store := multiSetupWith(t, "ops@corp.example", nil)
	viewer := signIn(t, srv, "viewer@corp.example")
	local.Update("nodes", "", "n1", func(o runtime.Object) {
		n := o.(*corev1.Node)
		n.Labels = map[string]string{"pool": "gpu"}
		n.Spec.Taints = []corev1.Taint{{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}}
	})
	local.Update("nodes", "", "n2", func(o runtime.Object) { o.(*corev1.Node).Labels = map[string]string{"pool": "gpu"} })
	store.Notify(kube.Change{Views: map[string][]string{live.ViewNodes: {kube.AllRows}, live.ViewFleets: {kube.AllRows}}})
	store.Process()

	gpu := `{"fleets":[{"id":"gpu","name":"GPU","color":1,"labels":["pool=gpu"],"taints":["gpu=true:NoSchedule"]},{"id":"pool","name":"Pool","color":2,"labels":["pool=gpu"],"taints":[]}]}`
	if r, b := send(t, viewer, http.MethodPut, srv.URL+"/api/v1/fleets", gpu); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer changed the fleets: %d %s", r.StatusCode, b)
	}
	for name, c := range map[string]struct {
		body, want string
		code       int
	}{
		"no name":         {`{"fleets":[{"id":"a","name":" ","labels":["a=b"],"taints":[]}]}`, "Give the fleet a name.", 400},
		"no requirement":  {`{"fleets":[{"id":"a","name":"A","labels":[],"taints":[]}]}`, "Pick at least one label or taint.", 400},
		"same name":       {`{"fleets":[{"id":"a","name":"A","labels":["a=b"]},{"id":"b","name":"a","labels":["a=c"]}]}`, "Another fleet is already called", 400},
		"bad taint":       {`{"fleets":[{"id":"a","name":"A","taints":["gpu=true:Sometimes"]}]}`, "", 400},
		"unknown field":   {`{"fleets":[],"nodes":[]}`, "", 400},
		"not json":        {`fleets=none`, "", 400},
		"unknown cluster": {`{"fleets":[]}`, "", 404},
	} {
		url := srv.URL + "/api/v1/fleets"
		if name == "unknown cluster" {
			url = srv.URL + "/api/v1/clusters/nowhere/fleets"
		}
		r, b := send(t, ops, http.MethodPut, url, c.body)
		if r.StatusCode != c.code || !strings.Contains(b, c.want) {
			t.Errorf("%s: %d %s", name, r.StatusCode, b)
		}
	}

	r, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/fleets", gpu)
	if r.StatusCode != http.StatusOK || !strings.Contains(b, `"name":"GPU"`) {
		t.Fatalf("PUT fleets %d %s", r.StatusCode, b)
	}
	if !strings.Contains(auditOut.String(), `"action":"fleets.update"`) || !strings.Contains(auditOut.String(), "2 fleets: GPU, Pool") {
		t.Fatalf("no audit record: %s", auditOut.String())
	}
	// Kept with the settings, by cluster.
	r, b = send(t, ops, http.MethodGet, srv.URL+"/api/v1/settings", "")
	if !strings.Contains(b, `"fleets":{"local":[{"id":"gpu"`) {
		t.Fatalf("settings %d %s", r.StatusCode, b)
	}

	store.Process()
	_, b = send(t, ops, http.MethodGet, srv.URL+"/api/v1/snapshot?views=fleets", "")
	var snap struct {
		Views map[string]struct {
			Rows []fleets.Row `json:"rows"`
		} `json:"views"`
	}
	if err := json.Unmarshal([]byte(b), &snap); err != nil {
		t.Fatal(err)
	}
	members := map[string]string{}
	for _, row := range snap.Views["fleets"].Rows {
		members[row.Kind+":"+row.ID] = strings.Join(row.Members, ",")
	}
	if members["fleet:gpu"] != "n1" || members["fleet:pool"] != "n1,n2" {
		t.Fatalf("members %v", members)
	}

	// A node that gets the taint joins on the next tick.
	local.Update("nodes", "", "n2", func(o runtime.Object) {
		o.(*corev1.Node).Spec.Taints = []corev1.Taint{{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}}
	})
	store.Notify(kube.Change{Views: map[string][]string{live.ViewFleets: {kube.AllRows}}})
	store.Process()
	_, b = send(t, ops, http.MethodGet, srv.URL+"/api/v1/snapshot?views=fleets", "")
	if !strings.Contains(b, `"members":["n1","n2"]`) {
		t.Fatalf("n2 did not join: %s", b)
	}

	// No fleets at all.
	if r, b := send(t, ops, http.MethodPut, srv.URL+"/api/v1/fleets", `{"fleets":[]}`); r.StatusCode != 200 || b != `{"fleets":[]}` {
		t.Fatalf("clearing %d %s", r.StatusCode, b)
	}
}
