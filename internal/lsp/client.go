package lspclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRequestTimeout = 20 * time.Second
	initializeTimeout     = 10 * time.Second
	maxServerOutput       = 64 * 1024
)

// Client is a single language-server process and its JSON-RPC session.
type Client struct {
	root   string
	name   string
	config ServerConfig

	command    *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser

	writeMu     sync.Mutex
	stateMu     sync.RWMutex
	closed      bool
	initialized bool
	processErr  error
	done        chan struct{}
	doneOnce    sync.Once
	closeOnce   sync.Once
	waitDone    chan error

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan rpcResponse

	diagnosticsMu     sync.RWMutex
	diagnostics       map[string][]diagnostic
	diagnosticWaiters map[string][]chan struct{}
	documentsMu       sync.Mutex
	documents         map[string]openDocument
	// documentSyncMu serializes the didOpen/didChange/didClose exchange. The
	// protocol requires notifications for one document to arrive in version
	// order; protecting only the in-memory map is insufficient when concurrent
	// tool calls write notifications outside that lock.
	documentSyncMu sync.Mutex

	capabilitiesMu sync.RWMutex
	capabilities   map[string]any
	dynamicMu      sync.RWMutex
	dynamicCaps    map[string]dynamicCapability
	applyEditMu    sync.RWMutex
	applyEdit      func(context.Context, workspaceEdit) error
	applyEditDepth int
	activityMu     sync.RWMutex
	activity       func()
	active         atomic.Int64

	stderrMu  sync.Mutex
	stderr    bytes.Buffer
	messageMu sync.Mutex
	messages  []string
}

// SetActivityHandler installs a lightweight callback used by the manager to
// implement idle-server shutdown. It is intentionally separate from protocol
// callbacks so callers can use Client directly without a Manager.
func (c *Client) SetActivityHandler(handler func()) {
	c.activityMu.Lock()
	c.activity = handler
	c.activityMu.Unlock()
}

func (c *Client) markActivity() {
	c.activityMu.RLock()
	handler := c.activity
	c.activityMu.RUnlock()
	if handler != nil {
		handler()
	}
}

func (c *Client) beginActivity() func() {
	c.active.Add(1)
	c.markActivity()
	return func() {
		c.active.Add(-1)
		// Idle reaping must measure silence from the end of the exchange. A
		// request that outlives the idle timeout is still busy while pending,
		// but becomes immediately eligible after it settles unless completion
		// also stamps a fresh activity time.
		c.markActivity()
	}
}

func (c *Client) activeRequests() int64 { return c.active.Load() }

// ClientStatus is a snapshot of a language server process.
type ClientStatus struct {
	Name          string
	Command       string
	Running       bool
	Initialized   bool
	OpenDocuments int
	Diagnostics   int
	Error         string
}

// NewClient starts and initializes a language server using Banka's default
// client version. It is kept as a small compatibility wrapper for callers
// outside the manager.
func NewClient(ctx context.Context, root string, name string, config ServerConfig) (*Client, error) {
	return newClient(ctx, root, name, config, "0.1.0")
}

func newClient(ctx context.Context, root string, name string, config ServerConfig, clientVersion string) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(config.ResolvedCommand) == "" {
		config.ResolvedCommand = config.Command
	}
	if strings.TrimSpace(config.ResolvedCommand) == "" {
		return nil, errors.New("LSP server command is empty")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve LSP root: %w", err)
	}
	args := make([]string, len(config.Args))
	for index, argument := range config.Args {
		args[index] = strings.ReplaceAll(argument, "$PID", strconv.Itoa(os.Getpid()))
	}
	command := exec.Command(config.ResolvedCommand, args...)
	command.Dir = root
	if strings.TrimSpace(config.Cwd) != "" {
		cwd, cwdErr := resolveServerCwd(root, config.Cwd)
		if cwdErr != nil {
			return nil, cwdErr
		}
		command.Dir = cwd
	}
	command.Env = mergedEnvironment(config.Env)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open LSP stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open LSP stdout: %w", err)
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("open LSP stderr: %w", err)
	}
	client := &Client{
		root: root, name: name, config: config, command: command,
		stdin: stdin, stdout: stdout, stderrPipe: stderrPipe, done: make(chan struct{}), waitDone: make(chan error, 1),
		pending: make(map[string]chan rpcResponse), diagnostics: make(map[string][]diagnostic), diagnosticWaiters: make(map[string][]chan struct{}),
		documents: make(map[string]openDocument), dynamicCaps: make(map[string]dynamicCapability),
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderrPipe.Close()
		return nil, fmt.Errorf("start LSP server %q: %w", name, err)
	}
	go client.readLoop()
	go client.captureStderr()
	go func() {
		waitErr := command.Wait()
		client.finish(waitErr)
		client.waitDone <- waitErr
		close(client.waitDone)
	}()
	initTimeout := initializeTimeout
	if config.WarmupTimeoutMS > 0 {
		initTimeout = time.Duration(config.WarmupTimeoutMS) * time.Millisecond
	}
	initContext, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	clientCapabilities := defaultClientCapabilities()
	mergeAnyMap(clientCapabilities, config.Capabilities)
	params := map[string]any{
		"processId":  os.Getpid(),
		"clientInfo": map[string]any{"name": "banka-code", "version": clientVersion},
		// rootPath is deprecated but still required by a number of older
		// language servers. Sending it alongside rootUri is harmless for modern
		// implementations and keeps the stdio client broadly compatible.
		"rootPath":         root,
		"rootUri":          fileURI(root),
		"workspaceFolders": []map[string]any{{"uri": fileURI(root), "name": filepath.Base(root)}},
		"capabilities":     clientCapabilities,
		"trace":            "off",
	}
	if config.InitOptions != nil {
		params["initializationOptions"] = config.InitOptions
	}
	var initialized map[string]any
	if err := client.request(initContext, "initialize", params, &initialized); err != nil {
		client.Close()
		return nil, fmt.Errorf("initialize LSP server %q: %w", name, err)
	}
	if caps, ok := initialized["capabilities"].(map[string]any); ok {
		client.capabilitiesMu.Lock()
		client.capabilities = caps
		client.capabilitiesMu.Unlock()
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		client.Close()
		return nil, fmt.Errorf("finish LSP initialization %q: %w", name, err)
	}
	client.stateMu.Lock()
	client.initialized = true
	client.stateMu.Unlock()
	if config.Settings != nil {
		_ = client.notify("workspace/didChangeConfiguration", map[string]any{"settings": config.Settings})
	}
	return client, nil
}

func defaultClientCapabilities() map[string]any {
	return map[string]any{
		"workspace": map[string]any{
			"applyEdit":        true,
			"workspaceEdit":    map[string]any{"documentChanges": true, "resourceOperations": []string{"create", "rename", "delete"}, "failureHandling": "abort"},
			"configuration":    true,
			"workspaceFolders": true,
			"symbol":           map[string]any{"dynamicRegistration": false, "symbolKind": map[string]any{"valueSet": symbolKindValues()}},
			"fileOperations":   map[string]any{"dynamicRegistration": false, "willCreate": false, "didCreate": false, "willRename": true, "didRename": true, "willDelete": false, "didDelete": false},
		},
		"textDocument": map[string]any{
			"synchronization":    map[string]any{"dynamicRegistration": false, "willSave": false, "willSaveWaitUntil": false, "didSave": true},
			"hover":              map[string]any{"dynamicRegistration": false, "contentFormat": []string{"markdown", "plaintext"}},
			"definition":         map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"typeDefinition":     map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"implementation":     map[string]any{"dynamicRegistration": false, "linkSupport": true},
			"references":         map[string]any{"dynamicRegistration": false},
			"documentSymbol":     map[string]any{"dynamicRegistration": false, "hierarchicalDocumentSymbolSupport": true, "symbolKind": map[string]any{"valueSet": symbolKindValues()}},
			"rename":             map[string]any{"dynamicRegistration": false, "prepareSupport": true},
			"codeAction":         map[string]any{"dynamicRegistration": false, "codeActionLiteralSupport": map[string]any{"codeActionKind": map[string]any{"valueSet": []string{"quickfix", "refactor", "refactor.extract", "refactor.inline", "refactor.rewrite", "source", "source.organizeImports", "source.fixAll"}}}, "resolveSupport": map[string]any{"properties": []string{"edit"}}},
			"formatting":         map[string]any{"dynamicRegistration": false},
			"rangeFormatting":    map[string]any{"dynamicRegistration": false},
			"publishDiagnostics": map[string]any{"relatedInformation": true, "versionSupport": true, "tagSupport": map[string]any{"valueSet": []int{1, 2}}, "codeDescriptionSupport": true, "dataSupport": true},
			"diagnostic":         map[string]any{"dynamicRegistration": true},
		},
		"window": map[string]any{"workDoneProgress": true},
	}
}

func symbolKindValues() []int {
	values := make([]int, 26)
	for index := range values {
		values[index] = index + 1
	}
	return values
}

// mergeAnyMap overlays source onto destination recursively. Configuration
// values are copied so a caller cannot mutate the map while a handshake is in
// flight; scalar and array values intentionally replace defaults wholesale.
func mergeAnyMap(destination map[string]any, source map[string]any) {
	for key, value := range source {
		if sourceMap, ok := value.(map[string]any); ok {
			if destinationMap, exists := destination[key].(map[string]any); exists {
				mergeAnyMap(destinationMap, sourceMap)
				continue
			}
			copyMap := cloneMap(sourceMap)
			destination[key] = copyMap
			continue
		}
		destination[key] = value
	}
}

// Name returns the configured server name.
func (c *Client) Name() string { return c.name }

// Capabilities returns the server capabilities received during initialization.
func (c *Client) Capabilities() map[string]any {
	c.capabilitiesMu.RLock()
	result := cloneMap(c.capabilities)
	c.capabilitiesMu.RUnlock()
	if result == nil {
		result = make(map[string]any)
	}
	c.dynamicMu.RLock()
	ids := make([]string, 0, len(c.dynamicCaps))
	for id := range c.dynamicCaps {
		ids = append(ids, id)
	}
	sortStrings(ids)
	for _, id := range ids {
		capability := c.dynamicCaps[id]
		key := dynamicCapabilityKey(capability.Method)
		if key == "" {
			continue
		}
		result[key] = cloneAnyValue(capability.Options)
	}
	c.dynamicMu.RUnlock()
	return result
}

// SetApplyEditHandler registers a callback for server-initiated workspace edits.
func (c *Client) SetApplyEditHandler(handler func(context.Context, workspaceEdit) error) {
	c.applyEditMu.Lock()
	c.applyEdit = handler
	c.applyEditMu.Unlock()
}

// withWorkspaceEdits permits server-initiated workspace/applyEdit requests
// only while an already-approved mutating operation is in flight.
func (c *Client) withWorkspaceEdits(run func() error) error {
	c.applyEditMu.Lock()
	c.applyEditDepth++
	c.applyEditMu.Unlock()
	defer func() {
		c.applyEditMu.Lock()
		c.applyEditDepth--
		c.applyEditMu.Unlock()
	}()
	return run()
}

func (c *Client) applyServerWorkspaceEdit(ctx context.Context, edit workspaceEdit) error {
	c.applyEditMu.RLock()
	handler := c.applyEdit
	allowed := c.applyEditDepth > 0
	c.applyEditMu.RUnlock()
	if !allowed {
		return errors.New("server-initiated workspace edit is outside an approved LSP mutation")
	}
	if handler == nil {
		return errors.New("server-initiated workspace edits are not supported")
	}
	return handler(ctx, edit)
}

// Status returns a process snapshot.
func (c *Client) Status() ClientStatus {
	c.stateMu.RLock()
	running := !c.closed
	initialized := c.initialized
	processErr := c.processErr
	c.stateMu.RUnlock()
	c.documentsMu.Lock()
	openCount := len(c.documents)
	c.documentsMu.Unlock()
	c.diagnosticsMu.RLock()
	diagnosticCount := 0
	for _, values := range c.diagnostics {
		diagnosticCount += len(values)
	}
	c.diagnosticsMu.RUnlock()
	status := ClientStatus{Name: c.name, Command: c.config.ResolvedCommand, Running: running,
		Initialized: initialized, OpenDocuments: openCount, Diagnostics: diagnosticCount}
	if processErr != nil {
		status.Error = processErr.Error()
	} else if stderr := c.Stderr(); stderr != "" {
		status.Error = stderr
	}
	return status
}

// Stderr returns the bounded diagnostic output collected from the server.
func (c *Client) Stderr() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return strings.TrimSpace(c.stderr.String())
}

// Messages returns bounded informational messages sent by the language
// server through window/showMessage or window/logMessage notifications.
// Servers are not allowed to block the protocol reader waiting for a TUI, so
// callers can inspect these messages opportunistically (for example in a
// status view) instead.
func (c *Client) Messages() []string {
	c.messageMu.Lock()
	defer c.messageMu.Unlock()
	return append([]string(nil), c.messages...)
}

func (c *Client) recordMessage(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	c.messageMu.Lock()
	const maxMessages = 100
	if len(c.messages) >= maxMessages {
		copy(c.messages, c.messages[len(c.messages)-maxMessages+1:])
		c.messages = c.messages[:maxMessages-1]
	}
	c.messages = append(c.messages, message)
	c.messageMu.Unlock()
}

// OpenDocument synchronizes a file with the language server.
func (c *Client) OpenDocument(ctx context.Context, path string, content string) (string, error) {
	finishActivity := c.beginActivity()
	defer finishActivity()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if safePath, pathErr := safeWorkspacePath(c.root, abs); pathErr != nil {
		return "", pathErr
	} else {
		abs = safePath
	}
	// Keep the in-memory version and the wire notification in one serialized
	// exchange. Without this lock two concurrent edits can compute the same
	// version and send didChange out of order.
	c.documentSyncMu.Lock()
	defer c.documentSyncMu.Unlock()
	uri := fileURI(abs)
	language := c.config.LanguageID
	if language == "" {
		language = languageIDForPath(abs)
	}
	c.documentsMu.Lock()
	previous, exists := c.documents[uri]
	version := 1
	if exists {
		version = previous.Version + 1
	}
	c.documents[uri] = openDocument{URI: uri, Path: abs, Language: language, Version: version, Content: content}
	c.documentsMu.Unlock()
	// Every didOpen/didChange starts a new diagnostic generation. Even when the
	// text is unchanged, workspace dependencies or server configuration may have
	// changed since the previous query, so cached diagnostics are no longer
	// authoritative.
	c.diagnosticsMu.Lock()
	previousDiagnostics, hadDiagnostics := c.diagnostics[uri]
	delete(c.diagnostics, uri)
	c.diagnosticsMu.Unlock()
	if !exists {
		params := map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": language, "version": version, "text": content}}
		if err := c.notify("textDocument/didOpen", params); err != nil {
			c.documentsMu.Lock()
			delete(c.documents, uri)
			c.documentsMu.Unlock()
			if hadDiagnostics {
				c.diagnosticsMu.Lock()
				c.diagnostics[uri] = append([]diagnostic(nil), previousDiagnostics...)
				c.diagnosticsMu.Unlock()
			}
			return uri, err
		}
		return uri, nil
	}
	params := map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []map[string]any{{"text": content}}}
	if err := c.notify("textDocument/didChange", params); err != nil {
		c.documentsMu.Lock()
		c.documents[uri] = previous
		c.documentsMu.Unlock()
		c.diagnosticsMu.Lock()
		if hadDiagnostics {
			c.diagnostics[uri] = append([]diagnostic(nil), previousDiagnostics...)
		} else {
			delete(c.diagnostics, uri)
		}
		c.diagnosticsMu.Unlock()
		return uri, err
	}
	return uri, nil
}

// CloseDocument informs the server that a document is no longer open.
func (c *Client) CloseDocument(ctx context.Context, path string) error {
	finishActivity := c.beginActivity()
	defer finishActivity()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if safePath, pathErr := safeWorkspacePath(c.root, abs); pathErr != nil {
		return pathErr
	} else {
		abs = safePath
	}
	c.documentSyncMu.Lock()
	defer c.documentSyncMu.Unlock()
	uri := fileURI(abs)
	c.documentsMu.Lock()
	_, exists := c.documents[uri]
	c.documentsMu.Unlock()
	if !exists {
		return nil
	}
	if err := c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}}); err != nil {
		return err
	}
	c.documentsMu.Lock()
	delete(c.documents, uri)
	c.documentsMu.Unlock()
	c.diagnosticsMu.Lock()
	delete(c.diagnostics, uri)
	c.diagnosticsMu.Unlock()
	return nil
}

// Diagnostics returns the latest diagnostics published for a file.
func (c *Client) Diagnostics(uri string) []diagnostic {
	c.diagnosticsMu.RLock()
	defer c.diagnosticsMu.RUnlock()
	return append([]diagnostic(nil), c.diagnostics[uri]...)
}

func (c *Client) setDiagnostics(uri string, values []diagnostic) {
	c.diagnosticsMu.Lock()
	if c.diagnostics == nil {
		c.diagnostics = make(map[string][]diagnostic)
	}
	c.diagnostics[uri] = append([]diagnostic(nil), values...)
	waiters := c.diagnosticWaiters[uri]
	delete(c.diagnosticWaiters, uri)
	for _, waiter := range waiters {
		close(waiter)
	}
	c.diagnosticsMu.Unlock()
}

func (c *Client) removeDiagnosticWaiter(uri string, target chan struct{}) {
	c.diagnosticsMu.Lock()
	if c.diagnosticWaiters == nil {
		c.diagnosticWaiters = make(map[string][]chan struct{})
	}
	waiters := c.diagnosticWaiters[uri]
	for index, waiter := range waiters {
		if waiter != target {
			continue
		}
		waiters = append(waiters[:index], waiters[index+1:]...)
		break
	}
	if len(waiters) == 0 {
		delete(c.diagnosticWaiters, uri)
	} else {
		c.diagnosticWaiters[uri] = waiters
	}
	c.diagnosticsMu.Unlock()
}

// WaitForDiagnostics waits until the server publishes diagnostics for the
// current document version, or until timeout/cancellation. An explicitly
// published empty list is ready state and returns immediately.
func (c *Client) WaitForDiagnostics(ctx context.Context, uri string, timeout time.Duration) []diagnostic {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 250 * time.Millisecond
	}
	c.diagnosticsMu.Lock()
	if c.diagnostics == nil {
		c.diagnostics = make(map[string][]diagnostic)
	}
	if c.diagnosticWaiters == nil {
		c.diagnosticWaiters = make(map[string][]chan struct{})
	}
	if values, ready := c.diagnostics[uri]; ready {
		result := append([]diagnostic(nil), values...)
		c.diagnosticsMu.Unlock()
		return result
	}
	waiter := make(chan struct{})
	c.diagnosticWaiters[uri] = append(c.diagnosticWaiters[uri], waiter)
	c.diagnosticsMu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		c.removeDiagnosticWaiter(uri, waiter)
	case <-timer.C:
		c.removeDiagnosticWaiter(uri, waiter)
	case <-c.done:
		c.removeDiagnosticWaiter(uri, waiter)
	case <-waiter:
	}
	return c.Diagnostics(uri)
}

// Request sends an arbitrary LSP request and decodes its result.
func (c *Client) Request(ctx context.Context, method string, params any, result any) error {
	return c.request(ctx, method, params, result)
}

// Notify sends an arbitrary LSP notification.
func (c *Client) Notify(method string, params any) error { return c.notify(method, params) }

func (c *Client) request(ctx context.Context, method string, params any, result any) error {
	finishActivity := c.beginActivity()
	defer finishActivity()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	id := strconv.FormatInt(c.nextID.Add(1), 10)
	responseCh := make(chan rpcResponse, 1)
	c.stateMu.RLock()
	if c.closed {
		c.stateMu.RUnlock()
		return errors.New("LSP client is closed")
	}
	c.pendingMu.Lock()
	c.pending[id] = responseCh
	c.pendingMu.Unlock()
	c.stateMu.RUnlock()
	if err := c.writeEnvelope(id, method, params); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}
	select {
	case response := <-responseCh:
		if response.err != nil {
			return response.err
		}
		if result == nil || len(response.result) == 0 || string(response.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decode LSP response %s: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		_ = c.notify("$/cancelRequest", map[string]any{"id": json.Number(id)})
		return ctx.Err()
	case <-c.done:
		return errors.New("LSP server exited")
	}
}

func (c *Client) notify(method string, params any) error {
	finishActivity := c.beginActivity()
	defer finishActivity()
	if c.isClosed() {
		return errors.New("LSP client is closed")
	}
	return c.writeEnvelope("", method, params)
}

func (c *Client) writeEnvelope(id string, method string, params any) error {
	payload := make(map[string]any, 4)
	payload["jsonrpc"] = "2.0"
	if id != "" {
		if number, err := strconv.ParseInt(id, 10, 64); err == nil {
			payload["id"] = number
		} else {
			payload["id"] = id
		}
	}
	if method != "" {
		payload["method"] = method
	}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return errors.New("LSP client is closed")
	}
	if _, err := io.WriteString(c.stdin, frame); err != nil {
		// A broken pipe means the process is no longer a usable protocol
		// session. Finish it immediately so every pending caller is released
		// instead of waiting for a reader EOF that may never arrive.
		c.finish(err)
		return err
	}
	if _, err = c.stdin.Write(body); err != nil {
		c.finish(err)
		return err
	}
	return nil
}

func (c *Client) readLoop() {
	reader := bufio.NewReaderSize(c.stdout, 64*1024)
	for {
		body, err := readFrame(reader)
		if err != nil {
			c.finish(err)
			return
		}
		c.handleEnvelope(body)
	}
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			length, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if parseErr != nil || length < 0 || length > 16*1024*1024 {
				return nil, fmt.Errorf("invalid LSP Content-Length: %q", value)
			}
			contentLength = length
		}
	}
	if contentLength < 0 {
		return nil, errors.New("LSP frame is missing Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) handleEnvelope(body []byte) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return
	}
	if len(envelope.ID) > 0 && envelope.Method == "" {
		id := strings.TrimSpace(string(envelope.ID))
		if strings.HasPrefix(id, "\"") {
			if decoded, err := strconv.Unquote(id); err == nil {
				id = decoded
			}
		}
		c.pendingMu.Lock()
		responseCh := c.pending[id]
		if responseCh != nil {
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
		if responseCh != nil {
			var responseErr error
			if envelope.Error != nil {
				responseErr = envelope.Error
			}
			responseCh <- rpcResponse{result: envelope.Result, err: responseErr}
		}
		return
	}
	if envelope.Method == "" {
		return
	}
	if len(envelope.ID) > 0 {
		c.handleServerRequest(envelope.ID, envelope.Method, envelope.Params)
		return
	}
	c.handleNotification(envelope.Method, envelope.Params)
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		var value struct {
			URI         string       `json:"uri"`
			Version     *int         `json:"version"`
			Diagnostics []diagnostic `json:"diagnostics"`
		}
		if json.Unmarshal(params, &value) != nil || value.URI == "" {
			return
		}
		// Diagnostics are state, not an instruction channel. Still enforce the
		// workspace boundary before caching them so a compromised server cannot
		// make the agent surface host paths or contents outside this project.
		if path, err := uriToPath(value.URI); err != nil {
			return
		} else if _, err := safeWorkspacePath(c.root, path); err != nil {
			return
		}
		// LSP 3.17 permits publishDiagnostics to include the document version.
		// A server may still deliver an older queued notification after a
		// didChange; accepting it would replace fresh diagnostics with stale ones.
		// Servers that omit version remain supported and are handled as the latest
		// push state.
		if value.Version != nil {
			c.documentsMu.Lock()
			current, exists := c.documents[value.URI]
			c.documentsMu.Unlock()
			if exists && *value.Version < current.Version {
				return
			}
		}
		c.setDiagnostics(value.URI, value.Diagnostics)
	case "window/showMessage", "window/logMessage":
		var value struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &value) == nil {
			prefix := "LSP"
			if method == "window/showMessage" {
				prefix = "LSP message"
			}
			c.recordMessage(prefix + ": " + value.Message)
		}
	case "window/showDocument", "telemetry/event", "$/progress":
		// These notifications are intentionally best-effort. Progress and
		// telemetry must never block or terminate the protocol reader.
		return
	}
}

func (c *Client) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	var result any
	var errValue *rpcError
	switch method {
	case "workspace/configuration":
		var request struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		_ = json.Unmarshal(params, &request)
		result = make([]any, len(request.Items))
		for index, item := range request.Items {
			result.([]any)[index] = lookupSetting(c.config.Settings, item.Section)
		}
	case "workspace/applyEdit":
		var request struct {
			Edit workspaceEdit `json:"edit"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			errValue = &rpcError{Code: -32602, Message: err.Error()}
			break
		}
		if applyErr := c.applyServerWorkspaceEdit(context.Background(), request.Edit); applyErr != nil {
			result = map[string]any{"applied": false, "failureReason": applyErr.Error()}
		} else {
			result = map[string]any{"applied": true}
		}
	case "window/showMessageRequest":
		// This client has no interactive UI channel. The protocol defines null as
		// "no action selected"; selecting an arbitrary first action can trigger
		// destructive server behaviour in a headless host.
		result = nil
	case "window/workDoneProgress/create":
		result = nil
	case "client/registerCapability":
		if err := c.updateDynamicCapabilities(params, true); err != nil {
			errValue = &rpcError{Code: -32602, Message: err.Error()}
			break
		}
		result = nil
	case "client/unregisterCapability":
		if err := c.updateDynamicCapabilities(params, false); err != nil {
			errValue = &rpcError{Code: -32602, Message: err.Error()}
			break
		}
		result = nil
	case "workspace/semanticTokens/refresh", "workspace/inlayHint/refresh", "workspace/codeLens/refresh",
		"workspace/codeAction/refresh", "workspace/diagnostic/refresh", "workspace/diagnostics/refresh",
		"workspace/inlineValue/refresh", "workspace/foldingRange/refresh":
		// All refresh requests are void acknowledgements per LSP. In particular,
		// use JSON null rather than an empty object; several servers wait for the
		// exact response shape before issuing the next request.
		result = nil
	case "window/showDocument":
		result = map[string]any{"success": false}
	case "workspace/workspaceFolders":
		result = []map[string]any{{"uri": fileURI(c.root), "name": filepath.Base(c.root)}}
	default:
		errValue = &rpcError{Code: -32601, Message: "method not found"}
	}
	_ = c.writeResponse(id, result, errValue)
}

type dynamicCapability struct {
	Method  string
	Options any
}

// updateDynamicCapabilities tracks client/registerCapability and
// client/unregisterCapability requests. Dynamic registrations are merged into
// Capabilities so callers can use the same provider checks for static and
// dynamically advertised features.
func (c *Client) updateDynamicCapabilities(params json.RawMessage, register bool) error {
	if register {
		var request struct {
			Registrations []struct {
				ID              string          `json:"id"`
				Method          string          `json:"method"`
				RegisterOptions json.RawMessage `json:"registerOptions"`
			} `json:"registrations"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return err
		}
		c.dynamicMu.Lock()
		defer c.dynamicMu.Unlock()
		if c.dynamicCaps == nil {
			c.dynamicCaps = make(map[string]dynamicCapability)
		}
		for _, registration := range request.Registrations {
			method := strings.TrimSpace(registration.Method)
			id := strings.TrimSpace(registration.ID)
			if method == "" || id == "" {
				continue
			}
			options := any(true)
			if len(registration.RegisterOptions) > 0 && string(registration.RegisterOptions) != "null" {
				var decoded any
				if err := json.Unmarshal(registration.RegisterOptions, &decoded); err != nil {
					return fmt.Errorf("decode registration %s: %w", id, err)
				}
				options = decoded
			}
			c.dynamicCaps[id] = dynamicCapability{Method: method, Options: options}
		}
		return nil
	}

	var request struct {
		// `unregisterations` is the misspelling used in early LSP drafts and is
		// still emitted by a few servers, so accept both spellings.
		Unregistrations []struct {
			ID string `json:"id"`
		} `json:"unregistrations"`
		Unregisterations []struct {
			ID string `json:"id"`
		} `json:"unregisterations"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return err
	}
	c.dynamicMu.Lock()
	defer c.dynamicMu.Unlock()
	for _, registration := range append(request.Unregistrations, request.Unregisterations...) {
		if id := strings.TrimSpace(registration.ID); id != "" {
			delete(c.dynamicCaps, id)
		}
	}
	return nil
}

func dynamicCapabilityKey(method string) string {
	method = strings.TrimSpace(method)
	switch method {
	case "textDocument/hover":
		return "hoverProvider"
	case "textDocument/definition":
		return "definitionProvider"
	case "textDocument/typeDefinition":
		return "typeDefinitionProvider"
	case "textDocument/implementation":
		return "implementationProvider"
	case "textDocument/references":
		return "referencesProvider"
	case "textDocument/documentSymbol":
		return "documentSymbolProvider"
	case "textDocument/rename":
		return "renameProvider"
	case "textDocument/codeAction":
		return "codeActionProvider"
	case "textDocument/formatting":
		return "documentFormattingProvider"
	case "textDocument/rangeFormatting":
		return "documentRangeFormattingProvider"
	case "textDocument/onTypeFormatting":
		return "documentOnTypeFormattingProvider"
	case "textDocument/completion":
		return "completionProvider"
	case "textDocument/signatureHelp":
		return "signatureHelpProvider"
	case "textDocument/declaration":
		return "declarationProvider"
	case "textDocument/diagnostic":
		return "diagnosticProvider"
	case "textDocument/semanticTokens", "textDocument/semanticTokens/full", "textDocument/semanticTokens/range":
		return "semanticTokensProvider"
	case "textDocument/inlayHint":
		return "inlayHintProvider"
	case "textDocument/inlineValue":
		return "inlineValueProvider"
	case "textDocument/foldingRange":
		return "foldingRangeProvider"
	case "textDocument/selectionRange":
		return "selectionRangeProvider"
	case "textDocument/linkedEditingRange":
		return "linkedEditingRangeProvider"
	case "textDocument/moniker":
		return "monikerProvider"
	case "workspace/symbol":
		return "workspaceSymbolProvider"
	case "workspace/executeCommand":
		return "executeCommandProvider"
	case "workspace/diagnostic":
		return "diagnosticProvider"
	default:
		return ""
	}
}

func (c *Client) writeResponse(id json.RawMessage, result any, rpcErr *rpcError) error {
	payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
	if rpcErr != nil {
		payload["error"] = rpcErr
	} else {
		payload["result"] = result
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return errors.New("LSP client is closed")
	}
	if _, err := io.WriteString(c.stdin, frame); err != nil {
		c.finish(err)
		return err
	}
	if _, err = c.stdin.Write(body); err != nil {
		c.finish(err)
		return err
	}
	return nil
}

func (c *Client) captureStderr() {
	// exec.Cmd exposes stderr only when explicitly wired. Keep this goroutine
	// separate so a noisy server cannot block on its error stream.
	// Do not hold stderrMu for the lifetime of the process: Status() is called
	// while a server is still running (for example by the idle reaper), and a
	// lock held around io.Copy would make that read wait until process exit.
	var captured bytes.Buffer
	limited := &limitedWriter{writer: &captured, remaining: maxServerOutput}
	_, _ = io.Copy(limited, c.stderrPipe)
	c.stderrMu.Lock()
	c.stderr.Reset()
	_, _ = c.stderr.Write(captured.Bytes())
	c.stderrMu.Unlock()
}

func (c *Client) finish(processErr error) {
	// The stdout reader commonly observes EOF before exec.Cmd.Wait reports the
	// process's real exit status. Record errors independently from doneOnce so a
	// later *exec.ExitError is not lost after the session has already closed.
	if processErr != nil && !errors.Is(processErr, io.EOF) {
		c.stateMu.Lock()
		var currentExitError *exec.ExitError
		var processExitError *exec.ExitError
		if c.processErr == nil || (!errors.As(c.processErr, &currentExitError) && errors.As(processErr, &processExitError)) {
			c.processErr = processErr
		}
		c.stateMu.Unlock()
	}
	c.doneOnce.Do(func() {
		c.stateMu.Lock()
		c.closed = true
		c.stateMu.Unlock()
		close(c.done)
		c.pendingMu.Lock()
		for id, channel := range c.pending {
			delete(c.pending, id)
			channel <- rpcResponse{err: errors.New("LSP server exited")}
		}
		c.pendingMu.Unlock()
	})
}

// Close shuts down the language-server process.
func (c *Client) Close() error {
	var waitErr error
	c.closeOnce.Do(func() {
		if !c.isClosed() {
			shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.request(shutdownContext, "shutdown", nil, nil)
			cancel()
			_ = c.notify("exit", nil)
		}
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.command != nil && c.command.Process != nil {
			select {
			case waitErr = <-c.waitDone:
			case <-time.After(2 * time.Second):
				_ = c.command.Process.Kill()
				waitErr = <-c.waitDone
			}
		}
		c.finish(waitErr)
	})
	return waitErr
}

func (c *Client) isClosed() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.closed
}

func resolveServerCwd(root string, configured string) (string, error) {
	candidate := strings.TrimSpace(configured)
	if candidate == "" {
		return root, nil
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	resolved, err := safeWorkspacePath(root, candidate)
	if err != nil {
		return "", fmt.Errorf("LSP cwd escapes workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("LSP cwd is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("LSP cwd must be a directory")
	}
	return resolved, nil
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && !strings.HasPrefix(key, "BANKA_") {
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
	sortStrings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "file://" + urlPathEscape(path)
}

func urlPathEscape(value string) string {
	const hex = "0123456789ABCDEF"
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("/-._~", rune(ch)) {
			builder.WriteByte(ch)
		} else {
			builder.WriteByte('%')
			builder.WriteByte(hex[ch>>4])
			builder.WriteByte(hex[ch&15])
		}
	}
	return builder.String()
}

func languageIDForPath(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") || base == "containerfile":
		return "dockerfile"
	case base == "makefile" || base == "gnumakefile":
		return "makefile"
	case base == "justfile":
		return "just"
	case base == "cmakelists.txt":
		return "cmake"
	case base == ".emacs":
		return "emacs-lisp"
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".ts", ".cts", ".mts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".py":
		return "python"
	case ".pyi":
		return "python"
	case ".rb", ".rbw", ".gemspec":
		return "ruby"
	case ".c":
		return "c"
	case ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp":
		return "cpp"
	case ".java":
		return "java"
	case ".swift":
		return "swift"
	case ".sh", ".bash", ".zsh":
		return "shellscript"
	case ".ksh", ".bats", ".command":
		return "shellscript"
	case ".json":
		return "json"
	case ".jsonc":
		return "jsonc"
	case ".yaml", ".yml":
		return "yaml"
	case ".md":
		return "markdown"
	case ".mdx":
		return "markdown"
	default:
		return "plaintext"
	}
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var clone map[string]any
	if json.Unmarshal(encoded, &clone) != nil {
		return map[string]any{}
	}
	return clone
}

func lookupSetting(settings any, section string) any {
	if section == "" {
		return settings
	}
	values, ok := settings.(map[string]any)
	if !ok {
		return nil
	}
	var current any = values
	for _, part := range strings.Split(section, ".") {
		mapValue, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = mapValue[part]
		if !ok {
			return nil
		}
	}
	return current
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(value []byte) (int, error) {
	length := len(value)
	if w.remaining <= 0 {
		return length, nil
	}
	toWrite := value
	if int64(len(toWrite)) > w.remaining {
		toWrite = toWrite[:w.remaining]
	}
	_, err := w.writer.Write(toWrite)
	w.remaining -= int64(len(toWrite))
	return length, err
}
