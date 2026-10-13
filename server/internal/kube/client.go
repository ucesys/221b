// Package kube connects to a Kubernetes API server and keeps a watched,
// in-memory copy of the nodes and pods the console shows.
package kube

import (
	"errors"
	"fmt"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Mode says how the server reached the cluster.
type Mode string

const (
	// ModeInCluster uses the pod's ServiceAccount. It is the normal mode.
	ModeInCluster Mode = "in-cluster"
	// ModeKubeconfig uses a kubeconfig context, for development on a laptop.
	ModeKubeconfig Mode = "kubeconfig"
	// ModeURL talks to a URL without credentials: the simulator and tests.
	ModeURL Mode = "url"
)

// ConnectOptions picks the cluster to connect to.
type ConnectOptions struct {
	Kubeconfig string
	Context    string
	APIServer  string
}

const serviceAccountToken = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// RestConfig builds the client configuration. Inside a pod with no kubeconfig
// or context given, it uses the pod's ServiceAccount.
func RestConfig(o ConnectOptions) (*rest.Config, Mode, error) {
	switch {
	case o.APIServer != "":
		if o.Kubeconfig != "" || o.Context != "" {
			return nil, "", errors.New("give either an API server URL or a kubeconfig, not both")
		}
		return &rest.Config{Host: o.APIServer}, ModeURL, nil
	case o.Kubeconfig == "" && o.Context == "" && inCluster():
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf("reading the in-cluster configuration: %w", err)
		}
		return cfg, ModeInCluster, nil
	default:
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		if o.Kubeconfig != "" {
			rules.ExplicitPath = o.Kubeconfig
		}
		overrides := &clientcmd.ConfigOverrides{CurrentContext: o.Context}
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, "", fmt.Errorf("reading the kubeconfig: %w", err)
		}
		return cfg, ModeKubeconfig, nil
	}
}

func inCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return false
	}
	_, err := os.Stat(serviceAccountToken)
	return err == nil
}
