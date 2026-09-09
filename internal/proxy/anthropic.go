package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (s *Server) anthropic(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/messages") {
		s.rewriteAnthropic(r)
	}
	s.anth.ServeHTTP(w, r)
}

func (s *Server) rewriteAnthropic(r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	reset := func(b []byte) {
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Set("Content-Length", fmt.Sprint(len(b)))
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		reset(body)
		return
	}
	d := s.decide("anthropic", payload, anthropicSession(payload), nil)
	if d.Rewritten {
		payload["model"] = d.Model
		body = marshal(payload)
	}
	reset(body)
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
