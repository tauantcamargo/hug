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

// DetectAnthropic classifies a Messages API request body.
func DetectAnthropic(body map[string]any, ship *regexp.Regexp) string {
	msgs, _ := body["messages"].([]any)
	planIdx, exitIdx := -1, -1
	lastUser := ""
	for i, m := range msgs {
		mm, _ := m.(map[string]any)
		switch mm["role"] {
		case "user":
			for _, t := range textBlocks(mm["content"]) {
				if strings.Contains(t, anthropicPlanMarker) {
					planIdx = i
				}
				if !strings.HasPrefix(strings.TrimSpace(t), "<system-reminder>") {
					lastUser = t
				}
			}
		case "assistant":
			for _, n := range toolUseNames(mm["content"]) {
				if n == "ExitPlanMode" {
					exitIdx = i
				}
			}
		}
	}
	if planIdx > exitIdx {
		return Plan
	}
	if ship != nil && ship.MatchString(lastUser) {
		return Ship
	}
	return Implement
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
	if ship != nil && ship.MatchString(lastUser) {
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
