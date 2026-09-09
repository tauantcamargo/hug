// Package proxy is the hug daemon: a local reverse proxy that classifies each model
// request into a phase, rewrites the model per policy, and learns usage from responses.
package proxy

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/phase"
	"github.com/tauantcamargo/hug/internal/policy"
	"github.com/tauantcamargo/hug/internal/state"
	"github.com/tauantcamargo/hug/internal/usage"
)

// Version is set by the CLI at startup.
var Version = "dev"

// Server holds the proxies for every vendor.
type Server struct {
	cfg     config.Config
	usage   *usage.Store
	log     *DecisionLog
	ship    *regexp.Regexp
	anth    *httputil.ReverseProxy
	oaiChat *httputil.ReverseProxy
	oaiAPI  *httputil.ReverseProxy
	started time.Time
}

// New builds a server from config.
func New(cfg config.Config, store *usage.Store, dlog *DecisionLog) *Server {
	s := &Server{cfg: cfg, usage: store, log: dlog, ship: phase.ShipMatcher(cfg.Detect.ShipKeywords), started: time.Now()}
	s.anth = newReverseProxy(cfg.Upstreams.Anthropic)
	s.anth.ModifyResponse = func(res *http.Response) error {
		if sn, ok := usage.ParseAnthropicHeaders(res.Header, time.Now()); ok {
			s.usage.Set(sn)
		}
		return nil
	}
	s.oaiChat = newReverseProxy(cfg.Upstreams.OpenAIChatGPT)
	s.oaiAPI = newReverseProxy(cfg.Upstreams.OpenAIAPI)
	return s
}

// Handler routes /anthropic/*, /openai/* and /hug/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hug/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/hug/status", s.status)
	mux.Handle("/anthropic/", http.StripPrefix("/anthropic", http.HandlerFunc(s.anthropic)))
	mux.Handle("/openai/", http.StripPrefix("/openai", http.HandlerFunc(s.openai)))
	return mux
}

// Status is what `hug status` renders.
type Status struct {
	Version  string                    `json:"version"`
	Listen   string                    `json:"listen"`
	Uptime   string                    `json:"uptime"`
	State    state.State               `json:"state"`
	Active   bool                      `json:"active"`
	Usage    map[string]usage.Snapshot `json:"usage"`
	Pressure map[string]usage.Pressure `json:"pressure"`
	Tiers    map[string]TierInfo       `json:"tiers"`
	Recent   []policy.Decision         `json:"recent"`
}

// TierInfo explains the current tier for a vendor.
type TierInfo struct {
	Tier   policy.Tier `json:"tier"`
	Reason string      `json:"reason"`
}

// CurrentStatus builds the status payload.
func (s *Server) CurrentStatus() Status {
	now := time.Now()
	st := state.Load()
	out := Status{Version: Version, Listen: s.cfg.Listen, Uptime: now.Sub(s.started).Truncate(time.Second).String(),
		State: st, Active: st.Active(now), Usage: s.usage.All(), Pressure: map[string]usage.Pressure{}, Tiers: map[string]TierInfo{}, Recent: s.log.Recent(20)}
	for vendor, sn := range out.Usage {
		snap := sn
		out.Pressure[vendor] = snap.Pressure(now)
		tier, why := policy.TierFor(s.cfg.Budget, &snap, now)
		out.Tiers[vendor] = TierInfo{Tier: tier, Reason: why}
	}
	return out
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.CurrentStatus())
}

func newReverseProxy(upstream string) *httputil.ReverseProxy {
	u, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("bad upstream %q: %v", upstream, err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1
	director := rp.Director
	rp.Director = func(r *http.Request) {
		director(r)
		r.Host = u.Host
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("upstream error %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "hug: upstream error: "+err.Error(), http.StatusBadGateway)
	}
	return rp
}

func (s *Server) snapshot(vendor string) *usage.Snapshot {
	if sn, ok := s.usage.Get(vendor); ok {
		return &sn
	}
	return nil
}

// decide runs detection and policy for one parsed request payload.
func (s *Server) decide(vendor string, payload map[string]any, session string) policy.Decision {
	requested, _ := payload["model"].(string)
	var ph string
	if vendor == "anthropic" {
		ph = phase.DetectAnthropic(payload, s.ship)
	} else {
		ph = phase.DetectOpenAI(payload, s.ship)
	}
	d := policy.Decide(s.cfg, state.Load(), s.snapshot(vendor), vendor, ph, requested, time.Now())
	d.Session = session
	s.log.Add(d)
	log.Printf("%-9s %-9s %-24s -> %-24s %-9s %s", d.App, d.Phase, d.Requested, d.Model, d.Tier, d.Reason)
	return d
}

// marshal encodes without HTML escaping so bodies stay byte-for-byte close to the original.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func isWebsocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
