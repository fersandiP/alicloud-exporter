package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	sdkerrors "github.com/aliyun/alibaba-cloud-sdk-go/sdk/errors"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cms"
	"github.com/prometheus/client_golang/prometheus"
)

// version is the exporter build version. Task 5's main package overrides it via
// -ldflags "-X main.version=..."; it is declared here so tests compile.
var version = "dev"

type datapoint map[string]any

func parseDatapoints(raw string) ([]datapoint, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var dps []datapoint
	if err := json.Unmarshal([]byte(raw), &dps); err != nil {
		return nil, fmt.Errorf("parse datapoints: %w", err)
	}
	return dps, nil
}

func (d datapoint) timestamp() float64 {
	if v, ok := d["timestamp"].(float64); ok {
		return v
	}
	return 0
}

func (d datapoint) statValue(stat string) (float64, bool) {
	v, ok := d[stat].(float64)
	return v, ok
}

func (d datapoint) labels(keys []string) prometheus.Labels {
	l := make(prometheus.Labels, len(keys))
	for _, k := range keys {
		if v, ok := d[k]; ok {
			l[k] = fmt.Sprint(v)
		} else {
			l[k] = ""
		}
	}
	return l
}

func latestByDimensions(dps []datapoint, keys []string) []datapoint {
	best := map[string]datapoint{}
	for _, dp := range dps {
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(fmt.Sprint(dp[k]))
			b.WriteByte(0)
		}
		key := b.String()
		if cur, ok := best[key]; !ok || dp.timestamp() > cur.timestamp() {
			best[key] = dp
		}
	}
	out := make([]datapoint, 0, len(best))
	for _, dp := range best {
		out = append(out, dp)
	}
	return out
}

func renderDimensions(sel map[string][]string) string {
	if len(sel) == 0 {
		return ""
	}
	combos := []map[string]string{{}}
	for k, vals := range sel {
		var next []map[string]string
		for _, c := range combos {
			for _, v := range vals {
				m := make(map[string]string, len(c)+1)
				for kk, vv := range c {
					m[kk] = vv
				}
				m[k] = v
				next = append(next, m)
			}
		}
		combos = next
	}
	b, _ := json.Marshal(combos)
	return string(b)
}

// backoffBase is the base delay for withBackoff's full-jitter schedule. It is a
// package var so tests can shrink it to keep sleeps negligible.
var backoffBase = time.Second

type listRequest struct {
	Namespace  string
	MetricName string
	Period     string
	StartTime  string
	EndTime    string
	Dimensions string
}

type metricLister interface {
	List(ctx context.Context, req listRequest) (datapoints string, err error)
}

// isThrottling reports whether err is an Alibaba Cloud throttling error, either a
// typed *sdkerrors.ServerError with a Throttling code or any error whose message
// contains the throttling markers (covers wrapped errors and test fakes).
func isThrottling(err error) bool {
	if err == nil {
		return false
	}
	var se *sdkerrors.ServerError
	if errors.As(err, &se) {
		switch se.ErrorCode() {
		case "Throttling.User", "Throttling":
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "Throttling.User") || strings.Contains(msg, "Throttling")
}

// withBackoff calls fn up to 5 times, invoking onAttempt (if non-nil) before
// every attempt. It retries only while isThrottling(err) is true, sleeping a
// full-jitter delay (base backoffBase, factor 2) between attempts and honouring
// ctx cancellation during the sleep.
func withBackoff(ctx context.Context, onAttempt func(), fn func() (string, error)) (string, error) {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if onAttempt != nil {
			onAttempt()
		}
		out, err := fn()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !isThrottling(err) {
			return "", err
		}
		if attempt == maxAttempts-1 {
			break
		}
		window := float64(backoffBase) * math.Pow(2, float64(attempt))
		sleep := time.Duration(rand.Int63n(int64(window) + 1)) // full jitter
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(sleep):
		}
	}
	return "", fmt.Errorf("throttled after %d attempts: %w", maxAttempts, lastErr)
}

type sdkLister struct {
	client *cms.Client
}

// newSDKLister builds a metricLister backed by the Alibaba Cloud Monitor SDK.
// Passing a nil credential to NewClientWithOptions makes the SDK use its default
// credentials provider chain (env AK/SK -> RRSA OIDC -> CLI/profile -> ECS RAM
// role), so this works both locally and in-cluster with RRSA.
func newSDKLister(regionID string) (metricLister, error) {
	client, err := cms.NewClientWithOptions(regionID, sdk.NewConfig(), nil)
	if err != nil {
		return nil, fmt.Errorf("create cms client: %w", err)
	}
	return &sdkLister{client: client}, nil
}

func (l *sdkLister) List(_ context.Context, req listRequest) (string, error) {
	r := cms.CreateDescribeMetricListRequest()
	r.Namespace = req.Namespace
	r.MetricName = req.MetricName
	r.Period = req.Period
	r.StartTime = req.StartTime
	r.EndTime = req.EndTime
	if req.Dimensions != "" {
		r.Dimensions = req.Dimensions
	}
	resp, err := l.client.DescribeMetricList(r)
	if err != nil {
		return "", err
	}
	if resp.Code != "" && resp.Code != "200" {
		return "", fmt.Errorf("cms DescribeMetricList code=%s message=%s", resp.Code, resp.Message)
	}
	return resp.Datapoints, nil
}

type Collector struct {
	cfg    *Config
	lister metricLister

	gauges map[string]*prometheus.GaugeVec // key: spec.FinalName()

	apiCalls     prometheus.Counter
	apiCallQuota prometheus.Gauge
	pollErrors   *prometheus.CounterVec
	lastSuccess  prometheus.Gauge
	lastDuration prometheus.Gauge
	buildInfo    *prometheus.GaugeVec
}

func specLabel(s MetricSpec) string { return s.Namespace + "/" + s.MetricName }

func NewCollector(cfg *Config, lister metricLister, reg prometheus.Registerer) *Collector {
	c := &Collector{
		cfg:    cfg,
		lister: lister,
		gauges: make(map[string]*prometheus.GaugeVec, len(cfg.Metrics)),
		apiCalls: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "alicloud_exporter_cms_api_calls_total",
			Help: "Total CMS DescribeMetricList HTTP attempts, including retries.",
		}),
		apiCallQuota: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alicloud_exporter_cms_api_call_quota",
			Help: "Documented monthly combined free quota for CMS query API calls.",
		}),
		pollErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alicloud_exporter_poll_errors_total",
			Help: "Per-spec poll or datapoint-parse failures.",
		}, []string{"spec"}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alicloud_exporter_last_poll_success_timestamp_seconds",
			Help: "Unix time of the last completed poll cycle.",
		}),
		lastDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alicloud_exporter_last_poll_duration_seconds",
			Help: "Wall-clock duration of the last poll cycle.",
		}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "alicloud_exporter_build_info",
			Help: "Build metadata; constant 1.",
		}, []string{"version", "goversion"}),
	}
	c.apiCallQuota.Set(1_000_000)
	c.buildInfo.WithLabelValues(version, runtime.Version()).Set(1)

	reg.MustRegister(c.apiCalls, c.apiCallQuota, c.pollErrors, c.lastSuccess, c.lastDuration, c.buildInfo)

	for _, s := range cfg.Metrics {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: s.FinalName(),
			Help: fmt.Sprintf("CMS metric %s/%s", s.Namespace, s.MetricName),
		}, s.Dimensions)
		reg.MustRegister(g)
		c.gauges[s.FinalName()] = g
	}
	return c
}

func (c *Collector) pollOnce(ctx context.Context) {
	start := time.Now()
	for _, s := range c.cfg.Metrics {
		if err := c.pollSpec(ctx, s); err != nil {
			log.Printf("poll %s: %v", specLabel(s), err)
			c.pollErrors.WithLabelValues(specLabel(s)).Inc()
		}
	}
	c.lastDuration.Set(time.Since(start).Seconds())
	c.lastSuccess.SetToCurrentTime()
}

func (c *Collector) pollSpec(ctx context.Context, s MetricSpec) error {
	now := time.Now()
	period := time.Duration(s.Period) * time.Second
	req := listRequest{
		Namespace:  s.Namespace,
		MetricName: s.MetricName,
		Period:     strconv.Itoa(s.Period),
		StartTime:  strconv.FormatInt(now.Add(-2*period).UnixMilli(), 10),
		EndTime:    strconv.FormatInt(now.UnixMilli(), 10),
		Dimensions: renderDimensions(s.DimensionSelect),
	}
	raw, err := withBackoff(ctx, c.apiCalls.Inc, func() (string, error) {
		return c.lister.List(ctx, req)
	})
	if err != nil {
		return err // series left intact — no Reset on failure
	}
	dps, err := parseDatapoints(raw)
	if err != nil {
		return err
	}
	g := c.gauges[s.FinalName()]
	g.Reset()
	var parseErr error
	for _, dp := range latestByDimensions(dps, s.Dimensions) {
		v, ok := dp.statValue(s.Statistic)
		if !ok {
			parseErr = fmt.Errorf("datapoint missing statistic %q", s.Statistic)
			continue
		}
		g.With(dp.labels(s.Dimensions)).Set(v)
	}
	return parseErr
}
