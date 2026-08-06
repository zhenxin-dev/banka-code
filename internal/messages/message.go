// Package messages defines the internal conversation model.
package messages

// ToolCall is a model requested tool invocation.
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// Message is one conversation item.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
	IsError    bool
}

// NewUserMessage creates a user message.
func NewUserMessage(content string) Message {
	return Message{Role: "user", Content: content}
}

// NewAssistantMessage creates an assistant message.
func NewAssistantMessage(content string, toolCalls []ToolCall) Message {
	return Message{Role: "assistant", Content: content, ToolCalls: toolCalls}
}

// NewToolMessage creates a tool result message.
func NewToolMessage(toolCallID string, toolName string, content string, isError bool) Message {
	return Message{
		Role:       "tool",
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Content:    content,
		IsError:    isError,
	}
}
