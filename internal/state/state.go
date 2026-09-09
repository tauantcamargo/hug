// Package state is the on/off/pin switch. It is a tiny JSON file read on every request,
// so toggling never requires restarting the daemon or touching app configs.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tauantcamargo/hug/internal/config"
)

// State is the runtime switch.
type State struct {
	Enabled   bool            `json:"enabled"`
	Until     *time.Time      `json:"until,omitempty"`  // temporary off: re-enable at this time
	Pin       string          `json:"pin,omitempty"`    // force one model for every phase
	Apps      map[string]bool `json:"apps,omitempty"`   // "claude" | "codex" -> false disables
	Phases    map[string]bool `json:"phases,omitempty"` // "plan" | "implement" | "ship" -> false disables
	UpdatedAt time.Time       `json:"updated_at"`
}

// Path returns the state file location.
func Path() string { return filepath.Join(config.Dir(), "state.json") }

// Load reads the state; a missing file means enabled.
func Load() State {
	s := State{Enabled: true}
	b, err := os.ReadFile(Path())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// Save writes the state atomically.
func Save(s State) error {
	s.UpdatedAt = time.Now()
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// Active reports whether routing is on at the given time (temporary off expires automatically).
func (s State) Active(now time.Time) bool {
	if s.Enabled {
		return true
	}
	return s.Until != nil && !now.Before(*s.Until)
}

// AppEnabled reports whether routing is on for an app ("claude", "codex").
func (s State) AppEnabled(app string) bool {
	v, ok := s.Apps[app]
	return !ok || v
}

// PhaseEnabled reports whether routing is on for a phase.
func (s State) PhaseEnabled(phase string) bool {
	v, ok := s.Phases[phase]
	return !ok || v
}

// Summary is a one-line human description.
func (s State) Summary(now time.Time) string {
	out := "OFF"
	if s.Active(now) {
		out = "ON"
	} else if s.Until != nil {
		out = fmt.Sprintf("OFF until %s", s.Until.Local().Format("15:04"))
	}
	if s.Pin != "" {
		out += " · pinned to " + s.Pin
	}
	for k, v := range s.Apps {
		if !v {
			out += " · " + k + " off"
		}
	}
	for k, v := range s.Phases {
		if !v {
			out += " · " + k + " off"
		}
	}
	return out
}
