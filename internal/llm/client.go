// Package llm contains language model client implementations.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

// Client is a chat model that can request tool calls.
type Client interface {
	Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error)
}

// GenerateRequest contains one model turn.
type GenerateRequest struct {
	SystemPrompt string
	Messages     []messages.Message
	Tools        []tools.Definition
	OnTextDelta  func(string)
}

// GenerateResponse is one assistant turn.
type GenerateResponse struct {
	Text      string
	ToolCalls []messages.ToolCall
}

// NewClient creates a configured language model client.
func NewClient(runtimeConfig config.RuntimeConfig) (Client, error) {
	switch runtimeConfig.Provider {
	case config.ProviderOpenAI:
		return NewOpenAIResponsesClient(runtimeConfig), nil
	case config.ProviderOpenAIChat:
		return NewOpenAIChatClient(runtimeConfig), nil
	case config.ProviderAnthropic:
		return NewAnthropicClient(runtimeConfig), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", runtimeConfig.Provider)
	}
}

// OpenAIChatClient calls OpenAI Chat Completions compatible APIs.
type OpenAIChatClient struct {
	model      string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIChatClient creates an OpenAI Chat Completions compatible client.
func NewOpenAIChatClient(runtimeConfig config.RuntimeConfig) *OpenAIChatClient {
	baseURL := strings.TrimRight(runtimeConfig.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIChatClient{
		model:   runtimeConfig.Model,
		apiKey:  runtimeConfig.APIKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Generate sends one streaming chat completion request.
func (c *OpenAIChatClient) Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error) {
	body := openAIChatRequest{
		Model:    c.model,
		Messages: toOpenAIMessages(request.SystemPrompt, request.Messages),
		Tools:    toOpenAITools(request.Tools),
		Stream:   true,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return GenerateResponse{}, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return GenerateResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return GenerateResponse{}, err
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		responseBody, err := io.ReadAll(httpResponse.Body)
		if err != nil {
			return GenerateResponse{}, err
		}
		return GenerateResponse{}, fmt.Errorf("model request failed: %s: %s", httpResponse.Status, string(responseBody))
	}

	if !strings.Contains(httpResponse.Header.Get("Content-Type"), "text/event-stream") {
		return decodeOpenAIResponse(httpResponse.Body, request.OnTextDelta)
	}

	return decodeOpenAIStream(ctx, httpResponse.Body, request.OnTextDelta)
}

func decodeOpenAIResponse(reader io.Reader, onTextDelta func(string)) (GenerateResponse, error) {
	var decoded openAIChatResponse
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		return GenerateResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return GenerateResponse{}, fmt.Errorf("model response contains no choices")
	}

	message := decoded.Choices[0].Message
	toolCalls := make([]messages.ToolCall, 0, len(message.ToolCalls))
	for _, toolCall := range message.ToolCalls {
		if toolCall.Type != "function" {
			continue
		}
		toolCalls = append(toolCalls, messages.ToolCall{
			ID:            toolCall.ID,
			Name:          toolCall.Function.Name,
			ArgumentsJSON: toolCall.Function.Arguments,
		})
	}

	if onTextDelta != nil && message.Content != "" {
		onTextDelta(message.Content)
	}
	return GenerateResponse{Text: message.Content, ToolCalls: toolCalls}, nil
}

func decodeOpenAIStream(ctx context.Context, reader io.Reader, onTextDelta func(string)) (GenerateResponse, error) {
	var text strings.Builder
	toolCalls := make(map[int]*openAIStreamToolCall)
	maxToolIndex := -1

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var event openAIStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return GenerateResponse{}, fmt.Errorf("decode OpenAI stream event: %w", err)
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			return GenerateResponse{}, fmt.Errorf("model stream failed: %s", string(event.Error))
		}
		if len(event.Choices) == 0 {
			continue
		}

		delta := event.Choices[0].Delta
		if delta.Content != "" {
			text.WriteString(delta.Content)
			if onTextDelta != nil {
				onTextDelta(delta.Content)
			}
		}
		for _, chunk := range delta.ToolCalls {
			call := toolCalls[chunk.Index]
			if call == nil {
				call = &openAIStreamToolCall{}
				toolCalls[chunk.Index] = call
			}
			if chunk.Index > maxToolIndex {
				maxToolIndex = chunk.Index
			}
			if chunk.ID != "" {
				call.ID = chunk.ID
			}
			if chunk.Function.Name != "" {
				call.Name = chunk.Function.Name
			}
			call.Arguments.WriteString(chunk.Function.Arguments)
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return GenerateResponse{}, ctx.Err()
		}
		return GenerateResponse{}, err
	}

	resultCalls := make([]messages.ToolCall, 0, len(toolCalls))
	for index := 0; index <= maxToolIndex; index++ {
		call := toolCalls[index]
		if call == nil {
			continue
		}
		resultCalls = append(resultCalls, messages.ToolCall{
			ID:            call.ID,
			Name:          call.Name,
			ArgumentsJSON: call.Arguments.String(),
		})
	}
	return GenerateResponse{Text: text.String(), ToolCalls: resultCalls}, nil
}

type openAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Tools    []openAITool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  tools.JSONSchema `json:"parameters"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
}

type openAIStreamEvent struct {
	Choices []struct {
		Delta struct {
			Content   string                      `json:"content"`
			ToolCalls []openAIStreamToolCallChunk `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

type openAIStreamToolCallChunk struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIStreamToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

func toOpenAIMessages(systemPrompt string, internalMessages []messages.Message) []openAIMessage {
	result := []openAIMessage{{Role: "system", Content: systemPrompt}}
	for _, message := range internalMessages {
		switch message.Role {
		case "user":
			result = append(result, openAIMessage{Role: "user", Content: message.Content})
		case "assistant":
			result = append(result, openAIMessage{
				Role:      "assistant",
				Content:   message.Content,
				ToolCalls: toOpenAIToolCalls(message.ToolCalls),
			})
		case "tool":
			result = append(result, openAIMessage{
				Role:       "tool",
				ToolCallID: message.ToolCallID,
				Content:    message.Content,
			})
		}
	}
	return result
}

func toOpenAITools(definitions []tools.Definition) []openAITool {
	result := make([]openAITool, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        definition.Name(),
				Description: definition.Description(),
				Parameters:  definition.InputSchema(),
			},
		})
	}
	return result
}

func toOpenAIToolCalls(toolCalls []messages.ToolCall) []openAIToolCall {
	result := make([]openAIToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		result = append(result, openAIToolCall{
			ID:   toolCall.ID,
			Type: "function",
			Function: openAIToolCallFunction{
				Name:      toolCall.Name,
				Arguments: toolCall.ArgumentsJSON,
			},
		})
	}
	return result
}
