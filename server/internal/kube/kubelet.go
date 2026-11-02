package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// KubeletConfig is the part of a kubelet's configuration that decides what
// it keeps from pods. It is read field by field, so nothing else in the
// configuration leaves the node.
type KubeletConfig struct {
	SystemReserved         map[string]string `json:"systemReserved,omitempty"`
	KubeReserved           map[string]string `json:"kubeReserved,omitempty"`
	ReservedSystemCPUs     string            `json:"reservedSystemCPUs,omitempty"`
	EvictionHard           map[string]string `json:"evictionHard,omitempty"`
	EnforceNodeAllocatable []string          `json:"enforceNodeAllocatable,omitempty"`
	MaxPods                int               `json:"maxPods,omitempty"`
}

func parseKubeletConfig(raw []byte) (KubeletConfig, error) {
	var v struct {
		Config KubeletConfig `json:"kubeletconfig"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return KubeletConfig{}, errors.New("the kubelet's configuration is not valid JSON")
	}
	return v.Config, nil
}

// KubeletConfig asks one kubelet, through the API server, how it is
// configured now. It needs get on nodes/proxy, as uptime does.
func (c *Cluster) KubeletConfig(ctx context.Context, node string) (KubeletConfig, error) {
	if !c.kubeletStats {
		return KubeletConfig{}, errors.New("kubeletStats is off, so the console does not read the kubelets")
	}
	ctx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	raw, err := c.rest.Get().AbsPath("/api/v1/nodes/"+node+"/proxy/configz").SetHeader("Accept", "application/json").MaxRetries(0).Do(ctx).Raw()
	if apierrors.IsForbidden(err) {
		return KubeletConfig{}, errors.New("not allowed to read the kubelet (get on nodes/proxy)")
	}
	if err != nil {
		return KubeletConfig{}, fmt.Errorf("the kubelet did not answer: %w", err)
	}
	return parseKubeletConfig(raw)
}
