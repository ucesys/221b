package sim

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

type watchEvent struct {
	Type   string `json:"type"`
	Object struct {
		Kind     string `json:"kind"`
		Code     int    `json:"code"`
		Metadata struct {
			Name            string            `json:"name"`
			ResourceVersion string            `json:"resourceVersion"`
			Annotations     map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	} `json:"object"`
}

func newServer(t *testing.T, o Options) (*Sim, *httptest.Server) {
	t.Helper()
	s := New(o)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func listRV(t *testing.T, url string) (string, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var l struct {
		Metadata struct{ ResourceVersion string } `json:"metadata"`
		Items    []json.RawMessage                `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		t.Fatal(err)
	}
	return l.Metadata.ResourceVersion, len(l.Items)
}

func openWatch(t *testing.T, url string) (*bufio.Scanner, func()) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	return sc, func() { resp.Body.Close() }
}

func next(t *testing.T, sc *bufio.Scanner) watchEvent {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("watch ended: %v", sc.Err())
	}
	var e watchEvent
	if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
		t.Fatalf("bad event %q: %v", sc.Text(), err)
	}
	return e
}

func TestListThenWatch(t *testing.T) {
	_, srv := newServer(t, Options{Nodes: 5, Pods: 40, Seed: 3})
	rv, n := listRV(t, srv.URL+"/api/v1/nodes")
	if n != 5 {
		t.Fatalf("nodes %d", n)
	}
	sc, stop := openWatch(t, srv.URL+"/api/v1/nodes?watch=true&resourceVersion="+rv)
	defer stop()
	go http.Post(srv.URL+"/sim/nodes/worker-001/cordon", "", nil)
	e := next(t, sc)
	if e.Type != "MODIFIED" || e.Object.Metadata.Name != "worker-001" || !e.Object.Spec.Unschedulable {
		t.Fatalf("event %+v", e)
	}
	got, _ := strconv.Atoi(e.Object.Metadata.ResourceVersion)
	old, _ := strconv.Atoi(rv)
	if got <= old {
		t.Fatalf("resource version did not increase: %d <= %d", got, old)
	}
}

func TestStreamingListEndsWithBookmark(t *testing.T) {
	_, srv := newServer(t, Options{Nodes: 4, Pods: 10})
	sc, stop := openWatch(t, srv.URL+"/api/v1/nodes?watch=1&sendInitialEvents=true&resourceVersionMatch=NotOlderThan&allowWatchBookmarks=true")
	defer stop()
	for i := 0; i < 4; i++ {
		if e := next(t, sc); e.Type != "ADDED" {
			t.Fatalf("event %d is %s", i, e.Type)
		}
	}
	e := next(t, sc)
	if e.Type != "BOOKMARK" || e.Object.Metadata.Annotations["k8s.io/initial-events-end"] != "true" {
		t.Fatalf("expected the initial-events-end bookmark, got %+v", e)
	}
}

func TestOldResourceVersionExpires(t *testing.T) {
	s, srv := newServer(t, Options{Nodes: 4, Pods: 4})
	for i := 0; i < maxEvents+10; i++ {
		s.Churn(1)
	}
	sc, stop := openWatch(t, srv.URL+"/api/v1/pods?watch=true&resourceVersion=1")
	defer stop()
	e := next(t, sc)
	if e.Type != "ERROR" || e.Object.Code != http.StatusGone {
		t.Fatalf("expected 410, got %+v", e)
	}
}

func TestDownClosesConnections(t *testing.T) {
	_, srv := newServer(t, Options{Nodes: 3, Pods: 3})
	sc, stop := openWatch(t, srv.URL+"/api/v1/nodes?watch=true")
	defer stop()
	for i := 0; i < 3; i++ {
		next(t, sc)
	}
	resp, err := http.Post(srv.URL+"/sim/down?on=true", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	done := make(chan bool)
	go func() { done <- !sc.Scan() }()
	select {
	case ended := <-done:
		if !ended {
			t.Fatal("watch kept streaming while down")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch not closed")
	}
	if _, err := http.Get(srv.URL + "/version"); err == nil {
		t.Fatal("requests should fail while down")
	}
}

func TestMetricsAndVersion(t *testing.T) {
	_, srv := newServer(t, Options{Nodes: 3, Pods: 9, Metrics: true})
	resp, err := http.Get(srv.URL + "/apis/metrics.k8s.io/v1beta1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"NodeMetricsList"`) || !strings.Contains(string(b), `"cpu"`) {
		t.Fatalf("metrics %s", b)
	}
	resp, _ = http.Get(srv.URL + "/version")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"gitVersion":"v1.37.1"`) {
		t.Fatalf("version %s", b)
	}
}

func TestPlacementRespectsAllocatable(t *testing.T) {
	s := New(Options{Nodes: 20, Pods: 1500, Seed: 9})
	used := map[string]int64{}
	for _, p := range s.pods {
		if p.Spec.NodeName != "" {
			used[p.Spec.NodeName] += p.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
		}
	}
	for name, cpu := range used {
		if alloc := s.nodes[name].Status.Allocatable.Cpu().MilliValue(); cpu > alloc {
			t.Fatalf("%s requests %dm of %dm", name, cpu, alloc)
		}
	}
}

func TestChurnBlipsRecover(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s, _ := newServer(t, Options{Nodes: 4, Pods: 40, Now: func() time.Time { return now }})
	notReady := func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		n := 0
		for _, p := range s.pods {
			if p.Status.Phase == corev1.PodRunning && !podReady(p) {
				n++
			}
		}
		return n
	}
	before := notReady()
	for i := 0; i < 50; i++ {
		s.Churn(2)
	}
	// However long it churns, only the pods of the last blipFor are not ready.
	if n := notReady(); n <= before || n > before+100 {
		t.Fatalf("not ready after churning: %d (was %d)", n, before)
	}
	now = now.Add(blipFor + time.Second)
	s.Churn(0)
	if n := notReady(); n != before {
		t.Fatalf("not ready after the blips passed: %d, want %d", n, before)
	}
}

// A Deployment created through the API gets its pods, a dry-run delete
// (its options in the body, as client-go sends them) keeps the object,
// and deleting the Deployment takes its pods with it.
func TestDeploymentLifecycleAndDryRunDeletes(t *testing.T) {
	_, srv := newServer(t, Options{Nodes: 3, Pods: 10, Seed: 1})
	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	do("POST", "/api/v1/namespaces", `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"t"}}`)
	if c, b := do("POST", "/apis/apps/v1/namespaces/t/deployments", `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"d","namespace":"t"},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"d"}},"template":{"metadata":{"labels":{"app":"d"}},"spec":{"containers":[{"name":"c","image":"x"}]}}}}`); c != 201 {
		t.Fatalf("create %d %s", c, b)
	}
	pods := func() []string {
		_, b := do("GET", "/api/v1/namespaces/t/pods", "")
		var l struct {
			Items []struct {
				Metadata struct{ Name string } `json:"metadata"`
			} `json:"items"`
		}
		_ = json.Unmarshal([]byte(b), &l)
		var out []string
		for _, p := range l.Items {
			out = append(out, p.Metadata.Name)
		}
		return out
	}
	ps := pods()
	if len(ps) != 2 {
		t.Fatalf("pods %v", ps)
	}
	if c, _ := do("DELETE", "/api/v1/namespaces/t/pods/"+ps[0], `{"kind":"DeleteOptions","apiVersion":"v1","dryRun":["All"]}`); c != 200 {
		t.Fatalf("dry-run delete %d", c)
	}
	if c, _ := do("GET", "/api/v1/namespaces/t/pods/"+ps[0], ""); c != 200 {
		t.Fatal("a dry-run delete deleted the pod")
	}
	do("DELETE", "/apis/apps/v1/namespaces/t/deployments/d", "")
	if ps := pods(); len(ps) != 0 {
		t.Fatalf("pods left after their Deployment went: %v", ps)
	}
}
