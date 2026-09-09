package proxy

import (
	"encoding/json"
	"testing"
)

func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAuxiliary(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		// Shape observed live from Claude Code 2.1.266: the title call rides the same
		// max_tokens as the real turn, so only the empty tool schema separates them.
		{"claude title call", `{"model":"claude-haiku-4-5-20251001","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"<session>hi</session>\n\nWrite the title in the predominant language of the session"}]}`, true},
		{"claude agent turn", `{"model":"claude-opus-5","max_tokens":32000,"tools":[{"name":"Bash"},{"name":"Read"}],"messages":[]}`, false},
		{"codex turn", `{"type":"response.create","model":"gpt-5.5","tools":[{"name":"exec_command"}]}`, false},
		{"openai classifier", `{"model":"gpt-5.5","max_output_tokens":256}`, true},
		{"tools present but empty", `{"model":"claude-opus-5","tools":[],"max_tokens":32000}`, true},
		{"cache warmup with tools", `{"model":"claude-opus-5","tools":[{"name":"Bash"}],"max_tokens":1}`, true},
	}
	for _, c := range cases {
		if got := auxiliary(parse(t, c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
