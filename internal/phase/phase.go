// Package phase classifies a model request as plan, implement or ship using markers the
// agents themselves inject, plus an optional keyword heuristic for shipping work.
package phase

import (
	"regexp"
	"strings"
)

const (
	Plan      = "plan"
	Implement = "implement"
	Ship      = "ship"
)

// Claude Code appends this system-reminder to the user turn while plan mode is on.
const anthropicPlanMarker = "Plan mode is active"

// Codex puts the collaboration mode in developer instructions.
var codexPlanMarker = regexp.MustCompile(`(?is)<collaboration_mode>\s*plan\s*</collaboration_mode>`)

// ShipMatcher builds the keyword regexp from config; nil disables the heuristic.
func ShipMatcher(keywords []string) *regexp.Regexp {
	var parts []string
	for _, k := range keywords {
		if k = strings.TrimSpace(k); k != "" {
			parts = append(parts, regexp.QuoteMeta(k))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return regexp.MustCompile(`(?i)\b(` + strings.Join(parts, "|") + `)\b`)
}

// A ship keyword is a poor signal on its own: "commit" is a noun as often as a verb, so talking
// about the work looks exactly like asking for it. These narrow the keyword to an instruction.
var (
	// Determiners, possessives and prepositions turn the keyword into a thing being named:
	// "the commit", "your PR", "about a changelog".
	shipNamed = regexp.MustCompile(`(?i)(\b(the|a|an|this|that|these|those|my|your|our|its|their|each|every|last|first|next|previous|no|any|some|one|two|other|about|of|for|from|in|on|per|via)\s+|[/\-_.])$`)
	// A noun after it does the same: "commit message", "commit history".
	shipNaming = regexp.MustCompile(`(?i)^(s|es)?\s*(message|msg|hash|sha|id|history|log|graph|count|template|convention|format|style|body|title|description|number|flow|process|policy|hook|author|date|range|diff|tree|list)\b`)
	// Questions ask about shipping rather than order it -- unless they are a polite request.
	// An imperative that produces or publishes overrides the noun rules: "update the changelog"
	// orders the work even though a determiner follows, while "the changelog format is ..." and
	// "look at the last commit" only name it.
	shipDoing  = regexp.MustCompile(`(?i)^[\s,]*((ok(ay)?|now|then|and|also|please|next|finally|first)[\s,]+)*(update|write|add|bump|generate|create|prepare|draft|make|edit|fill|amend|squash|revert|tag|publish|release|ship|push|commit|open|cut|do)\b`)
	shipAsking = regexp.MustCompile(`(?i)^\s*(should|shall|do|does|did|is|are|was|were|has|have|had|what|why|how|when|where|which|who|whether|if)\b`)
	shipPolite = regexp.MustCompile(`(?i)\b(can|could|would|will)\s+(you|u)\b|\bplease\b|\blet'?s\b|\bgo ahead\b`)
	// What the agent runs when shipping is genuinely happening. Unlike the user's wording this
	// is unambiguous, so it is trusted on its own.
	shipCommand = regexp.MustCompile(`(?i)\bgit\s+(commit|push|tag)\b|\bgh\s+(pr|release)\s+create\b|\bgit\s+cherry-pick\b`)
)

// sentences splits on terminators and keeps them, so a trailing "?" is still visible to the
// caller deciding whether a clause asks about shipping or orders it.
func sentences(text string) []string {
	var out []string
	start := 0
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' || r == '\n' || r == ';' {
			out = append(out, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// shipIntent reports whether text tells the agent to ship, as opposed to discussing shipping.
// Each keyword hit has to survive being read as a noun and as a question.
func shipIntent(text string, ship *regexp.Regexp) bool {
	if ship == nil || text == "" {
		return false
	}
	for _, s := range sentences(text) {
		asking := shipAsking.MatchString(s) || strings.Contains(s, "?")
		if asking && !shipPolite.MatchString(s) {
			continue
		}
		ordering := shipDoing.MatchString(s)
		for _, loc := range ship.FindAllStringIndex(s, -1) {
			if !ordering && (shipNamed.MatchString(s[:loc[0]]) || shipNaming.MatchString(s[loc[1]:])) {
				continue
			}
			return true
		}
	}
	return false
}

// isSystemReminder reports whether a text block is one Claude Code injected rather than
// something the user or a tool result wrote.
func isSystemReminder(t string) bool {
	return strings.HasPrefix(strings.TrimSpace(t), "<system-reminder>")
}

// DetectAnthropic classifies a Messages API request body.
//
// Plan mode is read from the newest user message only, and only from a system-reminder block
// inside it. Claude Code rebuilds that reminder from the live permission mode on every request,
// so its presence means plan mode is on *now* -- which makes this self-clearing. Scanning the
// whole history instead made the phase sticky: an ExitPlanMode call ages out of the context on
// compaction while the marker survives in the summary, and the session never leaves plan again.
// Requiring the system-reminder wrapper is what keeps a user who merely typed, quoted or read
// the words "Plan mode is active" from being routed as planning for the rest of the session.
func DetectAnthropic(body map[string]any, ship *regexp.Regexp) string {
	msgs, _ := body["messages"].([]any)
	lastUser, newest := "", -1
	for i, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "user" {
			continue
		}
		newest = i
		for _, t := range textBlocks(mm["content"]) {
			if !isSystemReminder(t) {
				lastUser = t
			}
		}
	}
	if newest >= 0 {
		mm, _ := msgs[newest].(map[string]any)
		for _, t := range textBlocks(mm["content"]) {
			if isSystemReminder(t) && strings.Contains(t, anthropicPlanMarker) {
				return Plan
			}
		}
	}
	if ship != nil && (shipIntent(lastUser, ship) || shippingUnderway(msgs)) {
		return Ship
	}
	return Implement
}

// shippingUnderway reports whether the agent has actually run a shipping command in this
// conversation. The user's wording is ambiguous; `git commit` is not. Once the work has begun
// the remaining turns are mechanical, which is the whole reason the ship chain is cheap.
func shippingUnderway(msgs []any) bool {
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		blocks, _ := mm["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			if bm["type"] != "tool_use" {
				continue
			}
			in, _ := bm["input"].(map[string]any)
			cmd, _ := in["command"].(string)
			if shipCommand.MatchString(cmd) {
				return true
			}
		}
	}
	return false
}

// DetectOpenAI classifies a Responses API `response.create` payload (websocket frame or HTTP body).
func DetectOpenAI(frame map[string]any, ship *regexp.Regexp) string {
	lastDev, lastUser := "", ""
	if s, ok := frame["instructions"].(string); ok && strings.Contains(s, "<collaboration_mode>") {
		lastDev = s
	}
	items, _ := frame["input"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] != "message" && m["type"] != nil {
			continue
		}
		text := strings.Join(textBlocks(m["content"]), "\n")
		switch m["role"] {
		case "developer", "system":
			if strings.Contains(text, "<collaboration_mode>") {
				lastDev = text
			}
		case "user":
			if text != "" {
				lastUser = text
			}
		}
	}
	if codexPlanMarker.MatchString(lastDev) {
		return Plan
	}
	if shipIntent(lastUser, ship) {
		return Ship
	}
	return Implement
}

func textBlocks(content any) []string {
	switch c := content.(type) {
	case string:
		return []string{c}
	case []any:
		var out []string
		for _, b := range c {
			bm, _ := b.(map[string]any)
			if t, ok := bm["text"].(string); ok {
				out = append(out, t)
			}
		}
		return out
	}
	return nil
}

func toolUseNames(content any) []string {
	blocks, _ := content.([]any)
	var out []string
	for _, b := range blocks {
		bm, _ := b.(map[string]any)
		if bm["type"] == "tool_use" {
			if n, ok := bm["name"].(string); ok {
				out = append(out, n)
			}
		}
	}
	return out
}
