package wire

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const codexKey = "openai_base_url"

var (
	codexKeyRe   = regexp.MustCompile(`^\s*` + codexKey + `\s*=\s*"([^"]*)"`)
	tableStartRe = regexp.MustCompile(`^\s*\[`)
)

// topLevelEnd returns the index of the first table header; keys after it belong to that table.
func topLevelEnd(lines []string) int {
	for i, l := range lines {
		if tableStartRe.MatchString(l) {
			return i
		}
	}
	return len(lines)
}

// WireCodex sets a top-level openai_base_url in config.toml. The key must live before
// any [table]; otherwise TOML scopes it to that table and Codex silently ignores it.
func WireCodex(path, baseURL string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	end := topLevelEnd(lines)
	line := fmt.Sprintf("%s = %q", codexKey, baseURL)
	for i := 0; i < end; i++ {
		if m := codexKeyRe.FindStringSubmatch(lines[i]); m != nil {
			if m[1] == baseURL {
				return false, nil
			}
			if !IsHugURL(m[1]) {
				lines[i] = "# hug: previous " + strings.TrimSpace(lines[i]) + "\n" + line
			} else {
				lines[i] = line
			}
			return true, writeAtomic(path, []byte(strings.Join(lines, "\n")))
		}
	}
	out := append([]string{"# managed by hug — routes Codex through the local hug daemon", line, ""}, lines...)
	return true, writeAtomic(path, []byte(strings.Join(out, "\n")))
}

// UnwireCodex removes hug's openai_base_url and restores a commented previous value.
func UnwireCodex(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	end := topLevelEnd(lines)
	var out []string
	changed, skipBlank := false, false
	for i, l := range lines {
		if skipBlank && l == "" {
			skipBlank = false
			continue
		}
		skipBlank = false
		if i < end {
			if m := codexKeyRe.FindStringSubmatch(l); m != nil && IsHugURL(m[1]) {
				changed, skipBlank = true, true
				continue
			}
			if strings.HasPrefix(l, "# hug: previous ") {
				out = append(out, strings.TrimPrefix(l, "# hug: previous "))
				changed = true
				continue
			}
			if l == "# managed by hug — routes Codex through the local hug daemon" {
				changed = true
				continue
			}
		}
		out = append(out, l)
	}
	if !changed {
		return false, nil
	}
	return true, writeAtomic(path, []byte(strings.Join(out, "\n")))
}

func codexWired(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	for i := 0; i < topLevelEnd(lines); i++ {
		if m := codexKeyRe.FindStringSubmatch(lines[i]); m != nil {
			return IsHugURL(m[1])
		}
	}
	return false
}
