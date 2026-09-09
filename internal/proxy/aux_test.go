package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/policy"
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
		// Shapes observed live from codex-cli 0.153.4 over websocket. No frame carries a
		// top-level tools array: the opening prewarm ships the schema as an input item, and
		// every real turn is an incremental frame that inherits it via previous_response_id.
		{"codex prewarm frame", `{"type":"response.create","model":"gpt-6-astra","generate":false,"tool_choice":"auto","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"wait"}]}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are Codex"}]}]}`, false},
		{"codex incremental turn", `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_0bd187","tool_choice":"auto","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Reply with exactly the word: pong"}]}]}`, false},
		{"codex fresh frame without any schema", `{"type":"response.create","model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize this thread in five words"}]}]}`, true},
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

// A degraded openai tier must actually reach Codex. Before hasToolSchema learned the websocket
// frame shapes, every Codex turn was filed as auxiliary and left on the model the app asked for.
func TestDecideRewritesCodexIncrementalTurn(t *testing.T) {
	s, _ := newTestServer(false, "1ms")
	s.recordUsage(snap("openai", 0.75)) // past conserve_at, below critical_at

	turn := parse(t, `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_0bd187","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the failing test"}]}]}`)
	d := s.decide("openai", turn, "thread-1", nil)
	if d.Phase != "implement" || d.Tier != policy.TierConserve {
		t.Fatalf("expected an implement decision at conserve, got %+v", d)
	}
	if !d.Rewritten || d.Model != "gpt-5.6-terra" {
		t.Fatalf("conserve must step the implement chain down to gpt-5.6-terra, got %+v", d)
	}
}

// Codex picks its wire protocol from the model it thinks it is using, so a swap to a model on
// the other side of that line is a hard 400 upstream. hug learns the line from the model list
// Codex fetches through it, and refuses to cross until it has seen one.
func TestCodexSwapStaysOnOneWireProtocol(t *testing.T) {
	s, _ := newTestServer(false, "1ms")
	s.cfg.Phases["implement"] = config.Phase{OpenAI: []string{"gpt-6-astra", "gpt-5.5"}, Effort: "medium"}
	s.recordUsage(snap("openai", 0.75))
	turn := parse(t, `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_1","input":[]}`)

	if d := s.decide("openai", turn, "t", s.catalog.Compatible); d.Rewritten {
		t.Fatalf("before any model list has been seen nothing may be swapped: %+v", d)
	}

	res := &http.Response{StatusCode: 200, Header: http.Header{},
		Request: &http.Request{URL: &url.URL{Path: "/backend-api/codex/models"}},
		Body:    io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-6-astra","use_responses_lite":true},{"slug":"gpt-5.6-luna","use_responses_lite":true},{"slug":"gpt-5.5","use_responses_lite":false}]}`))}
	if err := s.captureCodexModels(res); err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(res.Body); !strings.Contains(string(b), "gpt-5.5") {
		t.Fatalf("the app must still receive the full model list, got %q", b)
	}

	d := s.decide("openai", turn, "t", s.catalog.Compatible)
	if d.Rewritten || d.Model != "gpt-6-astra" || !strings.Contains(d.Reason, "wire protocol") {
		t.Fatalf("gpt-5.5 is not responses-lite: the swap must be refused and say why: %+v", d)
	}

	s.cfg.Phases["implement"] = config.Phase{OpenAI: []string{"gpt-6-astra", "gpt-5.6-luna"}, Effort: "medium"}
	if d := s.decide("openai", turn, "t", s.catalog.Compatible); !d.Rewritten || d.Model != "gpt-5.6-luna" {
		t.Fatalf("a lite-to-lite step down must go through: %+v", d)
	}
}
