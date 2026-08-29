package mcpclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestLoadConfigRejectsExplicitHTTPTransportWithCommand(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"bad":{"type":"http","command":"x"}}}`)
	if _, err := LoadConfig(project, ""); err == nil {
		t.Fatal("expected explicit HTTP transport/command mismatch to be rejected")
	}
}

func TestLoadConfigSupportsYAMLAndMCPAliases(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".omp", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_COMMAND", "echo")
	content := "mcpServers:\n  demo:\n    type: stdio\n    command: ${MCP_COMMAND}\n    timeout: 1200\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	server, ok := config.Servers["demo"]
	if !ok || server.Command != "echo" || server.TimeoutMS != 1200 || normalizedTransport(server) != "stdio" {
		t.Fatalf("unexpected YAML MCP config: %#v", config)
	}
}

func TestLoadConfigImportsOpenCodeAndCodexShapes(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	writeConfig(t, filepath.Join(project, "opencode.json"), `{"mcp":{"local":{"type":"local","command":["echo","hello"],"environment":{"TOKEN":"value"}},"remote":{"type":"remote","url":"https://example.com/mcp"}}}`)
	writeConfig(t, filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.codex]\ncommand = \"codex-server\"\nargs = [\"--stdio\"]\n")
	config, err := LoadConfig(project, home)
	if err != nil {
		t.Fatal(err)
	}
	local, ok := config.Servers["local"]
	if !ok || local.Command != "echo" || len(local.Args) != 1 || local.Args[0] != "hello" || local.Env["TOKEN"] != "value" || normalizedTransport(local) != "stdio" {
		t.Fatalf("OpenCode local server was not normalized: %#v", local)
	}
	remote, ok := config.Servers["remote"]
	if !ok || remote.URL != "https://example.com/mcp" || normalizedTransport(remote) != "streamable-http" {
		t.Fatalf("OpenCode remote server was not normalized: %#v", remote)
	}
	codex, ok := config.Servers["codex"]
	if !ok || codex.Command != "codex-server" || len(codex.Args) != 1 || codex.Args[0] != "--stdio" {
		t.Fatalf("Codex TOML server was not imported: %#v", codex)
	}
}

func TestLoadConfigIgnoresUnrelatedObjectsInGeneralClientSettings(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".claude.json"), `{"projects":{},"cachedStatsigGates":{"feature":true},"preferences":{"theme":"dark"}}`)
	writeConfig(t, filepath.Join(home, ".gemini", "settings.json"), `{"security":{"auth":{"selectedType":"oauth"}},"ui":{"theme":"dark"}}`)
	writeConfig(t, filepath.Join(home, ".codex", "config.toml"), "[features]\nexperimental = true\n[projects.\"/tmp/demo\"]\ntrust_level = \"trusted\"\n")

	config, err := LoadConfig(project, home)
	if err != nil {
		t.Fatalf("general client metadata should not be parsed as MCP servers: %v", err)
	}
	if len(config.Servers) != 0 {
		t.Fatalf("general client metadata produced MCP servers: %#v", config.Servers)
	}
}

func TestLoadConfigStillAcceptsFlatDedicatedMCPFile(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, "mcp.json"), `{"flat":{"command":"echo"}}`)
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if server, ok := config.Servers["flat"]; !ok || server.Command != "echo" {
		t.Fatalf("flat dedicated MCP file was not loaded: %#v", config.Servers)
	}
}

func TestExpandEnvironmentSupportsDefaults(t *testing.T) {
	value, err := expandEnvironment("${MCP_UNSET_FOR_TEST:-fallback}")
	if err != nil || value != "fallback" {
		t.Fatalf("default expansion failed: value=%q err=%v", value, err)
	}
	t.Setenv("MCP_EMPTY_FOR_TEST", "")
	value, err = expandEnvironment("${MCP_EMPTY_FOR_TEST:-fallback}")
	if err != nil || value != "fallback" {
		t.Fatalf("empty-variable default expansion failed: value=%q err=%v", value, err)
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

func TestMCPEnableDisablePrecedenceIsLastWriterWins(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"demo":{"command":"echo"}},"disabledServers":["demo"]}`)
	writeConfig(t, filepath.Join(project, ".banka", "mcp.json"), `{"mcpServers":{"demo":{"enabled":true}}}`)
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.Servers["demo"].Disabled {
		t.Fatalf("higher-precedence enabled field did not override disable: %#v", config.Servers["demo"])
	}
}

func TestMCPDisabledListWinsOverEnabledListInSameFile(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"demo":{"command":"echo"}},"enabledServers":["demo"],"disabledServers":["demo"]}`)
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if !config.Servers["demo"].Disabled {
		t.Fatalf("disabledServers did not veto enabledServers: %#v", config.Servers["demo"])
	}
}

func TestMCPEnabledListOverridesEntryDisabled(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"demo":{"command":"echo","enabled":false}},"enabledServers":["demo"]}`)
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.Servers["demo"].Disabled {
		t.Fatalf("enabledServers did not override entry enabled=false: %#v", config.Servers["demo"])
	}
}

func TestMCPImportsClaudeProjectScopedServers(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	content := `{"projects":{"` + project + `":{"mcpServers":{"project":{"command":"echo"}}}}}`
	writeConfig(t, filepath.Join(home, ".claude.json"), content)
	config, err := LoadConfig(project, home)
	if err != nil {
		t.Fatal(err)
	}
	if server, ok := config.Servers["project"]; !ok || server.Command != "echo" {
		t.Fatalf("Claude project-scoped server was not imported: %#v", config.Servers)
	}
}

func TestMCPConfigRejectsMalformedEnableList(t *testing.T) {
	project := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"disabledServers":"demo"}`)
	if _, err := LoadConfig(project, ""); err == nil {
		t.Fatal("malformed disabledServers was accepted")
	}
}

func TestLoadConfigExpandsAuthAndExplicitZeroTimeout(t *testing.T) {
	project := t.TempDir()
	t.Setenv("MCP_AUTH_TOKEN", "secret")
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"remote":{"url":"https://example.com/mcp","timeout":0,"auth":{"token":"${MCP_AUTH_TOKEN}"},"oauth":{"nested":{"value":"${MCP_AUTH_TOKEN}"}}}}}`)
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	server := config.Servers["remote"]
	if !server.timeoutSet {
		t.Fatal("explicit zero timeout was not retained")
	}
	if server.Auth["token"] != "secret" {
		t.Fatalf("auth environment was not expanded: %#v", server.Auth)
	}
	nested, ok := server.OAuth["nested"].(map[string]any)
	if !ok || nested["value"] != "secret" {
		t.Fatalf("oauth environment was not expanded: %#v", server.OAuth)
	}
	if got := mcpConfiguredTimeout(server, time.Second); got != 0 {
		t.Fatalf("explicit zero timeout became %s", got)
	}
}

func TestLoadConfigHonorsLiteralEnvironmentAndOriginLockedHeaders(t *testing.T) {
	project := t.TempDir()
	t.Setenv("MCP_LITERAL_VALUE", "expanded")
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"literal":{"command":"echo","envPolicy":"literal","env":{"TOKEN":"${MCP_LITERAL_VALUE}"}},"locked":{"url":"https://example.com/mcp","headerPolicy":"origin-locked","headers":{"X-Token":"${MCP_LITERAL_VALUE}"}}}}`)
	config, err := LoadConfig(project, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Servers["literal"].Env["TOKEN"]; got != "${MCP_LITERAL_VALUE}" {
		t.Fatalf("literal env was expanded: %q", got)
	}
	if got := config.Servers["locked"].Headers["X-Token"]; got != "${MCP_LITERAL_VALUE}" {
		t.Fatalf("origin-locked header was expanded: %q", got)
	}
}

func TestMCPUserDisabledListVetoesProjectEnable(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	writeConfig(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"demo":{"command":"echo","enabled":true}}}`)
	writeConfig(t, filepath.Join(home, ".banka", "mcp.json"), `{"disabledServers":["demo"]}`)
	config, err := LoadConfig(project, home)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Servers["demo"].Disabled {
		t.Fatalf("user disabled list did not veto project entry: %#v", config.Servers["demo"])
	}
}

func TestMCPProfileIsolatesNativeUserConfiguration(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".banka", "mcp.json"), `{"mcpServers":{"default":{"command":"echo"}}}`)
	writeConfig(t, filepath.Join(home, ".omp", "profiles", "work", "agent", "mcp.json"), `{"mcpServers":{"profile":{"command":"printf"}}}`)
	t.Setenv("OMP_PROFILE", "work")
	config, err := LoadConfig(project, home)
	if err != nil {
		t.Fatal(err)
	}
	if config.Profile != "work" {
		t.Fatalf("active profile was not recorded: %q", config.Profile)
	}
	if _, ok := config.Servers["default"]; ok {
		t.Fatalf("default native user server leaked into profile: %#v", config.Servers)
	}
	if _, ok := config.Servers["profile"]; !ok {
		t.Fatalf("profile server was not loaded: %#v", config.Servers)
	}
}

func TestMCPProfileRejectsPathTraversal(t *testing.T) {
	if _, err := LoadConfigWithProfile(t.TempDir(), t.TempDir(), "../escape"); err == nil {
		t.Fatal("path traversal profile was accepted")
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
