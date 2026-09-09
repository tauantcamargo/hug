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
