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
