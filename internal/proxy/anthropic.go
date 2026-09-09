package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tauantcamargo/hug/internal/policy"
)

func (s *Server) anthropic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages") {
		s.anth.ServeHTTP(w, r)
		return
	}
	body, d := s.rewriteAnthropic(r)
	if !d.Rewritten || len(d.Alternatives) == 0 {
		s.anth.ServeHTTP(w, r)
		return
	}
	s.serveWithFallback(w, r, s.anth, body, "anthropic", d.Alternatives, setAnthropicModel)
}

func setAnthropicModel(payload map[string]any, model string) []byte {
	payload["model"] = model
	return marshal(payload)
}

// rewriteAnthropic swaps the model in a Messages API body and returns the body it left on the
// request, so a caller that needs to send it more than once does not have to read it again.
func (s *Server) rewriteAnthropic(r *http.Request) ([]byte, policy.Decision) {
	body, err := io.ReadAll(r.Body)
	reset := func(b []byte) {
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Set("Content-Length", fmt.Sprint(len(b)))
	}
	if err != nil {
		// The body is already partly drained; hand back what we have rather than a reader that
		// is spent while Content-Length still promises the original size.
		reset(body)
		return body, policy.Decision{}
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		reset(body)
		return body, policy.Decision{}
	}
	d := s.decide("anthropic", payload, anthropicSession(payload), s.compatFor("anthropic", nil))
	if d.Rewritten {
		body = setAnthropicModel(payload, d.Model)
	}
	reset(body)
	return body, d
}

// anthropicSession extracts session_id from metadata.user_id, which Claude Code encodes as JSON.
func anthropicSession(payload map[string]any) string {
	meta, _ := payload["metadata"].(map[string]any)
	raw, _ := meta["user_id"].(string)
	var m struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(raw), &m) == nil {
		return m.SessionID
	}
	return ""
}
