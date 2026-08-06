// Package tools contains the tool registry and built-in tool implementations.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/permissions"
)

// JSONSchema is a JSON Schema document used to describe tool input.
// A map is used because MCP servers may expose schemas that are more expressive
// than Banka's built-in tools.
type JSONSchema map[string]any

// ApprovalDecision is the user's response to a privileged tool request.
type ApprovalDecision string

const (
	// ApprovalAllowOnce permits only the current tool call.
	ApprovalAllowOnce ApprovalDecision = "allow_once"
	// ApprovalAllowAlways permits matching requests for the current session.
	ApprovalAllowAlways ApprovalDecision = "allow_always"
	// ApprovalDeny rejects the current tool call.
	ApprovalDeny ApprovalDecision = "deny"
)

// ApprovalKind identifies the boundary a request needs to cross.
type ApprovalKind string

const (
	// ApprovalHost covers host execution and local filesystem access.
	ApprovalHost ApprovalKind = "host"
	// ApprovalNetwork covers direct network access.
	ApprovalNetwork ApprovalKind = "network"
	// ApprovalExternal covers calls to untrusted external tool providers.
	ApprovalExternal ApprovalKind = "external"
)

// ApprovalRequest describes a tool call that cannot run in the default sandbox.
type ApprovalRequest struct {
	ToolName      string
	Kind          ApprovalKind
	Scope         string
	Command       string
	Justification string
}

// QuestionRequest describes a question the agent needs the user to answer.
type QuestionRequest struct {
	Question string
	Options  []string
}

// Interaction handles tool-initiated user interaction.
type Interaction interface {
	RequestApproval(context.Context, ApprovalRequest) (ApprovalDecision, error)
	AskUser(context.Context, QuestionRequest) (string, error)
}

// Context contains tool execution context.
type Context struct {
	WorkspaceRoot string
	Interaction   Interaction
	Permissions   *permissions.Policy
}

// PermissionMode returns the active permission mode.
func (c Context) PermissionMode() permissions.Mode {
	if c.Permissions == nil {
		return permissions.ModeDefault
	}
	return c.Permissions.Mode()
}

// ResolvePath resolves a tool path according to the active permission mode.
func (c Context) ResolvePath(targetPath string) (string, error) {
	if !c.PermissionMode().HasFullAccess() {
		return ResolveSafePath(c.WorkspaceRoot, targetPath)
	}
	candidate := targetPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(c.WorkspaceRoot, candidate)
	}
	return filepath.Abs(candidate)
}

// RequestPermission applies automatic and remembered approvals before prompting.
func (c Context) RequestPermission(ctx context.Context, request ApprovalRequest) (bool, error) {
	mode := c.PermissionMode()
	scope := request.Scope
	if scope == "" {
		scope = request.ToolName
	}
	if mode == permissions.ModeYOLO || (mode == permissions.ModeFullAccess && request.Kind != ApprovalExternal) ||
		(c.Permissions != nil && c.Permissions.Allows(scope)) {
		return true, nil
	}
	if c.Interaction == nil {
		return false, nil
	}
	decision, err := c.Interaction.RequestApproval(ctx, request)
	if err != nil {
		return false, err
	}
	if decision == ApprovalAllowAlways {
		c.Permissions.Allow(scope)
	}
	return decision == ApprovalAllowOnce || decision == ApprovalAllowAlways, nil
}

// Result is a tool execution result.
type Result struct {
	Content string
	IsError bool
}

// Definition is one callable tool.
type Definition interface {
	Name() string
	Description() string
	InputSchema() JSONSchema
	Execute(context.Context, map[string]any, Context) (Result, error)
}

// Registry stores tools by name.
type Registry struct {
	tools map[string]Definition
	order []Definition
}

// NewRegistry creates a registry.
func NewRegistry(definitions []Definition) *Registry {
	registry := &Registry{tools: make(map[string]Definition), order: definitions}
	for _, definition := range definitions {
		registry.tools[definition.Name()] = definition
	}
	return registry
}

// List returns tools in display order.
func (r *Registry) List() []Definition {
	return append([]Definition(nil), r.order...)
}

// Execute runs a tool call and always converts failures into tool result messages.
func (r *Registry) Execute(ctx context.Context, toolCall messages.ToolCall, toolContext Context) messages.Message {
	definition, ok := r.tools[toolCall.Name]
	if !ok {
		return messages.NewToolMessage(toolCall.ID, toolCall.Name, fmt.Sprintf("Unknown tool: %s", toolCall.Name), true)
	}

	var arguments map[string]any
	if err := json.Unmarshal([]byte(toolCall.ArgumentsJSON), &arguments); err != nil {
		return messages.NewToolMessage(toolCall.ID, toolCall.Name, fmt.Sprintf("Invalid tool arguments: %v", err), true)
	}

	result, err := definition.Execute(ctx, arguments, toolContext)
	if err != nil {
		return messages.NewToolMessage(toolCall.ID, toolCall.Name, err.Error(), true)
	}

	return messages.NewToolMessage(toolCall.ID, toolCall.Name, result.Content, result.IsError)
}

// CreateBuiltins creates the standard Banka tool set.
func CreateBuiltins() []Definition {
	return []Definition{
		NewBashTool(),
		NewReadTool(),
		NewWriteTool(),
		NewEditTool(),
		NewGlobTool(),
		NewGrepTool(),
		NewWebFetchTool(),
		NewApplyPatchTool(),
		NewAskUserTool(),
	}
}

func objectSchema(properties map[string]any, required ...string) JSONSchema {
	return JSONSchema{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringSchema(description string) JSONSchema {
	return JSONSchema{"type": "string", "description": description}
}
