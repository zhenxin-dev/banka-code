/**
 * Anthropic 模型客户端。
 *
 * @author 真心
 */

import { ModelResponseError } from "../errors/banka-error.ts";
import type { AssistantMessage, ConversationMessage, ToolCall } from "../messages/message.ts";
import { isRecord } from "../shared/is-record.ts";
import type { ToolDefinition } from "../tools/tool.ts";
import type { ModelClient, ModelCompletionRequest } from "./model-client.ts";

const ANTHROPIC_VERSION = "2023-06-01";

/**
 * Anthropic 网关配置。
 */
export interface AnthropicClientConfig {
  readonly apiKey: string;
  readonly baseUrl: string;
  readonly model: string;
}

interface AnthropicTextBlock {
  readonly type: "text";
  readonly text: string;
}

interface AnthropicToolUseBlock {
  readonly type: "tool_use";
  readonly id: string;
  readonly name: string;
  readonly input: Record<string, unknown>;
}

interface AnthropicToolResultBlock {
  readonly type: "tool_result";
  readonly tool_use_id: string;
  readonly content: string;
  readonly is_error?: boolean;
}

type AnthropicContentBlock = AnthropicTextBlock | AnthropicToolUseBlock | AnthropicToolResultBlock;

interface AnthropicRequestMessage {
  readonly role: "user" | "assistant";
  readonly content: readonly AnthropicContentBlock[];
}

interface AnthropicRequestTool {
  readonly name: string;
  readonly description: string;
  readonly input_schema: ToolDefinition["inputSchema"];
}

interface AnthropicMessagesRequest {
  readonly model: string;
  readonly system: string;
  readonly max_tokens: number;
  readonly messages: readonly AnthropicRequestMessage[];
  readonly tools: readonly AnthropicRequestTool[];
}

/**
 * 基于 fetch 的 Anthropic 客户端。
 */
export class AnthropicModelClient implements ModelClient {
  readonly #apiKey: string;
  readonly #baseUrl: string;
  readonly #model: string;
  public constructor(config: AnthropicClientConfig) {
    this.#apiKey = config.apiKey;
    this.#baseUrl = trimTrailingSlash(config.baseUrl);
    this.#model = config.model;
  }

  /**
   * 调用远端 Anthropic 网关生成助手消息。
   */
  public async createAssistantMessage(
    request: ModelCompletionRequest
  ): Promise<AssistantMessage> {
    const payload: AnthropicMessagesRequest = {
      model: this.#model,
      system: request.systemPrompt,
      max_tokens: 4_096,
      messages: toAnthropicMessages(request.messages),
      tools: request.tools.map(toAnthropicTool)
    };

    const response = await fetch(`${this.#baseUrl}/messages`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": this.#apiKey,
        "anthropic-version": ANTHROPIC_VERSION
      },
      body: JSON.stringify(payload)
    });

    if (!response.ok) {
      throw new ModelResponseError(
        `Anthropic request failed with ${response.status}: ${await response.text()}`
      );
    }

    const rawBody: unknown = await response.json();
    return parseAssistantMessage(rawBody);
  }
}

function toAnthropicTool(tool: ToolDefinition): AnthropicRequestTool {
  return {
    name: tool.name,
    description: tool.description,
    input_schema: tool.inputSchema
  };
}

function toAnthropicMessages(messages: readonly ConversationMessage[]): readonly AnthropicRequestMessage[] {
  const mappedMessages: AnthropicRequestMessage[] = [];

  for (const message of messages) {
    switch (message.role) {
      case "user": {
        mappedMessages.push({
          role: "user",
          content: [
            {
              type: "text",
              text: message.content
            }
          ]
        });
        break;
      }
      case "assistant": {
        const content: AnthropicContentBlock[] = [];

        if (message.content !== "") {
          content.push({
            type: "text",
            text: message.content
          });
        }

        for (const toolCall of message.toolCalls) {
          content.push({
            type: "tool_use",
            id: toolCall.id,
            name: toolCall.name,
            input: parseToolInput(toolCall)
          });
        }

        mappedMessages.push({
          role: "assistant",
          content
        });
        break;
      }
      case "tool": {
        const toolResultBlock: AnthropicToolResultBlock = message.isError
          ? {
              type: "tool_result",
              tool_use_id: message.toolCallId,
              content: message.content,
              is_error: true
            }
          : {
              type: "tool_result",
              tool_use_id: message.toolCallId,
              content: message.content
            };

        mappedMessages.push({
          role: "user",
          content: [toolResultBlock]
        });
        break;
      }
    }
  }

  return mappedMessages;
}

function parseToolInput(toolCall: ToolCall): Record<string, unknown> {
  let parsedValue: unknown;

  try {
    parsedValue = JSON.parse(toolCall.argumentsJson);
  } catch (error) {
    throw new ModelResponseError("Anthropic tool input must be valid JSON.", { cause: error });
  }

  if (!isRecord(parsedValue)) {
    throw new ModelResponseError("Anthropic tool input must decode to a JSON object.");
  }

  return parsedValue;
}

function parseAssistantMessage(rawBody: unknown): AssistantMessage {
  if (!isRecord(rawBody)) {
    throw new ModelResponseError("Anthropic response body must be a JSON object.");
  }

  const rawContent = rawBody["content"];

  if (!Array.isArray(rawContent)) {
    throw new ModelResponseError("Anthropic response body must include a content array.");
  }

  const textParts: string[] = [];
  const toolCalls: ToolCall[] = [];

  for (const rawBlock of rawContent) {
    if (!isRecord(rawBlock)) {
      throw new ModelResponseError("Anthropic content block must be an object.");
    }

    const blockType = rawBlock["type"];

    if (blockType === "text") {
      const text = rawBlock["text"];

      if (typeof text !== "string") {
        throw new ModelResponseError("Anthropic text block must include a string text field.");
      }

      textParts.push(text);
      continue;
    }

    if (blockType === "tool_use") {
      toolCalls.push(parseToolUseBlock(rawBlock));
    }
  }

  return {
    role: "assistant",
    content: textParts.join("\n"),
    toolCalls
  };
}

function parseToolUseBlock(rawBlock: Record<string, unknown>): ToolCall {
  const id = rawBlock["id"];
  const name = rawBlock["name"];
  const input = rawBlock["input"];

  if (typeof id !== "string" || typeof name !== "string") {
    throw new ModelResponseError("Anthropic tool_use block must include string id and name.");
  }

  if (!isRecord(input)) {
    throw new ModelResponseError("Anthropic tool_use block must include an object input field.");
  }

  return {
    id,
    name,
    argumentsJson: JSON.stringify(input)
  };
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "");
}
