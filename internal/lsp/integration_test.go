package lspclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhenxin-dev/banka-code/internal/permissions"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestLSPToolLifecycleDiagnosticsNavigationAndRename(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.fake")
	if err := os.WriteFile(path, []byte("hello Banka\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := Config{Servers: map[string]ServerConfig{
		"fake": {
			Command: os.Args[0], ResolvedCommand: os.Args[0],
			Args: []string{"-test.run=TestLSPHelperProcess"}, Env: map[string]string{"BANKA_LSP_TEST_HELPER": "1"},
			FileTypes: []string{".fake"}, LanguageID: "fake", Priority: 100,
		},
	}}
	manager := NewManager(root, "test", config)
	defer manager.Close()
	definition := manager.NewTool()
	toolContext := tools.Context{WorkspaceRoot: root, Permissions: permissions.NewPolicy(permissions.ModeFullAccess)}

	status, err := definition.Execute(context.Background(), map[string]any{"action": "status"}, toolContext)
	if err != nil || !strings.Contains(status.Content, "not started") {
		t.Fatalf("unexpected lazy status: result=%#v err=%v", status, err)
	}
	hover, err := definition.Execute(context.Background(), map[string]any{
		"action": "hover", "file": "demo.fake", "line": float64(1), "symbol": "Banka",
	}, toolContext)
	if err != nil || hover.Content != "fake hover" {
		t.Fatalf("unexpected hover: result=%#v err=%v statuses=%#v", hover, err, manager.Statuses())
	}
	diagnostics, err := definition.Execute(context.Background(), map[string]any{"action": "diagnostics", "file": "demo.fake"}, toolContext)
	if err != nil || !strings.Contains(diagnostics.Content, "fake diagnostic") {
		t.Fatalf("unexpected diagnostics: result=%#v err=%v", diagnostics, err)
	}
	preview, err := definition.Execute(context.Background(), map[string]any{
		"action": "rename", "file": "demo.fake", "line": float64(1), "symbol": "Banka", "new_name": "Code", "apply": false,
	}, toolContext)
	if err != nil || !strings.Contains(preview.Content, "demo.fake") {
		t.Fatalf("unexpected rename preview: result=%#v err=%v", preview, err)
	}
	_, err = definition.Execute(context.Background(), map[string]any{
		"action": "rename", "file": "demo.fake", "line": float64(1), "symbol": "Banka", "new_name": "Code",
	}, toolContext)
	if err != nil {
		t.Fatalf("rename returned error: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "hello Code\n" {
		t.Fatalf("rename was not applied: content=%q err=%v", content, err)
	}
}

func TestLSPMutatingActionsRequireApproval(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.fake")
	if err := os.WriteFile(path, []byte("hello Banka\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{
		"fake": {Command: os.Args[0], ResolvedCommand: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess"}, Env: map[string]string{"BANKA_LSP_TEST_HELPER": "1"}, FileTypes: []string{".fake"}},
	}})
	defer manager.Close()
	result, err := manager.NewTool().Execute(context.Background(), map[string]any{
		"action": "rename", "file": "demo.fake", "line": float64(1), "symbol": "Banka", "new_name": "Code",
	}, tools.Context{WorkspaceRoot: root, Interaction: denyLSPInteraction{}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "denied") {
		t.Fatalf("mutating LSP action was not denied: result=%#v err=%v", result, err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != "hello Banka\n" {
		t.Fatalf("denied rename changed file: %q err=%v", content, readErr)
	}
}

func TestLSPManagerReapsIdleClient(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.fake")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, "test", Config{IdleTimeoutMS: 150, Servers: map[string]ServerConfig{
		"fake": {Command: os.Args[0], ResolvedCommand: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess"}, Env: map[string]string{"BANKA_LSP_TEST_HELPER": "1"}, FileTypes: []string{".fake"}},
	}})
	defer manager.Close()
	if _, _, err := manager.ClientForFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(manager.Statuses()) > 0 && !manager.Statuses()[0].Running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("idle LSP client was not reaped: %#v", manager.Statuses())
}

func TestLSPToolRejectsOutsideWorkspaceEvenWithFullAccess(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.fake")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{
		"fake": {Command: os.Args[0], ResolvedCommand: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess"}, Env: map[string]string{"BANKA_LSP_TEST_HELPER": "1"}, FileTypes: []string{".fake"}},
	}})
	defer manager.Close()
	result, err := manager.NewTool().Execute(context.Background(), map[string]any{
		"action": "hover", "file": outside, "line": float64(1), "symbol": "secret",
	}, tools.Context{WorkspaceRoot: root, Permissions: permissions.NewPolicy(permissions.ModeFullAccess)})
	if err == nil && !result.IsError {
		t.Fatalf("outside-workspace LSP request was accepted: result=%#v", result)
	}
}

func TestLSPToolUsesCLILinterForDiagnosticsAndFormatting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.ts")
	if err := os.WriteFile(path, []byte("const value=1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "biome")
	script := "#!/bin/sh\ncase \"$1\" in\nlint) printf '%s' '{\"diagnostics\":[{\"category\":\"lint/demo\",\"severity\":\"error\",\"message\":\"demo lint\",\"location\":{\"path\":\"demo.ts\",\"start\":{\"line\":1,\"column\":7},\"end\":{\"line\":1,\"column\":12}}}]}'; exit 1 ;;\nformat) sed 's/value/value2/'; exit 0 ;;\nesac\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{
		"biome": {Command: command, ResolvedCommand: command, FileTypes: []string{".ts"}, Linter: "biome", IsLinter: true},
	}})
	defer manager.Close()
	toolContext := tools.Context{WorkspaceRoot: root, Permissions: permissions.NewPolicy(permissions.ModeFullAccess)}
	definition := manager.NewTool()
	result, err := definition.Execute(context.Background(), map[string]any{"action": "diagnostics", "file": "demo.ts"}, toolContext)
	if err != nil || !strings.Contains(result.Content, "demo lint") || !result.IsError {
		t.Fatalf("CLI linter diagnostics failed: result=%#v err=%v", result, err)
	}
	formatted, err := definition.Execute(context.Background(), map[string]any{"action": "format", "file": "demo.ts", "apply": false}, toolContext)
	if err != nil || !strings.Contains(formatted.Content, "Formatting preview") {
		t.Fatalf("CLI linter formatting preview failed: result=%#v err=%v", formatted, err)
	}
}

func TestLSPRawRequestAlwaysRequiresApproval(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.fake")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{
		"fake": {Command: os.Args[0], ResolvedCommand: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess"}, Env: map[string]string{"BANKA_LSP_TEST_HELPER": "1"}, FileTypes: []string{".fake"}},
	}})
	defer manager.Close()
	result, err := manager.NewTool().Execute(context.Background(), map[string]any{
		"action": "request", "file": "demo.fake", "query": "custom/readOnlyLookingMethod",
	}, tools.Context{WorkspaceRoot: root, Interaction: denyLSPInteraction{}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "denied") {
		t.Fatalf("raw LSP request bypassed approval: result=%#v err=%v", result, err)
	}
}

func TestLSPReloadRemovesSupersededStartup(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{
		"fake": {Command: filepath.Join(root, "missing-server"), ResolvedCommand: filepath.Join(root, "missing-server"), FileTypes: []string{".fake"}},
	}})
	defer manager.Close()

	startContext, cancelStart := context.WithCancel(context.Background())
	pending := &clientStart{done: make(chan struct{}), cancel: cancelStart, epoch: manager.configEpoch}
	manager.mu.Lock()
	manager.starting["fake"] = pending
	epoch := manager.configEpoch
	manager.mu.Unlock()
	go func() {
		<-startContext.Done()
		close(pending.done)
	}()

	if err := manager.Reload(context.Background(), "fake"); err == nil {
		t.Fatal("reload unexpectedly started a missing language server")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.starting["fake"] != nil {
		t.Fatal("reload left the superseded startup in the manager")
	}
	if manager.configEpoch != epoch {
		t.Fatalf("single-server reload invalidated unrelated startups: epoch %d -> %d", epoch, manager.configEpoch)
	}
}

func TestLSPClientRejectsServerRemovedByConfigurationReload(t *testing.T) {
	root := t.TempDir()
	server := ServerConfig{Command: os.Args[0], ResolvedCommand: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess"}, Env: map[string]string{"BANKA_LSP_TEST_HELPER": "1"}, FileTypes: []string{".fake"}}
	manager := NewManager(root, "test", Config{Servers: map[string]ServerConfig{"fake": server}})
	defer manager.Close()
	if err := manager.ReloadConfiguration(context.Background(), Config{Servers: map[string]ServerConfig{}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Client(context.Background(), "fake", server); err == nil || !strings.Contains(err.Error(), "unknown language server") {
		t.Fatalf("stale server configuration was accepted after reload: %v", err)
	}
}

func TestWaitForDiagnosticsReturnsWhenServerPublishes(t *testing.T) {
	client := &Client{
		done:              make(chan struct{}),
		diagnostics:       make(map[string][]diagnostic),
		diagnosticWaiters: make(map[string][]chan struct{}),
	}
	uri := "file:///workspace/demo.fake"
	go func() {
		time.Sleep(40 * time.Millisecond)
		client.setDiagnostics(uri, []diagnostic{{Message: "late diagnostic"}})
	}()
	started := time.Now()
	values := client.WaitForDiagnostics(context.Background(), uri, time.Second)
	if len(values) != 1 || values[0].Message != "late diagnostic" {
		t.Fatalf("unexpected diagnostics: %#v", values)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("diagnostic waiter did not wake on publication: %s", elapsed)
	}

	client.setDiagnostics(uri, nil)
	started = time.Now()
	if values := client.WaitForDiagnostics(context.Background(), uri, time.Second); len(values) != 0 {
		t.Fatalf("explicit empty diagnostics changed shape: %#v", values)
	} else if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("explicit empty diagnostics did not return immediately: %s", elapsed)
	}
}

func TestClientIgnoresStaleVersionedDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.fake")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := fileURI(path)
	client := &Client{
		root:              root,
		diagnostics:       make(map[string][]diagnostic),
		diagnosticWaiters: make(map[string][]chan struct{}),
		documents:         map[string]openDocument{uri: {URI: uri, Path: path, Version: 2}},
	}
	client.handleNotification("textDocument/publishDiagnostics", mustJSONValue(t, map[string]any{
		"uri": uri, "version": 1, "diagnostics": []map[string]any{{"message": "stale"}},
	}))
	if got := client.Diagnostics(uri); len(got) != 0 {
		t.Fatalf("stale diagnostics were accepted: %#v", got)
	}
	client.handleNotification("textDocument/publishDiagnostics", mustJSONValue(t, map[string]any{
		"uri": uri, "version": 2, "diagnostics": []map[string]any{{"message": "current"}},
	}))
	got := client.Diagnostics(uri)
	if len(got) != 1 || got[0].Message != "current" {
		t.Fatalf("current diagnostics were not accepted: %#v", got)
	}
}

func mustJSONValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type denyLSPInteraction struct{}

func (denyLSPInteraction) RequestApproval(context.Context, tools.ApprovalRequest) (tools.ApprovalDecision, error) {
	return tools.ApprovalDeny, nil
}

func (denyLSPInteraction) AskUser(context.Context, tools.QuestionRequest) (string, error) {
	return "", nil
}

func TestLSPHelperProcess(t *testing.T) {
	if os.Getenv("BANKA_LSP_TEST_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		body, err := readFrame(reader)
		if err != nil {
			os.Exit(0)
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &message) != nil {
			os.Exit(2)
		}
		switch message.Method {
		case "initialize":
			writeTestLSPResponse(message.ID, map[string]any{"capabilities": map[string]any{
				"hoverProvider": true, "renameProvider": true, "documentSymbolProvider": true,
			}})
		case "textDocument/didOpen", "textDocument/didChange":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(message.Params, &params)
			writeTestLSPNotification("textDocument/publishDiagnostics", map[string]any{
				"uri": params.TextDocument.URI,
				"diagnostics": []map[string]any{{
					"range":    map[string]any{"start": map[string]int{"line": 0, "character": 6}, "end": map[string]int{"line": 0, "character": 11}},
					"severity": 2, "message": "fake diagnostic", "source": "fake",
				}},
			})
		case "textDocument/hover":
			writeTestLSPResponse(message.ID, map[string]any{"contents": map[string]any{"kind": "markdown", "value": "fake hover"}})
		case "textDocument/diagnostic":
			writeTestLSPError(message.ID, -32601, "method not found")
		case "textDocument/rename":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				NewName string `json:"newName"`
			}
			_ = json.Unmarshal(message.Params, &params)
			writeTestLSPResponse(message.ID, map[string]any{"changes": map[string]any{
				params.TextDocument.URI: []map[string]any{{
					"range":   map[string]any{"start": map[string]int{"line": 0, "character": 6}, "end": map[string]int{"line": 0, "character": 11}},
					"newText": params.NewName,
				}},
			}})
		case "shutdown":
			writeTestLSPResponse(message.ID, nil)
		case "exit":
			os.Exit(0)
		}
	}
}

func writeTestLSPResponse(id json.RawMessage, result any) {
	writeTestLSPMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeTestLSPError(id json.RawMessage, code int, message string) {
	writeTestLSPMessage(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func writeTestLSPNotification(method string, params any) {
	writeTestLSPMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func writeTestLSPMessage(value any) {
	body, err := json.Marshal(value)
	if err != nil {
		os.Exit(3)
	}
	if _, err := io.WriteString(os.Stdout, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))); err != nil {
		os.Exit(4)
	}
	if _, err := os.Stdout.Write(body); err != nil {
		os.Exit(5)
	}
}
