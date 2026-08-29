// Package mcpclient connects Banka to Model Context Protocol servers.
package mcpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

var environmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)
var mcpProfileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const maxMCPConfigBytes = 2_000_000

// ServerConfig configures one stdio or Streamable HTTP MCP server.
type ServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// EnvPolicy is retained for Agent Plugin/OpenCode interoperability. The
	// literal policy prevents indirect environment-name expansion for package
	// supplied values; ordinary user configuration keeps the historical
	// expansion behavior.
	EnvPolicy string            `json:"envPolicy,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	// HeaderPolicy controls whether configured headers are treated as literal
	// origin-bound package data. The HTTP client always protects protocol
	// generated headers, regardless of this setting.
	HeaderPolicy string `json:"headerPolicy,omitempty"`
	Type         string `json:"type,omitempty"`
	Transport    string `json:"transport,omitempty"`
	TimeoutMS    int    `json:"timeout_ms,omitempty"`
	// RequestIDFormat, Auth and OAuth are retained for interoperability with
	// OMP/Codex configuration. The current Go SDK owns request IDs and does not
	// implement an OAuth broker, but preserving these fields lets callers inspect
	// the effective configuration and leaves room for future auth support.
	RequestIDFormat string         `json:"requestIdFormat,omitempty"`
	Auth            map[string]any `json:"auth,omitempty"`
	OAuth           map[string]any `json:"oauth,omitempty"`
	Disabled        bool           `json:"disabled,omitempty"`
	Trusted         bool           `json:"trusted,omitempty"`
	// ResolvedCommand is populated by a manager when a workspace-relative or
	// project-local executable is resolved. It is runtime-only and never read
	// from configuration files.
	ResolvedCommand string `json:"-"`
	// timeoutSet distinguishes an omitted timeout from an explicit `timeout: 0`.
	// The latter disables client-side MCP deadlines according to the OMP
	// configuration contract, while the former keeps Banka's safe default.
	timeoutSet bool
}

// Config is the merged MCP server configuration.
type Config struct {
	Servers map[string]ServerConfig
	// Profile is the active user-level MCP profile. An empty value denotes the
	// default profile. It is informational for callers and does not affect
	// project-scoped configuration.
	Profile string
}

type configFile struct {
	Servers    map[string]ServerConfig `json:"servers"`
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfig merges global and project MCP configuration files.
func LoadConfig(projectRoot string, homeDir string) (Config, error) {
	return LoadConfigWithProfile(projectRoot, homeDir, "")
}

// LoadConfigWithProfile merges MCP configuration while selecting an optional
// user profile. The profile is resolved from BANKA_PROFILE, OMP_PROFILE, or
// PI_PROFILE (in that order) when profile is empty. Project configuration and
// third-party client configuration remain shared; only native user files and
// enable/disable lists are isolated by profile.
func LoadConfigWithProfile(projectRoot string, homeDir string, profile string) (Config, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve MCP project root: %w", err)
	}
	profile, err = normalizeMCPProfile(profile)
	if err != nil {
		return Config{}, err
	}
	config := Config{Servers: make(map[string]ServerConfig), Profile: profile}
	// Keep a last-writer-wins tri-state for enable/disable controls.  A plain
	// pair of sets loses the precedence of a per-server `disabled` field when a
	// higher-precedence file also contains enabledServers/disabledServers.
	forcedDisabled := make(map[string]bool)
	forcedState := make(map[string]bool)
	var userEnabled, userDisabled []string
	for _, path := range mcpConfigPathsForProfile(root, homeDir, profile) {
		content, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Config{}, fmt.Errorf("read MCP config %s: %w", path, err)
		}
		if len(content) > maxMCPConfigBytes {
			return Config{}, fmt.Errorf("MCP config %s exceeds %d bytes", path, maxMCPConfigBytes)
		}
		file, err := decodeConfigObject(content, filepath.Ext(path))
		if err != nil {
			return Config{}, fmt.Errorf("parse MCP config %s: %w", path, err)
		}
		enabled, listErr := parseStringArray(file["enabledServers"])
		if listErr != nil {
			return Config{}, fmt.Errorf("parse MCP config %s enabledServers: %w", path, listErr)
		}
		disabled, listErr := parseStringArray(file["disabledServers"])
		if listErr != nil {
			return Config{}, fmt.Errorf("parse MCP config %s disabledServers: %w", path, listErr)
		}
		if isUserMCPConfigPath(path, homeDir, root) {
			userEnabled = append(userEnabled, enabled...)
			userDisabled = append(userDisabled, disabled...)
		}
		servers, serversErr := configServersForRootMode(file, root, isDedicatedMCPConfigPath(path))
		if serversErr != nil {
			return Config{}, fmt.Errorf("parse MCP config %s servers: %w", path, serversErr)
		}
		for name, raw := range servers {
			name = strings.TrimSpace(name)
			if name == "" {
				return Config{}, fmt.Errorf("parse MCP config %s: server name must not be empty", path)
			}
			normalized, normalizeErr := normalizeServerRaw(raw)
			if normalizeErr != nil {
				return Config{}, fmt.Errorf("parse MCP server %q in %s: %w", name, path, normalizeErr)
			}
			var override map[string]any
			if err := json.Unmarshal(normalized, &override); err != nil || override == nil {
				if err == nil {
					err = errors.New("server configuration must be an object")
				}
				return Config{}, fmt.Errorf("parse MCP server %q in %s: %w", name, path, err)
			}
			if value, exists := override["disabled"]; exists {
				disabled, ok := parseConfigBool(value)
				if !ok {
					return Config{}, fmt.Errorf("parse MCP server %q in %s: disabled must be a boolean", name, path)
				}
				forcedDisabled[name] = disabled
				forcedState[name] = true
			}
			base := config.Servers[name]
			baseJSON, marshalErr := json.Marshal(base)
			if marshalErr != nil {
				return Config{}, fmt.Errorf("parse MCP server %q in %s: %w", name, path, marshalErr)
			}
			var merged map[string]any
			if err := json.Unmarshal(baseJSON, &merged); err != nil {
				return Config{}, fmt.Errorf("parse MCP server %q in %s: %w", name, path, err)
			}
			for key, value := range override {
				merged[key] = value
			}
			mergedJSON, marshalErr := json.Marshal(merged)
			if marshalErr != nil {
				return Config{}, fmt.Errorf("parse MCP server %q in %s: %w", name, path, marshalErr)
			}
			var server ServerConfig
			if err := json.Unmarshal(mergedJSON, &server); err != nil {
				return Config{}, fmt.Errorf("parse MCP server %q in %s: %w", name, path, err)
			}
			server.timeoutSet = hasTimeoutField(override)
			if previous, exists := config.Servers[name]; exists && !server.timeoutSet {
				server.timeoutSet = previous.timeoutSet
			}
			config.Servers[name] = server
		}
		// Apply list directives after the server objects from this file. This
		// mirrors OMP's semantics: an enabledServers allow-list can override an
		// entry's `enabled: false`, while disabledServers is the explicit veto when
		// both lists mention the same name. Directives from later files still win
		// over lower-precedence files because the maps are updated in merge order.
		for _, name := range enabled {
			forcedDisabled[name] = false
			forcedState[name] = true
		}
		for _, name := range disabled {
			forcedDisabled[name] = true
			forcedState[name] = true
		}
	}
	// A user-level denylist is the final cross-provider veto. Apply the allowlist
	// first so an explicitly disabled user server remains dominant when a name is
	// present in both lists.
	for _, name := range userEnabled {
		forcedDisabled[name] = false
		forcedState[name] = true
	}
	for _, name := range userDisabled {
		forcedDisabled[name] = true
		forcedState[name] = true
	}
	for name, server := range config.Servers {
		if forcedState[name] {
			server.Disabled = forcedDisabled[name]
		}
		config.Servers[name] = server
	}
	for name, server := range config.Servers {
		if server.Disabled {
			config.Servers[name] = server
			continue
		}
		expanded, err := expandServerConfig(server)
		if err != nil {
			return Config{}, fmt.Errorf("MCP server %q: %w", name, err)
		}
		if err := validateServerConfig(expanded); err != nil {
			return Config{}, fmt.Errorf("MCP server %q: %w", name, err)
		}
		config.Servers[name] = expanded
	}
	return config, nil
}

// ActiveProfile returns the profile selected by the current process
// environment. BANKA_PROFILE is the native override; OMP_PROFILE and
// PI_PROFILE are accepted for compatibility with oh-my-pi and pi clients.
func ActiveProfile() string {
	for _, key := range []string{"BANKA_PROFILE", "OMP_PROFILE", "PI_PROFILE"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func normalizeMCPProfile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(ActiveProfile())
	}
	if value == "" {
		return "", nil
	}
	if !mcpProfileName.MatchString(value) || value == "." || value == ".." {
		return "", fmt.Errorf("invalid MCP profile %q: use 1-64 letters, numbers, '.', '_' or '-'", value)
	}
	return value, nil
}

func isUserMCPConfigPath(path string, homeDir string, projectRoot string) bool {
	if strings.TrimSpace(homeDir) == "" {
		return false
	}
	home, homeErr := filepath.Abs(homeDir)
	if homeErr != nil {
		return false
	}
	candidate, candidateErr := filepath.Abs(path)
	if candidateErr != nil {
		return false
	}
	if absoluteRoot, err := filepath.Abs(projectRoot); err == nil && absoluteRoot != home {
		if relativeToProject, relErr := filepath.Rel(absoluteRoot, candidate); relErr == nil && relativeToProject != ".." && !strings.HasPrefix(relativeToProject, ".."+string(filepath.Separator)) {
			return false
		}
	}
	relative, relErr := filepath.Rel(home, candidate)
	if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	// When the caller deliberately uses the same directory for home and project
	// (common in tests), treat the project path as project-scoped instead of
	// accidentally promoting every directive to user precedence.
	if absoluteRoot, err := filepath.Abs(projectRoot); err == nil && absoluteRoot == home {
		return false
	}
	return true
}

func mcpConfigPaths(root string, homeDir string) []string {
	return mcpConfigPathsForProfile(root, homeDir, "")
}

func mcpConfigPathsForProfile(root string, homeDir string, profile string) []string {
	var paths []string
	seen := make(map[string]bool)
	add := func(directory string, names ...string) {
		if directory == "" {
			return
		}
		for _, name := range names {
			candidate := filepath.Clean(filepath.Join(directory, name))
			if !seen[candidate] {
				seen[candidate] = true
				paths = append(paths, candidate)
			}
		}
	}
	filenames := []string{".mcp.toml", "mcp.toml", ".mcp.yml", "mcp.yml", ".mcp.yaml", "mcp.yaml", ".mcp.json", "mcp.json"}
	// Imported third-party sources are lower precedence than Banka/OMP-native
	// files, but are still useful when a project already has Claude, Codex,
	// Gemini, Cursor, Windsurf, VS Code, or OpenCode configuration.
	if homeDir != "" {
		add(homeDir, ".claude.json")
		add(filepath.Join(homeDir, ".claude"), ".mcp.json", "mcp.json")
		add(filepath.Join(homeDir, ".gemini"), "settings.json")
		add(filepath.Join(homeDir, ".cursor"), "mcp.json")
		add(filepath.Join(homeDir, ".codeium", "windsurf"), "mcp_config.json")
		add(filepath.Join(homeDir, ".config", "opencode"), "opencode.json")
		add(filepath.Join(homeDir, ".codex"), "config.toml")
		// A named profile replaces the default native user locations. This is
		// deliberately an allow-list rather than an additional overlay: loading
		// the default profile as well would leak servers and denylist state across
		// identities.
		if profile != "" {
			add(filepath.Join(homeDir, ".omp", "profiles", profile, "agent"), filenames...)
		} else {
			// User-native files are intentionally listed before project sources so
			// later project files win.
			add(homeDir, filenames...)
			for _, directory := range []string{".omp/agent", ".pi/agent", ".gemini", ".cursor", ".windsurf", ".opencode", ".claude", ".codex", ".banka", ".agents"} {
				add(filepath.Join(homeDir, filepath.FromSlash(directory)), filenames...)
			}
		}
	}
	// Project imports are below the user scope and below the root fallback;
	// native project directories are appended last and therefore win.
	add(root, "opencode.json")
	add(filepath.Join(root, ".vscode"), "mcp.json")
	add(filepath.Join(root, ".claude"), ".mcp.json", "mcp.json")
	add(filepath.Join(root, ".gemini"), "settings.json")
	add(filepath.Join(root, ".cursor"), "mcp.json")
	add(filepath.Join(root, ".windsurf"), "mcp_config.json")
	add(filepath.Join(root, ".codeium", "windsurf"), "mcp_config.json")
	add(filepath.Join(root, ".codex"), "config.toml")
	add(root, filenames...)
	for _, directory := range []string{".omp", ".pi", ".gemini", ".cursor", ".windsurf", ".opencode", ".claude", ".codex", ".github", ".banka", ".agents"} {
		add(filepath.Join(root, filepath.FromSlash(directory)), filenames...)
	}
	return paths
}

func decodeConfigObject(content []byte, extension string) (map[string]json.RawMessage, error) {
	if strings.EqualFold(extension, ".toml") {
		var value map[string]any
		if _, err := toml.Decode(string(content), &value); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		content = encoded
	}
	if strings.EqualFold(extension, ".yaml") || strings.EqualFold(extension, ".yml") {
		var value any
		if err := yaml.Unmarshal(content, &value); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(yamlValueToJSON(value))
		if err != nil {
			return nil, err
		}
		content = encoded
	}
	var top map[string]json.RawMessage
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

func configServers(top map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	return configServersForRoot(top, "")
}

// configServersForRoot extracts server definitions from the standard MCP
// wrappers and the project-scoped section used by Claude Code's
// ~/.claude.json. Keeping the root parameter optional preserves the small
// package-level helper used by older embedders/tests.
func configServersForRoot(top map[string]json.RawMessage, projectRoot string) (map[string]json.RawMessage, error) {
	return configServersForRootMode(top, projectRoot, true)
}

// configServersForRootMode extracts server definitions while controlling the
// legacy flat-map form. Dedicated mcp.json/mcp.yaml/mcp.toml files may be a
// plain {name: server} map; general application settings such as
// ~/.claude.json, ~/.codex/config.toml, and Gemini settings must use an
// explicit MCP wrapper so unrelated object-valued settings are not mistaken
// for servers.
func configServersForRootMode(top map[string]json.RawMessage, projectRoot string, allowFlat bool) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage)
	wrappedSeen := false
	readWrapper := func(key string) error {
		wrapped := top[key]
		if wrapped == nil {
			return nil
		}
		wrappedSeen = true
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(wrapped, &servers); err != nil || servers == nil {
			if err == nil {
				err = fmt.Errorf("%s must be an object", key)
			}
			return err
		}
		for name, value := range servers {
			result[name] = value
		}
		return nil
	}
	if err := readWrapper("servers"); err != nil {
		return nil, err
	}
	if err := readWrapper("mcpServers"); err != nil {
		return nil, err
	}
	if err := readWrapper("mcp_servers"); err != nil {
		return nil, err
	}
	// Claude Code stores project-specific servers below
	// `projects.<absolute-project-path>.mcpServers`. A top-level mcpServers map
	// remains valid and is loaded first; the project entry is a same-file
	// override, matching the client that owns this format.
	if projectsRaw := top["projects"]; projectsRaw != nil && strings.TrimSpace(projectRoot) != "" {
		var projects map[string]json.RawMessage
		if err := json.Unmarshal(projectsRaw, &projects); err != nil || projects == nil {
			if err == nil {
				err = errors.New("projects must be an object")
			}
			return nil, err
		}
		projectKeys := []string{projectRoot, filepath.Clean(projectRoot)}
		for _, projectKey := range projectKeys {
			entryRaw := projects[projectKey]
			if entryRaw == nil {
				continue
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(entryRaw, &entry); err != nil || entry == nil {
				if err == nil {
					err = errors.New("project entry must be an object")
				}
				return nil, err
			}
			for _, wrapperKey := range []string{"mcpServers", "mcp_servers", "servers"} {
				wrapped := entry[wrapperKey]
				if wrapped == nil {
					continue
				}
				var projectServers map[string]json.RawMessage
				if err := json.Unmarshal(wrapped, &projectServers); err != nil || projectServers == nil {
					if err == nil {
						err = fmt.Errorf("projects.%s.%s must be an object", projectKey, wrapperKey)
					}
					return nil, err
				}
				for name, value := range projectServers {
					result[name] = value
				}
				wrappedSeen = true
				break
			}
			break
		}
	}
	// OpenCode stores servers below `mcp`; some versions nest them one level
	// deeper as `mcp.servers`. Normalize both forms into the common map.
	if wrapped := top["mcp"]; wrapped != nil {
		wrappedSeen = true
		var mcpValue map[string]json.RawMessage
		if err := json.Unmarshal(wrapped, &mcpValue); err != nil || mcpValue == nil {
			if err == nil {
				err = errors.New("mcp must be an object")
			}
			return nil, err
		}
		if nested := mcpValue["servers"]; nested != nil {
			var servers map[string]json.RawMessage
			if err := json.Unmarshal(nested, &servers); err != nil || servers == nil {
				if err == nil {
					err = errors.New("mcp.servers must be an object")
				}
				return nil, err
			}
			for name, value := range servers {
				result[name] = value
			}
		} else {
			for name, value := range mcpValue {
				// `mcp` metadata keys are not server definitions.  A server entry is
				// object-valued and is validated more strictly below.
				if name == "enabled" || name == "timeout" || name == "servers" {
					continue
				}
				result[name] = value
			}
		}
	}
	if wrappedSeen || !allowFlat {
		return result, nil
	}
	for key, value := range top {
		if key == "mcpServers" || key == "mcp_servers" || key == "mcp" || key == "servers" || key == "projects" || key == "disabledServers" || key == "enabledServers" || key == "$schema" ||
			key == "idleTimeoutMs" || key == "timeout" || key == "version" || key == "metadata" {
			continue
		}
		// A flat config is accepted only for object-valued entries; scalar
		// metadata must not be mistaken for a server definition.
		var object map[string]any
		if json.Unmarshal(value, &object) == nil {
			result[key] = value
		}
	}
	return result, nil
}

func isDedicatedMCPConfigPath(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.Contains(name, "mcp")
}

func stringArray(value json.RawMessage) []string {
	if len(value) == 0 {
		return nil
	}
	var values []string
	if json.Unmarshal(value, &values) != nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func parseStringArray(value json.RawMessage) ([]string, error) {
	if len(value) == 0 {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(value, &values); err != nil {
		return nil, errors.New("must be an array of strings")
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result, nil
}

// Names returns enabled server names in deterministic order.
func (c Config) Names() []string {
	names := make([]string, 0, len(c.Servers))
	for name, server := range c.Servers {
		if !server.Disabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func validateServerConfig(server ServerConfig) error {
	if server.Disabled {
		return nil
	}
	hasCommand := strings.TrimSpace(server.Command) != "" || strings.TrimSpace(server.ResolvedCommand) != ""
	hasURL := strings.TrimSpace(server.URL) != ""
	if hasCommand == hasURL {
		return errors.New("configure exactly one of 'command' or 'url'")
	}
	if hasURL {
		parsed, err := url.ParseRequestURI(strings.TrimSpace(server.URL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("'url' must be a valid http or https URL")
		}
	}
	transport := normalizedTransport(server)
	if transport != "stdio" && transport != "streamable-http" && transport != "sse" {
		return fmt.Errorf("unsupported transport %q", transport)
	}
	// An explicit transport declaration must agree with the endpoint shape.
	// Without this check a typo such as {"type":"http","command":"..."}
	// would be silently treated as stdio by the defaulting logic, hiding a
	// configuration error and making the server run with unexpected semantics.
	declaredTransport := strings.ToLower(strings.TrimSpace(server.Transport))
	if declaredTransport == "" {
		declaredTransport = strings.ToLower(strings.TrimSpace(server.Type))
	}
	if declaredTransport != "" {
		switch declaredTransport {
		case "http", "streamable_http", "streamable-http", "streamablehttp", "http-stream", "http_stream":
			if hasCommand {
				return errors.New("http transport requires a url and cannot use command")
			}
		case "sse", "server-sent-events", "server_sent_events":
			if hasCommand {
				return errors.New("sse transport requires a url and cannot use command")
			}
		case "stdio":
			if hasURL {
				return errors.New("stdio transport requires a command and cannot use url")
			}
		}
	}
	if hasCommand && transport != "stdio" {
		return errors.New("command servers must use stdio transport")
	}
	if hasURL && transport == "stdio" {
		return errors.New("URL servers must use streamable-http or sse transport")
	}
	if server.TimeoutMS < 0 || server.TimeoutMS > 600000 {
		return errors.New("timeout_ms must be between 0 and 600000")
	}
	if policy := strings.ToLower(strings.TrimSpace(server.EnvPolicy)); policy != "" && policy != "literal" {
		return fmt.Errorf("unsupported envPolicy %q", server.EnvPolicy)
	}
	if policy := strings.ToLower(strings.TrimSpace(server.HeaderPolicy)); policy != "" && policy != "origin-locked" {
		return fmt.Errorf("unsupported headerPolicy %q", server.HeaderPolicy)
	}
	if len(server.Headers) > 100 {
		return errors.New("too many HTTP headers")
	}
	for key, value := range server.Headers {
		if strings.TrimSpace(key) == "" || len(key) > 256 || len(value) > 16*1024 {
			return errors.New("invalid MCP HTTP header")
		}
	}
	return nil
}

func normalizeServerRaw(raw json.RawMessage) (json.RawMessage, error) {
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("server configuration must be an object")
	}
	aliases := map[string]string{
		"endpoint":          "url",
		"timeout":           "timeout_ms",
		"timeoutMs":         "timeout_ms",
		"timeout-ms":        "timeout_ms",
		"timeoutMS":         "timeout_ms",
		"timeoutMillis":     "timeout_ms",
		"timeout_millis":    "timeout_ms",
		"workingDirectory":  "cwd",
		"working_directory": "cwd",
		"working-directory": "cwd",
		"httpHeaders":       "headers",
		"http_headers":      "headers",
		"http-headers":      "headers",
		"environment":       "env",
		"envVars":           "env",
		"env_vars":          "env",
		"isTrusted":         "trusted",
		"is_trusted":        "trusted",
		"is-trusted":        "trusted",
		"request_id_format": "requestIdFormat",
		"request-id-format": "requestIdFormat",
		"requestIDFormat":   "requestIdFormat",
		"env_policy":        "envPolicy",
		"env-policy":        "envPolicy",
		"header_policy":     "headerPolicy",
		"header-policy":     "headerPolicy",
		"isOriginLocked":    "headerPolicy",
	}
	// OpenCode represents local commands as an argv array (`command: [bin,
	// arg...]`) and uses `local`/`remote` transport labels.  Convert that
	// shape before unmarshalling into the stricter Banka ServerConfig type.
	if commandValues, ok := values["command"].([]any); ok {
		if len(commandValues) == 0 {
			return nil, errors.New("command array must not be empty")
		}
		command, ok := commandValues[0].(string)
		if !ok || strings.TrimSpace(command) == "" {
			return nil, errors.New("command array must start with a string")
		}
		args := make([]any, 0, len(commandValues)-1)
		for _, value := range commandValues[1:] {
			if _, ok := value.(string); !ok {
				return nil, errors.New("command array arguments must be strings")
			}
			args = append(args, value)
		}
		values["command"] = command
		if _, exists := values["args"]; !exists {
			values["args"] = args
		}
	}
	for _, key := range []string{"type", "transport"} {
		if transport, ok := values[key].(string); ok {
			switch strings.ToLower(strings.TrimSpace(transport)) {
			case "local":
				values[key] = "stdio"
			case "remote":
				values[key] = "http"
			}
		}
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
		if enabled, ok := parseConfigBool(value); ok {
			if _, already := values["disabled"]; !already {
				values["disabled"] = !enabled
			}
		} else {
			return nil, errors.New("enabled must be a boolean")
		}
		delete(values, "enabled")
	}
	return json.Marshal(values)
}

func hasTimeoutField(values map[string]any) bool {
	for _, key := range []string{"timeout_ms", "timeoutMs", "timeout-ms", "timeoutMS", "timeoutMillis", "timeout_millis", "timeout"} {
		if _, exists := values[key]; exists {
			return true
		}
	}
	return false
}

func parseConfigBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func normalizedTransport(server ServerConfig) string {
	value := strings.ToLower(strings.TrimSpace(server.Transport))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(server.Type))
	}
	switch value {
	case "", "http", "streamable_http", "streamable-http", "streamablehttp", "http-stream", "http_stream":
		if strings.TrimSpace(server.Command) != "" || strings.TrimSpace(server.ResolvedCommand) != "" {
			return "stdio"
		}
		return "streamable-http"
	case "stdio":
		return "stdio"
	case "sse", "server-sent-events", "server_sent_events":
		return "sse"
	default:
		return value
	}
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
	server.URL, err = expandEnvironment(server.URL)
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
		if strings.ToLower(strings.TrimSpace(server.EnvPolicy)) != "literal" {
			server.Env[key], err = expandEnvironment(value)
			if err != nil {
				return ServerConfig{}, err
			}
			server.Env[key] = resolveIndirectEnvironment(server.Env[key])
		}
	}
	for key, value := range server.Headers {
		if strings.ToLower(strings.TrimSpace(server.HeaderPolicy)) != "origin-locked" {
			server.Headers[key], err = expandEnvironment(value)
			if err != nil {
				return ServerConfig{}, err
			}
			server.Headers[key] = resolveIndirectEnvironment(server.Headers[key])
		}
	}
	server.Auth, err = expandAnyEnvironmentMap(server.Auth)
	if err != nil {
		return ServerConfig{}, err
	}
	server.OAuth, err = expandAnyEnvironmentMap(server.OAuth)
	if err != nil {
		return ServerConfig{}, err
	}
	return server, nil
}

// resolveIndirectEnvironment accepts the convention used by Claude/Codex and
// OMP configs where an env/header value equal to a variable name means "copy
// that variable from the current process". Literal values remain unchanged.
func resolveIndirectEnvironment(value string) string {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " \t\r\n") || strings.Contains(value, "=") {
		return value
	}
	if replacement, ok := os.LookupEnv(value); ok && replacement != "" {
		return replacement
	}
	return value
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
		return nil, errors.New("MCP auth/oauth configuration must be an object")
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
					// `${VAR:-default}` follows shell-style "unset or empty"
					// semantics; the default may itself be an empty string.
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
	return "", errors.New("MCP environment expansion exceeded recursion limit")
}
