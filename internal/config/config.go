// Package config loads hug.toml: listen address, upstreams, per-phase model chains and budget policy.
package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Phase is a fallback chain per vendor, best model first, plus the effort hug asks for.
type Phase struct {
	Anthropic []string `toml:"anthropic"`
	OpenAI    []string `toml:"openai"`
	Effort    string   `toml:"effort"`
}

// Budget controls when hug steps down the chain to protect the subscription window.
type Budget struct {
	ConserveAt float64 `toml:"conserve_at"`
	CriticalAt float64 `toml:"critical_at"`
	Pace       bool    `toml:"pace"`
	PaceFactor float64 `toml:"pace_factor"`
}

// Detect holds heuristics that are not derivable from protocol markers.
type Detect struct {
	ShipKeywords []string `toml:"ship_keywords"`
}

// Notify controls desktop notifications on budget tier changes (see `hug watch` for the
// live terminal view of the same events).
type Notify struct {
	TierChanges    bool   `toml:"tier_changes"`
	RoutingChanges bool   `toml:"routing_changes"`
	Cooldown       string `toml:"cooldown"`
}

// CooldownDuration parses Cooldown, falling back to 5 minutes if unset or invalid.
func (n Notify) CooldownDuration() time.Duration {
	if d, err := time.ParseDuration(n.Cooldown); err == nil && d > 0 {
		return d
	}
	return 5 * time.Minute
}

// Upstreams are the real API hosts hug forwards to.
type Upstreams struct {
	Anthropic     string `toml:"anthropic"`
	OpenAIChatGPT string `toml:"openai_chatgpt"`
	OpenAIAPI     string `toml:"openai_api"`
}

// Config is the whole hug.toml.
type Config struct {
	Listen    string           `toml:"listen"`
	Upstreams Upstreams        `toml:"upstreams"`
	Phases    map[string]Phase `toml:"phases"`
	Budget    Budget           `toml:"budget"`
	Detect    Detect           `toml:"detect"`
	Notify    Notify           `toml:"notify"`
}

// DefaultTOML is written by `hug init` when no config exists.
const DefaultTOML = `# hug — route each phase of agentic work to the right model, across
# Claude Code, Codex, T3 Code, the Claude app (Code tab) and the ChatGPT app (Codex).

listen = "127.0.0.1:4711"

[upstreams]
anthropic      = "https://api.anthropic.com"
openai_chatgpt = "https://chatgpt.com/backend-api/codex"
openai_api     = "https://api.openai.com/v1"

# Each phase lists a fallback chain, best model first. hug walks down the chain
# as your subscription usage tightens (see [budget]).
[phases.plan]
anthropic = ["claude-fable-5-1", "claude-opus-5", "claude-sonnet-5"]
openai    = ["gpt-6-astra", "gpt-5.5"]
effort    = "xhigh"

[phases.implement]
anthropic = ["claude-opus-5", "claude-sonnet-5"]
openai    = ["gpt-6-astra", "gpt-5.5"]
effort    = "medium"

[phases.ship]
anthropic = ["claude-sonnet-5", "claude-haiku-4-5-20251001"]
openai    = ["gpt-5.6-luna", "gpt-5.5"]
effort    = "low"

[budget]
# utilization of the tightest window (0..1) at which hug steps down one model
conserve_at = 0.70
# utilization at which hug drops to the cheapest model in the chain
critical_at = 0.90
# also step down when the burn rate projects the window will run out before it resets
pace        = true
pace_factor = 1.15

[detect]
ship_keywords = ["commit", "pull request", "abrir pr", "abre um pr", "create a pr", "open a pr", "git push", "changelog", "release notes"]

[notify]
# desktop notification (macOS only today) when a vendor enters or leaves a budget tier.
# ` + "`hug watch`" + ` shows the same events live in a terminal, on every platform.
tier_changes = true
# notify when a phase starts being served by a different model than the one you picked.
# The apps show the model you selected, not the one that answered, so this is the only
# in-your-face signal that a rewrite happened.
routing_changes = true
cooldown     = "5m"
`

// Dir returns the hug home directory ($HUG_HOME or ~/.hug).
func Dir() string {
	if d := os.Getenv("HUG_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hug")
}

// Path returns the config file path.
func Path() string { return filepath.Join(Dir(), "hug.toml") }

// Default returns the built-in configuration.
func Default() Config {
	var c Config
	_ = toml.Unmarshal([]byte(DefaultTOML), &c)
	return c
}

// Load reads hug.toml on top of the defaults. A missing file is not an error.
func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

// EnsureDefault writes DefaultTOML if no config exists. Returns true when it created the file.
func EnsureDefault() (bool, error) {
	if _, err := os.Stat(Path()); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(Path(), []byte(DefaultTOML), 0o644)
}

// BaseURL is what apps must be pointed at for a vendor ("anthropic" or "openai").
func (c Config) BaseURL(vendor string) string { return "http://" + c.Listen + "/" + vendor }
