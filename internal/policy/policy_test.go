package policy

import (
	"testing"
	"time"

	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/state"
	"github.com/tauantcamargo/hug/internal/usage"
)

func snapAt(util float64, minutes int, resetIn time.Duration, now time.Time) *usage.Snapshot {
	return &usage.Snapshot{Vendor: "anthropic", Windows: []usage.Window{{Name: "5h", Utilization: util, WindowMinutes: minutes, ResetAt: now.Add(resetIn)}}}
}

func TestDecideTiers(t *testing.T) {
	cfg := config.Default()
	now := time.Now()
	on := state.State{Enabled: true}
	cases := []struct {
		name string
		snap *usage.Snapshot
		want string
		tier Tier
	}{
		{"no data", nil, "claude-fable-5-1", TierNormal},
		{"low usage", snapAt(0.20, 300, 4*time.Hour, now), "claude-fable-5-1", TierNormal},
		{"conserve", snapAt(0.75, 300, 2*time.Hour, now), "claude-opus-5", TierConserve},
		{"critical", snapAt(0.95, 300, 2*time.Hour, now), "claude-sonnet-5", TierCritical},
		// 40% used with 80% of the window still ahead: burn rate projects 200%.
		{"pace", snapAt(0.40, 300, 240*time.Minute, now), "claude-opus-5", TierConserve},
	}
	for _, c := range cases {
		d := Decide(cfg, on, c.snap, "anthropic", "plan", "claude-haiku-4-5-20251001", now)
		if d.Model != c.want || d.Tier != c.tier {
			t.Errorf("%s: got %s/%s want %s/%s (%s)", c.name, d.Model, d.Tier, c.want, c.tier, d.Reason)
		}
	}
}

func TestDecideSwitches(t *testing.T) {
	cfg := config.Default()
	now := time.Now()
	if d := Decide(cfg, state.State{Enabled: false}, nil, "anthropic", "plan", "x", now); d.Rewritten || d.Model != "x" {
		t.Errorf("off must pass through: %+v", d)
	}
	past := now.Add(-time.Minute)
	if d := Decide(cfg, state.State{Enabled: false, Until: &past}, nil, "anthropic", "plan", "x", now); !d.Rewritten {
		t.Errorf("expired temporary off must route again: %+v", d)
	}
	if d := Decide(cfg, state.State{Enabled: true, Pin: "claude-sonnet-5"}, nil, "anthropic", "plan", "x", now); d.Model != "claude-sonnet-5" || d.Reason != "pinned" {
		t.Errorf("pin: %+v", d)
	}
	if d := Decide(cfg, state.State{Enabled: true, Apps: map[string]bool{"codex": false}}, nil, "openai", "plan", "gpt-5.5", now); d.Rewritten {
		t.Errorf("app off: %+v", d)
	}
	if d := Decide(cfg, state.State{Enabled: true, Phases: map[string]bool{"ship": false}}, nil, "anthropic", "ship", "x", now); d.Rewritten {
		t.Errorf("phase off: %+v", d)
	}
}

func TestPerModelExhaustionSkips(t *testing.T) {
	cfg := config.Default()
	now := time.Now()
	snap := &usage.Snapshot{Vendor: "openai",
		Windows:  []usage.Window{{Name: "primary", Utilization: 0.3}},
		PerModel: map[string]usage.Window{"GPT-6-Astra": {Utilization: 1, Status: "rejected"}}}
	d := Decide(cfg, state.State{Enabled: true}, snap, "openai", "plan", "gpt-5.5", now)
	if d.Model != "gpt-5.5" {
		t.Errorf("exhausted model must be skipped, got %s", d.Model)
	}
	if d.Effort != "xhigh" {
		t.Errorf("effort at normal tier should be xhigh, got %s", d.Effort)
	}
}

func TestEffortStepsDown(t *testing.T) {
	if got := effortFor("xhigh", TierConserve); got != "high" {
		t.Errorf("conserve: %s", got)
	}
	if got := effortFor("medium", TierCritical); got != "low" {
		t.Errorf("critical clamps: %s", got)
	}
	if got := effortFor("", TierCritical); got != "" {
		t.Errorf("empty stays empty: %q", got)
	}
}
