package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func init() { backoffBase = time.Millisecond }

func TestParseDatapoints(t *testing.T) {
	if dps, err := parseDatapoints(""); err != nil || dps != nil {
		t.Errorf("empty: got (%v, %v), want (nil, nil)", dps, err)
	}
	if _, err := parseDatapoints("not json"); err == nil {
		t.Error("expected error for invalid JSON")
	}
	raw := `[{"timestamp":1609459200000,"instanceId":"vpn-a","tunnelId":"1","Average":1}]`
	dps, err := parseDatapoints(raw)
	if err != nil || len(dps) != 1 {
		t.Fatalf("got (%v, %v)", dps, err)
	}
	if v, ok := dps[0].statValue("Average"); !ok || v != 1 {
		t.Errorf("statValue = (%v, %v)", v, ok)
	}
	if _, ok := dps[0].statValue("Maximum"); ok {
		t.Error("statValue(Maximum) should be absent")
	}
}

func TestLatestByDimensionsDualTunnel(t *testing.T) {
	dps := []datapoint{
		{"timestamp": 100.0, "instanceId": "vpn-a", "tunnelId": "1", "Average": 1.0},
		{"timestamp": 200.0, "instanceId": "vpn-a", "tunnelId": "1", "Average": 0.0}, // newer, tunnel 1
		{"timestamp": 150.0, "instanceId": "vpn-a", "tunnelId": "2", "Average": 1.0}, // tunnel 2
	}
	got := latestByDimensions(dps, []string{"instanceId", "tunnelId"})
	if len(got) != 2 {
		t.Fatalf("got %d series, want 2 (one per tunnel)", len(got))
	}
	byTunnel := map[string]float64{}
	for _, d := range got {
		v, _ := d.statValue("Average")
		byTunnel[d["tunnelId"].(string)] = v
	}
	if byTunnel["1"] != 0.0 {
		t.Errorf("tunnel 1 = %v, want 0 (newest datapoint)", byTunnel["1"])
	}
	if byTunnel["2"] != 1.0 {
		t.Errorf("tunnel 2 = %v, want 1", byTunnel["2"])
	}
}

func TestRenderDimensions(t *testing.T) {
	if got := renderDimensions(nil); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	got := renderDimensions(map[string][]string{"instanceId": {"vpn-a", "vpn-b"}})
	var arr []map[string]string
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("not valid JSON array: %v (%s)", err, got)
	}
	ids := []string{arr[0]["instanceId"], arr[1]["instanceId"]}
	sort.Strings(ids)
	if len(arr) != 2 || ids[0] != "vpn-a" || ids[1] != "vpn-b" {
		t.Errorf("got %v, want two objects for vpn-a and vpn-b", arr)
	}
}

func TestWithBackoffRetriesThrottling(t *testing.T) {
	var attempts int
	out, err := withBackoff(context.Background(), func() { attempts++ }, func() (string, error) {
		if attempts < 3 {
			return "", errors.New("Throttling.User: Request was denied due to user flow control")
		}
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("got (%q, %v), want (ok, nil)", out, err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestWithBackoffGivesUp(t *testing.T) {
	var attempts int
	_, err := withBackoff(context.Background(), func() { attempts++ }, func() (string, error) {
		return "", errors.New("Throttling.User")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != 5 {
		t.Errorf("attempts = %d, want 5", attempts)
	}
}

func TestWithBackoffDoesNotRetryOtherErrors(t *testing.T) {
	var attempts int
	_, err := withBackoff(context.Background(), func() { attempts++ }, func() (string, error) {
		return "", errors.New("InvalidParameter")
	})
	if err == nil || attempts != 1 {
		t.Errorf("got (err=%v, attempts=%d), want (non-nil, 1)", err, attempts)
	}
}

func TestWithBackoffHonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := withBackoff(ctx, nil, func() (string, error) {
		return "", errors.New("Throttling.User")
	})
	if err == nil {
		t.Fatal("expected context error")
	}
}

type fakeLister struct {
	// keyed by metric_name -> datapoints JSON to return
	responses map[string]string
	errs      map[string]error
	calls     int
}

func (f *fakeLister) List(_ context.Context, req listRequest) (string, error) {
	f.calls++
	if err := f.errs[req.MetricName]; err != nil {
		return "", err
	}
	return f.responses[req.MetricName], nil
}

func testConfig(specs ...MetricSpec) *Config {
	return &Config{RegionID: "r", PollInterval: time.Minute, ListenAddr: ":0", MetricsPath: "/metrics", Metrics: specs}
}

func TestPollOnceDualTunnelDistinctSeries(t *testing.T) {
	spec := MetricSpec{
		Namespace: "acs_vpn", MetricName: "tun.bgp_state", Period: 60, Statistic: "Average",
		Dimensions: []string{"instanceId", "tunnelId"},
	}
	f := &fakeLister{responses: map[string]string{
		"tun.bgp_state": `[
			{"timestamp":100,"instanceId":"vpn-a","tunnelId":"1","Average":1},
			{"timestamp":100,"instanceId":"vpn-a","tunnelId":"2","Average":0}
		]`,
	}}
	reg := prometheus.NewRegistry()
	c := NewCollector(testConfig(spec), f, reg)
	c.pollOnce(context.Background())

	want := `
# HELP aliyun_acs_vpn_tun_bgp_state CMS metric acs_vpn/tun.bgp_state
# TYPE aliyun_acs_vpn_tun_bgp_state gauge
aliyun_acs_vpn_tun_bgp_state{instanceId="vpn-a",tunnelId="1"} 1
aliyun_acs_vpn_tun_bgp_state{instanceId="vpn-a",tunnelId="2"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "aliyun_acs_vpn_tun_bgp_state"); err != nil {
		t.Error(err)
	}
}

func TestPollOnceIsolatesFailingSpec(t *testing.T) {
	good := MetricSpec{Namespace: "acs_vpn", MetricName: "ipsec.state", Period: 60, Statistic: "Average", Dimensions: []string{"instanceId"}}
	bad := MetricSpec{Namespace: "acs_vpn", MetricName: "tun.state", Period: 60, Statistic: "Average", Dimensions: []string{"instanceId"}}
	f := &fakeLister{
		responses: map[string]string{"ipsec.state": `[{"timestamp":1,"instanceId":"vpn-a","Average":1}]`},
		errs:      map[string]error{"tun.state": errors.New("InvalidParameter")},
	}
	reg := prometheus.NewRegistry()
	c := NewCollector(testConfig(good, bad), f, reg)
	c.pollOnce(context.Background())

	if got := testutil.ToFloat64(c.pollErrors.WithLabelValues("acs_vpn/tun.state")); got != 1 {
		t.Errorf("poll_errors_total = %v, want 1", got)
	}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP aliyun_acs_vpn_ipsec_state CMS metric acs_vpn/ipsec.state
# TYPE aliyun_acs_vpn_ipsec_state gauge
aliyun_acs_vpn_ipsec_state{instanceId="vpn-a"} 1
`), "aliyun_acs_vpn_ipsec_state"); err != nil {
		t.Error(err)
	}
}

func TestPollOnceKeepsLastGoodOnError(t *testing.T) {
	spec := MetricSpec{Namespace: "acs_vpn", MetricName: "ipsec.state", Period: 60, Statistic: "Average", Dimensions: []string{"instanceId"}}
	f := &fakeLister{responses: map[string]string{"ipsec.state": `[{"timestamp":1,"instanceId":"vpn-a","Average":1}]`}}
	reg := prometheus.NewRegistry()
	c := NewCollector(testConfig(spec), f, reg)
	c.pollOnce(context.Background())

	f.responses = nil
	f.errs = map[string]error{"ipsec.state": errors.New("InternalError")}
	c.pollOnce(context.Background())

	if err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP aliyun_acs_vpn_ipsec_state CMS metric acs_vpn/ipsec.state
# TYPE aliyun_acs_vpn_ipsec_state gauge
aliyun_acs_vpn_ipsec_state{instanceId="vpn-a"} 1
`), "aliyun_acs_vpn_ipsec_state"); err != nil {
		t.Errorf("stale series should persist after a failed poll: %v", err)
	}
}

func TestPollOnceCountsAPICalls(t *testing.T) {
	spec := MetricSpec{Namespace: "acs_vpn", MetricName: "ipsec.state", Period: 60, Statistic: "Average"}
	f := &fakeLister{responses: map[string]string{"ipsec.state": `[]`}}
	reg := prometheus.NewRegistry()
	c := NewCollector(testConfig(spec), f, reg)
	c.pollOnce(context.Background())
	if got := testutil.ToFloat64(c.apiCalls); got != 1 {
		t.Errorf("cms_api_calls_total = %v, want 1", got)
	}
}

func TestPollOnceDoesNotAdvanceSuccessWhenSpecFails(t *testing.T) {
	spec := MetricSpec{Namespace: "acs_vpn", MetricName: "tun.state", Period: 60, Statistic: "Average", Dimensions: []string{"instanceId"}}
	f := &fakeLister{errs: map[string]error{"tun.state": errors.New("InternalError")}}
	reg := prometheus.NewRegistry()
	c := NewCollector(testConfig(spec), f, reg)
	c.pollOnce(context.Background())

	if got := testutil.ToFloat64(c.lastSuccess); got != 0 {
		t.Errorf("last_poll_success_timestamp_seconds = %v, want 0 (every spec errored)", got)
	}
}

func TestPollOnceAdvancesSuccessOnCleanCycle(t *testing.T) {
	spec := MetricSpec{Namespace: "acs_vpn", MetricName: "ipsec.state", Period: 60, Statistic: "Average", Dimensions: []string{"instanceId"}}
	f := &fakeLister{responses: map[string]string{"ipsec.state": `[{"timestamp":1,"instanceId":"vpn-a","Average":1}]`}}
	reg := prometheus.NewRegistry()
	c := NewCollector(testConfig(spec), f, reg)
	c.pollOnce(context.Background())

	if got := testutil.ToFloat64(c.lastSuccess); got <= 0 {
		t.Errorf("last_poll_success_timestamp_seconds = %v, want > 0 (clean cycle)", got)
	}
}

func TestRRSACredentialAbsentFallsBackToChain(t *testing.T) {
	for _, k := range []string{envRoleARN, envOIDCProvider, envOIDCTokenFil} {
		t.Setenv(k, "")
	}
	cred, err := rrsaCredential()
	if err != nil {
		t.Fatalf("no RRSA env should not error: %v", err)
	}
	if cred != nil {
		t.Errorf("cred = %v, want nil so the SDK uses its default chain", cred)
	}
}

func TestRRSACredentialPartialIsAnError(t *testing.T) {
	// A partial set means injection was attempted and did not complete. Falling
	// through would authenticate as the node, which is the failure this guards.
	cases := map[string][]string{
		"only role":      {envRoleARN},
		"role and token": {envRoleARN, envOIDCTokenFil},
		"only token":     {envOIDCTokenFil},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{envRoleARN, envOIDCProvider, envOIDCTokenFil} {
				t.Setenv(k, "")
			}
			for _, k := range set {
				t.Setenv(k, "value")
			}
			cred, err := rrsaCredential()
			if err == nil {
				t.Fatalf("cred = %v, want an error for a partial RRSA set", cred)
			}
			if !strings.Contains(err.Error(), "partially configured") {
				t.Errorf("error = %q, want it to name the partial configuration", err)
			}
		})
	}
}

func TestRRSACredentialSurfacesAssumeFailure(t *testing.T) {
	// A token file that does not exist stands in for any broken trust: the point
	// is that it errors here instead of silently using another provider.
	t.Setenv(envRoleARN, "acs:ram::1:role/r")
	t.Setenv(envOIDCProvider, "acs:ram::1:oidc-provider/p")
	t.Setenv(envOIDCTokenFil, "/nonexistent/rrsa-token")

	cred, err := rrsaCredential()
	if err == nil {
		t.Fatalf("cred = %v, want an error when the token cannot be read", cred)
	}
	if !strings.Contains(err.Error(), "acs:ram::1:role/r") {
		t.Errorf("error = %q, want it to name the role it failed to assume", err)
	}
}
