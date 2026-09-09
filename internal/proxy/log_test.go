package proxy

import (
	"testing"

	"github.com/tauantcamargo/hug/internal/policy"
)

func TestDecisionLogSinceAndSeq(t *testing.T) {
	l := NewDecisionLog("", 10)
	l.Add(policy.Decision{Phase: "implement"})
	l.Add(policy.Decision{Phase: "plan"})
	l.Add(policy.Decision{Phase: "ship"})

	all := l.Recent(10)
	if len(all) != 3 || all[0].Seq != 1 || all[1].Seq != 2 || all[2].Seq != 3 {
		t.Fatalf("seq not assigned in order: %+v", all)
	}
	since := l.Since(1)
	if len(since) != 2 || since[0].Phase != "plan" || since[1].Phase != "ship" {
		t.Fatalf("Since(1) wrong: %+v", since)
	}
	if got := l.Since(3); len(got) != 0 {
		t.Fatalf("Since(latest seq) must be empty, got %+v", got)
	}
}

func TestDecisionLogRingCapPreservesSeq(t *testing.T) {
	l := NewDecisionLog("", 2)
	l.Add(policy.Decision{Phase: "a"})
	l.Add(policy.Decision{Phase: "b"})
	l.Add(policy.Decision{Phase: "c"}) // evicts "a"

	got := l.Recent(10)
	if len(got) != 2 || got[0].Phase != "b" || got[1].Phase != "c" {
		t.Fatalf("ring cap not enforced: %+v", got)
	}
	// A watcher that last saw seq 1 (the evicted entry) must still get everything after it.
	if since := l.Since(1); len(since) != 2 {
		t.Fatalf("Since across an evicted entry: %+v", since)
	}
}

// A watcher outlives a daemon restart: it holds a Seq from the previous process while the
// new log counts from zero again. It must resync instead of matching nothing forever.
func TestDecisionLogSinceAfterDaemonRestart(t *testing.T) {
	restarted := NewDecisionLog("", 10)

	// Nothing recorded yet by the new process; a stale watcher must not error or block.
	if got := restarted.Since(47); len(got) != 0 {
		t.Fatalf("stale Since on an empty log: %+v", got)
	}

	restarted.Add(policy.Decision{Phase: "plan"})
	restarted.Add(policy.Decision{Phase: "implement"})

	got := restarted.Since(47)
	if len(got) != 2 || got[0].Phase != "plan" || got[1].Phase != "implement" {
		t.Fatalf("stale watcher did not resync after restart: %+v", got)
	}
	// Having resynced, it tracks the new counter normally.
	if after := restarted.Since(got[len(got)-1].Seq); len(after) != 0 {
		t.Fatalf("Since(latest) after resync must be empty, got %+v", after)
	}
}
