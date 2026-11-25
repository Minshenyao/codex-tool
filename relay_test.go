package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelayPreservesExistingConfiguration(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	oldCatalog := filepath.Join(dir, "old-models.json")
	unrelated := `startup_notice = """
[model_providers.not_a_table]
"""
[features]
apply_patch_freeform = true
[model_providers . 'relay'] # existing provider
request_max_retries = 2
requires_openai_auth = false # existing setting
[model_providers.relay.http_headers]
X-Test = "preserved"
[projects."/somewhere"]
trust_level = "trusted"
`
	old := `"model" = "old"` + "\n" + `model_catalog_json = ` + tomlQuote(oldCatalog) + "\n" + unrelated
	writeRelayFixture(t, configPath, old)
	writeRelayFixture(t, oldCatalog, `{"models":[]}`)
	original, err := readRelayConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	backup, catalog, err := saveRelayConfig(dir, original, testRelaySettings(), buildCatalog([]string{"test-model"}, "Test relay", 272000, false))
	if err != nil {
		t.Fatal(err)
	}
	data := readRelayFixture(t, configPath)
	for _, line := range []string{
		`model = "test-model"`, `model_provider = "relay"`, `model_catalog_json = ` + tomlQuote(catalog),
		`base_url = "https://relay.example.test/v1"`, `wire_api = "responses"`, `env_key = "TEST_RELAY_KEY"`,
		`[model_providers . 'relay'] # existing provider`, "request_max_retries = 2\nrequires_openai_auth = false # existing setting",
		"[model_providers.relay.http_headers]\nX-Test = \"preserved\"", "[projects.\"/somewhere\"]\ntrust_level = \"trusted\"",
		"startup_notice = \"\"\"\n[model_providers.not_a_table]\n\"\"\"",
	} {
		if !strings.Contains(data, line) {
			t.Errorf("missing configuration fragment %q", line)
		}
	}
	if strings.Count(data, "model = ") != 1 || strings.Contains(data, `"model" = "old"`) {
		t.Error("old model key was duplicated")
	}
	if backup == "" || readRelayFixture(t, backup) != old || readRelayFixture(t, oldCatalog) != `{"models":[]}` || catalog == oldCatalog {
		t.Fatal("original config backup or previously referenced catalog was not preserved")
	}
	var models catalogFile
	if err := json.Unmarshal([]byte(readRelayFixture(t, catalog)), &models); err != nil || len(models.Models) != 1 || models.Models[0].Slug != "test-model" {
		t.Fatalf("new model catalog does not load: %v", err)
	}
	for _, path := range []string{configPath, backup} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Errorf("config or backup was not made private: %v", err)
		}
	}
}

func TestRelayRequiresExplicitLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	legacyFixture := "pretend-migration-value"
	old := "[model_providers.relay]\n'experimental_bearer_token' = '" + legacyFixture + "'\nenv_key = 'OLD_RELAY_ENV'\nstream_max_retries = 2\n"
	writeRelayFixture(t, path, old)
	original, err := readRelayConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := testRelaySettings()
	if _, _, err := saveRelayConfig(dir, original, settings, catalogFile{}); !errors.Is(err, errRelayLegacyToken) {
		t.Fatalf("unconfirmed migration returned %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || readRelayFixture(t, path) != old {
		t.Fatal("unconfirmed migration created artifacts or changed the original file")
	}
	settings.removeLegacyToken = true
	backup, _, err := saveRelayConfig(dir, original, settings, catalogFile{})
	if err != nil {
		t.Fatal(err)
	}
	data := readRelayFixture(t, path)
	if strings.Contains(data, "experimental_bearer_token") || strings.Contains(data, legacyFixture) || strings.Contains(data, "OLD_RELAY_ENV") ||
		!strings.Contains(data, `env_key = "TEST_RELAY_KEY"`) || !strings.Contains(data, "stream_max_retries = 2") || readRelayFixture(t, backup) != old {
		t.Fatal("migration did not remove old credentials while retaining other fields and a private backup")
	}
}

func TestRelayLeavesFilesUntouchedOnConflict(t *testing.T) {
	for _, tc := range []struct {
		name, original, current string
	}{
		{"concurrent edit", "model = 'before'\n", "model = 'changed'\n"},
		{"inline provider", "model_providers = { relay = { name = 'existing' } }\n", ""},
		{"dotted provider", "model_providers.relay.env_key = 'EXISTING'\n", ""},
		{"invalid multiline", "notes = [\n'one'", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			writeRelayFixture(t, path, tc.original)
			original, err := readRelayConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			expected := tc.original
			if tc.current != "" {
				expected = tc.current
				writeRelayFixture(t, path, expected)
			}
			if _, _, err := saveRelayConfig(dir, original, testRelaySettings(), catalogFile{}); err == nil {
				t.Fatal("unsafe update succeeded")
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || readRelayFixture(t, path) != expected {
				t.Fatal("unsafe update changed the configuration directory")
			}
		})
	}
}

func TestFixedRelayCatalogContainsExactlyRequestedModels(t *testing.T) {
	catalog := buildCatalog(fixedRelayModels, "Test relay", 272000, false)
	want := []string{"deepseek-v4-flash", "deepseek-v4-pro", "gpt-6-luna", "glm-5.3", "glm-5.3-flash"}
	if len(catalog.Models) != len(want) {
		t.Fatalf("catalog has %d models, want %d", len(catalog.Models), len(want))
	}
	for i, model := range catalog.Models {
		if model.Slug != want[i] || model.DisplayName != want[i] || model.Priority != i+1 {
			t.Fatalf("model %d is %#v, want slug and display name %q", i, model, want[i])
		}
		if !model.SupportsSearchTool || !model.SupportsReasoningSummaries || !model.SupportVerbosity {
			t.Errorf("model %q does not enable search, reasoning summaries, and verbosity", model.Slug)
		}
		if model.DefaultReasoningLevel != "medium" || len(model.SupportedReasoningLevels) != 1 ||
			model.SupportedReasoningLevels[0].Effort != "medium" {
			t.Errorf("model %q does not use only the medium reasoning level", model.Slug)
		}
	}
}

func testRelaySettings() relaySettings {
	return relaySettings{
		providerID: "relay", providerName: "Test relay", baseURL: "https://relay.example.test/v1",
		envKey: "TEST_RELAY_KEY", activeModel: "test-model",
	}
}

func writeRelayFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRelayFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
