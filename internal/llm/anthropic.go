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

const anthropicVersion = "2023-06-01"
const defaultAnthropicMaxTokens = 4096

// AnthropicClient calls Anthropic Messages API.
type AnthropicClient struct {
	model      string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewAnthropicClient creates an Anthropic Messages API client.
func NewAnthropicClient(runtimeConfig config.RuntimeConfig) *AnthropicClient {
	baseURL := strings.TrimRight(runtimeConfig.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	return &AnthropicClient{
		model:   runtimeConfig.Model,
		apiKey:  runtimeConfig.APIKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Generate sends one streaming Messages API request.
func (c *AnthropicClient) Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error) {
	body := anthropicRequest{
		Model:     c.model,
		MaxTokens: defaultAnthropicMaxTokens,
		System:    request.SystemPrompt,
		Messages:  toAnthropicMessages(request.Messages),
		Tools:     toAnthropicTools(request.Tools),
		Stream:    true,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return GenerateResponse{}, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return GenerateResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("x-api-key", c.apiKey)
	httpRequest.Header.Set("anthropic-version", anthropicVersion)

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
		return decodeAnthropicResponse(httpResponse.Body, request.OnTextDelta)
	}

	return decodeAnthropicStream(ctx, httpResponse.Body, request.OnTextDelta)
}

func decodeAnthropicResponse(reader io.Reader, onTextDelta func(string)) (GenerateResponse, error) {
	var decoded anthropicResponse
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		return GenerateResponse{}, err
	}

	var text strings.Builder
	toolCalls := make([]messages.ToolCall, 0)
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
			if onTextDelta != nil && block.Text != "" {
				onTextDelta(block.Text)
			}
		case "tool_use":
			arguments, err := json.Marshal(block.Input)
			if err != nil {
				return GenerateResponse{}, err
			}
			toolCalls = append(toolCalls, messages.ToolCall{
				ID:            block.ID,
				Name:          block.Name,
				ArgumentsJSON: string(arguments),
			})
		}
	}

	return GenerateResponse{Text: text.String(), ToolCalls: toolCalls}, nil
}

func decodeAnthropicStream(ctx context.Context, reader io.Reader, onTextDelta func(string)) (GenerateResponse, error) {
	var text strings.Builder
	toolCalls := make(map[int]*anthropicStreamToolCall)
	maxToolIndex := -1

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return GenerateResponse{}, fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		switch event.Type {
		case "error":
			return GenerateResponse{}, fmt.Errorf("model stream failed: %s", event.Error.Message)
		case "content_block_start":
			if event.ContentBlock.Type == "text" && event.ContentBlock.Text != "" {
				text.WriteString(event.ContentBlock.Text)
				if onTextDelta != nil {
					onTextDelta(event.ContentBlock.Text)
				}
			}
			if event.ContentBlock.Type == "tool_use" {
				call := &anthropicStreamToolCall{ID: event.ContentBlock.ID, Name: event.ContentBlock.Name}
				if len(event.ContentBlock.Input) > 0 && string(event.ContentBlock.Input) != "{}" {
					call.InitialInput = append(json.RawMessage(nil), event.ContentBlock.Input...)
				}
				toolCalls[event.Index] = call
				if event.Index > maxToolIndex {
					maxToolIndex = event.Index
				}
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				text.WriteString(event.Delta.Text)
				if onTextDelta != nil {
					onTextDelta(event.Delta.Text)
				}
			}
			if event.Delta.Type == "input_json_delta" {
				call := toolCalls[event.Index]
				if call == nil {
					call = &anthropicStreamToolCall{}
					toolCalls[event.Index] = call
				}
				call.PartialJSON.WriteString(event.Delta.PartialJSON)
				if event.Index > maxToolIndex {
					maxToolIndex = event.Index
				}
			}
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
		arguments := call.PartialJSON.String()
		if arguments == "" {
			arguments = string(call.InitialInput)
		}
		if arguments == "" {
			arguments = "{}"
		}
		resultCalls = append(resultCalls, messages.ToolCall{
			ID:            call.ID,
			Name:          call.Name,
			ArgumentsJSON: arguments,
		})
	}
	return GenerateResponse{Text: text.String(), ToolCalls: resultCalls}, nil
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema tools.JSONSchema `json:"input_schema"`
}

type anthropicResponse struct {
	Content []anthropicContent `json:"content"`
}

type anthropicStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicStreamToolCall struct {
	ID           string
	Name         string
	InitialInput json.RawMessage
	PartialJSON  strings.Builder
}

func toAnthropicMessages(internalMessages []messages.Message) []anthropicMessage {
	result := make([]anthropicMessage, 0, len(internalMessages))
	for _, message := range internalMessages {
		switch message.Role {
		case "user":
			result = append(result, anthropicMessage{
				Role: "user",
				Content: []anthropicContent{{
					Type: "text",
					Text: message.Content,
				}},
			})
		case "assistant":
			content := make([]anthropicContent, 0, 1+len(message.ToolCalls))
			if message.Content != "" {
				content = append(content, anthropicContent{Type: "text", Text: message.Content})
			}
			for _, toolCall := range message.ToolCalls {
				var input map[string]any
				if err := json.Unmarshal([]byte(toolCall.ArgumentsJSON), &input); err != nil {
					input = map[string]any{}
				}
				content = append(content, anthropicContent{
					Type:  "tool_use",
					ID:    toolCall.ID,
					Name:  toolCall.Name,
					Input: input,
				})
			}
			result = append(result, anthropicMessage{Role: "assistant", Content: content})
		case "tool":
			content := anthropicContent{
				Type:      "tool_result",
				ToolUseID: message.ToolCallID,
				Content:   message.Content,
				IsError:   message.IsError,
			}
			if len(result) > 0 && isAnthropicToolResultMessage(result[len(result)-1]) {
				result[len(result)-1].Content = append(result[len(result)-1].Content, content)
			} else {
				result = append(result, anthropicMessage{Role: "user", Content: []anthropicContent{content}})
			}
		}
	}
	return result
}

func isAnthropicToolResultMessage(message anthropicMessage) bool {
	return message.Role == "user" && len(message.Content) > 0 && message.Content[0].Type == "tool_result"
}

func toAnthropicTools(definitions []tools.Definition) []anthropicTool {
	result := make([]anthropicTool, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, anthropicTool{
			Name:        definition.Name(),
			Description: definition.Description(),
			InputSchema: definition.InputSchema(),
		})
	}
	return result
}
