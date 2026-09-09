package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

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
