package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

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
