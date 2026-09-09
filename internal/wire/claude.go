package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	envKey      = "ANTHROPIC_BASE_URL"
	envPrevious = "HUG_PREVIOUS_ANTHROPIC_BASE_URL"
)

// IsHugURL reports whether a base URL points at a local hug daemon.
func IsHugURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return (h == "127.0.0.1" || h == "localhost") && (u.Path == "/anthropic" || u.Path == "/openai")
}

func readSettings(path string) (map[string]any, error) {
	doc := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

func writeSettings(path string, doc map[string]any) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// sharedPath maps .../settings.local.json to the .../settings.json beside it. Claude Code reads
// and merges both; hug writes only the local one because the shared one is commonly committed to
// a dotfiles repo, and a machine-specific daemon URL must not travel to a machine without a daemon.
func sharedPath(localPath string) string {
	return strings.TrimSuffix(localPath, ".local.json") + ".json"
}

// WireClaude sets env.ANTHROPIC_BASE_URL in a Claude settings.local.json, remembering any previous
// value. If an older hug wrote the URL into the shared settings.json, it is cleaned up here so the
// two files cannot disagree.
func WireClaude(path, baseURL string) (bool, error) {
	migrated := false
	if shared := sharedPath(path); shared != path {
		if ok, err := unwireClaudeFile(shared); err != nil {
			return false, err
		} else if ok {
			migrated = true
		}
	}
	doc, err := readSettings(path)
	if err != nil {
		return false, err
	}
	env, _ := doc["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if cur, _ := env[envKey].(string); cur == baseURL {
		return migrated, nil
	} else if cur != "" && !IsHugURL(cur) {
		env[envPrevious] = cur
	}
	env[envKey] = baseURL
	doc["env"] = env
	return true, writeSettings(path, doc)
}

// UnwireClaude removes hug's ANTHROPIC_BASE_URL from the local settings and from the shared
// settings.json beside it, restoring a remembered previous value. Both are cleaned so that an
// install predating the settings.local.json move cannot leave a stale URL behind.
func UnwireClaude(path string) (bool, error) {
	changed, err := unwireClaudeFile(path)
	if err != nil {
		return false, err
	}
	if shared := sharedPath(path); shared != path {
		ok, err := unwireClaudeFile(shared)
		if err != nil {
			return changed, err
		}
		changed = changed || ok
	}
	return changed, nil
}

func unwireClaudeFile(path string) (bool, error) {
	doc, err := readSettings(path)
	if err != nil {
		return false, err
	}
	env, _ := doc["env"].(map[string]any)
	cur, _ := env[envKey].(string)
	if !IsHugURL(cur) {
		return false, nil
	}
	if prev, ok := env[envPrevious].(string); ok && prev != "" {
		env[envKey] = prev
	} else {
		delete(env, envKey)
	}
	delete(env, envPrevious)
	if len(env) == 0 {
		delete(doc, "env")
	} else {
		doc["env"] = env
	}
	return true, writeSettings(path, doc)
}

// claudeWired reports whether either file Claude Code merges points at hug. Checking only the
// local one would report a false ✗ for anyone who moved the env block by hand.
func claudeWired(path string) bool {
	if claudeFileWired(path) {
		return true
	}
	shared := sharedPath(path)
	return shared != path && claudeFileWired(shared)
}

func claudeFileWired(path string) bool {
	doc, err := readSettings(path)
	if err != nil {
		return false
	}
	env, _ := doc["env"].(map[string]any)
	cur, _ := env[envKey].(string)
	return IsHugURL(cur)
}
