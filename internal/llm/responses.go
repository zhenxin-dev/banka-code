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

// OpenAIResponsesClient calls the OpenAI Responses API.
type OpenAIResponsesClient struct {
	model      string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIResponsesClient creates an OpenAI Responses API client.
func NewOpenAIResponsesClient(runtimeConfig config.RuntimeConfig) *OpenAIResponsesClient {
	baseURL := strings.TrimRight(runtimeConfig.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIResponsesClient{
		model:   runtimeConfig.Model,
		apiKey:  runtimeConfig.APIKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Generate sends one streaming Responses API request.
func (c *OpenAIResponsesClient) Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error) {
	body := openAIResponsesRequest{
		Model:        c.model,
		Instructions: request.SystemPrompt,
		Input:        toOpenAIResponseInput(request.Messages),
		Tools:        toOpenAIResponseTools(request.Tools),
		Stream:       true,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return GenerateResponse{}, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(payload))
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
		responseBody, readErr := io.ReadAll(httpResponse.Body)
		if readErr != nil {
			return GenerateResponse{}, readErr
		}
		return GenerateResponse{}, fmt.Errorf("model request failed: %s: %s", httpResponse.Status, string(responseBody))
	}
	if !strings.Contains(httpResponse.Header.Get("Content-Type"), "text/event-stream") {
		return decodeOpenAIResponsesResponse(httpResponse.Body, request.OnTextDelta)
	}
	return decodeOpenAIResponsesStream(ctx, httpResponse.Body, request.OnTextDelta)
}

type openAIResponsesRequest struct {
	Model        string                    `json:"model"`
	Instructions string                    `json:"instructions,omitempty"`
	Input        []openAIResponseInputItem `json:"input"`
	Tools        []openAIResponseTool      `json:"tools,omitempty"`
	Stream       bool                      `json:"stream,omitempty"`
}

type openAIResponseInputItem struct {
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type openAIResponseTool struct {
	Type        string           `json:"type"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  tools.JSONSchema `json:"parameters"`
}

type openAIResponsesResponse struct {
	Output []openAIResponseOutputItem `json:"output"`
}

type openAIResponseOutputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type openAIResponsesStreamEvent struct {
	Type        string                   `json:"type"`
	OutputIndex int                      `json:"output_index"`
	Delta       string                   `json:"delta"`
	Arguments   string                   `json:"arguments"`
	Item        openAIResponseOutputItem `json:"item"`
	Error       struct {
		Message string `json:"message"`
	} `json:"error"`
	Response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

type openAIResponseStreamToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

func toOpenAIResponseInput(internalMessages []messages.Message) []openAIResponseInputItem {
	var result []openAIResponseInputItem
	for _, message := range internalMessages {
		switch message.Role {
		case "user":
			result = append(result, openAIResponseInputItem{Role: "user", Content: message.Content})
		case "assistant":
			if message.Content != "" {
				result = append(result, openAIResponseInputItem{Role: "assistant", Content: message.Content})
			}
			for _, toolCall := range message.ToolCalls {
				result = append(result, openAIResponseInputItem{
					Type:      "function_call",
					CallID:    toolCall.ID,
					Name:      toolCall.Name,
					Arguments: toolCall.ArgumentsJSON,
				})
			}
		case "tool":
			result = append(result, openAIResponseInputItem{
				Type:   "function_call_output",
				CallID: message.ToolCallID,
				Output: message.Content,
			})
		}
	}
	return result
}

func toOpenAIResponseTools(definitions []tools.Definition) []openAIResponseTool {
	result := make([]openAIResponseTool, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, openAIResponseTool{
			Type:        "function",
			Name:        definition.Name(),
			Description: definition.Description(),
			Parameters:  definition.InputSchema(),
		})
	}
	return result
}

func decodeOpenAIResponsesResponse(reader io.Reader, onTextDelta func(string)) (GenerateResponse, error) {
	var decoded openAIResponsesResponse
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		return GenerateResponse{}, err
	}
	var text strings.Builder
	var toolCalls []messages.ToolCall
	for _, item := range decoded.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					text.WriteString(content.Text)
					if onTextDelta != nil && content.Text != "" {
						onTextDelta(content.Text)
					}
				}
			}
		case "function_call":
			toolCalls = append(toolCalls, messages.ToolCall{ID: item.CallID, Name: item.Name, ArgumentsJSON: item.Arguments})
		}
	}
	return GenerateResponse{Text: text.String(), ToolCalls: toolCalls}, nil
}

func decodeOpenAIResponsesStream(ctx context.Context, reader io.Reader, onTextDelta func(string)) (GenerateResponse, error) {
	var text strings.Builder
	toolCalls := make(map[int]*openAIResponseStreamToolCall)
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
		var event openAIResponsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return GenerateResponse{}, fmt.Errorf("decode OpenAI Responses stream event: %w", err)
		}
		switch event.Type {
		case "error":
			return GenerateResponse{}, fmt.Errorf("model stream failed: %s", event.Error.Message)
		case "response.failed":
			return GenerateResponse{}, fmt.Errorf("model stream failed: %s", event.Response.Error.Message)
		case "response.output_text.delta":
			text.WriteString(event.Delta)
			if onTextDelta != nil && event.Delta != "" {
				onTextDelta(event.Delta)
			}
		case "response.output_item.added":
			if event.Item.Type != "function_call" {
				continue
			}
			call := &openAIResponseStreamToolCall{ID: event.Item.CallID, Name: event.Item.Name}
			call.Arguments.WriteString(event.Item.Arguments)
			toolCalls[event.OutputIndex] = call
			if event.OutputIndex > maxToolIndex {
				maxToolIndex = event.OutputIndex
			}
		case "response.function_call_arguments.delta":
			call := toolCalls[event.OutputIndex]
			if call == nil {
				call = &openAIResponseStreamToolCall{}
				toolCalls[event.OutputIndex] = call
			}
			call.Arguments.WriteString(event.Delta)
			if event.OutputIndex > maxToolIndex {
				maxToolIndex = event.OutputIndex
			}
		case "response.function_call_arguments.done":
			call := toolCalls[event.OutputIndex]
			if call != nil && event.Arguments != "" {
				call.Arguments.Reset()
				call.Arguments.WriteString(event.Arguments)
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
		if call != nil {
			resultCalls = append(resultCalls, messages.ToolCall{ID: call.ID, Name: call.Name, ArgumentsJSON: call.Arguments.String()})
		}
	}
	return GenerateResponse{Text: text.String(), ToolCalls: resultCalls}, nil
}
