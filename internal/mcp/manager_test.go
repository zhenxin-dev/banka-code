package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestResolveMCPCommandRejectsRelativeWorkspaceEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-mcp")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMCPCommand(root, "../"+filepath.Base(outside)); err == nil {
		t.Fatal("relative MCP command escaped the workspace")
	}
}

func TestMCPHTTPTransportDropsProtocolOwnedConfiguredHeaders(t *testing.T) {
	var received http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := newMCPHTTPClient(server.URL, map[string]string{
		"Content-Type":         "text/plain",
		"Accept":               "text/plain",
		"Mcp-Protocol-Version": "bogus",
		"X-Custom":             "kept",
	})
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); err != nil {
		t.Fatal(err)
	}
	if got := received.Get("Content-Type"); got == "text/plain" {
		t.Fatalf("configured Content-Type was forwarded: %q", got)
	}
	if got := received.Get("Accept"); got == "text/plain" {
		t.Fatalf("configured Accept was forwarded: %q", got)
	}
	if got := received.Get("Mcp-Protocol-Version"); got == "bogus" {
		t.Fatalf("configured protocol version was forwarded: %q", got)
	}
	if got := received.Get("X-Custom"); got != "kept" {
		t.Fatalf("ordinary configured header was dropped: %q", got)
	}
}

func TestManagerConnectsStreamableHTTPServer(t *testing.T) {
	server := newEchoServer()
	var authorization string
	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		authorization = request.Header.Get("Authorization")
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), "test")
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"remote": {URL: httpServer.URL, Headers: map[string]string{"Authorization": "Bearer test"}, Trusted: true},
	}})
	defer manager.Close()
	definition := findDefinition(definitions, "mcp__remote__echo")
	if definition == nil {
		t.Fatalf("remote tool was not discovered: %#v", manager.Statuses())
	}
	result, err := definition.Execute(context.Background(), map[string]any{"text": "http"}, tools.Context{})
	if err != nil || result.IsError || !strings.Contains(result.Content, "echo: http") {
		t.Fatalf("unexpected HTTP MCP result: result=%#v err=%v", result, err)
	}
	if authorization != "Bearer test" {
		t.Fatalf("custom HTTP header was not sent: %q", authorization)
	}
}

func TestManagerAppliesStaticAuthTokenAndProtectsProtocolHeaders(t *testing.T) {
	server := newEchoServer()
	var authorization, contentType string
	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		authorization = request.Header.Get("Authorization")
		contentType = request.Header.Get("Content-Type")
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), "test")
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"remote": {
			URL:  httpServer.URL,
			Auth: map[string]any{"type": "apikey", "token": "static-secret"},
			// This must not replace the SDK's protocol-owned Content-Type.
			Headers: map[string]string{"content-type": "text/plain"}, Trusted: true,
		},
	}})
	defer manager.Close()
	if findDefinition(definitions, "mcp__remote__echo") == nil {
		t.Fatalf("remote tool was not discovered: %#v", manager.Statuses())
	}
	if authorization != "Bearer static-secret" {
		t.Fatalf("static auth token was not applied: %q", authorization)
	}
	if contentType != "application/json" {
		t.Fatalf("configured protocol header overrode SDK header: %q", contentType)
	}
}

func TestStaticMCPAuthHeaderSupportsCustomAPIKeyHeader(t *testing.T) {
	server := ServerConfig{Auth: map[string]any{
		"type": "apikey", "apiKey": "abc", "headerName": "X-API-Key",
	}}
	name, value, ok := staticMCPAuthHeader(server)
	if !ok || name != "X-API-Key" || value != "abc" {
		t.Fatalf("unexpected custom API-key header: name=%q value=%q ok=%v", name, value, ok)
	}
}

func TestManagerConnectsSSEServer(t *testing.T) {
	server := newEchoServer()
	httpServer := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	manager := NewManager(t.TempDir(), "test")
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"sse": {URL: httpServer.URL, Transport: "sse", Trusted: true},
	}})
	defer manager.Close()
	definition := findDefinition(definitions, "mcp__sse__echo")
	if definition == nil {
		t.Fatalf("SSE tool was not discovered: %#v", manager.Statuses())
	}
	result, err := definition.Execute(context.Background(), map[string]any{"text": "sse"}, tools.Context{})
	if err != nil || result.IsError || !strings.Contains(result.Content, "echo: sse") {
		t.Fatalf("unexpected SSE MCP result: result=%#v err=%v", result, err)
	}
}

func TestManagerConnectsStdioServer(t *testing.T) {
	manager := NewManager(t.TempDir(), "test")
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"stdio": {
			Command: os.Args[0], Args: []string{"-test.run=TestMCPStdioHelperProcess"},
			Env: map[string]string{"BANKA_MCP_TEST_HELPER": "1"}, Trusted: true,
		},
	}})
	defer manager.Close()
	definition := findDefinition(definitions, "mcp__stdio__echo")
	if definition == nil {
		t.Fatalf("stdio tool was not discovered: %#v", manager.Statuses())
	}
	result, err := definition.Execute(context.Background(), map[string]any{"text": "stdio"}, tools.Context{})
	if err != nil || result.IsError || !strings.Contains(result.Content, "echo: stdio") {
		t.Fatalf("unexpected stdio MCP result: result=%#v err=%v", result, err)
	}
}

func TestMCPProcessTimeoutOverride(t *testing.T) {
	t.Setenv("OMP_MCP_TIMEOUT_MS", "1234")
	if got := mcpConfiguredTimeout(ServerConfig{TimeoutMS: 50}, time.Second); got != 1234*time.Millisecond {
		t.Fatalf("OMP timeout override was not applied: %s", got)
	}
	t.Setenv("BANKA_MCP_TIMEOUT_MS", "0")
	if got := mcpConfiguredTimeout(ServerConfig{TimeoutMS: 50}, time.Second); got != 0 {
		t.Fatalf("Banka zero-timeout opt-out was not applied: %s", got)
	}
}

func TestMCPToolReconnectsAfterDroppedSession(t *testing.T) {
	manager := NewManager(t.TempDir(), "test")
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"stdio": {
			Command: os.Args[0], Args: []string{"-test.run=TestMCPStdioHelperProcess"},
			Env: map[string]string{"BANKA_MCP_TEST_HELPER": "1"}, Trusted: true,
		},
	}})
	defer manager.Close()
	definition := findDefinition(definitions, "mcp__stdio__echo")
	if definition == nil {
		t.Fatalf("stdio tool was not discovered: %#v", manager.Statuses())
	}
	connection, ok := manager.connection("stdio")
	if !ok || connection.session == nil {
		t.Fatal("stdio connection is unavailable")
	}
	if err := connection.session.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := definition.Execute(context.Background(), map[string]any{"text": "again"}, tools.Context{})
	if err != nil || result.IsError || !strings.Contains(result.Content, "echo: again") {
		t.Fatalf("MCP tool did not reconnect: result=%#v err=%v statuses=%#v", result, err, manager.Statuses())
	}
}

func TestMCPFailedReconnectRemovesStaleToolDefinition(t *testing.T) {
	manager := NewManager(t.TempDir(), "test")
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"stdio": {
			Command: os.Args[0], Args: []string{"-test.run=TestMCPStdioHelperProcess"},
			Env: map[string]string{"BANKA_MCP_TEST_HELPER": "1"}, Trusted: true,
		},
	}})
	defer manager.Close()
	if findDefinition(definitions, "mcp__stdio__echo") == nil {
		t.Fatal("initial MCP tool was not discovered")
	}

	manager.mu.Lock()
	server := manager.configs["stdio"]
	server.Command = "banka-missing-mcp-server"
	server.ResolvedCommand = ""
	manager.configs["stdio"] = server
	manager.mu.Unlock()
	if err := manager.Reconnect(context.Background(), "stdio"); err == nil {
		t.Fatal("reconnect unexpectedly found the missing MCP command")
	}
	if definition := findDefinition(manager.ToolDefinitions(), "mcp__stdio__echo"); definition != nil {
		t.Fatalf("failed reconnect left a stale tool definition: %#v", definition)
	}
}

func TestMCPToolsListChangedRefreshesSnapshot(t *testing.T) {
	server := newEchoServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	manager := NewManager(t.TempDir(), "test")
	defer manager.Close()
	updates := make(chan []tools.Definition, 4)
	manager.SetToolsChangedHandler(func(definitions []tools.Definition) {
		updates <- definitions
	})
	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"remote": {URL: httpServer.URL, Trusted: true},
	}})
	if findDefinition(definitions, "mcp__remote__echo") == nil {
		t.Fatal("initial MCP tool was not discovered")
	}
	mcp.AddTool(server, &mcp.Tool{Name: "second", Description: "Second tool"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			return nil, struct{}{}, nil
		})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case snapshot := <-updates:
			if findDefinition(snapshot, "mcp__remote__second") != nil {
				return
			}
		default:
			time.Sleep(25 * time.Millisecond)
		}
	}
	t.Fatal("tools/list_changed did not refresh manager snapshot")
}

func TestManagerConnectReturnsBeforeSlowDiscoveryAndPublishesLateTools(t *testing.T) {
	server := newEchoServer()
	delegate := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		delegate.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	manager := NewManager(t.TempDir(), "test")
	defer manager.Close()
	updates := make(chan []tools.Definition, 8)
	manager.SetToolsChangedHandler(func(definitions []tools.Definition) { updates <- definitions })
	started := time.Now()
	initial := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{
		"slow": {URL: httpServer.URL, Trusted: true},
	}})
	if elapsed := time.Since(started); elapsed >= startupGate+150*time.Millisecond {
		t.Fatalf("Connect blocked for slow discovery: %s", elapsed)
	}
	if findDefinition(initial, "mcp__slow__echo") != nil {
		t.Fatal("slow tool unexpectedly appeared in the synchronous snapshot")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case definitions := <-updates:
			if findDefinition(definitions, "mcp__slow__echo") != nil {
				return
			}
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("late MCP discovery did not publish tools")
}

func TestManagerConnectClearsDynamicToolsForEmptyReload(t *testing.T) {
	manager := NewManager(t.TempDir(), "test")
	defer manager.Close()
	manager.definitions = []tools.Definition{tools.NewReadTool()}

	updates := make(chan []tools.Definition, 4)
	manager.SetToolsChangedHandler(func(definitions []tools.Definition) {
		updates <- definitions
	})
	initial := <-updates
	if findDefinition(initial, "Read") == nil {
		t.Fatalf("initial snapshot was not published: %#v", initial)
	}

	definitions := manager.Connect(context.Background(), Config{Servers: map[string]ServerConfig{}})
	if len(definitions) != 0 {
		t.Fatalf("empty reload returned stale definitions: %#v", definitions)
	}
	select {
	case snapshot := <-updates:
		if len(snapshot) != 0 {
			t.Fatalf("empty reload published stale definitions: %#v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("empty reload did not publish the cleared tool snapshot")
	}
}

func TestMCPToolCallbacksDiscardStaleQueuedCatalog(t *testing.T) {
	manager := NewManager(t.TempDir(), "test")
	defer manager.Close()
	updates := make(chan string, 8)
	manager.SetToolsChangedHandler(func(definitions []tools.Definition) {
		if len(definitions) == 0 {
			updates <- "empty"
			return
		}
		updates <- definitions[0].Name()
	})
	// SetToolsChangedHandler's initial empty snapshot is not part of the
	// refresh sequence under test.
	select {
	case <-updates:
	default:
	}

	manager.mu.Lock()
	manager.catalogVersion = 1
	manager.definitions = []tools.Definition{tools.NewReadTool()}
	manager.mu.Unlock()

	// Hold callback delivery while replacing the catalog. Both notifiers are
	// then released together; only the current version may reach the handler,
	// regardless of which goroutine wins the mutex.
	manager.notifyMu.Lock()
	manager.mu.Lock()
	manager.catalogVersion = 2
	manager.definitions = []tools.Definition{tools.NewWriteTool()}
	manager.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() {
		manager.notifyToolsChangedVersion(1)
		done <- struct{}{}
	}()
	go func() {
		manager.notifyToolsChangedVersion(2)
		done <- struct{}{}
	}()
	manager.notifyMu.Unlock()
	<-done
	<-done

	select {
	case got := <-updates:
		if got != "Write" {
			t.Fatalf("stale catalog callback delivered %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("current catalog callback was not delivered")
	}
	select {
	case got := <-updates:
		t.Fatalf("unexpected second catalog callback: %q", got)
	default:
	}
}

func TestMCPToolCallbacksRemainInOrder(t *testing.T) {
	manager := NewManager(t.TempDir(), "test")
	defer manager.Close()
	updates := make(chan string, 8)
	manager.SetToolsChangedHandler(func(definitions []tools.Definition) {
		if len(definitions) > 0 {
			updates <- definitions[0].Name()
		}
	})
	manager.mu.Lock()
	manager.catalogVersion = 1
	manager.definitions = []tools.Definition{tools.NewReadTool()}
	manager.mu.Unlock()
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	// Replace the handler with a gate for the first callback. Registration's
	// immediate callback is intentionally empty by clearing definitions first.
	manager.mu.Lock()
	manager.definitions = nil
	manager.mu.Unlock()
	manager.SetToolsChangedHandler(func(definitions []tools.Definition) {
		if len(definitions) == 0 {
			return
		}
		updates <- definitions[0].Name()
		startedOnce.Do(func() { close(started) })
		<-release
	})
	manager.mu.Lock()
	manager.definitions = []tools.Definition{tools.NewReadTool()}
	manager.mu.Unlock()
	go manager.notifyToolsChangedVersion(1)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first catalog callback did not start")
	}
	manager.mu.Lock()
	manager.catalogVersion = 2
	manager.definitions = []tools.Definition{tools.NewWriteTool()}
	manager.mu.Unlock()
	secondDone := make(chan struct{})
	go func() {
		manager.notifyToolsChangedVersion(2)
		close(secondDone)
	}()
	close(release)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second catalog callback did not complete")
	}
	first, second := <-updates, <-updates
	if first != "Read" || second != "Write" {
		t.Fatalf("catalog callbacks out of order: %q, %q", first, second)
	}
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("BANKA_MCP_TEST_HELPER") != "1" {
		return
	}
	err := newEchoServer().Run(context.Background(), &mcp.StdioTransport{})
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func newEchoServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "echo-server", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo input"},
		func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{Reply: "echo: " + input.Text}, nil
		})
	return server
}

func findDefinition(definitions []tools.Definition, name string) tools.Definition {
	for _, definition := range definitions {
		if definition.Name() == name {
			return definition
		}
	}
	return nil
}
