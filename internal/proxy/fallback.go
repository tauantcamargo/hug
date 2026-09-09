package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// modelRejection matches the vendor telling us the model itself is the problem, as opposed to
// anything about the request that a different model would also fail. These are permanent for
// this client and account: retrying the same model cannot fix them.
//
// Seen live: Claude Code 2.1.236 against claude-fable-5-1 returns
// "Claude Code 2.1.236 does not support this model; version 2.1.251 or newer is required".
var modelRejection = []string{
	"does not support this model",
	"not supported when using",
	"model not found",
	"does not have access to model",
	"is not available",
	"unsupported model",
	"invalid model",
}

// rejectedModel reports whether a response is the vendor refusing the model we picked. It reads
// only 4xx bodies, which are small and never streamed, so a healthy turn is untouched.
func rejectedModel(status int, body []byte) (string, bool) {
	if status < 400 || status >= 500 {
		return "", false
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	msg := string(body)
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
		if e.Error.Param == "model" {
			return msg, true
		}
	}
	low := strings.ToLower(msg)
	for _, m := range modelRejection {
		if strings.Contains(low, m) {
			return msg, true
		}
	}
	return "", false
}

// capturingWriter forwards a response to the client as it arrives, except for 4xx bodies, which
// it holds so the caller can decide to discard them and try another model instead. Streaming
// success paths are never buffered.
type capturingWriter struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	held    bool
	written bool
}

func (w *capturingWriter) WriteHeader(status int) {
	w.status = status
	if status >= 400 && status < 500 {
		w.held = true
		return
	}
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	if w.held {
		return w.body.Write(b)
	}
	if !w.written {
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// flush releases a held response to the client unchanged.
func (w *capturingWriter) flush() {
	if !w.held {
		return
	}
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.body.Bytes())
	w.held = false
}

// serveWithFallback sends the request, and if the vendor rejects the model hug chose, rewrites
// the body with the next model in the chain and sends it again. The app never sees the failure.
//
// Only rewritten requests are retried: if the vendor rejects the model the user picked, that is
// the user's answer to receive, not something hug should paper over.
func (s *Server) serveWithFallback(w http.ResponseWriter, r *http.Request, rp http.Handler, body []byte, vendor string, alternatives []string, setModel func(map[string]any, string) []byte) {
	for attempt := 0; ; attempt++ {
		cw := &capturingWriter{ResponseWriter: w}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))

		rp.ServeHTTP(cw, r)

		why, rejected := rejectedModel(cw.status, cw.body.Bytes())
		if !rejected || attempt >= len(alternatives) {
			cw.flush()
			return
		}

		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			cw.flush()
			return
		}
		failed, _ := payload["model"].(string)
		next := alternatives[attempt]
		s.markUnusable(vendor, failed, why)
		log.Printf("%s rejected %s, retrying with %s", vendor, failed, next)
		body = setModel(payload, next)
	}
}
