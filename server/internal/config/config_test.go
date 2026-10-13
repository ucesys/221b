package config

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "")
	c, err := Parse([]string{"-cluster-name", "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.ClusterName != "prod" || c.MetricsInterval != 15*time.Second || c.HealthInterval != 10*time.Second || c.LogFormat != "json" {
		t.Fatalf("defaults %+v", c)
	}
}

func TestEnvironment(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "lakehouse-prod")
	t.Setenv("LISTEN", ":9090")
	t.Setenv("METRICS_INTERVAL", "30")
	t.Setenv("HEALTH_INTERVAL", "5s")
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClusterName != "lakehouse-prod" || c.Listen != ":9090" || c.MetricsInterval != 30*time.Second || c.HealthInterval != 5*time.Second {
		t.Fatalf("env %+v", c)
	}
	// Flags win over the environment.
	if c, _ = Parse([]string{"-listen", ":7000"}); c.Listen != ":7000" {
		t.Fatalf("flag did not override: %q", c.Listen)
	}
}

func TestValidation(t *testing.T) {
	for _, args := range [][]string{
		{"-cluster-name", ""},
		{"-metrics-interval", "100ms"},
		{"-log-level", "loud"},
		{"-log-format", "xml"},
		{"extra"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%v) should fail", args)
		}
	}
}
