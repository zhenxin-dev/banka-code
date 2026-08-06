package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/messages"
)

func TestNewClientPreservesProviderProtocolRouting(t *testing.T) {
	base := config.RuntimeConfig{Model: "model", APIKey: "key"}

	responsesClient, err := NewClient(config.RuntimeConfig{Provider: config.ProviderOpenAI, Model: base.Model, APIKey: base.APIKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := responsesClient.(*OpenAIResponsesClient); !ok {
		t.Fatalf("openai provider created %T, want *OpenAIResponsesClient", responsesClient)
	}

	chatClient, err := NewClient(config.RuntimeConfig{Provider: config.ProviderOpenAIChat, Model: base.Model, APIKey: base.APIKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := chatClient.(*OpenAIChatClient); !ok {
		t.Fatalf("openai-chat provider created %T, want *OpenAIChatClient", chatClient)
	}
}

func TestToOpenAIMessagesIncludesToolCallsAndResults(t *testing.T) {
	converted := toOpenAIMessages("system", []messages.Message{
		messages.NewUserMessage("hi"),
		messages.NewAssistantMessage("", []messages.ToolCall{{
			ID:            "call_1",
			Name:          "Read",
			ArgumentsJSON: `{"path":"README.md"}`,
		}}),
		messages.NewToolMessage("call_1", "Read", "content", false),
	})

	if len(converted) != 4 {
		t.Fatalf("got %d messages, want 4", len(converted))
	}
	if converted[2].ToolCalls[0].Function.Name != "Read" {
		t.Fatalf("tool call was not converted: %#v", converted[2].ToolCalls)
	}
	if converted[3].ToolCallID != "call_1" || converted[3].Content != "content" {
		t.Fatalf("tool result was not converted: %#v", converted[3])
	}
}

func TestDecodeOpenAIStreamCollectsTextAndToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello "}}]}`,
		`data: {"choices":[{"delta":{"content":"world","tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]}}]}`,
		`data: [DONE]`,
	}, "\n\n")
	var deltas []string

	result, err := decodeOpenAIStream(context.Background(), strings.NewReader(stream), func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("decodeOpenAIStream returned error: %v", err)
	}
	if result.Text != "Hello world" {
		t.Fatalf("got text %q, want %q", result.Text, "Hello world")
	}
	if !reflect.DeepEqual(deltas, []string{"Hello ", "world"}) {
		t.Fatalf("unexpected deltas: %#v", deltas)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ArgumentsJSON != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool calls: %#v", result.ToolCalls)
	}
}

func TestDecodeAnthropicStreamCollectsTextAndToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`,
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README.md\"}"}}`,
		"event: message_stop\n" + `data: {"type":"message_stop"}`,
	}, "\n\n")
	var deltas []string

	result, err := decodeAnthropicStream(context.Background(), strings.NewReader(stream), func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("decodeAnthropicStream returned error: %v", err)
	}
	if result.Text != "Hello" || !reflect.DeepEqual(deltas, []string{"Hello"}) {
		t.Fatalf("unexpected streamed text: %#v, deltas %#v", result, deltas)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "toolu_1" || result.ToolCalls[0].ArgumentsJSON != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool calls: %#v", result.ToolCalls)
	}
}

func TestOpenAIResponsesInputIncludesFunctionCallsAndOutputs(t *testing.T) {
	converted := toOpenAIResponseInput([]messages.Message{
		messages.NewUserMessage("hi"),
		messages.NewAssistantMessage("checking", []messages.ToolCall{{
			ID: "call_1", Name: "Read", ArgumentsJSON: `{"path":"README.md"}`,
		}}),
		messages.NewToolMessage("call_1", "Read", "content", false),
	})
	if len(converted) != 4 {
		t.Fatalf("got %d input items, want 4", len(converted))
	}
	if converted[2].Type != "function_call" || converted[2].CallID != "call_1" || converted[2].Name != "Read" {
		t.Fatalf("unexpected function call input: %#v", converted[2])
	}
	if converted[3].Type != "function_call_output" || converted[3].CallID != "call_1" || converted[3].Output != "content" {
		t.Fatalf("unexpected function output input: %#v", converted[3])
	}
}

func TestDecodeOpenAIResponsesStreamCollectsTextAndToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"Hello "}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"world"}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"README.md\"}"}`,
		`data: {"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"README.md\"}"}`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
	}, "\n\n")
	var deltas []string

	result, err := decodeOpenAIResponsesStream(context.Background(), strings.NewReader(stream), func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("decodeOpenAIResponsesStream returned error: %v", err)
	}
	if result.Text != "Hello world" || !reflect.DeepEqual(deltas, []string{"Hello ", "world"}) {
		t.Fatalf("unexpected streamed text: %#v, deltas %#v", result, deltas)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call_1" || result.ToolCalls[0].Name != "Read" || result.ToolCalls[0].ArgumentsJSON != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool calls: %#v", result.ToolCalls)
	}
}

func TestProviderClientsUseExpectedStreamingEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		response   string
		newClient  func(config.RuntimeConfig) Client
		checkHeads func(*testing.T, http.Header)
	}{
		{
			name: "responses", path: "/responses",
			response:  `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n",
			newClient: func(runtimeConfig config.RuntimeConfig) Client { return NewOpenAIResponsesClient(runtimeConfig) },
			checkHeads: func(t *testing.T, header http.Header) {
				if header.Get("Authorization") != "Bearer key" {
					t.Fatalf("unexpected authorization header: %q", header.Get("Authorization"))
				}
			},
		},
		{
			name: "chat", path: "/chat/completions",
			response:  `data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n" + `data: [DONE]` + "\n\n",
			newClient: func(runtimeConfig config.RuntimeConfig) Client { return NewOpenAIChatClient(runtimeConfig) },
			checkHeads: func(t *testing.T, header http.Header) {
				if header.Get("Authorization") != "Bearer key" {
					t.Fatalf("unexpected authorization header: %q", header.Get("Authorization"))
				}
			},
		},
		{
			name: "anthropic", path: "/messages",
			response:  "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n",
			newClient: func(runtimeConfig config.RuntimeConfig) Client { return NewAnthropicClient(runtimeConfig) },
			checkHeads: func(t *testing.T, header http.Header) {
				if header.Get("x-api-key") != "key" || header.Get("anthropic-version") != anthropicVersion {
					t.Fatalf("unexpected Anthropic headers: %#v", header)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.path {
					t.Errorf("got path %q, want %q", request.URL.Path, test.path)
				}
				test.checkHeads(t, request.Header)
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if body["stream"] != true {
					t.Errorf("request did not enable streaming: %#v", body)
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()

			client := test.newClient(config.RuntimeConfig{Model: "model", APIKey: "key", BaseURL: server.URL})
			result, err := client.Generate(context.Background(), GenerateRequest{Messages: []messages.Message{messages.NewUserMessage("hello")}})
			if err != nil {
				t.Fatalf("Generate returned error: %v", err)
			}
			if result.Text != "ok" {
				t.Fatalf("got text %q, want ok", result.Text)
			}
		})
	}
}

func TestToAnthropicMessagesUsesToolBlocks(t *testing.T) {
	converted := toAnthropicMessages([]messages.Message{
		messages.NewUserMessage("hi"),
		messages.NewAssistantMessage("", []messages.ToolCall{{
			ID:            "toolu_1",
			Name:          "Read",
			ArgumentsJSON: `{"path":"README.md"}`,
		}}),
		messages.NewToolMessage("toolu_1", "Read", "content", false),
	})

	if len(converted) != 3 {
		t.Fatalf("got %d messages, want 3", len(converted))
	}
	toolUse := converted[1].Content[0]
	if toolUse.Type != "tool_use" || toolUse.ID != "toolu_1" || toolUse.Name != "Read" {
		t.Fatalf("tool use block was not converted: %#v", toolUse)
	}
	toolResult := converted[2].Content[0]
	if converted[2].Role != "user" || toolResult.Type != "tool_result" || toolResult.ToolUseID != "toolu_1" {
		t.Fatalf("tool result block was not converted: %#v", converted[2])
	}
}

func TestToAnthropicMessagesGroupsParallelToolResults(t *testing.T) {
	converted := toAnthropicMessages([]messages.Message{
		messages.NewAssistantMessage("", []messages.ToolCall{
			{ID: "toolu_1", Name: "Read", ArgumentsJSON: `{"path":"one"}`},
			{ID: "toolu_2", Name: "Read", ArgumentsJSON: `{"path":"two"}`},
		}),
		messages.NewToolMessage("toolu_1", "Read", "one", false),
		messages.NewToolMessage("toolu_2", "Read", "two", true),
	})
	if len(converted) != 2 {
		t.Fatalf("got %d messages, want assistant plus one tool-result turn", len(converted))
	}
	results := converted[1]
	if results.Role != "user" || len(results.Content) != 2 || results.Content[0].ToolUseID != "toolu_1" || results.Content[1].ToolUseID != "toolu_2" || !results.Content[1].IsError {
		t.Fatalf("parallel tool results were not grouped: %#v", results)
	}
}
