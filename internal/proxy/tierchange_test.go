package proxy

import (
	"strings"
	"testing"

	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/notify"
	"github.com/tauantcamargo/hug/internal/policy"
	"github.com/tauantcamargo/hug/internal/usage"
)

func newTestServer(notifyEnabled bool, cooldown string) (*Server, *notify.Recording) {
	cfg := config.Default()
	cfg.Notify.TierChanges = notifyEnabled
	cfg.Notify.Cooldown = cooldown
	rec := &notify.Recording{}
	return New(cfg, usage.NewStore(""), NewDecisionLog("", 50), rec), rec
}

func snap(vendor string, util float64) usage.Snapshot {
	return usage.Snapshot{Vendor: vendor, Windows: []usage.Window{{Name: "5h", Utilization: util}}}
}

func TestRecordUsageFirstObservationIsQuietWhenNormal(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	// Nothing is wrong and nothing changed: seeding the baseline must stay silent.
	s.recordUsage(snap("anthropic", 0.10))
	if len(rec.Sent) != 0 {
		t.Fatalf("first observation at normal must not notify: %v", rec.Sent)
	}
	if got := s.log.Since(0); len(got) != 0 {
		t.Fatalf("first observation at normal must not log a tier-change: %+v", got)
	}
}

// The daemon restarting mid-window is the common case here: the crossing into the degraded
// tier happened while we were down, so silence would leave the user downgraded with no signal.
func TestRecordUsageFirstObservationReportsDegraded(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	s.recordUsage(snap("anthropic", 0.95))
	if len(rec.Sent) != 1 {
		t.Fatalf("starting up already critical must notify once: %v", rec.Sent)
	}
	if !strings.Contains(rec.Sent[0], "already critical") {
		t.Fatalf("startup notification should read as a standing state, got %q", rec.Sent[0])
	}
	ds := s.log.Since(0)
	if len(ds) != 1 || ds[0].Phase != "tier-change" || ds[0].Tier != policy.TierCritical {
		t.Fatalf("expected one tier-change decision, got %+v", ds)
	}
	// The baseline is seeded either way, so a second snapshot in the same tier stays quiet.
	s.recordUsage(snap("anthropic", 0.96))
	if len(rec.Sent) != 1 {
		t.Fatalf("staying in the same tier must not notify again: %v", rec.Sent)
	}
}

func TestRecordUsageNotifiesOnTierChange(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	s.recordUsage(snap("anthropic", 0.10)) // seeds "normal"
	s.recordUsage(snap("anthropic", 0.95)) // -> critical
	if len(rec.Sent) != 1 {
		t.Fatalf("expected one notification, got %v", rec.Sent)
	}
	ds := s.log.Since(0)
	if len(ds) != 1 || ds[0].Phase != "tier-change" || ds[0].Tier != policy.TierCritical || ds[0].Vendor != "anthropic" {
		t.Fatalf("expected one tier-change decision, got %+v", ds)
	}
}

func TestRecordUsageIgnoresRepeatedSameTier(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	s.recordUsage(snap("anthropic", 0.10))
	s.recordUsage(snap("anthropic", 0.12)) // still normal
	s.recordUsage(snap("anthropic", 0.15)) // still normal
	if len(rec.Sent) != 0 || len(s.log.Since(0)) != 0 {
		t.Fatalf("no tier boundary crossed: sent=%v log=%+v", rec.Sent, s.log.Since(0))
	}
}

func TestRecordUsageRespectsCooldown(t *testing.T) {
	s, rec := newTestServer(true, "1h")
	s.recordUsage(snap("openai", 0.10))
	s.recordUsage(snap("openai", 0.95)) // notifies
	s.recordUsage(snap("openai", 0.10)) // reverts, but within the 1h cooldown
	if len(rec.Sent) != 1 {
		t.Fatalf("second transition inside the cooldown must not notify again: %v", rec.Sent)
	}
	// hug watch must still see both transitions even while the notifier is cooling down.
	if got := s.log.Since(0); len(got) != 2 {
		t.Fatalf("both tier-changes must still be logged: %+v", got)
	}
}

func TestRecordUsageDisabledStillLogsForWatch(t *testing.T) {
	s, rec := newTestServer(false, "1ms")
	s.recordUsage(snap("anthropic", 0.10))
	s.recordUsage(snap("anthropic", 0.95))
	if len(rec.Sent) != 0 {
		t.Fatalf("notify.tier_changes=false must send nothing: %v", rec.Sent)
	}
	if got := s.log.Since(0); len(got) != 1 {
		t.Fatalf("hug watch must still see the tier-change even with notifications off: %+v", got)
	}
}

func TestRecordUsageTracksVendorsIndependently(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	s.recordUsage(snap("anthropic", 0.10))
	s.recordUsage(snap("openai", 0.10))
	s.recordUsage(snap("anthropic", 0.95))
	if len(rec.Sent) != 1 || rec.Sent[0][:14] != "hug: anthropic" {
		t.Fatalf("openai's own baseline must not be disturbed by anthropic's change: %v", rec.Sent)
	}
}

// A rewrite is invisible in the apps' own UI, so the first time a phase starts being served
// by a different model we say so out loud — once, not on every request in that phase.
func TestNotifyRoutingAnnouncesOncePerRoute(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	swap := policy.Decision{App: "claude", Phase: "plan", Requested: "claude-sonnet-5",
		Model: "claude-opus-5", Tier: policy.TierConserve, Rewritten: true}

	s.notifyRouting(swap)
	s.notifyRouting(swap)
	if len(rec.Sent) != 1 {
		t.Fatalf("same route must announce once, got %v", rec.Sent)
	}
	if !strings.Contains(rec.Sent[0], "claude-sonnet-5 -> claude-opus-5") {
		t.Fatalf("notification should name both models, got %q", rec.Sent[0])
	}

	// A different destination for the same phase is news again.
	swap.Model = "claude-fable-5-1"
	s.notifyRouting(swap)
	if len(rec.Sent) != 2 {
		t.Fatalf("a new destination must announce, got %v", rec.Sent)
	}

	// Phases are tracked apart, so implement swapping does not depend on plan's history.
	s.notifyRouting(policy.Decision{App: "claude", Phase: "implement", Requested: "claude-sonnet-5",
		Model: "claude-haiku-4-5", Tier: policy.TierCritical, Rewritten: true})
	if len(rec.Sent) != 3 {
		t.Fatalf("a second phase must announce independently, got %v", rec.Sent)
	}
}

func TestNotifyRoutingSilentWhenNothingWasRewritten(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	s.notifyRouting(policy.Decision{App: "claude", Phase: "plan", Requested: "claude-opus-5",
		Model: "claude-opus-5", Tier: policy.TierNormal})
	if len(rec.Sent) != 0 {
		t.Fatalf("serving the requested model is not worth interrupting for: %v", rec.Sent)
	}
}
