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
		{"title generation", `{"model":"claude-haiku-4-5-20251001","max_tokens":512,"messages":[{"role":"user","content":"summarize"}]}`, true},
		{"agent turn", `{"model":"claude-opus-5","max_tokens":32000,"tools":[{"name":"Bash"}],"messages":[]}`, false},
		{"big output, no tools", `{"model":"claude-opus-5","max_tokens":32000,"messages":[]}`, false},
		{"codex turn", `{"type":"response.create","model":"gpt-5.5","tools":[{"name":"exec_command"}]}`, false},
		{"openai classifier", `{"model":"gpt-5.5","max_output_tokens":256}`, true},
		{"tools present but empty", `{"model":"claude-opus-5","tools":[],"max_tokens":1000}`, true},
	}
	for _, c := range cases {
		if got := auxiliary(parse(t, c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
