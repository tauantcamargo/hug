package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/tauantcamargo/hug/internal/policy"
)

// DecisionLog keeps the last N decisions in memory and appends every one to a JSONL file.
type DecisionLog struct {
	mu   sync.Mutex
	ring []policy.Decision
	max  int
	path string
}

// NewDecisionLog creates a log; path may be empty to disable persistence.
func NewDecisionLog(path string, max int) *DecisionLog {
	return &DecisionLog{path: path, max: max}
}

// Add records a decision.
func (l *DecisionLog) Add(d policy.Decision) {
	l.mu.Lock()
	l.ring = append(l.ring, d)
	if len(l.ring) > l.max {
		l.ring = l.ring[len(l.ring)-l.max:]
	}
	l.mu.Unlock()
	if l.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(l.path), 0o755)
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(d)
	_, _ = f.Write(append(b, '\n'))
}

// Recent returns up to n most recent decisions, newest last.
func (l *DecisionLog) Recent(n int) []policy.Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.ring) {
		n = len(l.ring)
	}
	out := make([]policy.Decision, n)
	copy(out, l.ring[len(l.ring)-n:])
	return out
}
