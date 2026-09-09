// Package policy decides which model serves a request, given the phase, the user's
// switch state and the vendor's current usage pressure.
package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/state"
	"github.com/tauantcamargo/hug/internal/usage"
)

// Tier is how aggressively hug is saving the subscription window.
type Tier string

const (
	TierNormal    Tier = "normal"
	TierConserve  Tier = "conserve"
	TierCritical  Tier = "critical"
	TierExhausted Tier = "exhausted"
)

// Decision is one routing outcome; it is logged and shown by `hug status` and `hug watch`.
// Phase "tier-change" is synthetic: the daemon emits one whenever a vendor's budget tier
// changes, independent of any single routed request, so watchers see it even between turns.
type Decision struct {
	Seq       uint64    `json:"seq"`
	Time      time.Time `json:"time"`
	Vendor    string    `json:"vendor"`
	App       string    `json:"app,omitempty"`
	Session   string    `json:"session,omitempty"`
	Phase     string    `json:"phase"`
	Requested string    `json:"requested"`
	Model     string    `json:"model"`
	Effort    string    `json:"effort,omitempty"`
	Tier      Tier      `json:"tier"`
	Reason    string    `json:"reason"`
	Rewritten bool      `json:"rewritten"`
}

var effortLadder = []string{"low", "medium", "high", "xhigh"}

// AppFor maps a vendor to the app switch name used by `hug on/off`.
func AppFor(vendor string) string {
	if vendor == "openai" {
		return "codex"
	}
	return "claude"
}

// TierFor classifies the current pressure for a vendor.
func TierFor(b config.Budget, snap *usage.Snapshot, now time.Time) (Tier, string) {
	if snap == nil || len(snap.Windows) == 0 {
		return TierNormal, "no usage data yet"
	}
	p := snap.Pressure(now)
	pct := fmt.Sprintf("%s window at %.0f%%", p.Window, p.Utilization*100)
	switch {
	case p.Exhausted:
		return TierExhausted, p.Window + " window exhausted"
	case p.Utilization >= b.CriticalAt:
		return TierCritical, pct
	case p.Utilization >= b.ConserveAt:
		return TierConserve, pct
	case b.Pace && p.Projected >= b.PaceFactor:
		return TierConserve, fmt.Sprintf("burn rate projects %.0f%% of the %s window before it resets", p.Projected*100, p.Window)
	}
	return TierNormal, pct
}

// Compat reports whether candidate can answer a request the app built for requested. nil means
// no constraint. The proxy supplies one for Codex, where the wire protocol follows the model
// the app believes it is talking to (see catalog.Compatible).
type Compat func(requested, candidate string) bool

func compatible(c Compat, requested, candidate string) bool {
	return c == nil || c(requested, candidate)
}

// usable keeps the chain entries that can stand in for requested, in order, so the tier still
// indexes a best-first ladder even when the configured chain mixes protocols.
func usable(chain []string, requested string, c Compat) []string {
	if c == nil {
		return chain
	}
	var out []string
	for _, m := range chain {
		if c(requested, m) {
			out = append(out, m)
		}
	}
	return out
}

// Decide picks the model for one request.
func Decide(cfg config.Config, st state.State, snap *usage.Snapshot, vendor, phase, requested string, now time.Time, compat Compat) Decision {
	d := Decision{Time: now, Vendor: vendor, App: AppFor(vendor), Phase: phase, Requested: requested, Model: requested, Tier: TierNormal}
	if !st.Active(now) {
		d.Reason = "hug is off"
		return d
	}
	if !st.AppEnabled(d.App) {
		d.Reason = "hug is off for " + d.App
		return d
	}
	if st.Pin != "" {
		if !compatible(compat, requested, st.Pin) {
			d.Reason = fmt.Sprintf("pinned %s is not known to share the wire protocol of %s — kept the app's model", st.Pin, requested)
			return d
		}
		d.Model = st.Pin
		d.Rewritten = d.Model != requested
		d.Reason = "pinned"
		return d
	}
	if !st.PhaseEnabled(phase) {
		d.Reason = "routing is off for phase " + phase
		return d
	}
	ph, ok := cfg.Phases[phase]
	if !ok {
		d.Reason = "no configuration for phase " + phase
		return d
	}
	chain := ph.Anthropic
	if vendor == "openai" {
		chain = ph.OpenAI
	}
	if len(chain) == 0 {
		d.Reason = "empty chain for " + vendor
		return d
	}
	tier, why := TierFor(cfg.Budget, snap, now)
	d.Tier = tier
	d.Effort = effortFor(ph.Effort, tier)
	kept := usable(chain, requested, compat)
	skipped := len(chain) - len(kept)
	if len(kept) == 0 {
		d.Reason = fmt.Sprintf("%s phase, %s tier: %s — nothing in the chain is known to share the wire protocol of %s, kept it", phase, tier, why, requested)
		return d
	}
	chain = kept
	idx := 0
	switch tier {
	case TierConserve:
		idx = 1
	case TierCritical, TierExhausted:
		idx = len(chain) - 1
	}
	if idx > len(chain)-1 {
		idx = len(chain) - 1
	}
	chosen := chain[len(chain)-1]
	for i := idx; i < len(chain); i++ {
		if !modelExhausted(snap, chain[i]) {
			chosen = chain[i]
			break
		}
	}
	d.Model = chosen
	d.Rewritten = chosen != requested
	d.Reason = fmt.Sprintf("%s phase, %s tier: %s", phase, tier, why)
	if skipped > 0 {
		d.Reason += fmt.Sprintf(" (%d chain model(s) skipped: not known to share the wire protocol of %s)", skipped, requested)
	}
	return d
}

func effortFor(base string, tier Tier) string {
	if base == "" {
		return ""
	}
	i := -1
	for k, e := range effortLadder {
		if e == base {
			i = k
		}
	}
	if i < 0 {
		return base
	}
	switch tier {
	case TierConserve:
		i--
	case TierCritical, TierExhausted:
		i -= 2
	}
	if i < 0 {
		i = 0
	}
	return effortLadder[i]
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func modelExhausted(snap *usage.Snapshot, model string) bool {
	if snap == nil {
		return false
	}
	want := normalize(model)
	for name, w := range snap.PerModel {
		if normalize(name) == want && (w.Status == "rejected" || w.Utilization >= 1) {
			return true
		}
	}
	return false
}
