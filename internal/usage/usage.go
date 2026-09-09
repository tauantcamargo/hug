// Package usage tracks subscription utilization per vendor, learned passively from
// Anthropic rate-limit headers and Codex `codex.rate_limits` websocket frames.
package usage

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Window is one rate-limit bucket (5h, 7d, primary, secondary or a per-model bucket).
type Window struct {
	Name          string    `json:"name"`
	Utilization   float64   `json:"utilization"` // 0..1
	ResetAt       time.Time `json:"reset_at,omitempty"`
	WindowMinutes int       `json:"window_minutes,omitempty"`
	Status        string    `json:"status,omitempty"` // allowed | allowed_warning | rejected
}

// Snapshot is the latest known usage for one vendor.
type Snapshot struct {
	Vendor    string            `json:"vendor"`
	Plan      string            `json:"plan,omitempty"`
	Status    string            `json:"status,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	Windows   []Window          `json:"windows"`
	PerModel  map[string]Window `json:"per_model,omitempty"`
}

// Pressure summarizes how close a vendor is to running out.
type Pressure struct {
	Utilization float64 `json:"utilization"` // tightest window now
	Projected   float64 `json:"projected"`   // utilization the burn rate projects at reset time
	Window      string  `json:"window"`
	Exhausted   bool    `json:"exhausted"`
}

// Pressure computes the tightest window and a burn-rate projection.
func (s Snapshot) Pressure(now time.Time) Pressure {
	var p Pressure
	for _, w := range s.Windows {
		if w.Status == "rejected" || w.Utilization >= 1 {
			p.Exhausted = true
		}
		if w.Utilization >= p.Utilization {
			p.Utilization = w.Utilization
			p.Window = w.Name
		}
		if w.WindowMinutes > 0 && !w.ResetAt.IsZero() {
			remaining := w.ResetAt.Sub(now).Minutes()
			elapsed := 1 - remaining/float64(w.WindowMinutes)
			if elapsed >= 0.10 && elapsed <= 1 {
				if proj := w.Utilization / elapsed; proj > p.Projected {
					p.Projected = proj
				}
			}
		}
	}
	if s.Status == "rejected" {
		p.Exhausted = true
	}
	return p
}

// Store keeps snapshots in memory and mirrors them to disk for `hug status`.
type Store struct {
	mu    sync.RWMutex
	path  string
	snaps map[string]Snapshot
}

// NewStore loads any persisted snapshots from path.
func NewStore(path string) *Store {
	s := &Store{path: path, snaps: map[string]Snapshot{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.snaps)
	}
	return s
}

// Set stores a snapshot and persists it.
func (s *Store) Set(sn Snapshot) {
	s.mu.Lock()
	s.snaps[sn.Vendor] = sn
	b, _ := json.MarshalIndent(s.snaps, "", "  ")
	s.mu.Unlock()
	if s.path != "" {
		_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
		_ = os.WriteFile(s.path, b, 0o644)
	}
}

// Get returns the snapshot for a vendor.
func (s *Store) Get(vendor string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sn, ok := s.snaps[vendor]
	return sn, ok
}

// All returns a copy of every snapshot.
func (s *Store) All() map[string]Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Snapshot, len(s.snaps))
	for k, v := range s.snaps {
		out[k] = v
	}
	return out
}

// ParseAnthropicHeaders reads the anthropic-ratelimit-unified-* response headers.
func ParseAnthropicHeaders(h http.Header, now time.Time) (Snapshot, bool) {
	get := func(k string) string { return h.Get("anthropic-ratelimit-unified-" + k) }
	u5, u7 := get("5h-utilization"), get("7d-utilization")
	if u5 == "" && u7 == "" {
		return Snapshot{}, false
	}
	sn := Snapshot{Vendor: "anthropic", UpdatedAt: now, Status: get("status")}
	add := func(name, util, reset, status string, minutes int) {
		if util == "" {
			return
		}
		f, err := strconv.ParseFloat(util, 64)
		if err != nil {
			return
		}
		w := Window{Name: name, Utilization: f, WindowMinutes: minutes, Status: status}
		if sec, err := strconv.ParseInt(reset, 10, 64); err == nil {
			w.ResetAt = time.Unix(sec, 0)
		}
		sn.Windows = append(sn.Windows, w)
	}
	add("5h", u5, get("5h-reset"), get("5h-status"), 5*60)
	add("7d", u7, get("7d-reset"), get("7d-status"), 7*24*60)
	return sn, true
}

type codexWindow struct {
	UsedPercent       float64 `json:"used_percent"`
	WindowMinutes     int     `json:"window_minutes"`
	ResetAfterSeconds int64   `json:"reset_after_seconds"`
	ResetAt           int64   `json:"reset_at"`
}

type codexLimits struct {
	Allowed      bool         `json:"allowed"`
	LimitReached bool         `json:"limit_reached"`
	Primary      *codexWindow `json:"primary"`
	Secondary    *codexWindow `json:"secondary"`
}

// ParseCodexRateLimits reads a `codex.rate_limits` server frame.
func ParseCodexRateLimits(frame []byte, now time.Time) (Snapshot, bool) {
	var f struct {
		Type       string                 `json:"type"`
		PlanType   string                 `json:"plan_type"`
		RateLimits *codexLimits           `json:"rate_limits"`
		Additional map[string]codexLimits `json:"additional_rate_limits"`
	}
	if json.Unmarshal(frame, &f) != nil || f.Type != "codex.rate_limits" {
		return Snapshot{}, false
	}
	sn := Snapshot{Vendor: "openai", Plan: f.PlanType, UpdatedAt: now, PerModel: map[string]Window{}}
	conv := func(name string, w *codexWindow, reached bool) (Window, bool) {
		if w == nil {
			return Window{}, false
		}
		out := Window{Name: name, Utilization: w.UsedPercent / 100, WindowMinutes: w.WindowMinutes}
		if w.ResetAt > 0 {
			out.ResetAt = time.Unix(w.ResetAt, 0)
		} else if w.ResetAfterSeconds > 0 {
			out.ResetAt = now.Add(time.Duration(w.ResetAfterSeconds) * time.Second)
		}
		if reached {
			out.Status = "rejected"
		}
		return out, true
	}
	if rl := f.RateLimits; rl != nil {
		if w, ok := conv("primary", rl.Primary, rl.LimitReached); ok {
			sn.Windows = append(sn.Windows, w)
		}
		if w, ok := conv("secondary", rl.Secondary, rl.LimitReached); ok {
			sn.Windows = append(sn.Windows, w)
		}
		if rl.LimitReached {
			sn.Status = "rejected"
		}
	}
	for model, rl := range f.Additional {
		if w, ok := conv(model, rl.Primary, rl.LimitReached); ok {
			sn.PerModel[model] = w
		}
	}
	return sn, true
}
