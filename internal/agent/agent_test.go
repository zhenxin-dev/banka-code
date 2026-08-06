package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/llm"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestRunExecutesToolsUntilFinalText(t *testing.T) {
	model := &scriptedClient{responses: []llm.GenerateResponse{
		{ToolCalls: []messages.ToolCall{{ID: "call_1", Name: "Echo", ArgumentsJSON: `{"text":"hello"}`}}},
		{Text: "done"},
	}}
	registry := tools.NewRegistry([]tools.Definition{echoTool{}})
	var observed []string
	var observedResults []messages.Message

	result, err := Run(context.Background(), RunOptions{
		SystemPrompt:      "system",
		InitialUserPrompt: "Say hello",
		Model:             model,
		ToolRegistry:      registry,
		ToolContext:       tools.Context{WorkspaceRoot: t.TempDir()},
		OnToolCall: func(call messages.ToolCall) {
			observed = append(observed, call.Name+":"+call.ID)
		},
		OnToolResult: func(result messages.Message) {
			observedResults = append(observedResults, result)
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.FinalText != "done" || result.Iterations != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !reflect.DeepEqual(observed, []string{"Echo:call_1"}) {
		t.Fatalf("unexpected tool notifications: %#v", observed)
	}
	if len(observedResults) != 1 || observedResults[0].Content != "echo: hello" {
		t.Fatalf("unexpected tool result notifications: %#v", observedResults)
	}
	if len(model.requests) != 2 {
		t.Fatalf("got %d model requests, want 2", len(model.requests))
	}
	secondMessages := model.requests[1].Messages
	toolMessage := secondMessages[len(secondMessages)-1]
	if toolMessage.Role != "tool" || toolMessage.Content != "echo: hello" || toolMessage.IsError {
		t.Fatalf("unexpected tool result message: %#v", toolMessage)
	}
}

func TestRunAppendsPreviousMessagesAndStreamsText(t *testing.T) {
	model := &scriptedClient{responses: []llm.GenerateResponse{{Text: "continued"}}}
	var deltas []string
	previous := []messages.Message{
		messages.NewUserMessage("first turn"),
		messages.NewAssistantMessage("first reply", nil),
	}

	_, err := Run(context.Background(), RunOptions{
		SystemPrompt:      "system",
		InitialUserPrompt: "next turn",
		PreviousMessages:  previous,
		Model:             model,
		ToolRegistry:      tools.NewRegistry(nil),
		OnTextDelta: func(delta string) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := model.requests[0].Messages; len(got) != 3 || got[0].Content != "first turn" || got[2].Content != "next turn" {
		t.Fatalf("previous messages were not preserved: %#v", got)
	}
	if !reflect.DeepEqual(deltas, []string{"continued"}) {
		t.Fatalf("unexpected text deltas: %#v", deltas)
	}
}

func TestRunPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	model := &scriptedClient{err: context.Canceled}

	_, err := Run(ctx, RunOptions{
		InitialUserPrompt: "cancel",
		Model:             model,
		ToolRegistry:      tools.NewRegistry(nil),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got error %v, want context.Canceled", err)
	}
}

func TestRunStopsAtMaximumIterations(t *testing.T) {
	model := &scriptedClient{responses: []llm.GenerateResponse{
		{ToolCalls: []messages.ToolCall{{ID: "1", Name: "Echo", ArgumentsJSON: `{"text":"a"}`}}},
		{ToolCalls: []messages.ToolCall{{ID: "2", Name: "Echo", ArgumentsJSON: `{"text":"b"}`}}},
	}}
	result, err := Run(context.Background(), RunOptions{
		InitialUserPrompt: "loop", Model: model,
		ToolRegistry: tools.NewRegistry([]tools.Definition{echoTool{}}), MaxIterations: 2,
	})
	if err == nil || result.Iterations != 2 || len(result.Transcript) != 5 {
		t.Fatalf("unexpected iteration limit result: result=%#v err=%v", result, err)
	}
}

type scriptedClient struct {
	responses []llm.GenerateResponse
	requests  []llm.GenerateRequest
	err       error
}

func (c *scriptedClient) Generate(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	c.requests = append(c.requests, request)
	if c.err != nil {
		return llm.GenerateResponse{}, c.err
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	if request.OnTextDelta != nil && response.Text != "" {
		request.OnTextDelta(response.Text)
	}
	return response, nil
}

type echoTool struct{}

func (echoTool) Name() string        { return "Echo" }
func (echoTool) Description() string { return "Echo a string." }
func (echoTool) InputSchema() tools.JSONSchema {
	return tools.JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "Text to echo."},
		},
		"required": []string{"text"}, "additionalProperties": false,
	}
}
func (echoTool) Execute(_ context.Context, arguments map[string]any, _ tools.Context) (tools.Result, error) {
	text, ok := arguments["text"].(string)
	if !ok {
		return tools.Result{}, errors.New("Echo requires text")
	}
	return tools.Result{Content: "echo: " + text}, nil
}
