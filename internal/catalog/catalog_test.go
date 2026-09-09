package catalog

import (
	"path/filepath"
	"testing"
)

// Trimmed from a live GET /backend-api/codex/models (codex-cli 0.153.4, 2026-09-09).
const modelsBody = `{"models":[
 {"slug":"gpt-6-astra","display_name":"GPT-6-Astra","use_responses_lite":true,"tool_mode":"code_mode_only","visibility":"list"},
 {"slug":"gpt-5.6-luna","display_name":"GPT-5.6-Luna","use_responses_lite":true,"tool_mode":"code_mode_only","visibility":"list"},
 {"slug":"gpt-5.5","display_name":"GPT-5.5","use_responses_lite":false,"visibility":"list"},
 {"slug":"gpt-5.3-codex-spark","display_name":"GPT-5.3-Codex-Spark","use_responses_lite":false,"visibility":"list"}]}`

func TestParseCodexModels(t *testing.T) {
	models, ok := ParseCodexModels([]byte(modelsBody))
	if !ok || len(models) != 4 {
		t.Fatalf("expected 4 models, got ok=%v %+v", ok, models)
	}
	if !models[0].ResponsesLite || models[2].ResponsesLite {
		t.Fatalf("lite flags not read: %+v", models)
	}
	if _, ok := ParseCodexModels([]byte(`{"data":[]}`)); ok {
		t.Fatal("a body without models must not parse")
	}
}

func TestCompatible(t *testing.T) {
	c := Load("")
	// Nothing known yet: only the identity swap is safe.
	if !c.Compatible("gpt-6-astra", "gpt-6-astra") || c.Compatible("gpt-6-astra", "gpt-5.5") {
		t.Fatal("unknown models must not be swapped")
	}
	models, _ := ParseCodexModels([]byte(modelsBody))
	c.Update(models)
	cases := []struct {
		requested, candidate string
		want                 bool
	}{
		{"gpt-6-astra", "gpt-5.6-luna", true},        // lite -> lite
		{"gpt-6-astra", "gpt-5.5", false},            // lite -> classic: upstream 400
		{"gpt-5.5", "gpt-5.3-codex-spark", true},     // classic -> classic
		{"gpt-5.5", "gpt-6-astra", false},            // classic -> lite
		{"gpt-6-astra", "gpt-7-never-listed", false}, // unknown stays untouched
	}
	for _, tc := range cases {
		if got := c.Compatible(tc.requested, tc.candidate); got != tc.want {
			t.Errorf("%s -> %s: got %v want %v", tc.requested, tc.candidate, got, tc.want)
		}
	}
}

func TestCatalogPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	models, _ := ParseCodexModels([]byte(modelsBody))
	Load(path).Update(models)
	again := Load(path)
	if again.Len() != 4 {
		t.Fatalf("expected the catalog to survive a reload, got %d models", again.Len())
	}
	if !again.Compatible("gpt-6-astra", "gpt-5.6-luna") {
		t.Fatal("persisted flags must still drive compatibility")
	}
}
