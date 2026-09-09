package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"
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
