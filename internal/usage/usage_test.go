package usage

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseAnthropicHeaders(t *testing.T) {
	now := time.Now()
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.48")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(47*time.Minute).Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.07")
	h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(6*24*time.Hour).Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	sn, ok := ParseAnthropicHeaders(h, now)
	if !ok || len(sn.Windows) != 2 || sn.Windows[0].Utilization != 0.48 || sn.Windows[1].WindowMinutes != 10080 {
		t.Fatalf("parse: %+v", sn)
	}
	p := sn.Pressure(now)
	if p.Window != "5h" || p.Utilization != 0.48 || p.Exhausted {
		t.Fatalf("pressure: %+v", p)
	}
	// 48% used with 84% of the 5h window elapsed -> ~57% projected, not a pace problem
	if p.Projected < 0.5 || p.Projected > 0.65 {
		t.Fatalf("projection: %+v", p)
	}
	if _, ok := ParseAnthropicHeaders(http.Header{}, now); ok {
		t.Fatal("no headers must not produce a snapshot")
	}
}

func TestParseCodexRateLimits(t *testing.T) {
	now := time.Now()
	frame := []byte(`{"type":"codex.rate_limits","plan_type":"prolite","rate_limits":{"allowed":true,"limit_reached":false,
	 "primary":{"used_percent":48,"window_minutes":10080,"reset_after_seconds":490790,"reset_at":` + strconv.FormatInt(now.Add(136*time.Hour).Unix(), 10) + `},"secondary":null},
	 "additional_rate_limits":{"GPT-5.3-Codex-Spark":{"allowed":false,"limit_reached":true,"primary":{"used_percent":100,"window_minutes":300,"reset_after_seconds":100,"reset_at":0}}}}`)
	sn, ok := ParseCodexRateLimits(frame, now)
	if !ok || sn.Plan != "prolite" || len(sn.Windows) != 1 || sn.Windows[0].Utilization != 0.48 {
		t.Fatalf("parse: %+v", sn)
	}
	spark := sn.PerModel["GPT-5.3-Codex-Spark"]
	if spark.Status != "rejected" || spark.Utilization != 1 || spark.ResetAt.IsZero() {
		t.Fatalf("per-model: %+v", spark)
	}
	if sn.Pressure(now).Exhausted {
		t.Fatal("per-model exhaustion must not mark the vendor exhausted")
	}
	if _, ok := ParseCodexRateLimits([]byte(`{"type":"response.created"}`), now); ok {
		t.Fatal("other frames must be ignored")
	}
}

func TestExhausted(t *testing.T) {
	sn := Snapshot{Windows: []Window{{Name: "5h", Utilization: 0.3}, {Name: "7d", Utilization: 1.0}}}
	p := sn.Pressure(time.Now())
	if !p.Exhausted || p.Window != "7d" {
		t.Fatalf("%+v", p)
	}
}
