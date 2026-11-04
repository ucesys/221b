package kube

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Only the reservation fields leave the node: a kubelet's configuration
// also names its certificates and can hold headers with credentials.
func TestAKubeletConfigKeepsOnlyWhatItKeepsFromPods(t *testing.T) {
	got, err := parseKubeletConfig([]byte(`{"kubeletconfig": {
		"systemReserved": {"cpu": "4", "memory": "16Gi"},
		"kubeReserved": {"memory": "1Gi"},
		"evictionHard": {"memory.available": "100Mi"},
		"enforceNodeAllocatable": ["pods"],
		"maxPods": 110,
		"tlsCertFile": "/var/lib/kubelet/pki/kubelet.crt",
		"staticPodURLHeader": {"Authorization": ["Bearer secret-value"]}
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := KubeletConfig{
		SystemReserved: map[string]string{"cpu": "4", "memory": "16Gi"}, KubeReserved: map[string]string{"memory": "1Gi"},
		EvictionHard: map[string]string{"memory.available": "100Mi"}, EnforceNodeAllocatable: []string{"pods"}, MaxPods: 110,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "secret-value") || strings.Contains(string(b), "pki") {
		t.Fatalf("it leaked: %s", b)
	}
	if _, err := parseKubeletConfig([]byte("not json")); err == nil {
		t.Fatal("no error for a broken answer")
	}
}
