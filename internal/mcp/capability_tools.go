package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

type capabilityTool struct {
	kind    string
	manager *Manager
}

func newCapabilityTools(manager *Manager) []tools.Definition {
	return []tools.Definition{
		&capabilityTool{kind: "list_resources", manager: manager},
		&capabilityTool{kind: "read_resource", manager: manager},
		&capabilityTool{kind: "list_prompts", manager: manager},
		&capabilityTool{kind: "get_prompt", manager: manager},
	}
}

func (t *capabilityTool) Name() string {
	switch t.kind {
	case "list_resources":
		return "MCPListResources"
	case "read_resource":
		return "MCPReadResource"
	case "list_prompts":
		return "MCPListPrompts"
	default:
		return "MCPGetPrompt"
	}
}

func (t *capabilityTool) Description() string {
	switch t.kind {
	case "list_resources":
		return "List resources and resource templates exposed by connected MCP servers."
	case "read_resource":
		return "Read a resource from a connected MCP server."
	case "list_prompts":
		return "List prompts exposed by connected MCP servers."
	default:
		return "Get a prompt from a connected MCP server with optional string arguments."
	}
}

func (t *capabilityTool) InputSchema() tools.JSONSchema {
	server := map[string]any{
		"type": "string", "description": "Connected MCP server name.", "enum": t.manager.connectedNames(),
	}
	properties := map[string]any{"server": server}
	required := []string{}
	switch t.kind {
	case "read_resource":
		properties["uri"] = map[string]any{"type": "string", "description": "Resource URI returned by MCPListResources."}
		required = []string{"server", "uri"}
	case "get_prompt":
		properties["name"] = map[string]any{"type": "string", "description": "Prompt name returned by MCPListPrompts."}
		properties["arguments"] = map[string]any{
			"type": "object", "description": "Optional prompt arguments.",
			"additionalProperties": map[string]any{"type": "string"},
		}
		required = []string{"server", "name"}
	}
	return tools.JSONSchema{
		"type": "object", "properties": properties, "required": required, "additionalProperties": false,
	}
}

func (t *capabilityTool) Execute(ctx context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch t.kind {
	case "list_resources":
		return t.listResources(ctx, arguments, toolContext)
	case "read_resource":
		return t.readResource(ctx, arguments, toolContext)
	case "list_prompts":
		return t.listPrompts(ctx, arguments, toolContext)
	default:
		return t.getPrompt(ctx, arguments, toolContext)
	}
}

func (t *capabilityTool) listResources(ctx context.Context, arguments map[string]any, contexts ...tools.Context) (tools.Result, error) {
	var toolContext tools.Context
	if len(contexts) > 0 {
		toolContext = contexts[0]
	}
	names, err := t.selectedServers(arguments)
	if err != nil {
		return tools.Result{}, err
	}
	result := make(map[string]any)
	for _, name := range names {
		connection, exists := t.manager.connection(name)
		if !exists || connection.session == nil {
			continue
		}
		if approval, approvalErr := approveMCPAccess(ctx, toolContext, name, connection, "list resources"); approvalErr != nil || approval != nil {
			if approval != nil {
				return *approval, approvalErr
			}
			return tools.Result{}, approvalErr
		}
		operationContext, cancel := mcpOperationContext(ctx, connection.timeout)
		var resources []*mcp.Resource
		for resource, listErr := range connection.session.Resources(operationContext, nil) {
			if listErr != nil {
				if isMCPMethodNotFound(listErr) {
					// Servers are allowed to omit resources capability. Treat the
					// protocol's method-not-found response as an empty collection so
					// one limited server does not hide resources from other servers.
					result[name] = map[string]any{"resources": []*mcp.Resource{}, "resourceTemplates": []*mcp.ResourceTemplate{}}
					break
				}
				cancel()
				return tools.Result{}, listErr
			}
			resources = append(resources, resource)
		}
		var templates []*mcp.ResourceTemplate
		for template, listErr := range connection.session.ResourceTemplates(operationContext, nil) {
			if listErr != nil {
				cancel()
				if isMCPMethodNotFound(listErr) {
					break
				}
				return tools.Result{}, listErr
			}
			templates = append(templates, template)
		}
		cancel()
		result[name] = map[string]any{"resources": resources, "resourceTemplates": templates}
	}
	return jsonResult(result)
}

func (t *capabilityTool) readResource(ctx context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	name, connection, err := t.requiredServer(arguments)
	if err != nil {
		return tools.Result{}, err
	}
	uri, ok := arguments["uri"].(string)
	uri = strings.TrimSpace(uri)
	if !ok || uri == "" {
		return tools.Result{}, errors.New("MCPReadResource requires a non-empty 'uri' string")
	}
	if result, err := approveMCPAccess(ctx, toolContext, name, connection, "read resource "+uri); err != nil || result != nil {
		if result != nil {
			return *result, err
		}
		return tools.Result{}, err
	}
	operationContext, cancel := mcpOperationContext(ctx, connection.timeout)
	defer cancel()
	result, err := connection.session.ReadResource(operationContext, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return tools.Result{}, err
	}
	return jsonResult(result)
}

func (t *capabilityTool) listPrompts(ctx context.Context, arguments map[string]any, contexts ...tools.Context) (tools.Result, error) {
	var toolContext tools.Context
	if len(contexts) > 0 {
		toolContext = contexts[0]
	}
	names, err := t.selectedServers(arguments)
	if err != nil {
		return tools.Result{}, err
	}
	result := make(map[string]any)
	for _, name := range names {
		connection, exists := t.manager.connection(name)
		if !exists || connection.session == nil {
			continue
		}
		if approval, approvalErr := approveMCPAccess(ctx, toolContext, name, connection, "list prompts"); approvalErr != nil || approval != nil {
			if approval != nil {
				return *approval, approvalErr
			}
			return tools.Result{}, approvalErr
		}
		operationContext, cancel := mcpOperationContext(ctx, connection.timeout)
		var prompts []*mcp.Prompt
		for prompt, listErr := range connection.session.Prompts(operationContext, nil) {
			if listErr != nil {
				cancel()
				if isMCPMethodNotFound(listErr) {
					result[name] = []*mcp.Prompt{}
					break
				}
				return tools.Result{}, listErr
			}
			prompts = append(prompts, prompt)
		}
		cancel()
		result[name] = prompts
	}
	return jsonResult(result)
}

func (t *capabilityTool) getPrompt(ctx context.Context, arguments map[string]any, toolContext tools.Context) (tools.Result, error) {
	serverName, connection, err := t.requiredServer(arguments)
	if err != nil {
		return tools.Result{}, err
	}
	name, ok := arguments["name"].(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return tools.Result{}, errors.New("MCPGetPrompt requires a non-empty 'name' string")
	}
	promptArguments, err := stringMap(arguments["arguments"])
	if err != nil {
		return tools.Result{}, err
	}
	if result, err := approveMCPAccess(ctx, toolContext, serverName, connection, "get prompt "+name); err != nil || result != nil {
		if result != nil {
			return *result, err
		}
		return tools.Result{}, err
	}
	operationContext, cancel := mcpOperationContext(ctx, connection.timeout)
	defer cancel()
	result, err := connection.session.GetPrompt(operationContext, &mcp.GetPromptParams{Name: name, Arguments: promptArguments})
	if err != nil {
		return tools.Result{}, err
	}
	return jsonResult(result)
}

func (t *capabilityTool) selectedServers(arguments map[string]any) ([]string, error) {
	value, exists := arguments["server"]
	if !exists {
		return t.manager.connectedNames(), nil
	}
	name, ok := value.(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return nil, errors.New("'server' must be a non-empty string when provided")
	}
	if _, exists := t.manager.connection(name); !exists {
		return nil, fmt.Errorf("unknown connected MCP server: %s", name)
	}
	return []string{name}, nil
}

func (t *capabilityTool) requiredServer(arguments map[string]any) (string, serverConnection, error) {
	names, err := t.selectedServers(arguments)
	if err != nil {
		return "", serverConnection{}, err
	}
	if len(names) != 1 || arguments["server"] == nil {
		return "", serverConnection{}, errors.New("a connected MCP 'server' is required")
	}
	connection, exists := t.manager.connection(names[0])
	if !exists {
		return "", serverConnection{}, fmt.Errorf("unknown connected MCP server: %s", names[0])
	}
	return names[0], connection, nil
}

func approveMCPAccess(ctx context.Context, toolContext tools.Context, serverName string, connection serverConnection, action string) (*tools.Result, error) {
	if connection.trusted {
		return nil, nil
	}
	allowed, err := toolContext.RequestPermission(ctx, tools.ApprovalRequest{
		ToolName: "MCP", Kind: tools.ApprovalExternal, Scope: "mcp:" + serverName,
		Command:       "MCP " + serverName + ": " + action,
		Justification: "访问未标记为受信任的 MCP 服务器",
	})
	if err != nil {
		return nil, err
	}
	if !allowed {
		result := tools.Result{Content: "User denied MCP access.", IsError: true}
		return &result, nil
	}
	return nil, nil
}

func stringMap(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("MCPGetPrompt requires 'arguments' to be an object of string values")
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("MCPGetPrompt requires 'arguments' to contain only string values")
		}
		result[key] = text
	}
	return result, nil
}

func jsonResult(value any) (tools.Result, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return tools.Result{}, err
	}
	content := string(encoded)
	if len(content) > maxMCPResultBytes {
		content = truncateUTF8(content, maxMCPResultBytes) + "\n\n[truncated]"
	}
	return tools.Result{Content: content}, nil
}

func isMCPMethodNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "method not found") || strings.Contains(message, "method not implemented") || strings.Contains(message, "-32601")
}
