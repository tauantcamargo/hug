package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
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

// WireClaude sets env.ANTHROPIC_BASE_URL in a Claude settings.json, remembering any previous value.
func WireClaude(path, baseURL string) (bool, error) {
	doc, err := readSettings(path)
	if err != nil {
		return false, err
	}
	env, _ := doc["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if cur, _ := env[envKey].(string); cur == baseURL {
		return false, nil
	} else if cur != "" && !IsHugURL(cur) {
		env[envPrevious] = cur
	}
	env[envKey] = baseURL
	doc["env"] = env
	return true, writeSettings(path, doc)
}

// UnwireClaude removes hug's ANTHROPIC_BASE_URL, restoring a remembered previous value.
func UnwireClaude(path string) (bool, error) {
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

func claudeWired(path string) bool {
	doc, err := readSettings(path)
	if err != nil {
		return false
	}
	env, _ := doc["env"].(map[string]any)
	cur, _ := env[envKey].(string)
	return IsHugURL(cur)
}
