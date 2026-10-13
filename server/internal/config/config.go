// Package config reads the server's settings from flags, with environment
// variables as defaults so the Helm chart can set them.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the server needs to start.
type Config struct {
	// Listen is the address the HTTP server binds, such as ":8080".
	Listen string
	// ClusterName is the name shown in the top bar. Kubernetes has no
	// reliable name for itself, so the chart passes one in.
	ClusterName string
	// Kubeconfig and Context select a cluster when the server runs outside
	// one, for development. Both are ignored inside a cluster.
	Kubeconfig string
	Context    string
	// APIServer connects to a plain URL without credentials. It is meant for
	// the simulator (221b-sim) and for tests.
	APIServer string
	// MetricsInterval is how often CPU and memory in use are read from
	// metrics-server.
	MetricsInterval time.Duration
	// HealthInterval is how often the API server is checked.
	HealthInterval time.Duration
	// LogLevel is debug, info, warn or error. LogFormat is json or text.
	LogLevel  string
	LogFormat string
	// AllowedHosts are host names the server answers to besides localhost
	// and IP addresses, such as its Service's DNS names.
	AllowedHosts []string
}

// Parse reads the configuration from args (without the program name).
func Parse(args []string) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("221b", flag.ContinueOnError)
	fs.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "address to serve HTTP on")
	fs.StringVar(&c.ClusterName, "cluster-name", env("CLUSTER_NAME", "cluster"), "name of the cluster shown in the top bar")
	fs.StringVar(&c.Kubeconfig, "kubeconfig", env("KUBECONFIG_PATH", ""), "path to a kubeconfig file (outside a cluster only)")
	fs.StringVar(&c.Context, "context", env("KUBE_CONTEXT", ""), "kubeconfig context to use (outside a cluster only)")
	fs.StringVar(&c.APIServer, "api-server", env("API_SERVER", ""), "connect to this API server URL without credentials (simulator and tests)")
	fs.DurationVar(&c.MetricsInterval, "metrics-interval", envDuration("METRICS_INTERVAL", 15*time.Second), "how often to read CPU and memory in use")
	fs.DurationVar(&c.HealthInterval, "health-interval", envDuration("HEALTH_INTERVAL", 10*time.Second), "how often to check the API server")
	fs.StringVar(&c.LogLevel, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error")
	fs.StringVar(&c.LogFormat, "log-format", env("LOG_FORMAT", "json"), "json or text")
	hosts := fs.String("allowed-hosts", env("ALLOWED_HOSTS", ""), "comma-separated host names to answer to, besides localhost and IP addresses")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	for _, h := range strings.Split(*hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.AllowedHosts = append(c.AllowedHosts, h)
		}
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return c, c.validate()
}

func (c Config) validate() error {
	var errs []error
	if c.ClusterName == "" {
		errs = append(errs, errors.New("cluster name must not be empty"))
	}
	if c.MetricsInterval < time.Second {
		errs = append(errs, errors.New("metrics interval must be at least 1s"))
	}
	if c.HealthInterval < time.Second {
		errs = append(errs, errors.New("health interval must be at least 1s"))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("unknown log level %q", c.LogLevel))
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("unknown log format %q", c.LogFormat))
	}
	return errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
