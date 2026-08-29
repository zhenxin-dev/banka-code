package lspclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMergesCustomServerAndOptions(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.project"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".banka"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{
		"diagnosticsOnWrite": false,
		"servers": {
			"demo": {
				"command": "` + os.Args[0] + `",
				"args": ["serve"],
				"fileTypes": [".demo"],
				"rootMarkers": ["demo.project"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(root, ".banka", "lsp.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, home)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	server := config.Servers["demo"]
	if config.DiagnosticsOnWrite || server.ResolvedCommand == "" || len(server.Args) != 1 || server.Args[0] != "serve" {
		t.Fatalf("unexpected config: %#v server=%#v", config, server)
	}
}

func TestLoadConfigReportsDetectedUnavailableServer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	config, err := LoadConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	server, exists := config.Servers["gopls"]
	if !exists || server.ResolvedCommand != "" || server.UnavailableReason == "" {
		t.Fatalf("unavailable detected server was not retained: %#v", config.Servers)
	}
}

func TestLoadConfigSupportsYAMLAliasesAndEnvironmentExpansion(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.project"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "server")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANKA_LSP_COMMAND", command)
	configPath := filepath.Join(root, ".banka", "lsp.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "enabled: true\nservers:\n  demo:\n    command: ${BANKA_LSP_COMMAND}\n    file_types: [.demo]\n    root_markers: [demo.project]\n    initialization_options:\n      token: ${BANKA_LSP_COMMAND}\n"
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, home)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	server, ok := config.Servers["demo"]
	if !ok || server.Command != command || server.ResolvedCommand != command || len(server.FileTypes) != 1 || server.FileTypes[0] != ".demo" {
		t.Fatalf("unexpected YAML server: %#v", server)
	}
	options, ok := server.InitOptions.(map[string]any)
	if !ok || options["token"] != command {
		t.Fatalf("environment was not expanded in initialization options: %#v", server.InitOptions)
	}
}

func TestLSPEnvironmentExpansionSupportsDefaults(t *testing.T) {
	value, err := expandEnvironment("${BANKA_LSP_UNSET_FOR_TEST:-fallback}")
	if err != nil || value != "fallback" {
		t.Fatalf("default expansion failed: value=%q err=%v", value, err)
	}
	t.Setenv("BANKA_LSP_EMPTY_FOR_TEST", "")
	value, err = expandEnvironment("${BANKA_LSP_EMPTY_FOR_TEST:-fallback}")
	if err != nil || value != "fallback" {
		t.Fatalf("empty-variable default expansion failed: value=%q err=%v", value, err)
	}
}

func TestLoadConfigCanDisableLSP(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lsp.json")
	if err := os.WriteFile(path, []byte(`{"enabled":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled || len(config.Servers) != len(defaultServers()) {
		t.Fatalf("LSP disable flag was not preserved: enabled=%v servers=%d", config.Enabled, len(config.Servers))
	}
}

func TestLoadConfigHonorsShorthandAliasesAndWarmupSettings(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	marker := filepath.Join(root, "demo.project")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "server")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".banka", "lsp.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "servers:\n  demo:\n    command: " + command + "\n    file-types: [.demo]\n    root-markers: [demo.project]\n    language-id: demo\n    warmup-timeout-ms: 1234\n    workspace-ready-timings:\n      pollMs: ${LSP_POLL_MS}\n    capabilities:\n      feature: ${LSP_FEATURE}\n"
	t.Setenv("LSP_POLL_MS", "25")
	t.Setenv("LSP_FEATURE", "enabled")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, home)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	server, ok := config.Servers["demo"]
	if !ok || server.LanguageID != "demo" || server.WarmupTimeoutMS != 1234 || server.ResolvedCommand != command {
		t.Fatalf("unexpected aliased server: %#v", server)
	}
	timings, ok := server.WorkspaceReadyTimings.(map[string]any)
	if !ok || timings["pollMs"] != "25" {
		t.Fatalf("workspace readiness timings were not expanded: %#v", server.WorkspaceReadyTimings)
	}
	if server.Capabilities["feature"] != "enabled" {
		t.Fatalf("capabilities were not expanded: %#v", server.Capabilities)
	}
}

func TestLoadConfigAcceptsOMPServerNameAliases(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "yaml-language-server")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".banka", "lsp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"servers":{"yaml-language-server":{"command":"` + command + `","fileTypes":[".yaml"],"rootMarkers":[".git"]}}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, home)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := config.Servers["yamlls"]; !ok {
		t.Fatalf("legacy server name was not normalized: %#v", config.Servers)
	}
	if _, ok := config.Servers["yaml-language-server"]; ok {
		t.Fatalf("legacy alias leaked as a duplicate server: %#v", config.Servers)
	}
}

func TestLoadConfigCanonicalServerNameWinsOverAliasWithinOneFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	aliasCommand := filepath.Join(root, "alias-server")
	canonicalCommand := filepath.Join(root, "canonical-server")
	for _, command := range []string{aliasCommand, canonicalCommand} {
		if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, ".banka", "lsp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"servers":{"yaml-language-server":{"command":"` + aliasCommand + `","fileTypes":[".yaml"],"rootMarkers":[".git"]},"yamlls":{"command":"` + canonicalCommand + `","fileTypes":[".yaml"],"rootMarkers":[".git"]}}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Servers["yamlls"].Command; got != canonicalCommand {
		t.Fatalf("canonical server did not win alias: got %q want %q", got, canonicalCommand)
	}
}

func TestLoadConfigProjectOverridesGlobalShallowly(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.project"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "server")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLSPConfig := func(path string, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLSPConfig(filepath.Join(home, ".agents", "lsp.json"), `{"servers":{"demo":{"command":"`+command+`","fileTypes":[".demo"],"rootMarkers":["demo.project"],"settings":{"global":{"a":1,"b":2}}}}}`)
	writeLSPConfig(filepath.Join(root, ".banka", "lsp.json"), `{"servers":{"demo":{"settings":{"global":{"a":3}}}}}`)
	config, err := LoadConfig(root, home)
	if err != nil {
		t.Fatal(err)
	}
	settings, ok := config.Servers["demo"].Settings.(map[string]any)
	if !ok {
		t.Fatalf("settings type mismatch: %#v", config.Servers["demo"].Settings)
	}
	global, ok := settings["global"].(map[string]any)
	if !ok || global["a"] != float64(3) || global["b"] != nil {
		t.Fatalf("expected shallow replacement of settings, got %#v", settings)
	}
}

func TestLoadConfigAllowsNegativeIdleTimeoutToDisableReaping(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lsp.json"), []byte(`{"idleTimeoutMs":-1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.IdleTimeoutMS != -1 {
		t.Fatalf("idle timeout opt-out was not preserved: %d", config.IdleTimeoutMS)
	}
}

func TestLoadConfigIgnoresFlatMetadataKeys(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "server")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	content := `{"$schema":"https://example.test/lsp.schema.json","version":1,"metadata":{"owner":"test"},"demo":{"command":"` + command + `","fileTypes":[".demo"],"rootMarkers":["marker"]}}`
	if err := os.WriteFile(filepath.Join(root, "lsp.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, "")
	if err != nil {
		t.Fatalf("metadata keys should not be treated as servers: %v", err)
	}
	if _, ok := config.Servers["demo"]; !ok {
		t.Fatalf("custom server was not loaded: %#v", config.Servers)
	}
}

func TestDefaultServersIncludeOMPCompatibleOptions(t *testing.T) {
	servers := defaultServers()
	gopls := servers["gopls"]
	if !containsString(gopls.RootMarkers, "go.sum") {
		t.Fatalf("gopls root markers omit go.sum: %#v", gopls.RootMarkers)
	}
	settings, ok := gopls.Settings.(map[string]any)
	goplsSettings, nestedOK := settings["gopls"].(map[string]any)
	if !ok || !nestedOK || goplsSettings["staticcheck"] != true {
		t.Fatalf("gopls settings are not OMP-compatible: %#v", gopls.Settings)
	}
	rust := servers["rust-analyzer"]
	if !containsString(rust.RootMarkers, "rust-analyzer.toml") || rust.Capabilities["runnables"] != true {
		t.Fatalf("rust-analyzer defaults are incomplete: %#v", rust)
	}
	clangd := servers["clangd"]
	if len(clangd.Args) != 3 || clangd.Args[0] != "--background-index" || !containsString(clangd.RootMarkers, "Makefile") {
		t.Fatalf("clangd defaults are incomplete: %#v", clangd)
	}
	eslint := servers["eslint"]
	if !eslint.IsLinter || eslint.Settings == nil {
		t.Fatalf("eslint linter defaults are incomplete: %#v", eslint)
	}
	if servers["swiftlint"].Linter != "swiftlint" || servers["biome"].Linter != "biome" {
		t.Fatalf("CLI linter defaults are incomplete: swiftlint=%#v biome=%#v", servers["swiftlint"], servers["biome"])
	}
}

func TestNormalizeLinterKindHonorsExplicitMode(t *testing.T) {
	tests := []struct {
		name   string
		server ServerConfig
		want   string
		mode   string
	}{
		{name: "stdio suppresses swiftlint inference", server: ServerConfig{Command: "swiftlint", Mode: "stdio"}, mode: "stdio"},
		{name: "lsp suppresses biome inference", server: ServerConfig{Command: "biome", IsLinter: true, Mode: "lsp"}, mode: "lsp"},
		{name: "cli detects biome without flag", server: ServerConfig{Command: "biome", Mode: "cli"}, want: "biome", mode: "cli"},
		{name: "cli detects swiftlint", server: ServerConfig{Command: "swiftlint", Mode: "cli"}, want: "swiftlint", mode: "cli"},
		{name: "biome needs explicit linter intent by default", server: ServerConfig{Command: "biome"}},
		{name: "explicit linter wins over mode", server: ServerConfig{Command: "biome", Linter: "biome", Mode: "stdio"}, want: "biome", mode: "stdio"},
		{name: "legacy linter cli spelling", server: ServerConfig{Command: "biome", Linter: "cli"}, want: "biome", mode: "cli"},
		{name: "resolved command is used for cli detection", server: ServerConfig{ResolvedCommand: "/opt/tools/swiftlint", Mode: "cli"}, want: "swiftlint", mode: "cli"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeLinterKind(test.name, test.server)
			if err != nil {
				t.Fatal(err)
			}
			if got.Linter != test.want || got.Mode != test.mode {
				t.Fatalf("normalized server = %#v, want linter=%q mode=%q", got, test.want, test.mode)
			}
		})
	}
}

func TestNormalizeLinterKindRejectsUnknownMode(t *testing.T) {
	if _, err := normalizeLinterKind("demo", ServerConfig{Command: "demo", Mode: "something-else"}); err == nil {
		t.Fatal("unknown LSP mode was accepted")
	}
}

func TestNormalizeLinterKindRejectsUnknownCLILinter(t *testing.T) {
	if _, err := normalizeLinterKind("demo", ServerConfig{Command: "custom-linter", Mode: "cli"}); err == nil {
		t.Fatal("unknown CLI linter was accepted")
	}
}

func TestMergeServerModeReplacesInheritedLinterKind(t *testing.T) {
	base := ServerConfig{Command: "biome", IsLinter: true, Linter: "biome"}
	standard, err := mergeServerConfig(base, []byte(`{"mode":"stdio"}`))
	if err != nil {
		t.Fatal(err)
	}
	if standard.Linter != "" || standard.Mode != "stdio" {
		t.Fatalf("standard mode retained inherited linter: %#v", standard)
	}
	cli, err := mergeServerConfig(base, []byte(`{"mode":"cli"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cli.Linter != "" || cli.Mode != "cli" {
		t.Fatalf("raw merge should defer CLI detection to normalization: %#v", cli)
	}
	explicit, err := mergeServerConfig(base, []byte(`{"mode":"stdio","linter":"biome"}`))
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Linter != "biome" {
		t.Fatalf("explicit linter did not win over mode: %#v", explicit)
	}
}

func TestLoadConfigPreservesModeField(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "biome")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".banka", "lsp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"servers":{"demo":{"command":"` + command + `","mode":"cli","fileTypes":[".demo"],"rootMarkers":["marker"]}}}`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	server := config.Servers["demo"]
	if server.Mode != "cli" || server.Linter != "biome" {
		t.Fatalf("mode was not retained or CLI linter was not detected: %#v", server)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestHasRootMarkerAncestorFindsNestedProjectRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "packages", "demo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "package.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "src", "index.ts")
	matched, err := hasRootMarkerAncestor(file, []string{"package.json"})
	if err != nil || !matched {
		t.Fatalf("nested root marker was not detected: matched=%v err=%v", matched, err)
	}
	matched, err = hasRootMarkerAncestor(filepath.Join(root, "other.ts"), []string{"package.json"})
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("unrelated path unexpectedly matched nested root marker")
	}
}

func TestHasRootMarkerAncestorRespectsWorkspaceBoundary(t *testing.T) {
	container := t.TempDir()
	root := filepath.Join(container, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(container, "package.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	matched, err := hasRootMarkerAncestor(filepath.Join(root, "src", "index.ts"), []string{"package.json"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("marker outside workspace boundary was accepted")
	}
}

func TestHasRootMarkerAncestorRejectsSymlinkEscape(t *testing.T) {
	container := t.TempDir()
	root := filepath.Join(container, "workspace")
	outside := filepath.Join(container, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "package.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	matched, err := hasRootMarkerAncestor(filepath.Join(link, "src", "index.ts"), []string{"package.json"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("marker through an escaping symlink was accepted")
	}
}

func TestHasRootMarkerAncestorHandlesSymlinkedWorkspaceRoot(t *testing.T) {
	container := t.TempDir()
	realRoot := filepath.Join(container, "real")
	if err := os.MkdirAll(filepath.Join(realRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "package.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(container, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	matched, err := hasRootMarkerAncestor(filepath.Join(linkRoot, "src", "new.ts"), []string{"package.json"}, linkRoot)
	if err != nil || !matched {
		t.Fatalf("symlinked workspace root was not routed: matched=%v err=%v", matched, err)
	}
}

func TestServerRoutingHelpersAgreeWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "project.marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	server := ServerConfig{Command: os.Args[0], ResolvedCommand: os.Args[0], FileTypes: []string{".demo"}, RootMarkers: []string{"project.marker"}, explicit: true}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{"demo": server}})
	defer manager.Close()
	path := filepath.Join(nested, "src", "main.demo")
	name, _, err := manager.ServerForFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matches := manager.ServersForFile(path)
	if len(matches) != 1 || matches[0].Name != name {
		t.Fatalf("routing helpers disagree: name=%q matches=%#v", name, matches)
	}
}
