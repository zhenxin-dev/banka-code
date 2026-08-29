package mcpclient

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

type echoInput struct {
	Text string `json:"text"`
}

type echoOutput struct {
	Reply string `json:"reply"`
}

func TestMCPToolDiscoveryAndCall(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo input"},
		func(_ context.Context, _ *mcp.CallToolRequest, input echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{Reply: "echo: " + input.Text}, nil
		})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 {
		t.Fatalf("unexpected tools list: %#v err=%v", listed, err)
	}
	definition, err := newMCPTool("demo", listed.Tools[0], clientSession, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := definition.Execute(ctx, map[string]any{"text": "hello"}, tools.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, "echo: hello") || definition.Name() != "mcp__demo__echo" {
		t.Fatalf("unexpected MCP result: %#v name=%s", result, definition.Name())
	}
}

func TestUntrustedMCPToolRequiresApproval(t *testing.T) {
	interaction := &approvalInteraction{decision: tools.ApprovalDeny}
	definition := &mcpTool{name: "mcp__demo__write", serverName: "demo", remoteName: "write", trusted: false}
	result, err := definition.Execute(context.Background(), map[string]any{"value": "x"}, tools.Context{Interaction: interaction})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "denied") || !strings.Contains(interaction.request.Command, "MCP demo/write") {
		t.Fatalf("unexpected approval result: result=%#v request=%#v", result, interaction.request)
	}
}

func TestMCPResourceAndPromptTools(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "capability-server", Version: "1.0.0"}, nil)
	server.AddResource(&mcp.Resource{Name: "guide", URI: "docs://guide", MIMEType: "text/plain"},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "docs://guide", Text: "guide body"}}}, nil
		})
	server.AddPrompt(&mcp.Prompt{Name: "review"},
		func(_ context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{
				Role: mcp.Role("user"), Content: &mcp.TextContent{Text: "review " + request.Params.Arguments["target"]},
			}}}, nil
		})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	manager := &Manager{connections: map[string]serverConnection{"demo": {session: clientSession, trusted: true}}}
	definitions := newCapabilityTools(manager)

	listResult, err := definitions[0].Execute(ctx, map[string]any{"server": "demo"}, tools.Context{})
	if err != nil || !strings.Contains(listResult.Content, "docs://guide") {
		t.Fatalf("unexpected resource list: result=%#v err=%v", listResult, err)
	}
	readResult, err := definitions[1].Execute(ctx, map[string]any{"server": "demo", "uri": "docs://guide"}, tools.Context{})
	if err != nil || !strings.Contains(readResult.Content, "guide body") {
		t.Fatalf("unexpected resource content: result=%#v err=%v", readResult, err)
	}
	promptResult, err := definitions[3].Execute(ctx, map[string]any{
		"server": "demo", "name": "review", "arguments": map[string]any{"target": "code"},
	}, tools.Context{})
	if err != nil || !strings.Contains(promptResult.Content, "review code") {
		t.Fatalf("unexpected prompt result: result=%#v err=%v", promptResult, err)
	}
}

func TestExternalToolNameIsStableAndConservative(t *testing.T) {
	if got := externalToolName("My Server", "Search/Files"); got != "mcp__my_server__search_files" {
		t.Fatalf("unexpected normalized MCP tool name: %q", got)
	}
	long := externalToolName(strings.Repeat("server", 20), strings.Repeat("tool", 20))
	if len(long) > 64 || strings.ContainsAny(long, " ./") {
		t.Fatalf("long MCP tool name was not bounded/sanitized: %q (len=%d)", long, len(long))
	}
}

type approvalInteraction struct {
	decision tools.ApprovalDecision
	request  tools.ApprovalRequest
}

func (i *approvalInteraction) RequestApproval(_ context.Context, request tools.ApprovalRequest) (tools.ApprovalDecision, error) {
	i.request = request
	return i.decision, nil
}

func (*approvalInteraction) AskUser(context.Context, tools.QuestionRequest) (string, error) {
	return "", nil
}
