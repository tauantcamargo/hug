package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tauantcamargo/hug/internal/config"
)

func TestRejectedModel(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		// Verbatim from issue #1: Claude Code 2.1.236 asked for claude-fable-5-1.
		{"claude cli too old", 400,
			`{"type":"error","error":{"type":"invalid_request_error","message":"Claude Code 2.1.236 does not support this model; version 2.1.251 or newer is required."}}`, true},
		{"codex wire protocol", 400,
			`{"error":{"code":"unsupported_value","param":"model","message":"This model is not supported when using X-OpenAI-Internal-Codex-Responses-Lite."}}`, true},
		{"model not found", 404, `{"error":{"message":"model not found"}}`, true},
		// Must NOT retry: a different model would fail these too, and retrying burns budget.
		{"bad api key", 401, `{"error":{"message":"invalid x-api-key"}}`, false},
		{"rate limited", 429, `{"error":{"message":"rate limit exceeded"}}`, false},
		{"context too long", 400, `{"error":{"message":"prompt is too long: 300000 tokens > 200000"}}`, false},
		{"server error", 500, `{"error":{"message":"does not support this model"}}`, false},
		{"success", 200, `{"content":[]}`, false},
	}
	for _, c := range cases {
		if _, got := rejectedModel(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// Issue #1: a chain entry the client cannot use must cost one retry, not a failed turn.
func TestServeWithFallbackRetriesNextModel(t *testing.T) {
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &p)
		model, _ := p["model"].(string)
		seen = append(seen, model)
		if model == "claude-fable-5-1" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"Claude Code 2.1.236 does not support this model; version 2.1.251 or newer is required."}}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"content":[{"text":"ok"}]}`))
	}))
	defer upstream.Close()

	s, _ := newTestServer(false, "1ms")
	rp := newReverseProxy(upstream.URL)
	body := []byte(`{"model":"claude-fable-5-1","messages":[]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(string(body)))

	s.serveWithFallback(rec, req, rp, body, "anthropic", []string{"claude-opus-5", "claude-sonnet-5"}, setAnthropicModel)

	if rec.Code != 200 {
		t.Fatalf("the user should never see the 400, got %d: %s", rec.Code, rec.Body)
	}
	if len(seen) != 2 || seen[0] != "claude-fable-5-1" || seen[1] != "claude-opus-5" {
		t.Fatalf("expected a retry on the next chain model, upstream saw %v", seen)
	}
	// And the dud must not be tried again on later requests.
	if !s.isUnusable("anthropic", "claude-fable-5-1") {
		t.Fatal("a permanently rejected model must be remembered, not re-tried every request")
	}
	if s.isUnusable("anthropic", "claude-opus-5") {
		t.Fatal("the model that worked must stay usable")
	}
}

// An error that another model would hit too must reach the user unchanged, and cost one call.
func TestServeWithFallbackPassesThroughRealErrors(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
	}))
	defer upstream.Close()

	s, _ := newTestServer(false, "1ms")
	body := []byte(`{"model":"claude-opus-5","messages":[]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(string(body)))

	s.serveWithFallback(rec, req, newReverseProxy(upstream.URL), body, "anthropic", []string{"claude-sonnet-5"}, setAnthropicModel)

	if rec.Code != 401 || calls != 1 {
		t.Fatalf("auth failure must pass straight through: code=%d calls=%d", rec.Code, calls)
	}
	if !strings.Contains(rec.Body.String(), "invalid x-api-key") {
		t.Fatalf("the vendor's own message must survive: %s", rec.Body)
	}
}

// Once a model is known-bad, routing must stop selecting it at all.
func TestUnusableModelIsSkippedByRouting(t *testing.T) {
	s, _ := newTestServer(false, "1ms")
	s.cfg.Phases["implement"] = config.Phase{Anthropic: []string{"claude-fable-5-1", "claude-opus-5"}, Effort: "medium"}
	payload := map[string]any{"model": "claude-sonnet-5", "tools": []any{map[string]any{"name": "Bash"}}, "max_tokens": 32000.0}

	d := s.decide("anthropic", payload, "sess", s.compatFor("anthropic", nil))
	if d.Model != "claude-fable-5-1" {
		t.Fatalf("expected the top of the chain first, got %s", d.Model)
	}
	s.markUnusable("anthropic", "claude-fable-5-1", "client too old")

	d = s.decide("anthropic", payload, "sess", s.compatFor("anthropic", nil))
	if d.Model != "claude-opus-5" {
		t.Fatalf("a known-bad model must be skipped, got %s", d.Model)
	}
}
