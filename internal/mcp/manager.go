package mcpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

const connectionTimeout = 15 * time.Second

// Status reports one configured MCP server's connection state.
type Status struct {
	Name      string
	Transport string
	ToolCount int
	Error     string
}

// Manager owns MCP client sessions for one Banka process.
type Manager struct {
	projectRoot string
	version     string
	connections map[string]serverConnection
	statuses    []Status
}

type serverConnection struct {
	session *mcp.ClientSession
	trusted bool
}

// NewManager creates an MCP session manager.
func NewManager(projectRoot string, version string) *Manager {
	return &Manager{projectRoot: projectRoot, version: version, connections: make(map[string]serverConnection)}
}

// Connect starts configured servers and returns their discovered tools.
// A failed server is recorded in Statuses while other servers remain available.
func (m *Manager) Connect(ctx context.Context, config Config) []tools.Definition {
	var definitions []tools.Definition
	usedNames := make(map[string]bool)
	for _, name := range config.Names() {
		server := config.Servers[name]
		status := Status{Name: name, Transport: "stdio"}
		if server.URL != "" {
			status.Transport = "http"
		}
		session, err := m.connectServer(ctx, server)
		if err != nil {
			status.Error = err.Error()
			m.statuses = append(m.statuses, status)
			continue
		}
		m.connections[name] = serverConnection{session: session, trusted: server.Trusted}
		for remoteTool, listErr := range session.Tools(ctx, nil) {
			if listErr != nil {
				status.Error = listErr.Error()
				break
			}
			definition, definitionErr := newMCPTool(name, remoteTool, session, server.Trusted)
			if definitionErr != nil {
				status.Error = definitionErr.Error()
				break
			}
			if usedNames[definition.Name()] {
				status.Error = fmt.Sprintf("duplicate generated tool name: %s", definition.Name())
				break
			}
			usedNames[definition.Name()] = true
			definitions = append(definitions, definition)
			status.ToolCount++
		}
		m.statuses = append(m.statuses, status)
	}
	if len(m.connections) > 0 {
		definitions = append(definitions, newCapabilityTools(m)...)
	}
	return definitions
}

// Statuses returns MCP connection results in configuration order.
func (m *Manager) Statuses() []Status {
	return append([]Status(nil), m.statuses...)
}

// Close terminates all MCP sessions.
func (m *Manager) Close() error {
	var firstErr error
	for _, name := range m.connectedNames() {
		if err := m.connections[name].session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.connections = make(map[string]serverConnection)
	return firstErr
}

func (m *Manager) connectedNames() []string {
	names := make([]string, 0, len(m.connections))
	for name := range m.connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) connectServer(parent context.Context, server ServerConfig) (*mcp.ClientSession, error) {
	ctx, cancel := context.WithTimeout(parent, connectionTimeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "banka-code", Version: m.version}, nil)
	rootURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(m.projectRoot)}).String()
	client.AddRoots(&mcp.Root{URI: rootURI, Name: filepath.Base(m.projectRoot)})

	var transport mcp.Transport
	if server.Command != "" {
		command := exec.Command(server.Command, server.Args...)
		command.Dir = m.projectRoot
		if server.Cwd != "" {
			command.Dir = server.Cwd
			if !filepath.IsAbs(command.Dir) {
				command.Dir = filepath.Join(m.projectRoot, command.Dir)
			}
		}
		command.Env = mergedEnvironment(server.Env)
		transport = &mcp.CommandTransport{Command: command}
	} else {
		transport = &mcp.StreamableClientTransport{
			Endpoint: server.URL,
			HTTPClient: &http.Client{Transport: headerRoundTripper{
				base: http.DefaultTransport, headers: server.Headers,
			}},
		}
	}
	return client.Connect(ctx, transport, nil)
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for key, value := range t.headers {
		cloned.Header.Set(key, value)
	}
	return t.base.RoundTrip(cloned)
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok && !strings.HasPrefix(key, "BANKA_") {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
