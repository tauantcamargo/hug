package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tauantcamargo/hug/internal/policy"
	"github.com/tauantcamargo/hug/internal/usage"
)

// chatGPTAuth reports whether the request carries a ChatGPT subscription token rather than an API key.
func chatGPTAuth(r *http.Request) bool {
	return r.Header.Get("Chatgpt-Account-Id") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer eyJ")
}

// codexCompat confines swaps to models that speak the wire protocol of the one the app picked.
// Only the ChatGPT-subscription backend has that split; the plain API does not.
func (s *Server) codexCompat(r *http.Request) policy.Compat {
	if !chatGPTAuth(r) {
		return s.compatFor("openai", nil)
	}
	return s.compatFor("openai", s.catalog.Compatible)
}

func (s *Server) openai(w http.ResponseWriter, r *http.Request) {
	upstream, rp := s.cfg.Upstreams.OpenAIAPI, s.oaiAPI
	if chatGPTAuth(r) {
		upstream, rp = s.cfg.Upstreams.OpenAIChatGPT, s.oaiChat
	}
	if isWebsocket(r) {
		s.openaiWebsocket(w, r, upstream)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses") {
		s.rewriteOpenAIHTTP(r)
	}
	rp.ServeHTTP(w, r)
}

func (s *Server) rewriteOpenAIHTTP(r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) == nil {
		if d := s.decide("openai", payload, r.Header.Get("Session-Id"), s.codexCompat(r)); d.Rewritten || d.Effort != "" {
			applyOpenAI(payload, d.Model, d.Effort, d.Rewritten)
			body = marshal(payload)
			if d.Rewritten {
				r.Header.Set("X-Codex-Routing-Hint", "model="+d.Model)
			}
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprint(len(body)))
}

func applyOpenAI(payload map[string]any, model, effort string, rewritten bool) {
	if rewritten {
		payload["model"] = model
	}
	if effort != "" {
		if reasoning, ok := payload["reasoning"].(map[string]any); ok {
			reasoning["effort"] = effort
		}
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(*http.Request) bool { return true },
	ReadBufferSize:  1 << 16,
	WriteBufferSize: 1 << 16,
}

// openaiWebsocket terminates the client websocket, dials upstream after the first client frame
// (so the routing decision can shape the handshake), and relays frames both ways.
func (s *Server) openaiWebsocket(w http.ResponseWriter, r *http.Request, upstream string) {
	u, err := url.Parse(upstream)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	target := *u
	target.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	target.Path = strings.TrimRight(u.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	hdr := http.Header{}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "sec-websocket") || lk == "connection" || lk == "upgrade" || lk == "host" || lk == "content-length" {
			continue
		}
		hdr[k] = v
	}

	client, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer client.Close()

	var up *websocket.Conn
	defer func() {
		if up != nil {
			up.Close()
		}
	}()
	session := r.Header.Get("Session-Id")
	compat := s.codexCompat(r)

	// client -> upstream
	errc := make(chan error, 2)
	go func() {
		for {
			mt, msg, err := client.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if mt == websocket.TextMessage {
				msg = s.rewriteOpenAIFrame(msg, session, hdr, compat)
			}
			if up == nil {
				conn, resp, err := websocket.DefaultDialer.Dial(target.String(), hdr)
				if err != nil {
					body := []byte{}
					if resp != nil {
						body, _ = io.ReadAll(io.LimitReader(resp.Body, 2000))
					}
					log.Printf("upstream websocket dial %s failed: %v %s", target.String(), err, body)
					_ = client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "hug: upstream dial failed"), time.Now().Add(time.Second))
					errc <- err
					return
				}
				up = conn
				go s.pumpUpstream(conn, client, errc)
			}
			if err := up.WriteMessage(mt, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	<-errc
}

func (s *Server) pumpUpstream(up, client *websocket.Conn, errc chan<- error) {
	for {
		mt, msg, err := up.ReadMessage()
		if err != nil {
			errc <- err
			return
		}
		if mt == websocket.TextMessage && bytes.Contains(msg[:min(len(msg), 64)], []byte("codex.rate_limits")) {
			if sn, ok := usage.ParseCodexRateLimits(msg, time.Now()); ok {
				s.recordUsage(sn)
			}
		}
		if err := client.WriteMessage(mt, msg); err != nil {
			errc <- err
			return
		}
	}
}

// rewriteOpenAIFrame applies policy to a `response.create` frame and keeps the routing hint header consistent.
func (s *Server) rewriteOpenAIFrame(msg []byte, session string, hdr http.Header, compat policy.Compat) []byte {
	var payload map[string]any
	if json.Unmarshal(msg, &payload) != nil || payload["type"] != "response.create" {
		return msg
	}
	d := s.decide("openai", payload, session, compat)
	if !d.Rewritten && d.Effort == "" {
		return msg
	}
	applyOpenAI(payload, d.Model, d.Effort, d.Rewritten)
	if d.Rewritten {
		hdr.Set("X-Codex-Routing-Hint", "model="+d.Model)
	}
	return marshal(payload)
}
