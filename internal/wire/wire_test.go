package wire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexInsertsBeforeTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	orig := "model = \"gpt-6-astra\"\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n"
	_ = os.WriteFile(p, []byte(orig), 0o644)
	changed, err := WireCodex(p, "http://127.0.0.1:4711/openai")
	if err != nil || !changed {
		t.Fatalf("wire: %v %v", changed, err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Index(s, "openai_base_url") > strings.Index(s, "[projects") {
		t.Fatalf("key must be above the first table:\n%s", s)
	}
	if !codexWired(p) {
		t.Fatal("should report wired")
	}
	if changed, _ = WireCodex(p, "http://127.0.0.1:4711/openai"); changed {
		t.Fatal("second wire must be a no-op")
	}
	if changed, _ = UnwireCodex(p); !changed {
		t.Fatal("unwire should change")
	}
	b, _ = os.ReadFile(p)
	if string(b) != orig {
		t.Fatalf("unwire must restore original:\n%q\n%q", string(b), orig)
	}
}

func TestCodexPreservesUserValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(p, []byte("openai_base_url = \"https://corp.example/v1\"\n[tui]\nx = 1\n"), 0o644)
	if _, err := WireCodex(p, "http://127.0.0.1:4711/openai"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "# hug: previous openai_base_url") || !strings.Contains(string(b), "127.0.0.1:4711/openai") {
		t.Fatalf("expected previous value kept as comment:\n%s", b)
	}
	if _, err := UnwireCodex(p); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if !strings.HasPrefix(string(b), "openai_base_url = \"https://corp.example/v1\"") {
		t.Fatalf("expected restore:\n%s", b)
	}
}

func TestClaudeRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(p, []byte(`{"model":"opusplan","env":{"FOO":"1"}}`), 0o644)
	if changed, err := WireClaude(p, "http://127.0.0.1:4711/anthropic"); err != nil || !changed {
		t.Fatalf("wire: %v %v", changed, err)
	}
	if !claudeWired(p) {
		t.Fatal("should be wired")
	}
	if changed, _ := WireClaude(p, "http://127.0.0.1:4711/anthropic"); changed {
		t.Fatal("idempotent")
	}
	if changed, err := UnwireClaude(p); err != nil || !changed {
		t.Fatalf("unwire: %v %v", changed, err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Contains(s, "ANTHROPIC_BASE_URL") || !strings.Contains(s, `"FOO": "1"`) || !strings.Contains(s, `"opusplan"`) {
		t.Fatalf("unwire must remove only our key:\n%s", s)
	}
	// missing file
	p2 := filepath.Join(t.TempDir(), "sub", "settings.json")
	if _, err := WireClaude(p2, "http://127.0.0.1:4711/anthropic"); err != nil {
		t.Fatal(err)
	}
	if !claudeWired(p2) {
		t.Fatal("new file should be wired")
	}
}

// Issue #2: settings.json is commonly committed to a dotfiles repo, so a 127.0.0.1 daemon URL
// written there follows you to a machine with no daemon and breaks every request. hug writes the
// per-machine settings.local.json instead, and cleans up the shared file if an older hug used it.
func TestClaudeWritesLocalAndMigratesShared(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "settings.json")
	local := claudeSettingsPath(dir)
	const url = "http://127.0.0.1:4711/anthropic"

	// An older hug put the URL in the shared file, next to the user's own settings.
	_ = os.WriteFile(shared, []byte(`{"model":"opusplan","env":{"ANTHROPIC_BASE_URL":"`+url+`","FOO":"1"}}`), 0o644)
	if !claudeWired(local) {
		t.Fatal("a URL in the shared file must still read as wired, or status reports a false ✗")
	}

	if _, err := WireClaude(local, url); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(shared)
	if strings.Contains(string(b), "ANTHROPIC_BASE_URL") {
		t.Fatalf("wiring must move the URL out of the synced file:\n%s", b)
	}
	if !strings.Contains(string(b), `"FOO": "1"`) || !strings.Contains(string(b), "opusplan") {
		t.Fatalf("migration must leave the user's own settings alone:\n%s", b)
	}
	if b, _ = os.ReadFile(local); !strings.Contains(string(b), url) {
		t.Fatalf("the per-machine file should now carry the URL:\n%s", b)
	}

	if _, err := UnwireClaude(local); err != nil {
		t.Fatal(err)
	}
	if claudeWired(local) {
		t.Fatal("unwire must clear both files")
	}
}

// hug off has to restore an install that predates the move, even though it never wrote that file.
func TestClaudeUnwireCleansSharedFile(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(shared, []byte(`{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:4711/anthropic"}}`), 0o644)

	changed, err := UnwireClaude(claudeSettingsPath(dir))
	if err != nil || !changed {
		t.Fatalf("unwire: %v %v", changed, err)
	}
	b, _ := os.ReadFile(shared)
	if strings.Contains(string(b), "ANTHROPIC_BASE_URL") {
		t.Fatalf("stale URL left behind:\n%s", b)
	}
}

func TestIsHugURL(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:4711/anthropic", "http://localhost:1/openai"} {
		if !IsHugURL(ok) {
			t.Errorf("%s should be hug", ok)
		}
	}
	for _, bad := range []string{"https://api.anthropic.com", "http://127.0.0.1:4711/other", ""} {
		if IsHugURL(bad) {
			t.Errorf("%s should not be hug", bad)
		}
	}
}
