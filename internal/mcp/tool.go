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
	"sync"
	"time"
	"unicode/utf8"

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
	manager     *Manager
	trusted     bool
	timeout     time.Duration
	sessionMu   sync.RWMutex
}

// newMCPTool builds a model-facing wrapper for a remote MCP tool.  The
// optional timeout argument keeps the pre-timeout API source-compatible for
// embedders that used the internal package in tests; omitted values use the
// normal per-call default.
func newMCPTool(serverName string, remote *mcp.Tool, session *mcp.ClientSession, trusted bool, timeouts ...time.Duration) (tools.Definition, error) {
	if remote == nil {
		return nil, errors.New("MCP server returned a nil tool definition")
	}
	if strings.TrimSpace(remote.Name) == "" {
		return nil, errors.New("MCP server returned a tool with an empty name")
	}
	if session == nil {
		return nil, errors.New("MCP tool requires an active client session")
	}
	var timeout time.Duration
	if len(timeouts) > 0 {
		timeout = timeouts[0]
	} else {
		timeout = 30 * time.Second
	}
	return newMCPToolNamed(serverName, remote, session, trusted, timeout, externalToolName(serverName, remote.Name))
}

func newMCPToolNamed(serverName string, remote *mcp.Tool, session *mcp.ClientSession, trusted bool, timeout time.Duration, generatedName string) (tools.Definition, error) {
	schema, err := normalizeSchema(remote.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s/%s has invalid input schema: %w", serverName, remote.Name, err)
	}
	description := strings.TrimSpace(remote.Description)
	if description == "" {
		description = "MCP tool " + remote.Name
	}
	return &mcpTool{
		name: generatedName, serverName: serverName,
		remoteName: remote.Name, description: "[MCP " + serverName + "] " + description,
		schema: schema, session: session, trusted: trusted, timeout: timeout,
	}, nil
}

func (t *mcpTool) Name() string                  { return t.name }
func (t *mcpTool) Description() string           { return t.description }
func (t *mcpTool) InputSchema() tools.JSONSchema { return t.schema }
func (t *mcpTool) Execute(ctx context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !t.trusted {
		encoded, _ := json.Marshal(arguments)
		allowed, err := toolContext.RequestPermission(ctx, tools.ApprovalRequest{
			ToolName:      t.name,
			Kind:          tools.ApprovalExternal,
			Scope:         "mcp:" + t.serverName,
			Command:       fmt.Sprintf("MCP %s/%s %s", t.serverName, t.remoteName, encoded),
			Justification: "调用未标记为受信任的 MCP 服务器工具",
		})
		if err != nil {
			return tools.Result{}, err
		}
		if !allowed {
			return tools.Result{Content: "User denied MCP tool execution.", IsError: true}, nil
		}
	}
	callContext, cancel := mcpOperationContext(ctx, t.timeout)
	defer cancel()
	session := t.currentSession()
	if session == nil && t.manager != nil {
		// A disconnect notification can remove the session from the manager
		// before a tool call arrives. Re-establish it eagerly so a stale tool
		// definition remains useful instead of requiring a manual reload.
		if reconnectErr := t.manager.Reconnect(callContext, t.serverName); reconnectErr != nil {
			return tools.Result{}, fmt.Errorf("reconnect MCP server %s: %w", t.serverName, reconnectErr)
		}
		session = t.currentSession()
	}
	if session == nil {
		return tools.Result{}, errors.New("MCP client session is unavailable")
	}
	result, err := session.CallTool(callContext, &mcp.CallToolParams{Name: t.remoteName, Arguments: arguments})
	// A remote process can disappear after discovery (or an HTTP session can
	// expire). Reconnect once for transport-level failures; do not retry normal
	// tool errors, because those may represent a completed side effect.
	if err != nil && t.manager != nil && isRetryableMCPError(err) && callContext.Err() == nil {
		if reconnectErr := t.manager.Reconnect(callContext, t.serverName); reconnectErr == nil {
			session = t.currentSession()
			if session != nil {
				result, err = session.CallTool(callContext, &mcp.CallToolParams{Name: t.remoteName, Arguments: arguments})
			}
		}
	}
	if err != nil {
		return tools.Result{}, err
	}
	content, err := formatMCPResult(result)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: content, IsError: result.IsError}, nil
}

func (t *mcpTool) currentSession() *mcp.ClientSession {
	if t.manager != nil {
		// Once a manager owns the wrapper, its connection map is the source of
		// truth. Do not fall back to the discovery-time session after a reload or
		// shutdown: that stale session may still accept a call briefly and would
		// bypass the manager's lifecycle/approval state.
		connection, ok := t.manager.connection(t.serverName)
		if !ok {
			return nil
		}
		return connection.session
	}
	t.sessionMu.RLock()
	defer t.sessionMu.RUnlock()
	return t.session
}

func isRetryableMCPError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mcp.ErrConnectionClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"connection closed", "broken pipe", "unexpected eof", "eof", "transport is closed"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func mcpOperationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	// A zero timeout is the documented opt-out used by OMP/BANKA config. Keep
	// the caller's cancellation in that case instead of silently imposing a
	// second deadline.
	if timeout == 0 {
		return parent, func() {}
	}
	if timeout < 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(parent, timeout)
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
	// MCP names are opaque UTF-8 strings, while model tool names need a stable
	// conservative identifier. OMP-compatible clients normalize components to
	// lowercase so `Search` and `search` do not produce surprising aliases.
	value = strings.ToLower(strings.Trim(invalidToolNameCharacter.ReplaceAllString(value, "_"), "_"))
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
		if content == nil {
			continue
		}
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
		joined = truncateUTF8(joined, maxMCPResultBytes) + "\n\n[truncated]"
	}
	return joined, nil
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
