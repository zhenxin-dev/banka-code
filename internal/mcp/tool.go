package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

const maxMCPResultBytes = 12_000

var invalidToolNameCharacter = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

type mcpTool struct {
	name        string
	serverName  string
	remoteName  string
	description string
	schema      tools.JSONSchema
	session     *mcp.ClientSession
	trusted     bool
}

func newMCPTool(serverName string, remote *mcp.Tool, session *mcp.ClientSession, trusted bool) (tools.Definition, error) {
	schema, err := normalizeSchema(remote.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s/%s has invalid input schema: %w", serverName, remote.Name, err)
	}
	description := strings.TrimSpace(remote.Description)
	if description == "" {
		description = "MCP tool " + remote.Name
	}
	return &mcpTool{
		name: externalToolName(serverName, remote.Name), serverName: serverName,
		remoteName: remote.Name, description: "[MCP " + serverName + "] " + description,
		schema: schema, session: session, trusted: trusted,
	}, nil
}

func (t *mcpTool) Name() string                  { return t.name }
func (t *mcpTool) Description() string           { return t.description }
func (t *mcpTool) InputSchema() tools.JSONSchema { return t.schema }
func (t *mcpTool) Execute(ctx context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	if !t.trusted {
		if toolContext.Interaction == nil {
			return tools.Result{Content: "MCP tool execution requires interactive user approval.", IsError: true}, nil
		}
		encoded, _ := json.Marshal(arguments)
		decision, err := toolContext.Interaction.RequestApproval(ctx, tools.ApprovalRequest{
			ToolName:      t.name,
			Command:       fmt.Sprintf("MCP %s/%s %s", t.serverName, t.remoteName, encoded),
			Justification: "调用未标记为受信任的 MCP 服务器工具",
		})
		if err != nil {
			return tools.Result{}, err
		}
		if decision != tools.ApprovalAllowOnce {
			return tools.Result{Content: "User denied MCP tool execution.", IsError: true}, nil
		}
	}
	result, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: t.remoteName, Arguments: arguments})
	if err != nil {
		return tools.Result{}, err
	}
	content, err := formatMCPResult(result)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: content, IsError: result.IsError}, nil
}

func normalizeSchema(value any) (tools.JSONSchema, error) {
	if value == nil {
		return tools.JSONSchema{"type": "object", "properties": map[string]any{}}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema tools.JSONSchema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	if schema["type"] == nil {
		schema["type"] = "object"
	}
	return schema, nil
}

func externalToolName(serverName string, remoteName string) string {
	name := "mcp__" + sanitizeToolName(serverName) + "__" + sanitizeToolName(remoteName)
	if len(name) <= 64 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:4])
	return name[:64-len(suffix)] + suffix
}

func sanitizeToolName(value string) string {
	value = strings.Trim(invalidToolNameCharacter.ReplaceAllString(value, "_"), "_")
	if value == "" {
		return "unnamed"
	}
	return value
}

func formatMCPResult(result *mcp.CallToolResult) (string, error) {
	if result == nil {
		return "", errors.New("MCP server returned an empty result")
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		encoded, err := content.MarshalJSON()
		if err != nil {
			return "", err
		}
		parts = append(parts, string(encoded))
	}
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return "", err
		}
		parts = append(parts, "structured_content: "+string(encoded))
	}
	if len(parts) == 0 {
		parts = append(parts, "MCP tool completed without content.")
	}
	joined := strings.Join(parts, "\n")
	if len(joined) > maxMCPResultBytes {
		joined = joined[:maxMCPResultBytes] + "\n\n[truncated]"
	}
	return joined, nil
}
