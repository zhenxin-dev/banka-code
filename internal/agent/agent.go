// Package agent implements the Banka agent loop.
package agent

import (
	"context"
	"fmt"

	"github.com/zhenxin-dev/banka-code/internal/llm"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

const defaultMaxIterations = 100

// ToolCallObserver observes model requested tool calls.
type ToolCallObserver func(messages.ToolCall)

// ToolResultObserver observes completed tool calls.
type ToolResultObserver func(messages.Message)

// RunOptions configures one agent run.
type RunOptions struct {
	SystemPrompt      string
	InitialUserPrompt string
	PreviousMessages  []messages.Message
	Model             llm.Client
	ToolRegistry      *tools.Registry
	ToolContext       tools.Context
	OnToolCall        ToolCallObserver
	OnToolResult      ToolResultObserver
	OnTextDelta       func(string)
	MaxIterations     int
}

// RunResult is the final agent loop result.
type RunResult struct {
	FinalText  string
	Transcript []messages.Message
	Iterations int
}

// Run runs Banka until the model stops requesting tools.
func Run(ctx context.Context, options RunOptions) (RunResult, error) {
	conversation := append([]messages.Message(nil), options.PreviousMessages...)
	conversation = append(conversation, messages.NewUserMessage(options.InitialUserPrompt))

	iterations := 0
	maxIterations := options.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}
	for {
		if iterations >= maxIterations {
			return RunResult{Transcript: conversation, Iterations: iterations},
				fmt.Errorf("agent exceeded the maximum of %d model iterations", maxIterations)
		}
		response, err := options.Model.Generate(ctx, llm.GenerateRequest{
			SystemPrompt: options.SystemPrompt,
			Messages:     conversation,
			Tools:        options.ToolRegistry.List(),
			OnTextDelta:  options.OnTextDelta,
		})
		if err != nil {
			return RunResult{}, err
		}

		assistantMessage := messages.NewAssistantMessage(response.Text, response.ToolCalls)
		conversation = append(conversation, assistantMessage)
		iterations++

		if len(response.ToolCalls) == 0 {
			return RunResult{FinalText: response.Text, Transcript: conversation, Iterations: iterations}, nil
		}

		for _, toolCall := range response.ToolCalls {
			if options.OnToolCall != nil {
				options.OnToolCall(toolCall)
			}
			toolResult := options.ToolRegistry.Execute(ctx, toolCall, options.ToolContext)
			conversation = append(conversation, toolResult)
			if options.OnToolResult != nil {
				options.OnToolResult(toolResult)
			}
		}
	}
}
