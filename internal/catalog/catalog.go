// Package catalog remembers what a vendor says about its models, learned passively from the
// model lists the apps fetch through hug. Today that is one fact per Codex model: which wire
// protocol it speaks.
package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Model is the subset of a Codex model entry that routing needs.
type Model struct {
	Slug          string `json:"slug"`
	ResponsesLite bool   `json:"use_responses_lite"`
}

// Catalog is an in-memory model table mirrored to disk so a daemon restart does not forget
// the list until the next app start fetches it again.
type Catalog struct {
	mu     sync.RWMutex
	path   string
	models map[string]Model
}

// Load reads any persisted catalog from path; an empty path keeps it in memory only.
func Load(path string) *Catalog {
	c := &Catalog{path: path, models: map[string]Model{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.models)
	}
	return c
}

// Update merges models into the catalog and persists it.
func (c *Catalog) Update(models []Model) {
	c.mu.Lock()
	for _, m := range models {
		if m.Slug != "" {
			c.models[m.Slug] = m
		}
	}
	b, _ := json.MarshalIndent(c.models, "", "  ")
	c.mu.Unlock()
	if c.path != "" {
		_ = os.MkdirAll(filepath.Dir(c.path), 0o755)
		_ = os.WriteFile(c.path, b, 0o644)
	}
}

// Lookup returns what is known about a model slug.
func (c *Catalog) Lookup(slug string) (Model, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.models[slug]
	return m, ok
}

// Len reports how many models are known.
func (c *Catalog) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models)
}

// Compatible reports whether candidate can answer a request the app built for requested.
//
// Codex chooses its wire protocol from the model it believes it is talking to: responses-lite
// models get a different handshake header and a different tool schema (the `exec` code-mode
// tool instead of classic function tools). The server rejects a lite request naming a non-lite
// model with a 400, so a swap across that line breaks the turn outright. Models hug has not
// seen in a model list are never swapped: guessing wrong costs the user a failed request,
// while not swapping costs nothing.
func (c *Catalog) Compatible(requested, candidate string) bool {
	if requested == candidate {
		return true
	}
	a, okA := c.Lookup(requested)
	b, okB := c.Lookup(candidate)
	return okA && okB && a.ResponsesLite == b.ResponsesLite
}

// ParseCodexModels reads the body of GET /backend-api/codex/models.
func ParseCodexModels(body []byte) ([]Model, bool) {
	var out struct {
		Models []Model `json:"models"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Models) == 0 {
		return nil, false
	}
	return out.Models, true
}
