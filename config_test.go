package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	p := writeTempConfig(t, `
region_id: ap-southeast-5
metrics:
  - namespace: acs_vpn
    metric_name: tun.bgp_state
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want 60s", cfg.PollInterval)
	}
	if cfg.ListenAddr != ":9525" {
		t.Errorf("ListenAddr = %q, want :9525", cfg.ListenAddr)
	}
	if cfg.MetricsPath != "/metrics" {
		t.Errorf("MetricsPath = %q, want /metrics", cfg.MetricsPath)
	}
	if cfg.Metrics[0].Period != 60 {
		t.Errorf("Period = %d, want 60", cfg.Metrics[0].Period)
	}
	if cfg.Metrics[0].Statistic != "Average" {
		t.Errorf("Statistic = %q, want Average", cfg.Metrics[0].Statistic)
	}
}

func TestLoadParsesPollInterval(t *testing.T) {
	p := writeTempConfig(t, `
region_id: ap-southeast-5
poll_interval: 300s
metrics:
  - namespace: acs_vpn
    metric_name: tun.bgp_state
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 300*time.Second {
		t.Errorf("PollInterval = %v, want 300s", cfg.PollInterval)
	}
}

func TestFinalName(t *testing.T) {
	cases := []struct {
		spec MetricSpec
		want string
	}{
		{MetricSpec{Namespace: "acs_vpn", MetricName: "tun.bgp_state"}, "aliyun_acs_vpn_tun_bgp_state"},
		{MetricSpec{Namespace: "acs_vpn", MetricName: "tun.bgp_state", Rename: "vpn_tunnel_bgp_state"}, "vpn_tunnel_bgp_state"},
		{MetricSpec{Namespace: "acs_ecs", MetricName: "net.rate-in"}, "aliyun_acs_ecs_net_rate_in"},
	}
	for _, c := range cases {
		if got := c.spec.FinalName(); got != c.want {
			t.Errorf("FinalName(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

func TestLoadValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing region_id": `
metrics:
  - {namespace: acs_vpn, metric_name: x}
`,
		"no metrics": `
region_id: ap-southeast-5
metrics: []
`,
		"missing metric_name": `
region_id: ap-southeast-5
metrics:
  - namespace: acs_vpn
`,
		"bad statistic": `
region_id: ap-southeast-5
metrics:
  - {namespace: acs_vpn, metric_name: x, statistic: p99}
`,
		"select key not in dimensions": `
region_id: ap-southeast-5
metrics:
  - namespace: acs_vpn
    metric_name: x
    dimensions: [instanceId]
    dimension_select:
      tunnelId: ["1"]
`,
		"duplicate final name": `
region_id: ap-southeast-5
metrics:
  - {namespace: acs_vpn, metric_name: tun.state, rename: dup}
  - {namespace: acs_vpn, metric_name: ipsec.state, rename: dup}
`,
		"invalid metric name": `
region_id: ap-southeast-5
metrics:
  - {namespace: acs_vpn, metric_name: "tun state"}
`,
		"metrics_path without leading slash": `
region_id: ap-southeast-5
metrics_path: metrics
metrics:
  - {namespace: acs_vpn, metric_name: x}
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTempConfig(t, body)); err == nil {
				t.Fatalf("expected error for %s, got nil", name)
			}
		})
	}
}

// TestLoadRejectsInvalidDimensionLabel guards against a dimension key that is not
// a valid Prometheus label name (here it contains a "."). Load must return an
// error rather than let prometheus.NewGaugeVec / MustRegister panic at startup.
func TestLoadRejectsInvalidDimensionLabel(t *testing.T) {
	p := writeTempConfig(t, `
region_id: ap-southeast-5
metrics:
  - namespace: acs_vpn
    metric_name: tun.bgp_state
    dimensions: ["instance.id"]
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for dimension key containing \".\", got nil")
	}
}
