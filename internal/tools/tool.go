// Package tools contains the tool registry and built-in tool implementations.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zhenxin-dev/banka-code/internal/messages"
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
	// ApprovalDeny rejects the current tool call.
	ApprovalDeny ApprovalDecision = "deny"
)

// ApprovalRequest describes a tool call that cannot run in the default sandbox.
type ApprovalRequest struct {
	ToolName      string
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
