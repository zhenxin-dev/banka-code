package lspclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

// ServerStatus describes a configured or running language server.
type ServerStatus struct {
	Name              string
	Command           string
	Available         bool
	Running           bool
	Connecting        bool
	OpenDocuments     int
	Diagnostics       int
	UnavailableReason string
	Error             string
}

// ServerMatch identifies one enabled language server that handles a file.
type ServerMatch struct {
	Name   string
	Config ServerConfig
}

// Manager owns lazily-created language-server clients for a workspace.
type Manager struct {
	root    string
	version string
	config  Config
	// configEpoch changes whenever the effective configuration or a server
	// lifecycle is reloaded.  A client handshake can take several seconds; the
	// epoch lets its completion path detect that the result belongs to an older
	// configuration and close it instead of publishing a stale client.
	configEpoch uint64

	mu         sync.Mutex
	clients    map[string]*Client
	errors     map[string]string
	starting   map[string]*clientStart
	lastUsed   map[string]time.Time
	closed     bool
	reaperStop chan struct{}
	reaperDone chan struct{}
	reaperOnce sync.Once
}

type clientStart struct {
	done   chan struct{}
	client *Client
	err    error
	cancel context.CancelFunc
	epoch  uint64
}

// NewManager creates an LSP manager. Servers are not started until needed.
func NewManager(root string, version string, config Config) *Manager {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	effective := prepareConfig(root, config)
	manager := &Manager{root: root, version: version, config: effective, configEpoch: 1, clients: make(map[string]*Client), errors: make(map[string]string), starting: make(map[string]*clientStart), lastUsed: make(map[string]time.Time), reaperStop: make(chan struct{}), reaperDone: make(chan struct{})}
	go manager.idleReaper()
	return manager
}

// prepareConfig normalizes aliases and resolves executable names for both
// startup and live configuration reloads. It deliberately does not return an
// error for an unavailable command: status views should retain that server and
// explain why it cannot be started.
func prepareConfig(root string, config Config) Config {
	effective := cloneConfig(config)
	// Normalize compatibility aliases for callers that construct Config
	// directly instead of using LoadConfig.
	if len(effective.Servers) > 0 {
		normalizedServers := make(map[string]ServerConfig, len(effective.Servers))
		names := make([]string, 0, len(effective.Servers))
		for name := range effective.Servers {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			left, right := strings.TrimSpace(names[i]), strings.TrimSpace(names[j])
			leftCanonical := canonicalLSPServerName(left) == left
			rightCanonical := canonicalLSPServerName(right) == right
			if leftCanonical != rightCanonical {
				return !leftCanonical
			}
			return left < right
		})
		for _, name := range names {
			server := effective.Servers[name]
			canonical := canonicalLSPServerName(name)
			if _, already := normalizedServers[canonical]; already && canonical != name {
				continue
			}
			normalizedServers[canonical] = server
		}
		effective.Servers = normalizedServers
	}
	// Callers embedding the package may construct Config directly instead of
	// going through LoadConfig.  Resolve command names here as a compatibility
	// convenience while preserving an explicitly supplied unavailable reason.
	for name, server := range effective.Servers {
		if normalized, err := normalizeLinterKind(name, server); err == nil {
			server = normalized
		} else {
			if server.UnavailableReason == "" {
				server.UnavailableReason = err.Error()
			}
			// An invalid mode/linter must never become startable merely because
			// its executable happens to resolve successfully.
			server.ResolvedCommand = ""
			effective.Servers[name] = server
			continue
		}
		if server.Disabled || server.ResolvedCommand != "" || strings.TrimSpace(server.Command) == "" {
			continue
		}
		if resolved, err := resolveCommand(root, server.Command); err == nil {
			server.ResolvedCommand = resolved
		} else if server.UnavailableReason == "" {
			server.UnavailableReason = err.Error()
		}
		effective.Servers[name] = server
	}
	return effective
}

// Config returns a copy of the manager configuration.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneConfig(m.config)
}

// serverConfig returns a defensive copy of one configured server. Keeping this
// lookup behind the manager lock lets tool calls safely overlap an interactive
// configuration reload.
func (m *Manager) serverConfig(name string) (ServerConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	server, ok := m.config.Servers[name]
	if !ok {
		return ServerConfig{}, false
	}
	copy := server
	copy.Args = append([]string(nil), server.Args...)
	copy.FileTypes = append([]string(nil), server.FileTypes...)
	copy.RootMarkers = append([]string(nil), server.RootMarkers...)
	copy.Env = cloneStringMap(server.Env)
	copy.Capabilities = cloneAnyMap(server.Capabilities)
	copy.InitOptions = cloneAnyValue(server.InitOptions)
	copy.Settings = cloneAnyValue(server.Settings)
	copy.WorkspaceReadyTimings = cloneAnyValue(server.WorkspaceReadyTimings)
	return copy, true
}

// NewTool creates the model-facing LSP tool.
func (m *Manager) NewTool() tools.Definition { return &tool{manager: m} }

// ReloadConfiguration atomically replaces the effective LSP configuration and
// closes existing clients. The next operation lazily starts servers from the
// new configuration. It is intended for interactive `/lsp reload` commands
// after the configuration files have been edited on disk.
func (m *Manager) ReloadConfiguration(ctx context.Context, config Config) error {
	if m == nil {
		return errors.New("LSP manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	effective := prepareConfig(m.root, config)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("LSP manager is closed")
	}
	clients := make([]*Client, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	starts := make([]*clientStart, 0, len(m.starting))
	for name, pending := range m.starting {
		starts = append(starts, pending)
		delete(m.starting, name)
		if pending.cancel != nil {
			pending.cancel()
		}
	}
	m.clients = make(map[string]*Client)
	m.errors = make(map[string]string)
	m.lastUsed = make(map[string]time.Time)
	m.configEpoch++
	m.config = effective
	m.mu.Unlock()

	// Do not leave old subprocesses attached to the new configuration. A
	// bounded wait keeps reload responsive even if a server ignores shutdown.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	var waitErr error
	waiting := true
	for _, pending := range starts {
		if !waiting {
			break
		}
		select {
		case <-pending.done:
		case <-ctx.Done():
			// Cancellation should not skip cleanup of already-running clients.
			// The pending startup was cancelled above and will close any process it
			// manages when it observes the superseded entry; stop waiting here and
			// let that bounded asynchronous cleanup finish independently.
			waitErr = ctx.Err()
			waiting = false
		case <-deadline.C:
			// The old startup will finish in the background and close its client
			// when it observes that its pending entry was superseded.
			waiting = false
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].Name() < clients[j].Name() })
	for _, client := range clients {
		if err := client.Close(); err != nil && !errors.Is(err, context.Canceled) {
			// A reload should still install the new configuration when one old
			// process reports a shutdown error; status on the next start exposes
			// any real failure.
			continue
		}
	}
	return waitErr
}

// Statuses returns deterministic status snapshots without starting servers.
func (m *Manager) Statuses() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ServerStatus, 0, len(m.config.Servers))
	for _, name := range m.config.Statuses() {
		server := m.config.Servers[name]
		status := ServerStatus{Name: name, Command: server.Command, Available: server.ResolvedCommand != "", UnavailableReason: server.UnavailableReason}
		if client := m.clients[name]; client != nil {
			snapshot := client.Status()
			status.Running = snapshot.Running
			status.OpenDocuments = snapshot.OpenDocuments
			status.Diagnostics = snapshot.Diagnostics
			status.Command = snapshot.Command
			if status.Error == "" {
				status.Error = snapshot.Error
			}
		}
		if m.starting[name] != nil {
			status.Connecting = true
		}
		if err := m.errors[name]; err != "" {
			status.Error = err
		}
		result = append(result, status)
	}
	return result
}

// ClientForFile returns (and lazily starts) the highest-priority server for a file.
func (m *Manager) ClientForFile(ctx context.Context, path string) (*Client, ServerConfig, error) {
	name, config, err := m.ServerForFile(path)
	if err != nil {
		return nil, ServerConfig{}, err
	}
	return m.Client(ctx, name, config)
}

// ServerForFile selects a server by extension/basename and priority.
func (m *Manager) ServerForFile(path string) (string, ServerConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if safePath, err := safeWorkspacePath(m.root, path); err != nil {
		return "", ServerConfig{}, err
	} else {
		path = safePath
	}
	candidates := serverMatchesLockedWithin(m.config, path, m.root)
	if len(candidates) == 0 {
		return "", ServerConfig{}, fmt.Errorf("no configured language server handles %s", path)
	}
	return candidates[0].Name, candidates[0].Config, nil
}

// ServersForFile returns all enabled servers matching a file, ordered by
// priority. This is useful for diagnostics where a primary server and one or
// more linter servers should all contribute results.
func (m *Manager) ServersForFile(path string) []ServerMatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	if safePath, err := safeWorkspacePath(m.root, path); err != nil {
		return nil
	} else {
		path = safePath
	}
	return serverMatchesLockedWithin(m.config, path, m.root)
}

// serverMatchesLocked retains the original package-local helper signature for
// embedders/tests; callers that know the workspace root should use the bounded
// variant below.
func serverMatchesLocked(config Config, path string) []ServerMatch {
	return serverMatchesLockedWithin(config, path, "")
}

func serverMatchesLockedWithin(config Config, path string, workspaceRoot string) []ServerMatch {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(path))
	var candidates []ServerMatch
	for serverName, server := range config.Servers {
		if server.Disabled || server.ResolvedCommand == "" {
			continue
		}
		if server.explicit && len(server.RootMarkers) > 0 {
			matchedRoot, rootErr := hasRootMarkerAncestor(path, server.RootMarkers, workspaceRoot)
			if rootErr != nil || !matchedRoot {
				continue
			}
		}
		for _, fileType := range server.FileTypes {
			if matchesFileType(fileType, base, ext) {
				candidates = append(candidates, ServerMatch{Name: serverName, Config: server})
				break
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Config.Priority != candidates[j].Config.Priority {
			return candidates[i].Config.Priority > candidates[j].Config.Priority
		}
		if candidates[i].Config.IsLinter != candidates[j].Config.IsLinter {
			return !candidates[i].Config.IsLinter
		}
		return candidates[i].Name < candidates[j].Name
	})
	return candidates
}

// Client starts one configured server if necessary.
func (m *Manager) Client(ctx context.Context, name string, server ServerConfig) (*Client, ServerConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, server, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ServerConfig{}, errors.New("LSP manager is closed")
	}
	// Always use the manager's current snapshot. Callers often obtain a
	// ServerConfig immediately before a concurrent reload; accepting a stale
	// value here could start a server that was removed or disabled by the new
	// configuration.
	configured, exists := m.config.Servers[name]
	if !exists {
		m.mu.Unlock()
		return nil, ServerConfig{}, fmt.Errorf("unknown language server: %s", name)
	}
	server = configured
	if server.Disabled {
		m.mu.Unlock()
		return nil, server, fmt.Errorf("LSP server %q is disabled", name)
	}
	if server.UnavailableReason != "" && server.ResolvedCommand == "" {
		m.mu.Unlock()
		return nil, server, fmt.Errorf("LSP server %q is unavailable: %s", name, server.UnavailableReason)
	}
	if server.Linter != "" {
		m.mu.Unlock()
		return nil, server, fmt.Errorf("LSP server %q is a CLI linter; use the diagnostics or formatting action", name)
	}
	if err := validateManagerServer(server); err != nil {
		m.mu.Unlock()
		return nil, server, err
	}
	if existing := m.clients[name]; existing != nil {
		m.mu.Unlock()
		return existing, server, nil
	}
	if pending := m.starting[name]; pending != nil {
		m.mu.Unlock()
		select {
		case <-pending.done:
			if pending.client != nil {
				return pending.client, server, nil
			}
			if pending.err != nil {
				return nil, server, pending.err
			}
			return nil, server, errors.New("LSP server start failed")
		case <-ctx.Done():
			return nil, server, ctx.Err()
		}
	}
	// A startup is shared by every waiter for the same server.  Do not bind it
	// directly to the first caller's cancellation: an aborted tool call should
	// not tear down a handshake that a concurrent caller is still waiting for.
	// Manager.Close cancels this context through pending.cancel, and newClient
	// applies its own bounded initialization timeout.
	startContext, startCancel := context.WithCancel(context.WithoutCancel(ctx))
	startEpoch := m.configEpoch
	pending := &clientStart{done: make(chan struct{}), cancel: startCancel, epoch: startEpoch}
	m.starting[name] = pending
	m.mu.Unlock()
	client, err := newClient(startContext, m.root, name, server, m.version)
	startCancel()
	var closeClient *Client
	var resultErr error
	var resultClient *Client
	m.mu.Lock()
	// A configuration reload (or a newer concurrent start) may have removed
	// this pending entry while newClient was handshaking. Never let the stale
	// result delete or overwrite the replacement entry.
	if current := m.starting[name]; current != pending || m.configEpoch != pending.epoch {
		if current == pending {
			delete(m.starting, name)
		}
		pending.client = nil
		if err == nil {
			resultErr = errors.New("LSP server start was superseded")
		} else {
			resultErr = err
		}
		pending.err = resultErr
		close(pending.done)
		m.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return nil, server, resultErr
	}
	delete(m.starting, name)
	pending.client = client
	pending.err = err
	if err != nil {
		m.errors[name] = err.Error()
		close(pending.done)
		m.mu.Unlock()
		return nil, server, err
	}
	client.SetApplyEditHandler(func(ctx context.Context, edit workspaceEdit) error {
		return applyWorkspaceEdit(ctx, m.root, edit, nil)
	})
	client.SetActivityHandler(func() { m.touch(name, client) })
	if m.closed {
		resultErr = errors.New("LSP manager is closed")
		pending.err = resultErr
		pending.client = nil
		closeClient = client
		close(pending.done)
		m.mu.Unlock()
		_ = closeClient.Close()
		return nil, server, resultErr
	}
	if previous := m.clients[name]; previous != nil {
		pending.client = previous
		resultClient = previous
		closeClient = client
		close(pending.done)
		m.mu.Unlock()
		_ = closeClient.Close()
		return resultClient, server, nil
	}
	m.clients[name] = client
	m.lastUsed[name] = time.Now()
	delete(m.errors, name)
	close(pending.done)
	m.mu.Unlock()
	return client, server, nil
}

func validateManagerServer(server ServerConfig) error {
	if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.ResolvedCommand) == "" {
		return errors.New("LSP server command is empty")
	}
	if server.WarmupTimeoutMS < 0 || server.WarmupTimeoutMS > 600000 {
		return errors.New("LSP warmupTimeoutMs must be between 0 and 600000")
	}
	return nil
}

func matchesFileType(pattern string, base string, ext string) bool {
	normalized := strings.TrimSpace(pattern)
	if normalized == "" {
		return false
	}
	if strings.EqualFold(normalized, base) || strings.EqualFold(normalized, ext) {
		return true
	}
	// Accept extension names with or without a leading dot ("go" and
	// ".go"), as well as basename/glob patterns used by common clients.
	if strings.HasPrefix(normalized, ".") && strings.EqualFold(normalized[1:], strings.TrimPrefix(ext, ".")) {
		return true
	}
	if !strings.ContainsAny(normalized, "*?[") && !strings.ContainsRune(normalized, filepath.Separator) {
		if strings.EqualFold(strings.TrimPrefix(normalized, "."), strings.TrimPrefix(ext, ".")) {
			return true
		}
	}
	if strings.ContainsAny(normalized, "*?[") {
		matched, err := filepath.Match(strings.ToLower(normalized), strings.ToLower(base))
		if err != nil {
			return false
		}
		if matched {
			return true
		}
		// File-type globs are often copied from editor configurations as
		// `**/*.ext`.  A server receives a basename here, so the recursive
		// prefix is equivalent to an ordinary basename glob.
		trimmed := strings.TrimPrefix(filepath.ToSlash(normalized), "**/")
		if trimmed != normalized {
			matched, err = filepath.Match(strings.ToLower(trimmed), strings.ToLower(base))
			return err == nil && matched
		}
		return false
	}
	return false
}

// Reload closes a server so the next operation starts a fresh process.
func (m *Manager) Reload(ctx context.Context, name string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	client := m.clients[name]
	delete(m.clients, name)
	delete(m.lastUsed, name)
	delete(m.errors, name)
	pending := m.starting[name]
	if pending != nil && pending.cancel != nil {
		// Removing the entry is sufficient to supersede this one startup. Do not
		// advance the configuration epoch: that would also invalidate unrelated
		// servers that happen to be starting concurrently.
		delete(m.starting, name)
		pending.cancel()
	}
	server, exists := m.config.Servers[name]
	m.mu.Unlock()
	if !exists {
		return fmt.Errorf("unknown language server: %s", name)
	}
	if client != nil {
		if err := client.Close(); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	if pending != nil {
		select {
		case <-pending.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if server.Disabled || server.ResolvedCommand == "" {
		return errors.New("language server is unavailable")
	}
	if server.Linter != "" {
		// CLI linters do not own a persistent process. Reload still validates
		// that the configured executable is available so the command gives the
		// user useful feedback, but there is no session to restart.
		if _, err := m.resolveLinterCommand(server); err != nil {
			m.mu.Lock()
			m.errors[name] = err.Error()
			m.mu.Unlock()
			return err
		}
		return nil
	}
	// Start immediately to make reload failures visible to the model.
	_, _, err := m.Client(ctx, name, server)
	return err
}

// Close shuts down all language-server processes.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	clients := make([]*Client, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.clients = make(map[string]*Client)
	m.lastUsed = make(map[string]time.Time)
	starts := make([]*clientStart, 0, len(m.starting))
	for _, pending := range m.starting {
		starts = append(starts, pending)
		if pending.cancel != nil {
			pending.cancel()
		}
	}
	if m.reaperStop != nil {
		m.reaperOnce.Do(func() { close(m.reaperStop) })
	}
	m.mu.Unlock()
	if m.reaperDone != nil {
		<-m.reaperDone
	}
	// A startup may be blocked in an external executable. Give cancelled
	// starts a bounded opportunity to finish so Close never waits forever.
	deadline := time.NewTimer(2 * time.Second)
	waitDone := false
	for _, pending := range starts {
		if waitDone {
			break
		}
		select {
		case <-pending.done:
		case <-deadline.C:
			waitDone = true
		}
	}
	if !deadline.Stop() {
		select {
		case <-deadline.C:
		default:
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].Name() < clients[j].Name() })
	var firstErr error
	for _, client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) touch(name string, client *Client) {
	m.mu.Lock()
	if !m.closed && m.clients[name] == client {
		m.lastUsed[name] = time.Now()
	}
	m.mu.Unlock()
}

func (m *Manager) idleReaper() {
	// A fixed short tick lets live configuration reloads change the idle timeout
	// without replacing the reaper goroutine. The actual timeout is read under
	// the manager lock on every tick; a disabled/negative value simply skips a
	// sweep.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer func() {
		ticker.Stop()
		close(m.reaperDone)
	}()
	for {
		select {
		case <-ticker.C:
			m.mu.Lock()
			configured := m.config.IdleTimeoutMS
			m.mu.Unlock()
			if configured > 0 {
				m.reapIdleClients(time.Duration(configured) * time.Millisecond)
			}
		case <-m.reaperStop:
			return
		}
	}
}

func (m *Manager) reapIdleClients(timeout time.Duration) {
	now := time.Now()
	var expired []*Client
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	for name, client := range m.clients {
		last := m.lastUsed[name]
		if last.IsZero() || now.Sub(last) < timeout || client.activeRequests() > 0 {
			continue
		}
		delete(m.clients, name)
		delete(m.lastUsed, name)
		expired = append(expired, client)
	}
	m.mu.Unlock()
	for _, client := range expired {
		_ = client.Close()
	}
}

// AfterFileChanges synchronizes completed built-in tool writes with matching
// language servers and returns concise diagnostics as non-fatal feedback.
func (m *Manager) AfterFileChanges(parent context.Context, changes []tools.FileChange) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	m.mu.Lock()
	diagnosticsOnWrite := m.config.DiagnosticsOnWrite
	diagnosticsOnEdit := m.config.DiagnosticsOnEdit
	formatOnWrite := m.config.FormatOnWrite
	m.mu.Unlock()
	var feedback []string
	var failures []string
	for _, change := range changes {
		if safePath, pathErr := safeWorkspacePath(m.root, change.Path); pathErr != nil {
			// LSP sessions are deliberately workspace-scoped even when the host
			// tool is running in full-access mode; do not leak outside-workspace
			// file contents to a language server.
			continue
		} else {
			change.Path = safePath
		}
		matches := m.ServersForFile(change.Path)
		if len(matches) == 0 {
			continue
		}
		checkDiagnostics := diagnosticsOnWrite
		if change.Operation == "edit" {
			checkDiagnostics = diagnosticsOnEdit
		}
		for _, match := range matches {
			name, server := match.Name, match.Config
			if server.Linter != "" {
				// CLI linters have no persistent document session. Run them only
				// when the configured observer actually needs formatting or
				// diagnostics, and never for deleted paths.
				if strings.EqualFold(change.Operation, "delete") {
					continue
				}
				if !checkDiagnostics && !formatOnWrite {
					continue
				}
				content, readErr := os.ReadFile(change.Path)
				if readErr != nil {
					if errors.Is(readErr, os.ErrNotExist) {
						continue
					}
					failures = append(failures, fmt.Sprintf("%s: read %s: %v", name, displayWorkspacePath(m.root, change.Path), readErr))
					continue
				}
				if formatOnWrite {
					formatted, formatErr := m.LinterFormat(ctx, name, server, change.Path, string(content))
					if formatErr != nil {
						failures = append(failures, fmt.Sprintf("%s formatting: %v", name, formatErr))
					} else if formatted != string(content) {
						if writeErr := atomicWrite(change.Path, []byte(formatted)); writeErr != nil {
							failures = append(failures, fmt.Sprintf("%s formatting: %v", name, writeErr))
						} else {
							content = []byte(formatted)
						}
					}
				}
				if !checkDiagnostics {
					continue
				}
				diagnostics, lintErr := m.LinterDiagnostics(ctx, name, server, change.Path, string(content))
				if lintErr != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", name, lintErr))
					continue
				}
				for index, item := range diagnostics {
					if index >= 20 {
						feedback = append(feedback, fmt.Sprintf("LSP: %d additional diagnostic(s) omitted", len(diagnostics)-index))
						break
					}
					feedback = append(feedback, fmt.Sprintf("LSP %s %s:%d:%d: %s", diagnosticSeverity(item.Severity), displayWorkspacePath(m.root, change.Path), item.Range.Start.Line+1, item.Range.Start.Character+1, item.Message))
				}
				continue
			}
			m.mu.Lock()
			client := m.clients[name]
			m.mu.Unlock()
			if client == nil && !checkDiagnostics && !formatOnWrite {
				continue
			}
			if client == nil {
				var err error
				client, _, err = m.Client(ctx, name, server)
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", name, err))
					continue
				}
			}
			uri := fileURI(change.Path)
			var err error
			if strings.EqualFold(change.Operation, "delete") {
				if err := client.CloseDocument(ctx, change.Path); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				}
				client.diagnosticsMu.Lock()
				delete(client.diagnostics, uri)
				client.diagnosticsMu.Unlock()
				continue
			}
			content, err := os.ReadFile(change.Path)
			if err != nil {
				// A patch may delete a file even when the observer receives a generic
				// operation label. Keep the server's document state coherent.
				if errors.Is(err, os.ErrNotExist) {
					_ = client.CloseDocument(ctx, change.Path)
					continue
				}
				failures = append(failures, fmt.Sprintf("%s: read %s: %v", name, displayWorkspacePath(m.root, change.Path), err))
				continue
			}
			uri, err = client.OpenDocument(ctx, change.Path, string(content))
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			if formatOnWrite {
				var edits []textEdit
				formatErr := client.Request(ctx, "textDocument/formatting", map[string]any{
					"textDocument": map[string]any{"uri": uri},
					"options":      map[string]any{"tabSize": 4, "insertSpaces": true},
				}, &edits)
				if formatErr != nil && !isMethodNotFound(formatErr) {
					failures = append(failures, fmt.Sprintf("%s formatting: %v", name, formatErr))
				} else if len(edits) > 0 {
					workspace := workspaceEdit{Changes: map[string][]textEdit{uri: edits}}
					if formatApplyErr := applyWorkspaceEdit(ctx, m.root, workspace, nil); formatApplyErr != nil {
						failures = append(failures, fmt.Sprintf("%s formatting: %v", name, formatApplyErr))
					} else if formatted, readErr := os.ReadFile(change.Path); readErr == nil {
						// Formatting writes directly to disk; synchronize the new version
						// before asking for diagnostics so the server cannot report stale
						// ranges.
						_, _ = client.OpenDocument(ctx, change.Path, string(formatted))
						content = formatted
					}
				}
			}
			_ = client.Notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": string(content)})
			if !checkDiagnostics {
				continue
			}
			diagnostics := client.WaitForDiagnostics(ctx, uri, writeThroughDiagnosticsWaitTimeout)
			if len(diagnostics) == 0 {
				continue
			}
			pathValue := displayWorkspacePath(m.root, change.Path)
			for index, item := range diagnostics {
				if index >= 20 {
					feedback = append(feedback, fmt.Sprintf("LSP: %d additional diagnostic(s) omitted", len(diagnostics)-index))
					break
				}
				feedback = append(feedback, fmt.Sprintf("LSP %s %s:%d:%d: %s", diagnosticSeverity(item.Severity), pathValue,
					item.Range.Start.Line+1, item.Range.Start.Character+1, item.Message))
			}
		}
	}
	if len(failures) > 0 {
		return strings.Join(feedback, "\n"), errors.New(strings.Join(failures, "; "))
	}
	return strings.Join(feedback, "\n"), nil
}

func cloneConfig(config Config) Config {
	result := Config{Servers: make(map[string]ServerConfig, len(config.Servers)), Enabled: config.Enabled, IdleTimeoutMS: config.IdleTimeoutMS,
		DiagnosticsOnWrite: config.DiagnosticsOnWrite, DiagnosticsOnEdit: config.DiagnosticsOnEdit, FormatOnWrite: config.FormatOnWrite}
	for name, server := range config.Servers {
		server.Args = append([]string(nil), server.Args...)
		server.FileTypes = append([]string(nil), server.FileTypes...)
		server.RootMarkers = append([]string(nil), server.RootMarkers...)
		server.Env = cloneStringMap(server.Env)
		server.Capabilities = cloneAnyMap(server.Capabilities)
		server.InitOptions = cloneAnyValue(server.InitOptions)
		server.Settings = cloneAnyValue(server.Settings)
		server.WorkspaceReadyTimings = cloneAnyValue(server.WorkspaceReadyTimings)
		result.Servers[name] = server
	}
	return result
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneAnyValue(item)
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

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

// DefaultRequestTimeout is exposed for callers that need a consistent LSP timeout.
func DefaultRequestTimeout() time.Duration { return defaultRequestTimeout }
