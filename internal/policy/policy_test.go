package policy

import (
	"strings"
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
		d := Decide(cfg, on, c.snap, "anthropic", "plan", "claude-haiku-4-5-20251001", now, nil)
		if d.Model != c.want || d.Tier != c.tier {
			t.Errorf("%s: got %s/%s want %s/%s (%s)", c.name, d.Model, d.Tier, c.want, c.tier, d.Reason)
		}
	}
}

func TestDecideSwitches(t *testing.T) {
	cfg := config.Default()
	now := time.Now()
	if d := Decide(cfg, state.State{Enabled: false}, nil, "anthropic", "plan", "x", now, nil); d.Rewritten || d.Model != "x" {
		t.Errorf("off must pass through: %+v", d)
	}
	past := now.Add(-time.Minute)
	if d := Decide(cfg, state.State{Enabled: false, Until: &past}, nil, "anthropic", "plan", "x", now, nil); !d.Rewritten {
		t.Errorf("expired temporary off must route again: %+v", d)
	}
	if d := Decide(cfg, state.State{Enabled: true, Pin: "claude-sonnet-5"}, nil, "anthropic", "plan", "x", now, nil); d.Model != "claude-sonnet-5" || d.Reason != "pinned" {
		t.Errorf("pin: %+v", d)
	}
	if d := Decide(cfg, state.State{Enabled: true, Apps: map[string]bool{"codex": false}}, nil, "openai", "plan", "gpt-5.5", now, nil); d.Rewritten {
		t.Errorf("app off: %+v", d)
	}
	if d := Decide(cfg, state.State{Enabled: true, Phases: map[string]bool{"ship": false}}, nil, "anthropic", "ship", "x", now, nil); d.Rewritten {
		t.Errorf("phase off: %+v", d)
	}
}

func TestPerModelExhaustionSkips(t *testing.T) {
	cfg := config.Default()
	now := time.Now()
	snap := &usage.Snapshot{Vendor: "openai",
		Windows:  []usage.Window{{Name: "primary", Utilization: 0.3}},
		PerModel: map[string]usage.Window{"GPT-6-Astra": {Utilization: 1, Status: "rejected"}}}
	d := Decide(cfg, state.State{Enabled: true}, snap, "openai", "plan", "gpt-5.5", now, nil)
	if d.Model != "gpt-5.6-sol" {
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

// Codex's wire protocol follows the model the app selected, so a step down may only land on a
// model that speaks the same one. The chain is filtered first so the tier still walks a ladder.
func TestDecideHonorsCompat(t *testing.T) {
	cfg := config.Default()
	cfg.Phases["implement"] = config.Phase{OpenAI: []string{"gpt-6-astra", "gpt-5.5", "gpt-5.6-luna"}, Effort: "medium"}
	now := time.Now()
	on := state.State{Enabled: true}
	conserve := &usage.Snapshot{Vendor: "openai", Windows: []usage.Window{{Name: "primary", Utilization: 0.75}}}
	lite := map[string]bool{"gpt-6-astra": true, "gpt-5.6-luna": true}
	sameLite := func(requested, candidate string) bool { return lite[requested] == lite[candidate] }

	d := Decide(cfg, on, conserve, "openai", "implement", "gpt-6-astra", now, sameLite)
	if d.Model != "gpt-5.6-luna" || !d.Rewritten {
		t.Fatalf("conserve must skip gpt-5.5 and land on the next lite model: %+v", d)
	}
	if !strings.Contains(d.Reason, "1 chain model(s) skipped") {
		t.Fatalf("the skip must be visible in the reason: %q", d.Reason)
	}

	cfg.Phases["implement"] = config.Phase{OpenAI: []string{"gpt-5.5", "gpt-5.3-codex-spark"}, Effort: "medium"}
	d = Decide(cfg, on, conserve, "openai", "implement", "gpt-6-astra", now, sameLite)
	if d.Rewritten || d.Model != "gpt-6-astra" || !strings.Contains(d.Reason, "kept it") {
		t.Fatalf("with no compatible step down the app's model must be kept: %+v", d)
	}
	if d.Effort != "low" {
		t.Fatalf("effort still steps down with the tier even when the model cannot: %+v", d)
	}

	pinned := state.State{Enabled: true, Pin: "gpt-5.5"}
	d = Decide(cfg, pinned, nil, "openai", "implement", "gpt-6-astra", now, sameLite)
	if d.Rewritten || !strings.Contains(d.Reason, "pinned gpt-5.5") {
		t.Fatalf("a pin across the protocol line must be refused, not applied: %+v", d)
	}
	if d := Decide(cfg, pinned, nil, "openai", "implement", "gpt-5.3-codex-spark", now, sameLite); !d.Rewritten || d.Model != "gpt-5.5" {
		t.Fatalf("a pin on the same side must still apply: %+v", d)
	}
}
