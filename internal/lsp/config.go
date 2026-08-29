// Package lspclient provides Language Server Protocol integration for Banka.
package lspclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var environmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// ServerConfig describes one language server or CLI linter.
type ServerConfig struct {
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	FileTypes   []string          `json:"fileTypes,omitempty"`
	RootMarkers []string          `json:"rootMarkers,omitempty"`
	LanguageID  string            `json:"languageId,omitempty"`
	InitOptions any               `json:"initOptions,omitempty"`
	Settings    any               `json:"settings,omitempty"`
	Disabled    bool              `json:"disabled,omitempty"`
	Priority    int               `json:"priority,omitempty"`
	IsLinter    bool              `json:"isLinter,omitempty"`
	// Linter selects a CLI-backed linter implementation (currently "swiftlint"
	// and "biome"). Empty means a standard JSON-RPC language server, even when
	// IsLinter is true (for example eslint and ruff expose LSP endpoints).
	Linter string `json:"linter,omitempty"`
	// Mode disambiguates configurations that use a generic command name. "cli"
	// enables known CLI-linter detection, while "stdio", "lsp", and
	// "json-rpc" force a standard JSON-RPC language server. An explicit Linter
	// value takes precedence when both fields are present.
	Mode                  string         `json:"mode,omitempty"`
	WarmupTimeoutMS       int            `json:"warmupTimeoutMs,omitempty"`
	Capabilities          map[string]any `json:"capabilities,omitempty"`
	WorkspaceReadyTimings any            `json:"workspaceReadyTimings,omitempty"`

	ResolvedCommand   string `json:"-"`
	UnavailableReason string `json:"-"`
	explicit          bool
}

// Config is the effective LSP configuration for a workspace.
type Config struct {
	Servers            map[string]ServerConfig
	Enabled            bool
	IdleTimeoutMS      int
	DiagnosticsOnWrite bool
	DiagnosticsOnEdit  bool
	FormatOnWrite      bool
}

type configOptions struct {
	Enabled            *bool
	IdleTimeoutMS      *int
	DiagnosticsOnWrite *bool
	DiagnosticsOnEdit  *bool
	FormatOnWrite      *bool
}

// lspServerAliases keeps names used by earlier Banka configurations and other
// clients compatible with the canonical OMP/defaults.json names.
var lspServerAliases = map[string]string{
	"deno":                    "denols",
	"bash-language-server":    "bashls",
	"yaml-language-server":    "yamlls",
	"terraform-ls":            "terraformls",
	"docker-language-server":  "dockerls",
	"graphql-language-server": "graphql",
	"html-language-server":    "vscode-html-language-server",
	"css-language-server":     "vscode-css-language-server",
	"json-language-server":    "vscode-json-language-server",
	"svelte-language-server":  "svelte",
}

func canonicalLSPServerName(name string) string {
	if canonical, ok := lspServerAliases[strings.TrimSpace(name)]; ok {
		return canonical
	}
	return strings.TrimSpace(name)
}

// configFileNamesLowToHigh returns supported spelling variants in merge order.
// The visible JSON file is the canonical/highest-precedence spelling.
func configFileNamesLowToHigh() []string {
	return []string{".lsp.yml", "lsp.yml", ".lsp.yaml", "lsp.yaml", ".lsp.json", "lsp.json"}
}

// LoadConfig merges built-in server definitions with global and project JSON
// configuration, then detects servers applicable to the workspace.
func LoadConfig(projectRoot string, homeDir string) (Config, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve LSP project root: %w", err)
	}
	config := Config{
		Servers:            defaultServers(),
		Enabled:            true,
		DiagnosticsOnWrite: true,
	}
	explicit := make(map[string]bool)
	paths := lspConfigPaths(root, homeDir)
	for _, path := range paths {
		servers, options, err := readConfigFile(path)
		if err != nil {
			return Config{}, err
		}
		// JSON object iteration is deliberately random.  Process compatibility
		// aliases before the canonical name so a config containing both (for
		// example `deno` and `denols`) has a deterministic, unsurprising result:
		// the canonical spelling wins within the same file, while a later file
		// still overrides an earlier file regardless of spelling.
		rawNames := make([]string, 0, len(servers))
		for rawName := range servers {
			rawNames = append(rawNames, rawName)
		}
		sort.Slice(rawNames, func(i, j int) bool {
			left, right := strings.TrimSpace(rawNames[i]), strings.TrimSpace(rawNames[j])
			leftCanonical := canonicalLSPServerName(left) == left
			rightCanonical := canonicalLSPServerName(right) == right
			if leftCanonical != rightCanonical {
				return !leftCanonical
			}
			return left < right
		})
		for _, rawName := range rawNames {
			override := servers[rawName]
			name := canonicalLSPServerName(rawName)
			if name == "" {
				return Config{}, fmt.Errorf("parse LSP config %s: server name must not be empty", path)
			}
			merged, err := mergeServerConfig(config.Servers[name], override)
			if err != nil {
				return Config{}, fmt.Errorf("parse LSP config %s server %q: %w", path, name, err)
			}
			if !merged.Disabled {
				merged, err = expandServerConfig(merged)
				if err != nil {
					return Config{}, fmt.Errorf("expand LSP config %s server %q: %w", path, name, err)
				}
			}
			merged.explicit = true
			config.Servers[name] = merged
			explicit[name] = true
		}
		if options.Enabled != nil {
			config.Enabled = *options.Enabled
		}
		if options.IdleTimeoutMS != nil {
			// Zero and negative values explicitly disable idle reaping.  Accept
			// -1 as the conventional opt-out spelling while still rejecting
			// values that are most likely accidental unit/configuration errors.
			if *options.IdleTimeoutMS < -1 || *options.IdleTimeoutMS > 24*60*60*1000 {
				return Config{}, fmt.Errorf("parse LSP config %s idleTimeoutMs must be between -1 and 86400000", path)
			}
			config.IdleTimeoutMS = *options.IdleTimeoutMS
		}
		if options.DiagnosticsOnWrite != nil {
			config.DiagnosticsOnWrite = *options.DiagnosticsOnWrite
		}
		if options.DiagnosticsOnEdit != nil {
			config.DiagnosticsOnEdit = *options.DiagnosticsOnEdit
		}
		if options.FormatOnWrite != nil {
			config.FormatOnWrite = *options.FormatOnWrite
		}
	}
	if !config.Enabled {
		for name, server := range config.Servers {
			server.Disabled = true
			server.UnavailableReason = "disabled by LSP configuration"
			config.Servers[name] = server
		}
		return config, nil
	}

	for name, server := range config.Servers {
		if server.Disabled {
			server.UnavailableReason = "disabled"
			config.Servers[name] = server
			continue
		}
		server, err = expandServerConfig(server)
		if err != nil {
			return Config{}, fmt.Errorf("expand LSP server %q: %w", name, err)
		}
		server, err = normalizeLinterKind(name, server)
		if err != nil {
			return Config{}, fmt.Errorf("configure LSP server %q: %w", name, err)
		}
		if server.WarmupTimeoutMS < 0 || server.WarmupTimeoutMS > 600000 {
			return Config{}, fmt.Errorf("LSP server %q warmupTimeoutMs must be between 0 and 600000", name)
		}
		if strings.TrimSpace(server.Command) == "" || len(server.FileTypes) == 0 || len(server.RootMarkers) == 0 {
			if explicit[name] {
				return Config{}, fmt.Errorf("LSP server %q requires command, fileTypes, and rootMarkers", name)
			}
			delete(config.Servers, name)
			continue
		}
		matchesRoot, err := matchesRootMarkers(root, server.RootMarkers)
		if err != nil {
			return Config{}, fmt.Errorf("detect LSP server %q: %w", name, err)
		}
		if !matchesRoot {
			delete(config.Servers, name)
			continue
		}
		resolved, err := resolveCommand(root, server.Command)
		if err != nil {
			server.UnavailableReason = err.Error()
		} else {
			server.ResolvedCommand = resolved
		}
		if server.Cwd != "" {
			if _, cwdErr := resolveServerCwd(root, server.Cwd); cwdErr != nil {
				server.ResolvedCommand = ""
				server.UnavailableReason = cwdErr.Error()
			}
		}
		// Mark every configuration that passed file-based detection so the
		// manager can apply ancestor-root routing. Config values supplied directly
		// to NewManager retain the historical extension-only behavior.
		server.explicit = true
		config.Servers[name] = server
	}
	return config, nil
}

func lspConfigPaths(root string, homeDir string) []string {
	var paths []string
	add := func(directory string, names ...string) {
		if directory == "" {
			return
		}
		for _, name := range names {
			paths = append(paths, filepath.Join(directory, name))
		}
	}
	filenames := configFileNamesLowToHigh()
	// Lower-precedence user files first. Later project/cwd files override them.
	if homeDir != "" {
		add(homeDir, filenames...)
		// Compatibility locations are ordered from low to high precedence. The
		// canonical .agents directory is intentionally last among user sources.
		for _, directory := range []string{".omp/agent", ".pi/agent", ".gemini", ".cursor", ".windsurf", ".opencode", ".claude", ".codex", ".banka", ".agents"} {
			add(filepath.Join(homeDir, filepath.FromSlash(directory)), filenames...)
		}
	}
	for _, directory := range []string{".omp", ".pi", ".gemini", ".cursor", ".windsurf", ".opencode", ".claude", ".codex", ".github", ".banka", ".agents"} {
		add(filepath.Join(root, filepath.FromSlash(directory)), filenames...)
	}
	add(root, filenames...)
	return paths
}

// Names returns available server names in routing order.
func (c Config) Names() []string {
	names := make([]string, 0, len(c.Servers))
	for name, server := range c.Servers {
		if !server.Disabled && server.ResolvedCommand != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := c.Servers[names[i]], c.Servers[names[j]]
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		return names[i] < names[j]
	})
	return names
}

// Statuses returns all detected server names, including unavailable servers.
func (c Config) Statuses() []string {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readConfigFile(path string) (map[string]json.RawMessage, configOptions, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, configOptions{}, nil
		}
		return nil, configOptions{}, fmt.Errorf("read LSP config %s: %w", path, err)
	}
	top, err := decodeConfigObject(content, filepath.Ext(path))
	if err != nil {
		return nil, configOptions{}, fmt.Errorf("parse LSP config %s: %w", path, err)
	}
	var options configOptions
	if value := top["enabled"]; value != nil {
		if err := json.Unmarshal(value, &options.Enabled); err != nil {
			return nil, options, fmt.Errorf("parse LSP config %s enabled: %w", path, err)
		}
	}
	for _, key := range []string{"idleTimeoutMs", "idle_timeout_ms", "idle-timeout-ms", "idleTimeoutMS"} {
		if value := top[key]; value != nil {
			if err := json.Unmarshal(value, &options.IdleTimeoutMS); err != nil {
				return nil, options, fmt.Errorf("parse LSP config %s %s: %w", path, key, err)
			}
			break
		}
	}
	boolOptions := []struct {
		keys   []string
		target **bool
	}{
		{keys: []string{"diagnosticsOnWrite", "diagnostics_on_write", "diagnostics-on-write", "diagnosticsOnSave", "diagnostics_on_save", "diagnostics-on-save"}, target: &options.DiagnosticsOnWrite},
		{keys: []string{"diagnosticsOnEdit", "diagnostics_on_edit", "diagnostics-on-edit"}, target: &options.DiagnosticsOnEdit},
		{keys: []string{"formatOnWrite", "format_on_write", "format-on-write", "formatOnSave", "format_on_save", "format-on-save"}, target: &options.FormatOnWrite},
	}
	for _, item := range boolOptions {
		for _, key := range item.keys {
			if value := top[key]; value != nil {
				if err := json.Unmarshal(value, item.target); err != nil {
					return nil, options, fmt.Errorf("parse LSP config %s %s: %w", path, key, err)
				}
				break
			}
		}
	}
	if wrapped := top["servers"]; wrapped != nil {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(wrapped, &servers); err != nil {
			return nil, options, fmt.Errorf("parse LSP config %s servers: %w", path, err)
		}
		return servers, options, nil
	}
	// In flat form every non-option object is a server.  Remove every accepted
	// spelling of the top-level options first; leaving an alias such as
	// `format-on-save` here would make it look like a malformed server object.
	for _, key := range []string{
		"diagnosticsOnWrite", "diagnostics_on_write", "diagnostics-on-write",
		"diagnosticsOnSave", "diagnostics_on_save", "diagnostics-on-save",
		"diagnosticsOnEdit", "diagnostics_on_edit", "diagnostics-on-edit",
		"formatOnWrite", "format_on_write", "format-on-write",
		"formatOnSave", "format_on_save", "format-on-save",
		"enabled", "idleTimeoutMs", "idle_timeout_ms", "idle-timeout-ms", "idleTimeoutMS",
		"$schema", "version", "metadata",
	} {
		delete(top, key)
	}
	return top, options, nil
}

func decodeConfigObject(content []byte, extension string) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if strings.EqualFold(extension, ".yaml") || strings.EqualFold(extension, ".yml") {
		var value any
		if err := yaml.Unmarshal(content, &value); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(yamlValueToJSON(value))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &top); err != nil {
			return nil, err
		}
		return top, nil
	}
	if err := json.Unmarshal(content, &top); err != nil {
		return nil, err
	}
	return top, nil
}

func yamlValueToJSON(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			result[key] = yamlValueToJSON(item)
		}
		return result
	case map[any]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			result[fmt.Sprint(key)] = yamlValueToJSON(item)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			result[index] = yamlValueToJSON(item)
		}
		return result
	default:
		return value
	}
}

func mergeServerConfig(base ServerConfig, override json.RawMessage) (ServerConfig, error) {
	encoded, err := json.Marshal(base)
	if err != nil {
		return ServerConfig{}, err
	}
	var values map[string]any
	if err := json.Unmarshal(encoded, &values); err != nil {
		return ServerConfig{}, err
	}
	normalized, err := normalizeServerRaw(override)
	if err != nil {
		return ServerConfig{}, err
	}
	var overrides map[string]any
	if err := json.Unmarshal(normalized, &overrides); err != nil {
		return ServerConfig{}, err
	}
	if overrides == nil {
		return ServerConfig{}, errors.New("server configuration must be an object")
	}
	// A mode supplied by a higher-precedence file is an explicit replacement for
	// inherited linter metadata. Without clearing the base value, overriding the
	// built-in `biome` server with `mode: stdio` would still route it through the
	// CLI linter implementation. An explicit linter in the same override remains
	// authoritative and is handled by normalizeLinterKind.
	if _, modeSpecified := overrides["mode"]; modeSpecified {
		if _, linterSpecified := overrides["linter"]; !linterSpecified {
			values["linter"] = ""
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	encoded, err = json.Marshal(values)
	if err != nil {
		return ServerConfig{}, err
	}
	var result ServerConfig
	if err := json.Unmarshal(encoded, &result); err != nil {
		return ServerConfig{}, err
	}
	return result, nil
}

// normalizeServerRaw accepts the field spellings used by common LSP clients
// while keeping the public ServerConfig JSON representation canonical.
func normalizeServerRaw(raw json.RawMessage) (json.RawMessage, error) {
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("server configuration must be an object")
	}
	aliases := map[string]string{
		"file_types":              "fileTypes",
		"file-types":              "fileTypes",
		"filetypes":               "fileTypes",
		"root_markers":            "rootMarkers",
		"root-markers":            "rootMarkers",
		"rootmarkers":             "rootMarkers",
		"language_id":             "languageId",
		"language-id":             "languageId",
		"initializationOptions":   "initOptions",
		"initialization_options":  "initOptions",
		"initialization-options":  "initOptions",
		"init_options":            "initOptions",
		"init-options":            "initOptions",
		"is_linter":               "isLinter",
		"is-linter":               "isLinter",
		"clientType":              "linter",
		"client_type":             "linter",
		"client-type":             "linter",
		"linterType":              "linter",
		"linter_type":             "linter",
		"linter-type":             "linter",
		"server_mode":             "mode",
		"server-mode":             "mode",
		"kind":                    "linter",
		"warmup_timeout_ms":       "warmupTimeoutMs",
		"warmup-timeout-ms":       "warmupTimeoutMs",
		"warmupTimeoutMS":         "warmupTimeoutMs",
		"workspace_ready_timings": "workspaceReadyTimings",
		"workspace-ready-timings": "workspaceReadyTimings",
		"extension_to_language":   "extensionToLanguage",
		"extension-to-language":   "extensionToLanguage",
	}
	extensionRaw, extensionExists := values["extensionToLanguage"]
	if !extensionExists {
		extensionRaw, extensionExists = values["extension_to_language"]
	}
	if extensionMap, ok := extensionRaw.(map[string]any); ok && extensionExists {
		if _, exists := values["fileTypes"]; !exists {
			fileTypes := make([]string, 0, len(extensionMap))
			for extension := range extensionMap {
				if strings.TrimSpace(extension) != "" {
					fileTypes = append(fileTypes, extension)
				}
			}
			sort.Strings(fileTypes)
			values["fileTypes"] = fileTypes
		}
		if _, exists := values["rootMarkers"]; !exists {
			values["rootMarkers"] = []string{"."}
		}
		delete(values, "extensionToLanguage")
		delete(values, "extension_to_language")
	}
	aliasKeys := make([]string, 0, len(aliases))
	for from := range aliases {
		aliasKeys = append(aliasKeys, from)
	}
	sort.Strings(aliasKeys)
	for _, from := range aliasKeys {
		to := aliases[from]
		if value, exists := values[from]; exists {
			if _, already := values[to]; !already {
				values[to] = value
			}
			delete(values, from)
		}
	}
	if value, exists := values["enabled"]; exists {
		if enabled, ok := value.(bool); ok {
			if _, already := values["disabled"]; !already {
				values["disabled"] = !enabled
			}
		}
		delete(values, "enabled")
	}
	return json.Marshal(values)
}

func expandServerConfig(server ServerConfig) (ServerConfig, error) {
	var err error
	server.Command, err = expandEnvironment(server.Command)
	if err != nil {
		return ServerConfig{}, err
	}
	server.Cwd, err = expandEnvironment(server.Cwd)
	if err != nil {
		return ServerConfig{}, err
	}
	for index, argument := range server.Args {
		server.Args[index], err = expandEnvironment(argument)
		if err != nil {
			return ServerConfig{}, err
		}
	}
	for key, value := range server.Env {
		server.Env[key], err = expandEnvironment(value)
		if err != nil {
			return ServerConfig{}, err
		}
	}
	server.InitOptions, err = expandAnyEnvironment(server.InitOptions)
	if err != nil {
		return ServerConfig{}, err
	}
	server.Settings, err = expandAnyEnvironment(server.Settings)
	if err != nil {
		return ServerConfig{}, err
	}
	server.Capabilities, err = expandAnyEnvironmentMap(server.Capabilities)
	if err != nil {
		return ServerConfig{}, err
	}
	server.WorkspaceReadyTimings, err = expandAnyEnvironment(server.WorkspaceReadyTimings)
	if err != nil {
		return ServerConfig{}, err
	}
	return server, nil
}

func normalizeLinterKind(name string, server ServerConfig) (ServerConfig, error) {
	explicitKind := strings.ToLower(strings.TrimSpace(server.Linter))
	mode := strings.ToLower(strings.TrimSpace(server.Mode))

	// A few clients historically put the transport selector in `linter`. Keep
	// those spellings as standard/CLI mode hints, but let a real supported
	// linter name remain authoritative.
	if explicitKind == "cli" || explicitKind == "linter" {
		if mode == "" {
			mode = "cli"
		}
		explicitKind = ""
	} else if explicitKind == "none" || explicitKind == "lsp" || explicitKind == "json-rpc" || explicitKind == "jsonrpc" || explicitKind == "stdio" || explicitKind == "language-server" {
		if mode == "" {
			mode = explicitKind
		}
		explicitKind = ""
	}
	if explicitKind != "" && explicitKind != "swiftlint" && explicitKind != "biome" {
		return ServerConfig{}, fmt.Errorf("unsupported LSP linter %q for server %q", explicitKind, name)
	}

	cliMode := false
	forceStandard := false
	switch mode {
	case "", "auto":
	case "cli", "linter":
		cliMode = true
	case "none", "stdio", "lsp", "json-rpc", "jsonrpc", "language-server":
		forceStandard = true
	default:
		return ServerConfig{}, fmt.Errorf("unsupported LSP mode %q for server %q", mode, name)
	}

	// Explicit linter selection wins over automatic mode detection. This lets
	// a caller keep a deliberate `linter: biome` even when a shared config also
	// declares a generic mode.
	kind := explicitKind
	if kind == "" && !forceStandard {
		commandName := strings.TrimSpace(server.Command)
		if commandName == "" {
			commandName = strings.TrimSpace(server.ResolvedCommand)
		}
		base := strings.ToLower(filepath.Base(commandName))
		base = strings.TrimSuffix(base, ".exe")
		switch {
		case base == "swiftlint":
			// SwiftLint has no standard LSP mode, so its distinctive executable is
			// safe to infer unless the caller explicitly forced stdio/lsp above.
			kind = "swiftlint"
		case base == "biome" && (server.IsLinter || cliMode):
			// `biome` also has an LSP proxy. Require IsLinter unless mode: cli
			// makes the user's intent explicit.
			kind = "biome"
		}
	}
	if cliMode && kind == "" {
		return ServerConfig{}, fmt.Errorf("mode cli requires a supported CLI linter (swiftlint or biome) for server %q", name)
	}
	server.Linter = kind
	server.Mode = mode
	return server, nil
}

func expandAnyEnvironmentMap(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	expanded, err := expandAnyEnvironment(value)
	if err != nil {
		return nil, err
	}
	result, ok := expanded.(map[string]any)
	if !ok {
		return nil, errors.New("LSP capabilities must be an object")
	}
	return result, nil
}

func expandAnyEnvironment(value any) (any, error) {
	switch current := value.(type) {
	case string:
		return expandEnvironment(current)
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			expanded, err := expandAnyEnvironment(item)
			if err != nil {
				return nil, err
			}
			result[index] = expanded
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			expanded, err := expandAnyEnvironment(item)
			if err != nil {
				return nil, err
			}
			result[key] = expanded
		}
		return result, nil
	default:
		return value, nil
	}
}

func expandEnvironment(value string) (string, error) {
	for iteration := 0; iteration < 8; iteration++ {
		changed := false
		missing := ""
		result := environmentReference.ReplaceAllStringFunc(value, func(reference string) string {
			matches := environmentReference.FindStringSubmatch(reference)
			name := matches[1]
			replacement, ok := os.LookupEnv(name)
			if !ok || (replacement == "" && len(matches) > 2 && matches[2] != "") {
				if len(matches) > 2 && matches[2] != "" {
					changed = true
					return matches[3]
				}
				missing = name
				return reference
			}
			changed = true
			return replacement
		})
		if missing != "" {
			return "", fmt.Errorf("environment variable %s is not set", missing)
		}
		if !changed || result == value {
			return result, nil
		}
		value = result
	}
	return "", errors.New("LSP environment expansion exceeded recursion limit")
}

func matchesRootMarkers(root string, markers []string) (bool, error) {
	if len(markers) == 0 {
		return true, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if marker == "" {
			continue
		}
		cleanMarker := filepath.Clean(filepath.FromSlash(marker))
		if filepath.IsAbs(cleanMarker) || cleanMarker == ".." || strings.HasPrefix(cleanMarker, ".."+string(filepath.Separator)) {
			return false, fmt.Errorf("root marker escapes workspace: %s", marker)
		}
		if strings.ContainsAny(marker, "*?[") {
			for _, entry := range entries {
				matched, matchErr := filepath.Match(marker, entry.Name())
				if matchErr != nil {
					return false, fmt.Errorf("invalid root marker %q: %w", marker, matchErr)
				}
				if matched {
					return true, nil
				}
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

// hasRootMarkerAncestor reports whether the directory containing filePath or
// one of its parents contains any configured project marker. It mirrors the
// behavior of editors that host nested projects: a workspace may contain
// several package roots, and a server should only handle files below one of
// its own roots.
func hasRootMarkerAncestor(filePath string, markers []string, boundaries ...string) (bool, error) {
	if len(markers) == 0 {
		return false, nil
	}
	// The API receives a file path (which may not exist yet, e.g. a rename
	// destination), so derive its containing directory without stat'ing it.
	directory := filepath.Dir(filePath)
	directory, err := filepath.Abs(directory)
	if err != nil {
		return false, err
	}
	boundary := ""
	if len(boundaries) > 0 && strings.TrimSpace(boundaries[0]) != "" {
		boundary, err = filepath.Abs(boundaries[0])
		if err != nil {
			return false, err
		}
		boundary = filepath.Clean(boundary)
		if !isPathWithinBoundary(boundary, filePath) {
			return false, nil
		}
		// Validate the real path of the nearest existing ancestor as well as the
		// lexical path. A workspace-internal symlink can otherwise make a marker
		// outside the workspace appear to belong to this server.
		realBoundary, resolveErr := resolveExistingAncestor(boundary)
		if resolveErr != nil {
			return false, resolveErr
		}
		realTarget, resolveErr := resolveExistingAncestor(filePath)
		if resolveErr != nil {
			return false, resolveErr
		}
		if !isPathWithinBoundary(realBoundary, realTarget) {
			return false, nil
		}
	}
	for {
		if boundary != "" && !isPathWithinBoundary(boundary, directory) {
			return false, nil
		}
		if _, statErr := os.Stat(directory); errors.Is(statErr, os.ErrNotExist) {
			parent := filepath.Dir(directory)
			if parent == directory {
				return false, nil
			}
			directory = parent
			continue
		} else if statErr != nil {
			return false, statErr
		}
		matched, matchErr := matchesRootMarkers(directory, markers)
		if matchErr != nil {
			return false, matchErr
		}
		if matched {
			return true, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false, nil
		}
		directory = parent
	}
}

// resolveExistingAncestor resolves the nearest existing path component. It is
// intentionally tolerant of a not-yet-created target, which is common during
// rename and create operations, while still resolving any symlink component
// that does exist.
func resolveExistingAncestor(path string) (string, error) {
	candidate := filepath.Clean(path)
	for {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			return filepath.Abs(resolved)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", err
		}
		candidate = parent
	}
}

func isPathWithinBoundary(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func resolveCommand(root string, command string) (string, error) {
	command = strings.TrimSpace(command)
	if filepath.IsAbs(command) {
		if isExecutable(command) {
			return command, nil
		}
		return "", fmt.Errorf("command is not executable: %s", command)
	}
	if strings.ContainsRune(command, filepath.Separator) || (filepath.Separator != '/' && strings.ContainsRune(command, '/')) {
		candidate := filepath.Join(root, command)
		if isExecutable(candidate) {
			return candidate, nil
		}
		return "", fmt.Errorf("command is not executable: %s", command)
	}
	localDirectories := []string{
		filepath.Join(root, "node_modules", ".bin"),
		filepath.Join(root, ".venv", "bin"),
		filepath.Join(root, ".venv", "Scripts"),
		filepath.Join(root, "venv", "bin"),
		filepath.Join(root, "venv", "Scripts"),
		filepath.Join(root, ".env", "bin"),
		filepath.Join(root, ".env", "Scripts"),
		filepath.Join(root, "vendor", "bundle", "bin"),
		filepath.Join(root, "bin"),
	}
	for _, directory := range localDirectories {
		candidate := filepath.Join(directory, command)
		if isExecutable(candidate) {
			return candidate, nil
		}
		for _, suffix := range []string{".exe", ".cmd", ".bat"} {
			if isExecutable(candidate + suffix) {
				return candidate + suffix, nil
			}
		}
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("command not found: %s", command)
	}
	return resolved, nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	return info.Mode().Perm()&0o111 != 0 || ext == ".exe" || ext == ".cmd" || ext == ".bat"
}

func defaultServers() map[string]ServerConfig {
	return map[string]ServerConfig{
		"gopls": {
			Command: "gopls", Args: []string{"serve"}, FileTypes: []string{".go", ".mod", ".sum", "go.mod", "go.work"},
			RootMarkers: []string{"go.mod", "go.work", "go.sum"}, LanguageID: "go", Priority: 100,
			Settings: map[string]any{"gopls": map[string]any{
				"analyses":    map[string]any{"unusedparams": true, "shadow": true},
				"staticcheck": true, "gofumpt": true,
			}},
		},
		"rust-analyzer": {
			Command: "rust-analyzer", FileTypes: []string{".rs"},
			RootMarkers: []string{"Cargo.toml", "rust-analyzer.toml"}, LanguageID: "rust", Priority: 100,
			InitOptions: map[string]any{},
			Settings:    map[string]any{"rust-analyzer": map[string]any{"checkOnSave": false}},
			Capabilities: map[string]any{
				"flycheck": true, "ssr": true, "expandMacro": true, "runnables": true, "relatedTests": true,
			},
		},
		"clangd": {
			Command: "clangd", Args: []string{"--background-index", "--clang-tidy", "--header-insertion=iwyu"},
			FileTypes:   []string{".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx", ".m", ".mm"},
			RootMarkers: []string{"compile_commands.json", "CMakeLists.txt", ".clangd", ".clang-format", "Makefile"}, Priority: 100,
		},
		"typescript-language-server": {
			Command: "typescript-language-server", Args: []string{"--stdio"},
			FileTypes:   []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
			RootMarkers: []string{"package.json", "tsconfig.json", "jsconfig.json"}, Priority: 100,
			InitOptions: map[string]any{
				"hostInfo": "omp-coding-agent",
				"preferences": map[string]any{
					"includeInlayParameterNameHints":         "all",
					"includeInlayVariableTypeHints":          true,
					"includeInlayFunctionParameterTypeHints": true,
				},
			},
		},
		"basedpyright": {
			Command: "basedpyright-langserver", Args: []string{"--stdio"}, FileTypes: []string{".py", ".pyi"},
			RootMarkers: []string{"pyproject.toml", "pyrightconfig.json", "setup.py", "requirements.txt"}, LanguageID: "python", Priority: 110,
			Settings: map[string]any{"basedpyright": map[string]any{"analysis": map[string]any{
				"autoSearchPaths": true, "diagnosticMode": "openFilesOnly", "useLibraryCodeForTypes": true,
			}}},
		},
		"pyright": {
			Command: "pyright-langserver", Args: []string{"--stdio"}, FileTypes: []string{".py", ".pyi"},
			RootMarkers: []string{"pyproject.toml", "pyrightconfig.json", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"}, LanguageID: "python", Priority: 100,
			Settings: map[string]any{"python": map[string]any{"analysis": map[string]any{
				"autoSearchPaths": true, "diagnosticMode": "openFilesOnly", "useLibraryCodeForTypes": true,
			}}},
		},
		"pylsp": {
			Command: "pylsp", FileTypes: []string{".py", ".pyi"},
			RootMarkers: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"}, LanguageID: "python", Priority: 90,
		},
		"bashls": {
			Command: "bash-language-server", Args: []string{"start"}, FileTypes: []string{".sh", ".bash", ".zsh"},
			RootMarkers: []string{".git"}, LanguageID: "shellscript", Priority: 100,
			Settings: map[string]any{"bashIde": map[string]any{"globPattern": "*@(.sh|.inc|.bash|.command)"}},
		},
		"lua-language-server": {
			Command: "lua-language-server", FileTypes: []string{".lua"}, RootMarkers: []string{".luarc.json", ".luarc.jsonc", ".luacheckrc", ".stylua.toml", "stylua.toml"},
			LanguageID: "lua", Priority: 100,
			Settings: map[string]any{"Lua": map[string]any{
				"runtime":     map[string]any{"version": "LuaJIT"},
				"diagnostics": map[string]any{"globals": []string{"vim"}},
				"workspace":   map[string]any{"checkThirdParty": false},
				"telemetry":   map[string]any{"enable": false},
			}},
		},
		"yamlls": {
			Command: "yaml-language-server", Args: []string{"--stdio"}, FileTypes: []string{".yaml", ".yml"},
			RootMarkers: []string{".git"}, LanguageID: "yaml", Priority: 100,
			Settings: map[string]any{
				"yaml":   map[string]any{"validate": true, "format": map[string]any{"enable": true}, "hover": true, "completion": true},
				"redhat": map[string]any{"telemetry": map[string]any{"enabled": false}},
			},
		},
		"terraformls": {
			Command: "terraform-ls", Args: []string{"serve"}, FileTypes: []string{".tf", ".tfvars"},
			RootMarkers: []string{".terraform", "terraform.tfstate", "*.tf"}, LanguageID: "terraform", Priority: 100,
		},
		"sourcekit-lsp": {
			Command: "sourcekit-lsp", FileTypes: []string{".swift"}, RootMarkers: []string{"Package.swift", "*.xcodeproj", "*.xcworkspace", "project.yml", ".swiftpm"},
			LanguageID: "swift", Priority: 100,
		},
		"swiftlint": {
			Command: "swiftlint", Args: []string{"lint", "--quiet", "--reporter", "json"},
			FileTypes: []string{".swift"}, RootMarkers: []string{".swiftlint.yml", ".swiftlint.yaml", "Package.swift", "*.xcodeproj"},
			LanguageID: "swift", IsLinter: true, Linter: "swiftlint", Priority: 80,
		},
		"zls": {
			Command: "zls", FileTypes: []string{".zig"}, RootMarkers: []string{"build.zig", "build.zig.zon", "zls.json"}, LanguageID: "zig", Priority: 100,
		},
		"denols": {
			Command: "deno", Args: []string{"lsp"}, FileTypes: []string{".ts", ".tsx", ".js", ".jsx"},
			RootMarkers: []string{"deno.json", "deno.jsonc", "deno.lock"}, LanguageID: "typescript", Priority: 105,
			InitOptions: map[string]any{"enable": true, "lint": true, "unstable": true},
		},
		"biome": {
			Command: "biome", Args: []string{"lsp-proxy"}, FileTypes: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".json", ".jsonc", ".css"},
			RootMarkers: []string{"biome.json", "biome.jsonc"}, IsLinter: true, Linter: "biome", Priority: 80,
		},
		"eslint": {
			Command: "vscode-eslint-language-server", Args: []string{"--stdio"}, FileTypes: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte"},
			RootMarkers: []string{".eslintrc", ".eslintrc.js", ".eslintrc.json", ".eslintrc.yml", "eslint.config.js", "eslint.config.mjs"}, IsLinter: true, Priority: 80,
			Settings: map[string]any{"validate": "on", "run": "onType"},
		},
		"vscode-html-language-server": {
			Command: "vscode-html-language-server", Args: []string{"--stdio"}, FileTypes: []string{".html", ".htm"},
			RootMarkers: []string{"package.json", ".git"}, LanguageID: "html", Priority: 100,
			InitOptions: map[string]any{"provideFormatter": true},
		},
		"vscode-css-language-server": {
			Command: "vscode-css-language-server", Args: []string{"--stdio"}, FileTypes: []string{".css", ".scss", ".sass", ".less"},
			RootMarkers: []string{"package.json", ".git"}, LanguageID: "css", Priority: 100,
			InitOptions: map[string]any{"provideFormatter": true},
		},
		"vscode-json-language-server": {
			Command: "vscode-json-language-server", Args: []string{"--stdio"}, FileTypes: []string{".json", ".jsonc"},
			RootMarkers: []string{"package.json", ".git"}, LanguageID: "json", Priority: 100,
			InitOptions: map[string]any{"provideFormatter": true},
		},
		"svelte": {
			Command: "svelteserver", Args: []string{"--stdio"}, FileTypes: []string{".svelte"},
			RootMarkers: []string{"svelte.config.js", "svelte.config.mjs", "package.json"}, LanguageID: "svelte", Priority: 100,
		},
		"vue-language-server": {
			Command: "vue-language-server", Args: []string{"--stdio"}, FileTypes: []string{".vue"},
			RootMarkers: []string{"vue.config.js", "nuxt.config.js", "nuxt.config.ts", "package.json"}, LanguageID: "vue", Priority: 100,
		},
		"jdtls": {
			Command: "jdtls", FileTypes: []string{".java"}, RootMarkers: []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", ".project"}, LanguageID: "java", Priority: 100,
		},
		"kotlin-lsp": {
			Command: "kotlin-lsp", Args: []string{"--stdio"}, FileTypes: []string{".kt", ".kts"}, RootMarkers: []string{"build.gradle", "build.gradle.kts", "pom.xml", "settings.gradle", "settings.gradle.kts"}, LanguageID: "kotlin", Priority: 100,
		},
		"solargraph": {
			Command: "solargraph", Args: []string{"stdio"}, FileTypes: []string{".rb", ".rake", ".gemspec"}, RootMarkers: []string{"Gemfile", ".solargraph.yml", "Rakefile"}, LanguageID: "ruby", Priority: 100,
			InitOptions: map[string]any{"formatting": true},
			Settings: map[string]any{"solargraph": map[string]any{
				"diagnostics": true, "completion": true, "hover": true, "formatting": true,
				"references": true, "rename": true, "symbols": true,
			}},
		},
		"ruby-lsp": {
			Command: "ruby-lsp", FileTypes: []string{".rb", ".rake", ".gemspec", ".erb"}, RootMarkers: []string{"Gemfile", ".ruby-version", ".ruby-gemset"}, LanguageID: "ruby", Priority: 105,
			InitOptions: map[string]any{"formatter": "auto"},
		},
		"intelephense": {
			Command: "intelephense", Args: []string{"--stdio"}, FileTypes: []string{".php", ".phtml"}, RootMarkers: []string{"composer.json", "composer.lock", ".git"}, LanguageID: "php", Priority: 100,
		},
		"omnisharp": {
			Command: "omnisharp", Args: []string{"-z", "--hostPID", "$PID", "--encoding", "utf-8", "--languageserver"}, FileTypes: []string{".cs", ".csx"}, RootMarkers: []string{"*.sln", "*.csproj", "omnisharp.json", ".git"}, LanguageID: "csharp", Priority: 100,
			Settings: map[string]any{
				"FormattingOptions":       map[string]any{"EnableEditorConfigSupport": true},
				"RoslynExtensionsOptions": map[string]any{"EnableAnalyzersSupport": true},
			},
		},
		"dockerls": {
			Command: "docker-langserver", Args: []string{"--stdio"}, FileTypes: []string{"Dockerfile", ".dockerfile"}, RootMarkers: []string{"Dockerfile", "docker-compose.yml", "docker-compose.yaml", ".dockerignore"}, LanguageID: "dockerfile", Priority: 100,
		},
		"marksman": {
			Command: "marksman", Args: []string{"server"}, FileTypes: []string{".md", ".markdown"}, RootMarkers: []string{".marksman.toml", ".git"}, LanguageID: "markdown", Priority: 90,
		},
		"graphql": {
			Command: "graphql-lsp", Args: []string{"server", "-m", "stream"}, FileTypes: []string{".graphql", ".gql"}, RootMarkers: []string{".graphqlrc", ".graphqlrc.json", ".graphqlrc.yml", ".graphqlrc.yaml", "graphql.config.js"}, LanguageID: "graphql", Priority: 100,
		},
		"tlaplus": {
			Command: "tlapm_lsp", Args: []string{"--stdio"}, FileTypes: []string{".tla", ".tlaplus"}, RootMarkers: []string{"*.tla"}, LanguageID: "tlaplus", Priority: 100,
		},
		"tailwindcss": {
			Command: "tailwindcss-language-server", Args: []string{"--stdio"}, FileTypes: []string{".html", ".css", ".scss", ".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte"}, RootMarkers: []string{"tailwind.config.js", "tailwind.config.ts", "tailwind.config.mjs", "tailwind.config.cjs"}, Priority: 70,
		},
		"astro": {
			Command: "astro-ls", Args: []string{"--stdio"}, FileTypes: []string{".astro"}, RootMarkers: []string{"astro.config.mjs", "astro.config.js", "astro.config.ts"}, LanguageID: "astro", Priority: 100,
		},
		"ty": {
			Command: "ty", Args: []string{"server"}, FileTypes: []string{".py", ".pyi"}, RootMarkers: []string{"pyproject.toml", "ty.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"}, LanguageID: "python", Priority: 115,
		},
		"ruff": {
			Command: "ruff", Args: []string{"server"}, FileTypes: []string{".py", ".pyi"}, RootMarkers: []string{"pyproject.toml", "ruff.toml", ".ruff.toml"}, LanguageID: "python", IsLinter: true, Priority: 80,
		},
		"metals": {
			Command: "metals", FileTypes: []string{".scala", ".sbt", ".sc"}, RootMarkers: []string{"build.sbt", "build.sc", "build.gradle", "pom.xml"}, LanguageID: "scala", Priority: 100,
			InitOptions: map[string]any{"statusBarProvider": "show-message", "isHttpEnabled": true},
		},
		"hls": {
			Command: "haskell-language-server-wrapper", Args: []string{"--lsp"}, FileTypes: []string{".hs", ".lhs"}, RootMarkers: []string{"stack.yaml", "cabal.project", "hie.yaml", "package.yaml", "*.cabal"}, LanguageID: "haskell", Priority: 100,
			Settings: map[string]any{"haskell": map[string]any{"formattingProvider": "ormolu", "checkProject": true}},
		},
		"ocamllsp": {
			Command: "ocamllsp", FileTypes: []string{".ml", ".mli", ".mll", ".mly"}, RootMarkers: []string{"dune-project", "dune-workspace", "*.opam", ".ocamlformat"}, LanguageID: "ocaml", Priority: 100,
		},
		"elixirls": {
			Command: "elixir-ls", FileTypes: []string{".ex", ".exs", ".heex", ".eex"}, RootMarkers: []string{"mix.exs", "mix.lock"}, LanguageID: "elixir", Priority: 100,
			Settings: map[string]any{"elixirLS": map[string]any{"dialyzerEnabled": true, "fetchDeps": false}},
		},
		"expert": {
			Command: "expert", Args: []string{"--stdio"}, FileTypes: []string{".ex", ".exs", ".heex", ".eex"}, RootMarkers: []string{"mix.exs", "mix.lock"}, LanguageID: "elixir", Priority: 105,
		},
		"erlangls": {
			Command: "erlang_ls", FileTypes: []string{".erl", ".hrl"}, RootMarkers: []string{"rebar.config", "erlang.mk", "rebar.lock"}, LanguageID: "erlang", Priority: 100,
		},
		"gleam": {
			Command: "gleam", Args: []string{"lsp"}, FileTypes: []string{".gleam"}, RootMarkers: []string{"gleam.toml"}, LanguageID: "gleam", Priority: 100,
		},
		"rubocop": {
			Command: "rubocop", Args: []string{"--lsp"}, FileTypes: []string{".rb", ".rake"}, RootMarkers: []string{".rubocop.yml", "Gemfile"}, LanguageID: "ruby", IsLinter: true, Priority: 80,
		},
		"phpactor": {
			Command: "phpactor", Args: []string{"language-server"}, FileTypes: []string{".php"}, RootMarkers: []string{"composer.json", ".phpactor.json", ".phpactor.yml"}, LanguageID: "php", Priority: 100,
		},
		"helm-ls": {
			Command: "helm_ls", Args: []string{"serve"}, FileTypes: []string{".yaml", ".yml", ".tpl"}, RootMarkers: []string{"Chart.yaml", "Chart.yml"}, LanguageID: "helm", Priority: 90,
		},
		"nixd": {
			Command: "nixd", FileTypes: []string{".nix"}, RootMarkers: []string{"flake.nix", "default.nix", "shell.nix"}, LanguageID: "nix", Priority: 100,
		},
		"nil": {
			Command: "nil", FileTypes: []string{".nix"}, RootMarkers: []string{"flake.nix", "default.nix", "shell.nix"}, LanguageID: "nix", Priority: 90,
		},
		"ols": {
			Command: "ols", FileTypes: []string{".odin"}, RootMarkers: []string{"ols.json", ".git"}, LanguageID: "odin", Priority: 100,
		},
		"dartls": {
			Command: "dart", Args: []string{"language-server", "--protocol=lsp"}, FileTypes: []string{".dart"}, RootMarkers: []string{"pubspec.yaml", "pubspec.lock"}, LanguageID: "dart", Priority: 100,
			InitOptions: map[string]any{"closingLabels": true, "flutterOutline": true, "outline": true},
		},
		"texlab": {
			Command: "texlab", FileTypes: []string{".tex", ".bib", ".sty", ".cls"}, RootMarkers: []string{".latexmkrc", "latexmkrc", ".texlabroot", "texlabroot", "Tectonic.toml"}, LanguageID: "latex", Priority: 100,
			Settings: map[string]any{"texlab": map[string]any{
				"build":  map[string]any{"executable": "latexmk", "args": []string{"-pdf", "-interaction=nonstopmode", "-synctex=1", "%f"}},
				"chktex": map[string]any{"onOpenAndSave": true},
			}},
		},
		"prismals": {
			Command: "prisma-language-server", Args: []string{"--stdio"}, FileTypes: []string{".prisma"}, RootMarkers: []string{"schema.prisma", "prisma/schema.prisma"}, LanguageID: "prisma", Priority: 100,
		},
		"vimls": {
			Command: "vim-language-server", Args: []string{"--stdio"}, FileTypes: []string{".vim", ".vimrc"}, RootMarkers: []string{".git"}, LanguageID: "viml", Priority: 100,
			InitOptions: map[string]any{"isNeovim": true, "diagnostic": map[string]any{"enable": true}},
		},
		"emmet-language-server": {
			Command: "emmet-language-server", Args: []string{"--stdio"}, FileTypes: []string{".html", ".css", ".scss", ".less", ".jsx", ".tsx", ".vue", ".svelte"}, RootMarkers: []string{".git"}, Priority: 60,
		},
	}
}
