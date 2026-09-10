package phase

import (
	"encoding/json"
	"testing"
)

func body(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic(err)
	}
	return m
}

func TestAnthropic(t *testing.T) {
	ship := ShipMatcher([]string{"commit", "pull request"})
	plan := body(`{"messages":[{"role":"user","content":[{"type":"text","text":"add a readme"},{"type":"text","text":"<system-reminder>\nPlan mode is active. The user indicated...</system-reminder>"}]}]}`)
	if got := DetectAnthropic(plan, ship); got != Plan {
		t.Errorf("plan marker: got %s", got)
	}
	exited := body(`{"messages":[
	 {"role":"user","content":[{"type":"text","text":"<system-reminder>Plan mode is active</system-reminder>"},{"type":"text","text":"plan it"}]},
	 {"role":"assistant","content":[{"type":"tool_use","name":"ExitPlanMode","id":"1","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"go ahead"}]}]}`)
	if got := DetectAnthropic(exited, ship); got != Implement {
		t.Errorf("after ExitPlanMode: got %s", got)
	}
	shipReq := body(`{"messages":[{"role":"user","content":"please commit this and open a pull request"}]}`)
	if got := DetectAnthropic(shipReq, ship); got != Ship {
		t.Errorf("ship keywords: got %s", got)
	}
	if got := DetectAnthropic(shipReq, nil); got != Implement {
		t.Errorf("ship disabled: got %s", got)
	}
	if got := DetectAnthropic(body(`{"messages":[{"role":"user","content":"fix the bug"}]}`), ship); got != Implement {
		t.Errorf("default: got %s", got)
	}
}

// Issue #4: a ship keyword is a noun as often as a verb, so talking about the work looked
// exactly like asking for it and quietly routed the turn to the cheapest model.
func TestShipIntentSeparatesOrdersFromTalk(t *testing.T) {
	ship := ShipMatcher([]string{"commit", "pull request", "create a pr", "open a pr", "git push", "changelog", "release notes"})

	orders := []string{
		"commit this",
		"ok now commit and push it",
		"please open a pr against main",
		"let's commit what we have so far",
		"can you commit this and write release notes?",
		"go ahead and create a pr",
		"fix the test then git push",
		"update the changelog",
	}
	for _, s := range orders {
		if !shipIntent(s, ship) {
			t.Errorf("should read as ship: %q", s)
		}
	}

	talk := []string{
		// The reporter's case: discussing the feature, not using it.
		"we were talking about the commit flow, not doing it",
		"how does hug detect a commit?",
		"should we commit this?",
		"what goes in the changelog?",
		"the commit message convention here is lowercase",
		"read the pull request template",
		"explain how a commit is different from a pull request",
		"this commit broke the build",
		"look at the last commit",
		"the changelog format is keepachangelog",
	}
	for _, s := range talk {
		if shipIntent(s, ship) {
			t.Errorf("should NOT read as ship: %q", s)
		}
	}

	if shipIntent("commit this", nil) {
		t.Error("a nil matcher disables the heuristic entirely")
	}
}

// Once the agent has actually run a shipping command, wording stops mattering: the work is
// demonstrably underway and the remaining turns are mechanical.
func TestShipDetectedFromGitActivity(t *testing.T) {
	ship := ShipMatcher([]string{"commit"})
	underway := body(`{"messages":[
	 {"role":"user","content":[{"type":"text","text":"wrap this up"}]},
	 {"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"1","input":{"command":"git commit -m 'fix'"}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"[main abc123]"},{"type":"text","text":"now the tag"}]}]}`)
	if got := DetectAnthropic(underway, ship); got != Ship {
		t.Errorf("a real git commit must put the session in ship, got %s", got)
	}

	reading := body(`{"messages":[
	 {"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"1","input":{"command":"git log --oneline -5"}}]},
	 {"role":"user","content":[{"type":"text","text":"what changed here?"}]}]}`)
	if got := DetectAnthropic(reading, ship); got != Implement {
		t.Errorf("reading history is not shipping, got %s", got)
	}
}

// Issue #3: reading or quoting the words must not hijack the session.
func TestAnthropicPlanMarkerNeedsSystemReminder(t *testing.T) {
	ship := ShipMatcher([]string{"commit"})

	quoted := body(`{"messages":[{"role":"user","content":[{"type":"text","text":"the detector looks for Plan mode is active in the user turn — is that right?"}]}]}`)
	if got := DetectAnthropic(quoted, ship); got != Implement {
		t.Errorf("plain prose holding the marker must not read as plan, got %s", got)
	}

	// A tool result echoing hug's own source is the reporter's exact scenario.
	fromFile := body(`{"messages":[
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"const anthropicPlanMarker = \"Plan mode is active\""},{"type":"text","text":"explain this"}]}]}`)
	if got := DetectAnthropic(fromFile, ship); got != Implement {
		t.Errorf("source code in a tool result must not read as plan, got %s", got)
	}

	real := body(`{"messages":[{"role":"user","content":[{"type":"text","text":"design it"},{"type":"text","text":"<system-reminder>\nPlan mode is active. The user indicated...</system-reminder>"}]}]}`)
	if got := DetectAnthropic(real, ship); got != Plan {
		t.Errorf("the injected reminder must still read as plan, got %s", got)
	}
}

// Claude Code rebuilds the reminder from the live mode each request, so an older turn carrying
// it means nothing once it is gone from the newest one. Previously this stuck for good after a
// compaction dropped the ExitPlanMode call that used to clear it.
func TestAnthropicPlanIsNotStickyAcrossTurns(t *testing.T) {
	ship := ShipMatcher([]string{"commit"})
	stale := body(`{"messages":[
	 {"role":"user","content":[{"type":"text","text":"<system-reminder>Plan mode is active</system-reminder>"},{"type":"text","text":"plan it"}]},
	 {"role":"assistant","content":[{"type":"text","text":"here is the plan"}]},
	 {"role":"user","content":[{"type":"text","text":"go ahead and build it"}]}]}`)
	if got := DetectAnthropic(stale, ship); got != Implement {
		t.Errorf("plan must not outlive the reminder, got %s", got)
	}

	// And a compaction summary that quotes the marker must not resurrect it either.
	compacted := body(`{"messages":[
	 {"role":"user","content":[{"type":"text","text":"Summary: the session began with <system-reminder>Plan mode is active</system-reminder> and then we implemented it."}]},
	 {"role":"user","content":[{"type":"text","text":"keep going"}]}]}`)
	if got := DetectAnthropic(compacted, ship); got != Implement {
		t.Errorf("a summary quoting the marker must not read as plan, got %s", got)
	}
}

func TestOpenAI(t *testing.T) {
	ship := ShipMatcher([]string{"commit"})
	plan := body(`{"type":"response.create","model":"gpt-5.5","instructions":"You are Codex","input":[
	 {"type":"message","role":"developer","content":[{"type":"input_text","text":"Mode: <collaboration_mode>Plan</collaboration_mode> ..."}]},
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"design the api"}]}]}`)
	if got := DetectOpenAI(plan, ship); got != Plan {
		t.Errorf("codex plan: got %s", got)
	}
	def := body(`{"type":"response.create","model":"gpt-5.5","input":[
	 {"type":"message","role":"developer","content":[{"type":"input_text","text":"<collaboration_mode>Default</collaboration_mode>"}]},
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"commit it"}]}]}`)
	if got := DetectOpenAI(def, ship); got != Ship {
		t.Errorf("codex ship: got %s", got)
	}
	if got := DetectOpenAI(body(`{"type":"response.create","model":"gpt-5.5","input":[]}`), ship); got != Implement {
		t.Errorf("codex default: got %s", got)
	}
}
