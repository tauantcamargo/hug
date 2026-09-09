// Package wire points each installed app at the hug daemon by editing the config files
// the apps already read: Claude settings.json `env` blocks and Codex config.toml.
package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Kind identifies how a target is wired.
type Kind string

const (
	KindClaudeSettings Kind = "claude-settings"
	KindCodexConfig    Kind = "codex-config"
)

// Target is one config file hug manages.
type Target struct {
	App   string `json:"app"` // claude | codex
	Kind  Kind   `json:"kind"`
	Path  string `json:"path"`
	Label string `json:"label"`
}

// Discover finds every Claude Code home and Codex home on this machine, including the
// per-instance homes T3 Code creates.
func Discover() []Target {
	home, _ := os.UserHomeDir()
	var out []Target
	seen := map[string]bool{}
	add := func(t Target) {
		t.Path = expand(t.Path, home)
		if !seen[t.Path] {
			seen[t.Path] = true
			out = append(out, t)
		}
	}
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	add(Target{App: "claude", Kind: KindClaudeSettings, Path: filepath.Join(claudeHome, "settings.json"), Label: "Claude Code · Claude app (Code tab)"})
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	add(Target{App: "codex", Kind: KindCodexConfig, Path: filepath.Join(codexHome, "config.toml"), Label: "Codex CLI · ChatGPT app (Codex)"})

	for _, t := range t3Instances(filepath.Join(home, ".t3", "userdata", "settings.json")) {
		add(t)
	}
	return out
}

func t3Instances(path string) []Target {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		ProviderInstances map[string]struct {
			Driver  string `json:"driver"`
			Enabled *bool  `json:"enabled"`
			Config  struct {
				HomePath string `json:"homePath"`
			} `json:"config"`
		} `json:"providerInstances"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var out []Target
	for id, inst := range doc.ProviderInstances {
		if inst.Config.HomePath == "" || (inst.Enabled != nil && !*inst.Enabled) {
			continue
		}
		switch inst.Driver {
		case "claudeAgent":
			out = append(out, Target{App: "claude", Kind: KindClaudeSettings, Path: filepath.Join(inst.Config.HomePath, "settings.json"), Label: "T3 Code · " + id})
		case "codex":
			out = append(out, Target{App: "codex", Kind: KindCodexConfig, Path: filepath.Join(inst.Config.HomePath, "config.toml"), Label: "T3 Code · " + id})
		}
	}
	return out
}

func expand(p, home string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Apply wires or unwires one target. baseURL is the vendor base URL of the daemon.
func Apply(t Target, baseURL string, enable bool) (bool, error) {
	switch t.Kind {
	case KindClaudeSettings:
		if enable {
			return WireClaude(t.Path, baseURL)
		}
		return UnwireClaude(t.Path)
	case KindCodexConfig:
		if enable {
			return WireCodex(t.Path, baseURL)
		}
		return UnwireCodex(t.Path)
	}
	return false, nil
}

// Wired reports whether the target currently points at hug.
func Wired(t Target) bool {
	switch t.Kind {
	case KindClaudeSettings:
		return claudeWired(t.Path)
	case KindCodexConfig:
		return codexWired(t.Path)
	}
	return false
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".hug-tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
