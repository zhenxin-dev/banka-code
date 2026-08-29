package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

const connectionTimeout = 15 * time.Second
const discoveryTimeout = 30 * time.Second

// startupGate is the maximum amount of time Connect waits synchronously for
// MCP discovery.  Slow servers continue in the background and publish their
// tools through SetToolsChangedHandler once ready, keeping the TUI responsive.
const startupGate = 250 * time.Millisecond

func mcpConfiguredTimeout(server ServerConfig, fallback time.Duration) time.Duration {
	// OMP uses OMP_MCP_TIMEOUT_MS as a process-wide override. BANKA_MCP_TIMEOUT_MS
	// is accepted as the native spelling; the native BANKA value wins when both
	// are set so an embedding application can override imported OMP settings.
	for _, key := range []string{"BANKA_MCP_TIMEOUT_MS", "OMP_MCP_TIMEOUT_MS"} {
		if raw, ok := os.LookupEnv(key); ok {
			value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err == nil && value >= 0 && value <= 600000 {
				return time.Duration(value) * time.Millisecond
			}
		}
	}
	if server.TimeoutMS > 0 {
		return time.Duration(server.TimeoutMS) * time.Millisecond
	}
	if server.timeoutSet {
		return 0
	}
	return fallback
}

func withMCPTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// Status reports one configured MCP server's connection state.
type Status struct {
	Name      string
	Transport string
	ToolCount int
	Error     string
	// Connecting is true while the initial handshake or tools/list request is
	// still running. Connected distinguishes a live session from a merely
	// configured server and is intentionally additive for compatibility with
	// callers that only used the original fields.
	Connecting bool
	Connected  bool
}

// Manager owns MCP client sessions for one Banka process.
type Manager struct {
	projectRoot string
	version     string
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	// notifyMu serializes callbacks into the host tool registry. Building a
	// catalog happens outside mu, so without a separate ordering lock an older
	// build could finish after a newer one and overwrite the registry callback.
	notifyMu     sync.Mutex
	connections  map[string]serverConnection
	configs      map[string]ServerConfig
	remoteTools  map[string][]*mcp.Tool
	definitions  []tools.Definition
	toolsChanged func([]tools.Definition)
	toolRefresh  map[string]bool
	statuses     []Status
	closed       bool
	epoch        uint64
	// catalogVersion monotonically identifies the remote tool snapshot. A
	// refresh builds wrappers outside the manager lock; the version prevents a
	// slower, older build from overwriting a newer snapshot when refreshes race.
	catalogVersion uint64
	generations    map[string]uint64
	startupCancel  context.CancelFunc
	startupEpoch   uint64
}

type serverConnection struct {
	session *mcp.ClientSession
	trusted bool
	timeout time.Duration
}

type discoveryResult struct {
	name       string
	server     ServerConfig
	status     Status
	session    *mcp.ClientSession
	timeout    time.Duration
	tools      []*mcp.Tool
	generation uint64
	epoch      uint64
}

// NewManager creates an MCP session manager.
func NewManager(projectRoot string, version string) *Manager {
	if absolute, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = absolute
	}
	return &Manager{
		projectRoot: projectRoot,
		version:     version,
		connections: make(map[string]serverConnection),
		configs:     make(map[string]ServerConfig),
		remoteTools: make(map[string][]*mcp.Tool),
		toolRefresh: make(map[string]bool),
		generations: make(map[string]uint64),
	}
}

// SetToolsChangedHandler registers a callback for the complete MCP-facing tool
// set. The callback is invoked immediately with the current snapshot and again
// after tools/list_changed notifications or successful reconnects. Callbacks
// run without manager locks and may safely update a tool registry.
func (m *Manager) SetToolsChangedHandler(handler func([]tools.Definition)) {
	if m == nil {
		return
	}
	// Registration itself participates in callback ordering. Keep the ordering
	// lock through the immediate callback so a concurrent refresh cannot invoke
	// the new handler before its initial snapshot has been delivered.
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	m.toolsChanged = handler
	snapshot := append([]tools.Definition(nil), m.definitions...)
	m.mu.Unlock()
	if handler != nil {
		handler(snapshot)
	}
}

// ToolDefinitions returns the latest MCP tool snapshot, including capability
// tools for resources and prompts. The returned slice is independent of the
// manager's internal registry and is safe for the caller to retain.
func (m *Manager) ToolDefinitions() []tools.Definition {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]tools.Definition(nil), m.definitions...)
}

// Connect starts configured servers and returns the tools discovered during a
// short startup gate. Slow servers continue in the background and publish a
// refreshed snapshot through SetToolsChangedHandler. A failed server is
// recorded in Statuses while other servers remain available.
func (m *Manager) Connect(ctx context.Context, config Config) []tools.Definition {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	// Cancel and invalidate a previous discovery run before replacing the
	// manager's state. The generation map prevents a late result from an older
	// run (or a manual reconnect) resurrecting a stale session.
	m.mu.Lock()
	if m.startupCancel != nil {
		m.startupCancel()
		m.startupCancel = nil
		m.startupEpoch = 0
	}
	m.epoch++
	m.catalogVersion++
	epoch := m.epoch
	oldConnections := m.connections
	m.connections = make(map[string]serverConnection)
	m.configs = cloneServerConfigs(config.Servers)
	m.remoteTools = make(map[string][]*mcp.Tool)
	m.definitions = nil
	m.toolRefresh = make(map[string]bool)
	m.generations = make(map[string]uint64, len(config.Servers))
	m.statuses = nil
	m.closed = false
	for name, server := range config.Servers {
		m.generations[name] = 1
		if server.Disabled {
			continue
		}
		m.statuses = append(m.statuses, Status{Name: name, Transport: normalizedTransport(server), Connecting: true})
	}
	sort.Slice(m.statuses, func(i, j int) bool { return m.statuses[i].Name < m.statuses[j].Name })
	startupContext, startupCancel := context.WithCancel(ctx)
	m.startupCancel = startupCancel
	m.startupEpoch = epoch
	m.mu.Unlock()
	for _, connection := range oldConnections {
		if connection.session != nil {
			_ = connection.session.Close()
		}
	}
	// Drop wrappers from the previous configuration immediately. Without this
	// notification, `/mcp reload` with an empty or slow configuration leaves
	// stale tools visible in the registry until another server finishes
	// discovery (and forever when the new configuration has no servers).
	m.notifyToolsChanged()

	names := config.Names()
	if len(names) == 0 {
		startupCancel()
		m.mu.Lock()
		if m.epoch == epoch {
			m.startupCancel = nil
			m.startupEpoch = 0
			for index := range m.statuses {
				m.statuses[index].Connecting = false
			}
		}
		m.mu.Unlock()
		return m.rebuildToolDefinitions(false)
	}

	// Connecting each server independently avoids one unavailable endpoint
	// delaying healthy servers. Results are buffered so a canceled background
	// collector can never strand a discovery goroutine trying to report.
	results := make(chan discoveryResult, len(names))
	for _, name := range names {
		name, server := name, config.Servers[name]
		generation := m.generations[name]
		go m.discoverServer(startupContext, epoch, generation, name, server, results)
	}

	// Return whichever results are ready within the fast gate. Remaining
	// results are committed by a background collector and trigger the callback.
	ready := 0
	var gate *time.Timer
	var gateC <-chan time.Time
	gate = time.NewTimer(startupGate)
	gateC = gate.C
	for ready < len(names) {
		select {
		case result := <-results:
			ready++
			m.commitDiscovery(result)
		case <-gateC:
			gateC = nil
		case <-ctx.Done():
			// Return promptly when the caller cancels. Discovery goroutines still
			// report into the buffered channel and the background collector will
			// close any late sessions after the epoch changes.
			gateC = nil
		}
		if gateC == nil {
			break
		}
	}
	if gate != nil && !gate.Stop() {
		select {
		case <-gate.C:
		default:
		}
	}
	if ready < len(names) {
		remaining := len(names) - ready
		go func() {
			for index := 0; index < remaining; index++ {
				m.commitDiscovery(<-results)
			}
			startupCancel()
			m.mu.Lock()
			if m.epoch == epoch && m.startupEpoch == epoch {
				m.startupCancel = nil
				m.startupEpoch = 0
			}
			m.mu.Unlock()
		}()
	} else {
		startupCancel()
		m.mu.Lock()
		if m.epoch == epoch && m.startupEpoch == epoch {
			m.startupCancel = nil
			m.startupEpoch = 0
		}
		m.mu.Unlock()
	}
	return m.rebuildToolDefinitions(false)
}

func (m *Manager) discoverServer(ctx context.Context, epoch, generation uint64, name string, server ServerConfig, results chan<- discoveryResult) {
	result := discoveryResult{name: name, server: server, epoch: epoch, generation: generation,
		status: Status{Name: name, Transport: normalizedTransport(server)}}
	if err := validateServerConfig(server); err != nil {
		result.status.Error = err.Error()
		results <- result
		return
	}
	if normalizedTransport(server) == "stdio" {
		resolved := strings.TrimSpace(server.ResolvedCommand)
		if resolved == "" {
			var resolveErr error
			resolved, resolveErr = resolveMCPCommand(m.projectRoot, server.Command)
			if resolveErr != nil {
				result.status.Error = resolveErr.Error()
				results <- result
				return
			}
		}
		server.Command = resolved
		server.ResolvedCommand = resolved
		result.server.Command = resolved
		result.server.ResolvedCommand = resolved
	}
	session, err := m.connectServer(ctx, name, server)
	if err != nil {
		result.status.Error = err.Error()
		results <- result
		return
	}
	result.session = session
	result.timeout = mcpConfiguredTimeout(server, discoveryTimeout)
	discoveryContext, cancel := withMCPTimeout(ctx, result.timeout)
	for remoteTool, listErr := range session.Tools(discoveryContext, nil) {
		if listErr != nil {
			result.status.Error = listErr.Error()
			_ = session.Close()
			result.session = nil
			break
		}
		if remoteTool != nil {
			result.tools = append(result.tools, remoteTool)
		}
	}
	cancel()
	results <- result
}

// commitDiscovery installs one result if it still belongs to the active
// Connect/reconnect generation. Stale sessions are closed immediately.
func (m *Manager) commitDiscovery(discovery discoveryResult) {
	if discovery.session == nil {
		m.mu.Lock()
		if discovery.epoch == m.epoch && m.generations[discovery.name] == discovery.generation {
			m.updateStatusLocked(discovery.name, discovery.status, false)
		}
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	valid := !m.closed && discovery.epoch == m.epoch && m.generations[discovery.name] == discovery.generation
	if valid {
		m.connections[discovery.name] = serverConnection{session: discovery.session, trusted: discovery.server.Trusted, timeout: discovery.timeout}
		m.remoteTools[discovery.name] = append([]*mcp.Tool(nil), discovery.tools...)
		m.catalogVersion++
		m.updateStatusLocked(discovery.name, discovery.status, true)
	}
	m.mu.Unlock()
	if !valid {
		_ = discovery.session.Close()
		return
	}
	m.observeSession(discovery.name, discovery.session)
	m.rebuildToolDefinitions(true)
}

func (m *Manager) updateStatusLocked(name string, status Status, connected bool) {
	for index := range m.statuses {
		if m.statuses[index].Name != name {
			continue
		}
		if status.Transport != "" {
			m.statuses[index].Transport = status.Transport
		}
		m.statuses[index].ToolCount = len(m.remoteTools[name])
		m.statuses[index].Error = status.Error
		m.statuses[index].Connecting = false
		m.statuses[index].Connected = connected
		return
	}
	m.statuses = append(m.statuses, Status{Name: name, Transport: status.Transport, ToolCount: len(m.remoteTools[name]), Error: status.Error, Connected: connected})
	sort.Slice(m.statuses, func(i, j int) bool { return m.statuses[i].Name < m.statuses[j].Name })
}

// Reconnect closes and recreates one MCP server session. Existing tool
// wrappers remain usable: they resolve the manager's current session for each
// call and retry once when the previous transport was dropped.
func (m *Manager) Reconnect(ctx context.Context, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("MCP manager is closed")
	}
	server, exists := m.configs[name]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("unknown MCP server: %s", name)
	}
	if server.Disabled {
		m.mu.Unlock()
		return fmt.Errorf("MCP server %q is disabled", name)
	}
	// Invalidate any still-running initial discovery for this server before
	// replacing its session. Other servers' startup work can continue.
	m.generations[name]++
	generation := m.generations[name]
	old := m.connections[name]
	delete(m.connections, name)
	delete(m.remoteTools, name)
	m.catalogVersion++
	for index := range m.statuses {
		if m.statuses[index].Name == name {
			m.statuses[index].Connecting = true
			m.statuses[index].Connected = false
			m.statuses[index].ToolCount = 0
			m.statuses[index].Error = ""
		}
	}
	m.mu.Unlock()
	if old.session != nil {
		_ = old.session.Close()
	}
	// A reconnect can fail after the previous session has been removed. Publish
	// that removal before dialing so stale wrappers do not remain callable when
	// command resolution, authentication, or discovery fails.
	m.rebuildToolDefinitions(true)
	if err := validateServerConfig(server); err != nil {
		m.setStatusError(name, err)
		return err
	}
	if normalizedTransport(server) == "stdio" {
		resolved := strings.TrimSpace(server.ResolvedCommand)
		if resolved == "" {
			var resolveErr error
			resolved, resolveErr = resolveMCPCommand(m.projectRoot, server.Command)
			if resolveErr != nil {
				m.setStatusError(name, resolveErr)
				return resolveErr
			}
		}
		server.Command = resolved
		server.ResolvedCommand = resolved
	}
	session, err := m.connectServer(ctx, name, server)
	if err != nil {
		m.setStatusError(name, err)
		return err
	}
	timeout := mcpConfiguredTimeout(server, discoveryTimeout)
	discoveryContext, cancel := withMCPTimeout(ctx, timeout)
	var discovered []*mcp.Tool
	for remoteTool, listErr := range session.Tools(discoveryContext, nil) {
		if listErr != nil {
			cancel()
			_ = session.Close()
			m.setStatusError(name, listErr)
			return listErr
		}
		if remoteTool != nil {
			discovered = append(discovered, remoteTool)
		}
	}
	cancel()
	m.mu.Lock()
	if m.closed || m.generations[name] != generation {
		m.mu.Unlock()
		_ = session.Close()
		if m.closed {
			return errors.New("MCP manager is closed")
		}
		return errors.New("MCP server reconnect was superseded")
	}
	m.connections[name] = serverConnection{session: session, trusted: server.Trusted, timeout: timeout}
	m.remoteTools[name] = append([]*mcp.Tool(nil), discovered...)
	m.catalogVersion++
	for index := range m.statuses {
		if m.statuses[index].Name == name {
			m.statuses[index].Transport = normalizedTransport(server)
			m.statuses[index].ToolCount = len(discovered)
			m.statuses[index].Error = ""
			m.statuses[index].Connecting = false
			m.statuses[index].Connected = true
		}
	}
	m.mu.Unlock()
	m.observeSession(name, session)
	m.rebuildToolDefinitions(true)
	return nil
}

// Reload is an alias for Reconnect retained for callers that expose a reload
// command in their UI.
func (m *Manager) Reload(ctx context.Context, name string) error { return m.Reconnect(ctx, name) }

func (m *Manager) setStatusError(name string, err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.statuses {
		if m.statuses[index].Name == name {
			m.statuses[index].Error = err.Error()
			m.statuses[index].Connecting = false
			m.statuses[index].Connected = false
			m.statuses[index].ToolCount = 0
			return
		}
	}
	m.statuses = append(m.statuses, Status{Name: name, Error: err.Error(), Connecting: false})
	sort.Slice(m.statuses, func(i, j int) bool { return m.statuses[i].Name < m.statuses[j].Name })
}

// observeSession records an unexpected remote close without turning an
// explicit manager shutdown into a reconnect/error event.
func (m *Manager) observeSession(name string, session *mcp.ClientSession) {
	if session == nil {
		return
	}
	go func() {
		err := session.Wait()
		m.mu.Lock()
		current, ok := m.connections[name]
		if !ok || current.session != session || m.closed {
			m.mu.Unlock()
			return
		}
		delete(m.connections, name)
		delete(m.remoteTools, name)
		m.catalogVersion++
		for index := range m.statuses {
			if m.statuses[index].Name != name {
				continue
			}
			m.statuses[index].Connected = false
			m.statuses[index].Connecting = false
			m.statuses[index].ToolCount = 0
			if err != nil {
				m.statuses[index].Error = err.Error()
			} else {
				m.statuses[index].Error = "MCP session closed"
			}
			break
		}
		m.mu.Unlock()
		m.rebuildToolDefinitions(true)
	}()
}

func cloneServerConfigs(values map[string]ServerConfig) map[string]ServerConfig {
	result := make(map[string]ServerConfig, len(values))
	for name, server := range values {
		server.Args = append([]string(nil), server.Args...)
		server.Env = cloneStringMap(server.Env)
		server.Headers = cloneStringMap(server.Headers)
		server.Auth = cloneAnyMap(server.Auth)
		server.OAuth = cloneAnyMap(server.OAuth)
		result[name] = server
	}
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = cloneAnyValue(value)
	}
	return result
}

func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneAnyValue(item)
		}
		return result
	default:
		return value
	}
}

func uniqueMCPToolName(base string, serverName string, remoteName string, used map[string]bool) string {
	sum := sha256.Sum256([]byte(serverName + "\x00" + remoteName))
	suffix := "_" + hex.EncodeToString(sum[:4])
	if len(base) > 64-len(suffix) {
		base = base[:64-len(suffix)]
	}
	candidate := base + suffix
	for index := 2; used[candidate]; index++ {
		numericSuffix := fmt.Sprintf("_%d", index)
		prefixLength := 64 - len(numericSuffix)
		if prefixLength < 1 {
			prefixLength = 1
		}
		prefix := base
		if len(prefix) > prefixLength {
			prefix = prefix[:prefixLength]
		}
		candidate = prefix + numericSuffix
	}
	return candidate
}

// Statuses returns MCP connection results in configuration order.
func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Status(nil), m.statuses...)
}

// Close terminates all MCP sessions.
func (m *Manager) Close() error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.epoch++
	m.catalogVersion++
	if m.startupCancel != nil {
		m.startupCancel()
		m.startupCancel = nil
		m.startupEpoch = 0
	}
	for name := range m.generations {
		m.generations[name]++
	}
	connections := m.connections
	m.connections = make(map[string]serverConnection)
	m.remoteTools = make(map[string][]*mcp.Tool)
	m.definitions = nil
	for index := range m.statuses {
		m.statuses[index].Connected = false
		m.statuses[index].Connecting = false
		m.statuses[index].ToolCount = 0
	}
	m.mu.Unlock()
	var firstErr error
	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		connection := connections[name]
		if connection.session == nil {
			continue
		}
		if err := connection.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.notifyToolsChanged()
	return firstErr
}

func (m *Manager) notifyToolsChanged() {
	if m == nil {
		return
	}
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.RLock()
	handler := m.toolsChanged
	snapshot := append([]tools.Definition(nil), m.definitions...)
	m.mu.RUnlock()
	if handler != nil {
		handler(snapshot)
	}
}

// notifyToolsChangedVersion publishes a snapshot only when the catalog still
// has the version that produced it. The check is intentionally performed after
// acquiring notifyMu: an older build may have been waiting while a newer
// callback was delivered, and must be discarded rather than delivered late.
func (m *Manager) notifyToolsChangedVersion(version uint64) {
	if m == nil {
		return
	}
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.RLock()
	if m.closed || m.catalogVersion != version {
		m.mu.RUnlock()
		return
	}
	handler := m.toolsChanged
	snapshot := append([]tools.Definition(nil), m.definitions...)
	m.mu.RUnlock()
	if handler != nil {
		handler(snapshot)
	}
}

func (m *Manager) connectedNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.connections))
	for name := range m.connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) connection(name string) (serverConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	connection, ok := m.connections[name]
	return connection, ok
}

func (m *Manager) connectServer(parent context.Context, name string, server ServerConfig) (*mcp.ClientSession, error) {
	timeout := mcpConfiguredTimeout(server, connectionTimeout)
	if parent == nil {
		parent = context.Background()
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "banka-code", Version: m.version}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			m.scheduleToolRefresh(name)
		},
	})
	rootURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(m.projectRoot)}).String()
	client.AddRoots(&mcp.Root{URI: rootURI, Name: filepath.Base(m.projectRoot)})

	var transport mcp.Transport
	if server.Command != "" {
		commandName := strings.TrimSpace(server.ResolvedCommand)
		if commandName == "" {
			commandName = strings.TrimSpace(server.Command)
		}
		command := exec.Command(commandName, server.Args...)
		cwd, err := resolveMCPCwd(m.projectRoot, server.Cwd)
		if err != nil {
			return nil, err
		}
		command.Dir = cwd
		command.Env = mergedEnvironment(server.Env)
		transport = &mcp.CommandTransport{Command: command}
	} else {
		httpClient := newMCPHTTPClient(server.URL, effectiveMCPHeaders(server), strings.EqualFold(strings.TrimSpace(server.HeaderPolicy), "origin-locked"))
		if normalizedTransport(server) == "sse" {
			transport = &mcp.SSEClientTransport{Endpoint: server.URL, HTTPClient: httpClient}
		} else {
			transport = &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: httpClient}
		}
	}
	// A successful MCP session outlives the startup deadline.  Passing a
	// context.WithTimeout directly to the SDK is especially problematic for
	// legacy SSE transport: its long-lived GET stream is tied to that context
	// and would be cancelled as soon as this function returned.  Use a detached
	// cancellable context for the session and race the initial handshake against
	// an explicit timer instead.
	connectCtx, cancel := context.WithCancel(context.WithoutCancel(parent))
	type connectResult struct {
		session *mcp.ClientSession
		err     error
	}
	resultCh := make(chan connectResult, 1)
	go func() {
		session, err := client.Connect(connectCtx, transport, nil)
		if err == nil && connectCtx.Err() != nil {
			_ = session.Close()
			session = nil
		}
		resultCh <- connectResult{session: session, err: err}
	}()
	var timer *time.Timer
	var timerChannel <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timerChannel = timer.C
		defer timer.Stop()
	}
	select {
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return result.session, result.err
		}
		if result.session == nil {
			cancel()
			return nil, errors.New("MCP client returned an empty session")
		}
		// Keep the detached session context alive for the lifetime of the
		// connection, then release it as soon as the SDK reports that the
		// session has closed.  This both preserves long-lived SSE streams and
		// avoids leaking the cancel function after a successful handshake.
		go func(session *mcp.ClientSession, release context.CancelFunc) {
			_ = session.Wait()
			release()
		}(result.session, cancel)
		return result.session, result.err
	case <-timerChannel:
		cancel()
		return nil, fmt.Errorf("MCP server connection timed out after %s", timeout)
	case <-parent.Done():
		cancel()
		return nil, parent.Err()
	}
}

func (m *Manager) scheduleToolRefresh(name string) {
	m.mu.Lock()
	if m.closed || m.toolRefresh[name] {
		m.mu.Unlock()
		return
	}
	m.toolRefresh[name] = true
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.toolRefresh, name)
			m.mu.Unlock()
		}()
		connection, exists := m.connection(name)
		if !exists || connection.session == nil {
			return
		}
		ctx, cancel := mcpOperationContext(context.Background(), connection.timeout)
		defer cancel()
		var remoteTools []*mcp.Tool
		for remoteTool, err := range connection.session.Tools(ctx, nil) {
			if err != nil {
				m.setStatusError(name, err)
				return
			}
			if remoteTool != nil {
				remoteTools = append(remoteTools, remoteTool)
			}
		}
		m.mu.Lock()
		current, stillConnected := m.connections[name]
		if m.closed || !stillConnected || current.session != connection.session {
			m.mu.Unlock()
			return
		}
		m.remoteTools[name] = remoteTools
		m.catalogVersion++
		m.mu.Unlock()
		m.rebuildToolDefinitions(true)
	}()
}

// rebuildToolDefinitions recreates wrappers from the current server/tool
// snapshots. Rebuilding the whole set keeps collision suffixes deterministic
// when a server adds or removes a name that sanitizes to an existing tool.
func (m *Manager) rebuildToolDefinitions(notify bool) []tools.Definition {
	m.mu.RLock()
	catalogVersion := m.catalogVersion
	names := make([]string, 0, len(m.connections))
	connections := make(map[string]serverConnection, len(m.connections))
	remoteTools := make(map[string][]*mcp.Tool, len(m.remoteTools))
	for name, connection := range m.connections {
		names = append(names, name)
		connections[name] = connection
		remoteTools[name] = append([]*mcp.Tool(nil), m.remoteTools[name]...)
	}
	m.mu.RUnlock()
	sort.Strings(names)
	usedNames := make(map[string]bool)
	definitions := make([]tools.Definition, 0)
	toolCounts := make(map[string]int, len(names))
	toolErrors := make(map[string]string)
	for _, name := range names {
		connection := connections[name]
		serverTools := remoteTools[name]
		sort.SliceStable(serverTools, func(i, j int) bool {
			if serverTools[i] == nil {
				return false
			}
			if serverTools[j] == nil {
				return true
			}
			return serverTools[i].Name < serverTools[j].Name
		})
		for _, remoteTool := range serverTools {
			definition, err := newMCPTool(name, remoteTool, connection.session, connection.trusted, connection.timeout)
			if err != nil {
				if toolErrors[name] == "" {
					toolErrors[name] = err.Error()
				}
				continue
			}
			typed, ok := definition.(*mcpTool)
			if ok {
				typed.manager = m
			}
			if usedNames[definition.Name()] && ok {
				typed.name = uniqueMCPToolName(typed.name, name, remoteTool.Name, usedNames)
				definition = typed
			}
			if usedNames[definition.Name()] {
				toolErrors[name] = fmt.Sprintf("duplicate generated tool name: %s", definition.Name())
				continue
			}
			usedNames[definition.Name()] = true
			definitions = append(definitions, definition)
			toolCounts[name]++
		}
	}
	if len(names) > 0 {
		definitions = append(definitions, newCapabilityTools(m)...)
	}
	m.mu.Lock()
	accepted := !m.closed && catalogVersion == m.catalogVersion
	if accepted {
		m.definitions = append([]tools.Definition(nil), definitions...)
		for index := range m.statuses {
			name := m.statuses[index].Name
			if _, connected := connections[name]; !connected {
				continue
			}
			m.statuses[index].ToolCount = toolCounts[name]
			if toolErrors[name] != "" {
				m.statuses[index].Error = toolErrors[name]
			} else if m.statuses[index].Error != "" {
				// A successful list refresh clears an older discovery/schema error.
				m.statuses[index].Error = ""
			}
		}
	}
	m.mu.Unlock()
	if notify && accepted {
		m.notifyToolsChangedVersion(catalogVersion)
	}
	return append([]tools.Definition(nil), definitions...)
}

// resolveMCPCommand resolves workspace-relative command paths while leaving
// ordinary PATH commands to exec.LookPath. MCP commands are intentionally not
// shell-parsed: each argument remains an argv element, preventing accidental
// shell interpolation.
func resolveMCPCommand(root string, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("MCP stdio command is empty")
	}
	if filepath.IsAbs(command) {
		if info, err := os.Stat(command); err == nil && info.Mode().IsRegular() {
			if info.Mode().Perm()&0o111 != 0 || strings.EqualFold(filepath.Ext(command), ".exe") || strings.EqualFold(filepath.Ext(command), ".cmd") || strings.EqualFold(filepath.Ext(command), ".bat") {
				return command, nil
			}
		}
		return "", fmt.Errorf("MCP command is not executable: %s", command)
	}
	if strings.ContainsRune(command, filepath.Separator) || (filepath.Separator != '/' && strings.ContainsRune(command, '/')) {
		candidate := filepath.Join(root, command)
		resolvedCandidate, pathErr := tools.ResolveSafePath(root, candidate)
		if pathErr != nil {
			return "", fmt.Errorf("MCP command path escapes workspace: %s", command)
		}
		if info, err := os.Stat(resolvedCandidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return resolvedCandidate, nil
		}
		return "", fmt.Errorf("MCP command is not executable: %s", command)
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("MCP command not found: %s", command)
	}
	return resolved, nil
}

func resolveMCPCwd(root string, configured string) (string, error) {
	candidate := strings.TrimSpace(configured)
	if candidate == "" {
		candidate = root
	} else if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	resolved, err := tools.ResolveSafePath(root, filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("MCP cwd escapes workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("MCP cwd is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("MCP cwd must be a directory")
	}
	return resolved, nil
}

func newMCPHTTPClient(endpoint string, headers map[string]string, originLocked ...bool) *http.Client {
	origin, _ := url.Parse(endpoint)
	headerCopy := make(map[string]string, len(headers))
	for key, value := range headers {
		if isReservedMCPHeader(key) {
			// Protocol-owned headers must never be configurable. Silently
			// dropping them keeps malformed third-party config from corrupting
			// the SDK handshake while preserving ordinary custom auth headers.
			continue
		}
		headerCopy[key] = value
	}
	return &http.Client{
		Transport: headerRoundTripper{base: http.DefaultTransport, headers: headerCopy},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many MCP HTTP redirects")
			}
			// Configured credentials are never forwarded to another origin. This is
			// stricter than net/http's default redirect behavior (which only strips
			// a small set of sensitive headers) and protects custom API-key headers.
			// The optional originLocked flag is accepted for compatibility with
			// Agent Plugin configuration; both modes fail closed when credentials
			// would cross an origin.
			if origin != nil && len(headerCopy) > 0 && !sameHTTPOrigin(origin, request.URL) {
				return errors.New("refusing to forward MCP headers across origins")
			}
			return nil
		},
	}
}

func sameHTTPOrigin(left *url.URL, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for key, value := range t.headers {
		// The SDK sets protocol-owned headers (Content-Type, Accept,
		// Mcp-Session-Id, and protocol version) immediately before the request
		// reaches the transport. Do not let user configuration override those
		// values, including through a differently-cased duplicate key.
		if isReservedMCPHeader(key) || hasHeaderCaseInsensitive(cloned.Header, key) {
			continue
		}
		cloned.Header.Set(key, value)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func isReservedMCPHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-type", "content-length", "accept", "host", "mcp-session-id", "mcp-protocol-version":
		return true
	default:
		return false
	}
}

func hasHeaderCaseInsensitive(headers http.Header, name string) bool {
	lower := strings.ToLower(name)
	for key := range headers {
		if strings.ToLower(key) == lower {
			return true
		}
	}
	return false
}

// effectiveMCPHeaders combines ordinary configured headers with the static
// authentication forms used by MCP clients. OMP normally obtains OAuth tokens
// from a credential vault; Banka has no vault yet, so accepting an explicitly
// supplied token keeps common `auth`/`oauth` configurations useful without
// pretending to implement an interactive OAuth flow.
func effectiveMCPHeaders(server ServerConfig) map[string]string {
	headers := cloneStringMap(server.Headers)
	if headers == nil {
		headers = make(map[string]string)
	}
	if name, value, ok := staticMCPAuthHeader(server); ok {
		setHeaderCaseInsensitive(headers, name, value)
	}
	return headers
}

func staticMCPAuthHeader(server ServerConfig) (string, string, bool) {
	// An explicit authorization value is already in wire format and must not be
	// prefixed a second time.
	for _, source := range []map[string]any{server.Auth, server.OAuth} {
		if value := firstStringField(source, "authorization", "authorizationHeader", "authorization_header"); value != "" {
			return "Authorization", resolveIndirectEnvironment(value), true
		}
	}
	authType := strings.ToLower(strings.TrimSpace(firstStringField(server.Auth, "type", "kind", "strategy")))
	token := firstStringField(server.Auth,
		"accessToken", "access_token", "apiKey", "api_key", "token", "bearer", "secret", "key", "value")
	if token == "" {
		token = firstStringField(server.OAuth, "accessToken", "access_token", "token", "bearer", "value")
	}
	token = resolveIndirectEnvironment(token)
	if token == "" {
		return "", "", false
	}
	headerName := firstStringField(server.Auth, "header", "headerName", "header_name", "headerKey", "header_key")
	if headerName == "" {
		headerName = firstStringField(server.OAuth, "header", "headerName", "header_name")
	}
	if headerName == "" {
		headerName = "Authorization"
	}
	scheme := firstStringField(server.Auth, "scheme", "authScheme", "auth_scheme", "prefix")
	if scheme == "" {
		scheme = firstStringField(server.OAuth, "scheme", "prefix")
	}
	if strings.EqualFold(headerName, "authorization") {
		if scheme == "" {
			// Preserve values that already carry a standard auth scheme. Bare
			// tokens use Bearer, which is the interoperable MCP convention.
			if !looksLikeAuthScheme(token) {
				scheme = "Bearer"
			}
		}
		if scheme != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(token)), strings.ToLower(strings.TrimSpace(scheme))+" ") {
			token = strings.TrimSpace(scheme) + " " + strings.TrimSpace(token)
		}
	} else if authType == "oauth" && scheme != "" {
		// A custom header with OAuth semantics still commonly expects the
		// configured scheme (for example `X-Access-Token: Bearer ...`).
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(token)), strings.ToLower(strings.TrimSpace(scheme))+" ") {
			token = strings.TrimSpace(scheme) + " " + strings.TrimSpace(token)
		}
	}
	return headerName, token, true
}

func firstStringField(values map[string]any, keys ...string) string {
	if values == nil {
		return ""
	}
	for _, key := range keys {
		for actual, value := range values {
			if !strings.EqualFold(actual, key) {
				continue
			}
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func looksLikeAuthScheme(value string) bool {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "bearer", "basic", "digest", "negotiate", "oauth", "token":
		return true
	default:
		return false
	}
}

func setHeaderCaseInsensitive(headers map[string]string, name string, value string) {
	lower := strings.ToLower(name)
	for key := range headers {
		if strings.ToLower(key) == lower {
			delete(headers, key)
		}
	}
	headers[name] = value
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
