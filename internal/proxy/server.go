// Package proxy is the hug daemon: a local reverse proxy that classifies each model
// request into a phase, rewrites the model per policy, and learns usage from responses.
package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tauantcamargo/hug/internal/catalog"
	"github.com/tauantcamargo/hug/internal/config"
	"github.com/tauantcamargo/hug/internal/notify"
	"github.com/tauantcamargo/hug/internal/phase"
	"github.com/tauantcamargo/hug/internal/policy"
	"github.com/tauantcamargo/hug/internal/state"
	"github.com/tauantcamargo/hug/internal/usage"
)

// Version is set by the CLI at startup.
var Version = "dev"

// Server holds the proxies for every vendor.
type Server struct {
	cfg      config.Config
	usage    *usage.Store
	catalog  *catalog.Catalog
	log      *DecisionLog
	ship     *regexp.Regexp
	notifier notify.Notifier
	anth     *httputil.ReverseProxy
	oaiChat  *httputil.ReverseProxy
	oaiAPI   *httputil.ReverseProxy
	started  time.Time

	tierMu       sync.Mutex
	lastTier     map[string]policy.Tier
	lastNotifyAt map[string]time.Time
	lastRoute    map[string]string
}

// New builds a server from config. notifier may be nil to disable desktop notifications
// entirely (tier-change events still show up in `hug watch` and the daemon log). models may
// be nil for an in-memory catalog.
func New(cfg config.Config, store *usage.Store, dlog *DecisionLog, models *catalog.Catalog, notifier notify.Notifier) *Server {
	if notifier == nil {
		notifier = notify.NoopNotifier{}
	}
	if models == nil {
		models = catalog.Load("")
	}
	s := &Server{cfg: cfg, usage: store, catalog: models, log: dlog, ship: phase.ShipMatcher(cfg.Detect.ShipKeywords), notifier: notifier, started: time.Now(),
		lastTier: map[string]policy.Tier{}, lastNotifyAt: map[string]time.Time{}, lastRoute: map[string]string{}}
	s.anth = newReverseProxy(cfg.Upstreams.Anthropic)
	s.anth.ModifyResponse = func(res *http.Response) error {
		if sn, ok := usage.ParseAnthropicHeaders(res.Header, time.Now()); ok {
			s.recordUsage(sn)
		}
		return nil
	}
	s.oaiChat = newReverseProxy(cfg.Upstreams.OpenAIChatGPT)
	s.oaiChat.ModifyResponse = s.captureCodexModels
	s.oaiAPI = newReverseProxy(cfg.Upstreams.OpenAIAPI)
	return s
}

// Handler routes /anthropic/*, /openai/* and /hug/*.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hug/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/hug/status", s.status)
	mux.HandleFunc("/hug/decisions", s.decisionsSince)
	mux.HandleFunc("/hug/notify/test", s.notifyTest)
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

// decisionsSince serves `?since=N`: every decision newer than N, for `hug watch` to poll.
func (s *Server) decisionsSince(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.log.Since(since))
}

// notifyTest sends one notification through the real notifier. It exists because the daemon
// runs under a supervisor, and whether a desktop notification actually reaches the screen
// from there is not something the CLI's own process can answer — it has to be sent from here.
// It deliberately ignores notify.tier_changes and the cooldown: this is a delivery probe.
func (s *Server) notifyTest(w http.ResponseWriter, _ *http.Request) {
	err := s.notifier.Notify("hug", "test notification — delivery is working")
	w.Header().Set("Content-Type", "application/json")
	out := map[string]any{"sent": err == nil}
	if err != nil {
		out["error"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(out)
}

// recordUsage stores a fresh usage snapshot and, when it moves the vendor across a budget
// tier boundary, appends a synthetic "tier-change" decision and — if configured and past
// the cooldown — fires a desktop notification. This is the only place tier changes are
// detected, so `hug watch` and notifications never disagree about when one happened.
func (s *Server) recordUsage(sn usage.Snapshot) {
	s.usage.Set(sn)
	tier, why := policy.TierFor(s.cfg.Budget, &sn, time.Now())

	s.tierMu.Lock()
	prev, seen := s.lastTier[sn.Vendor]
	s.lastTier[sn.Vendor] = tier
	// A first observation normally just seeds the baseline. But if the daemon comes up
	// already degraded — a restart mid-window — staying quiet means you are downgraded
	// with no signal at all, since the transition into that tier happened while we were down.
	changed := seen && prev != tier || !seen && tier != policy.TierNormal
	notifyDue := changed && s.cfg.Notify.TierChanges && time.Since(s.lastNotifyAt[sn.Vendor]) >= s.cfg.Notify.CooldownDuration()
	if notifyDue {
		s.lastNotifyAt[sn.Vendor] = time.Now()
	}
	s.tierMu.Unlock()

	if !changed {
		return
	}
	d := policy.Decision{Time: time.Now(), Vendor: sn.Vendor, App: policy.AppFor(sn.Vendor), Phase: "tier-change", Tier: tier, Reason: why}
	s.log.Add(d)
	log.Printf("tier-change %-9s %-9s %s", d.App, d.Tier, d.Reason)
	if notifyDue {
		lead := "now"
		if !seen {
			lead = "already"
		}
		_ = s.notifier.Notify("hug: "+sn.Vendor, fmt.Sprintf("%s %s — %s", lead, tier, why))
	}
}

// clientGone reports whether a proxy error is just the caller hanging up mid-flight — an
// app closing a poll, a superseded request, a cancelled turn — rather than a real upstream
// failure. Nothing failed, and nobody is left to read a 502, so these are not worth logging.
func clientGone(err error, r *http.Request) bool {
	return errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled)
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
		if clientGone(err, r) {
			return
		}
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

// captureCodexModels learns each model's wire protocol from the list Codex fetches on start-up,
// which already flows through here. The body is handed on untouched; only a copy is parsed.
func (s *Server) captureCodexModels(res *http.Response) error {
	if res.StatusCode != http.StatusOK || res.Request == nil || !strings.HasSuffix(res.Request.URL.Path, "/models") {
		return nil
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	res.ContentLength = int64(len(body))
	res.Header.Set("Content-Length", strconv.Itoa(len(body)))

	raw := body
	if res.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil
		}
		if raw, err = io.ReadAll(zr); err != nil {
			return nil
		}
	}
	models, ok := catalog.ParseCodexModels(raw)
	if !ok {
		return nil
	}
	s.catalog.Update(models)
	lite := 0
	for _, m := range models {
		if m.ResponsesLite {
			lite++
		}
	}
	log.Printf("codex model list: %d models, %d responses-lite", len(models), lite)
	return nil
}

// auxiliary reports whether a request is one of the side calls agents make around a turn
// rather than the turn itself: conversation titles, classifiers, cache warmups.
//
// The decisive signal is the absence of a tool schema. An agentic turn always has tools in
// play; Claude Code's title request, for example, arrives with zero tools and a 3 KB system
// prompt next to the real turn's 30 tools and 27 KB. Routing those to the phase's top model
// spends premium budget on work the app already assigned to a cheap model.
func auxiliary(payload map[string]any) bool {
	if !hasToolSchema(payload) {
		return true
	}
	// Tools present but almost no output budget: a warmup or probe, not a turn.
	if maxTok, ok := payload["max_tokens"].(float64); ok && maxTok <= auxMaxTokens {
		return true
	}
	if maxOut, ok := payload["max_output_tokens"].(float64); ok && maxOut <= auxMaxTokens {
		return true
	}
	return false
}

// auxMaxTokens is the output budget at or below which a request counts as a probe.
const auxMaxTokens = 64

// hasToolSchema reports whether tools are in play for a request. Anthropic and the plain
// Responses API list them top-level. Codex over websocket does not: the schema rides once as
// an `additional_tools` input item in the thread's opening frame, and every turn after that is
// an incremental frame chained by previous_response_id that inherits it server-side. Seen live
// from codex-cli 0.153.4 — reading those as toolless swallowed every Codex turn as auxiliary.
func hasToolSchema(payload map[string]any) bool {
	if tools, _ := payload["tools"].([]any); len(tools) > 0 {
		return true
	}
	if prev, _ := payload["previous_response_id"].(string); prev != "" {
		return true
	}
	items, _ := payload["input"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m["type"] == "additional_tools" {
			return true
		}
	}
	return false
}

// decide runs detection and policy for one parsed request payload.
func (s *Server) decide(vendor string, payload map[string]any, session string, compat policy.Compat) policy.Decision {
	requested, _ := payload["model"].(string)
	if auxiliary(payload) {
		d := policy.Decision{Time: time.Now(), Vendor: vendor, App: policy.AppFor(vendor), Session: session,
			Phase: "aux", Requested: requested, Model: requested, Tier: policy.TierNormal,
			Reason: "auxiliary call (no tool schema) — left on the model the app chose"}
		s.log.Add(d)
		return d
	}
	var ph string
	if vendor == "anthropic" {
		ph = phase.DetectAnthropic(payload, s.ship)
	} else {
		ph = phase.DetectOpenAI(payload, s.ship)
	}
	d := policy.Decide(s.cfg, state.Load(), s.snapshot(vendor), vendor, ph, requested, time.Now(), compat)
	d.Session = session
	s.log.Add(d)
	log.Printf("%-9s %-9s %-24s -> %-24s %-9s %s", d.App, d.Phase, d.Requested, d.Model, d.Tier, d.Reason)
	s.notifyRouting(d)
	return d
}

// notifyRouting tells you when hug starts serving a phase from a different model than the one
// the app is showing. The apps display the model you picked, not the one that answered, so a
// rewrite is otherwise invisible. Keyed by app+phase rather than by session so that opening a
// new chat does not re-announce a routing you already know about.
func (s *Server) notifyRouting(d policy.Decision) {
	if !s.cfg.Notify.RoutingChanges || !d.Rewritten {
		return
	}
	key := d.App + "|" + d.Phase
	route := d.Requested + " -> " + d.Model

	s.tierMu.Lock()
	changed := s.lastRoute[key] != route
	s.lastRoute[key] = route
	s.tierMu.Unlock()

	if !changed {
		return
	}
	_ = s.notifier.Notify("hug: "+d.App+" "+d.Phase, route+" — "+string(d.Tier))
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
