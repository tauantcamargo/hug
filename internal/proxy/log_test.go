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
