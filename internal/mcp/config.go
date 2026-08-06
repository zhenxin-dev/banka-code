// Package mcpclient connects Banka to Model Context Protocol servers.
package mcpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var environmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ServerConfig configures one stdio or Streamable HTTP MCP server.
type ServerConfig struct {
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	Trusted  bool              `json:"trusted,omitempty"`
}

// Config is the merged MCP server configuration.
type Config struct {
	Servers map[string]ServerConfig
}

type configFile struct {
	Servers    map[string]ServerConfig `json:"servers"`
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfig merges global and project MCP configuration files.
func LoadConfig(projectRoot string, homeDir string) (Config, error) {
	paths := []string{}
	if homeDir != "" {
		paths = append(paths, filepath.Join(homeDir, ".banka", "mcp.json"))
	}
	paths = append(paths,
		filepath.Join(projectRoot, ".mcp.json"),
		filepath.Join(projectRoot, ".banka", "mcp.json"),
	)
	config := Config{Servers: make(map[string]ServerConfig)}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Config{}, fmt.Errorf("read MCP config %s: %w", path, err)
		}
		var file configFile
		if err := json.Unmarshal(content, &file); err != nil {
			return Config{}, fmt.Errorf("parse MCP config %s: %w", path, err)
		}
		for name, server := range file.MCPServers {
			config.Servers[name] = server
		}
		for name, server := range file.Servers {
			config.Servers[name] = server
		}
	}
	for name, server := range config.Servers {
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
	hasCommand := strings.TrimSpace(server.Command) != ""
	hasURL := strings.TrimSpace(server.URL) != ""
	if hasCommand == hasURL {
		return errors.New("configure exactly one of 'command' or 'url'")
	}
	if hasURL && !strings.HasPrefix(server.URL, "http://") && !strings.HasPrefix(server.URL, "https://") {
		return errors.New("'url' must use http or https")
	}
	return nil
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
		server.Env[key], err = expandEnvironment(value)
		if err != nil {
			return ServerConfig{}, err
		}
	}
	for key, value := range server.Headers {
		server.Headers[key], err = expandEnvironment(value)
		if err != nil {
			return ServerConfig{}, err
		}
	}
	return server, nil
}

func expandEnvironment(value string) (string, error) {
	var missing string
	result := environmentReference.ReplaceAllStringFunc(value, func(reference string) string {
		name := environmentReference.FindStringSubmatch(reference)[1]
		replacement, ok := os.LookupEnv(name)
		if !ok {
			missing = name
			return reference
		}
		return replacement
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return result, nil
}
