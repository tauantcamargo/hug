package proxy

import (
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

func TestRecordUsageFirstObservationIsQuiet(t *testing.T) {
	s, rec := newTestServer(true, "1ms")
	// Crossing straight into critical on the very first snapshot must not fire: there is
	// no prior tier to have "changed" from, and daemon startup would otherwise always alert.
	s.recordUsage(snap("anthropic", 0.95))
	if len(rec.Sent) != 0 {
		t.Fatalf("first observation must not notify: %v", rec.Sent)
	}
	if got := s.log.Since(0); len(got) != 0 {
		t.Fatalf("first observation must not log a tier-change: %+v", got)
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
