package mcpclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMergesStandardAndBankaFiles(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("MCP_TOKEN", "secret")
	writeConfig(t, filepath.Join(home, ".banka", "mcp.json"), `{"servers":{"global":{"command":"global-server"}}}`)
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"demo":{"command":"old"},"remote":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${MCP_TOKEN}"}}}}`)
	writeConfig(t, filepath.Join(project, ".banka", "mcp.json"), `{"servers":{"demo":{"command":"new","args":["serve"]}}}`)

	config, err := LoadConfig(project, home)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if len(config.Servers) != 3 || config.Servers["demo"].Command != "new" || config.Servers["remote"].Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("unexpected merged config: %#v", config)
	}
}

func TestLoadConfigRejectsAmbiguousTransport(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"bad":{"command":"x","url":"https://example.com"}}}`)
	if _, err := LoadConfig(project, ""); err == nil {
		t.Fatal("expected ambiguous transport to be rejected")
	}
}

func TestMCPChildEnvironmentExcludesBankaCredentialsUnlessOverridden(t *testing.T) {
	t.Setenv("BANKA_API_KEY", "must-not-leak")
	environment := mergedEnvironment(map[string]string{"EXPLICIT": "value"})
	for _, entry := range environment {
		if entry == "BANKA_API_KEY=must-not-leak" {
			t.Fatal("MCP child inherited BANKA_API_KEY")
		}
	}
}

func writeConfig(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
