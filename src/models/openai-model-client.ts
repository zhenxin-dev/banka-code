/**
 * OpenAI 模型客户端。
 *
 * @author 真心
 */

import { ModelResponseError } from "../errors/banka-error.ts";
import type { AssistantMessage, ConversationMessage, ToolCall } from "../messages/message.ts";
import { isRecord } from "../shared/is-record.ts";
import type { ToolDefinition } from "../tools/tool.ts";
import type { ModelClient, ModelCompletionRequest } from "./model-client.ts";

/**
 * OpenAI 网关配置。
 */
export interface OpenAIClientConfig {
  readonly apiKey: string;
  readonly baseUrl: string;
  readonly model: string;
  readonly timeoutMs: number;
}

const DEFAULT_OPENAI_TIMEOUT_MS = 60_000;

interface OpenAIRequestTool {
  readonly type: "function";
  readonly function: {
    readonly name: string;
    readonly description: string;
    readonly parameters: ToolDefinition["inputSchema"];
  };
}

interface OpenAIRequestToolCall {
  readonly id: string;
  readonly type: "function";
  readonly function: {
    readonly name: string;
    readonly arguments: string;
  };
}

interface OpenAISystemMessage {
  readonly role: "system";
  readonly content: string;
}

interface OpenAIUserMessage {
  readonly role: "user";
  readonly content: string;
}

interface OpenAIAssistantRequestMessage {
  readonly role: "assistant";
  readonly content: string | null;
  readonly tool_calls?: readonly OpenAIRequestToolCall[];
}

interface OpenAIToolMessage {
  readonly role: "tool";
  readonly tool_call_id: string;
  readonly content: string;
}

type OpenAIRequestMessage =
  | OpenAISystemMessage
  | OpenAIUserMessage
  | OpenAIAssistantRequestMessage
  | OpenAIToolMessage;

interface OpenAIChatCompletionRequest {
  readonly model: string;
  readonly messages: readonly OpenAIRequestMessage[];
  readonly tools: readonly OpenAIRequestTool[];
  readonly tool_choice: "auto";
}

/**
 * 基于 fetch 的 OpenAI 客户端。
 */
export class OpenAIModelClient implements ModelClient {
  readonly #apiKey: string;
  readonly #baseUrl: string;
  readonly #model: string;
  readonly #timeoutMs: number;

  public constructor(config: OpenAIClientConfig) {
    this.#apiKey = config.apiKey;
    this.#baseUrl = trimTrailingSlash(config.baseUrl);
    this.#model = config.model;
    this.#timeoutMs = config.timeoutMs;
  }

  /**
   * 调用远端网关生成助手消息。
   */
  public async createAssistantMessage(
    request: ModelCompletionRequest
  ): Promise<AssistantMessage> {
    const payload: OpenAIChatCompletionRequest = {
      model: this.#model,
      messages: toOpenAIRequestMessages(request.systemPrompt, request.messages),
      tools: request.tools.map(toOpenAIRequestTool),
      tool_choice: "auto"
    };

    const response = await fetch(`${this.#baseUrl}/chat/completions`, {
      method: "POST",
      signal: AbortSignal.timeout(this.#timeoutMs),
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.#apiKey}`
      },
      body: JSON.stringify(payload)
    });

    if (!response.ok) {
      throw new ModelResponseError(
        `OpenAI request failed with ${response.status}: ${await response.text()}`
      );
    }

    const rawBody: unknown = await response.json();

    return parseAssistantMessage(rawBody);
  }
}

function toOpenAIRequestTool(tool: ToolDefinition): OpenAIRequestTool {
  return {
    type: "function",
    function: {
      name: tool.name,
      description: tool.description,
      parameters: tool.inputSchema
    }
  };
}

function toOpenAIRequestMessages(
  systemPrompt: string,
  messages: readonly ConversationMessage[]
): readonly OpenAIRequestMessage[] {
  const mappedMessages: OpenAIRequestMessage[] = [
    {
      role: "system",
      content: systemPrompt
    }
  ];

  for (const message of messages) {
    switch (message.role) {
      case "user": {
        mappedMessages.push({
          role: "user",
          content: message.content
        });
        break;
      }
      case "assistant": {
        if (message.toolCalls.length > 0) {
          mappedMessages.push({
            role: "assistant",
            content: message.content === "" ? null : message.content,
            tool_calls: message.toolCalls.map(toOpenAIRequestToolCall)
          });
        } else {
          mappedMessages.push({
            role: "assistant",
            content: message.content
          });
        }
        break;
      }
      case "tool": {
        mappedMessages.push({
          role: "tool",
          tool_call_id: message.toolCallId,
          content: message.content
        });
        break;
      }
    }
  }

  return mappedMessages;
}

function toOpenAIRequestToolCall(toolCall: ToolCall): OpenAIRequestToolCall {
  return {
    id: toolCall.id,
    type: "function",
    function: {
      name: toolCall.name,
      arguments: toolCall.argumentsJson
    }
  };
}

function parseAssistantMessage(rawBody: unknown): AssistantMessage {
  const choices = readChoices(rawBody);
  const firstChoice = choices[0];

  if (firstChoice === undefined || !isRecord(firstChoice)) {
    throw new ModelResponseError("OpenAI response did not include a valid first choice.");
  }

  const rawMessage = firstChoice["message"];

  if (!isRecord(rawMessage)) {
    throw new ModelResponseError("OpenAI response did not include a valid message object.");
  }

  const content = readAssistantContent(rawMessage);
  const toolCalls = readAssistantToolCalls(rawMessage);

  return {
    role: "assistant",
    content,
    toolCalls
  };
}

function readChoices(rawBody: unknown): readonly unknown[] {
  if (!isRecord(rawBody)) {
    throw new ModelResponseError("OpenAI response body must be a JSON object.");
  }

  const choices = rawBody["choices"];

  if (!Array.isArray(choices)) {
    throw new ModelResponseError("OpenAI response body must include a choices array.");
  }

  return choices;
}

function readAssistantContent(rawMessage: Record<string, unknown>): string {
  const content = rawMessage["content"];

  if (content === null || content === undefined) {
    return "";
  }

  if (typeof content === "string") {
    return content;
  }

  throw new ModelResponseError("Assistant content must be a string or null.");
}

function readAssistantToolCalls(rawMessage: Record<string, unknown>): readonly ToolCall[] {
  const toolCalls = rawMessage["tool_calls"];

  if (toolCalls === undefined) {
    return [];
  }

  if (!Array.isArray(toolCalls)) {
    throw new ModelResponseError("Assistant tool_calls must be an array when present.");
  }

  return toolCalls.map(parseToolCall);
}

function parseToolCall(rawToolCall: unknown): ToolCall {
  if (!isRecord(rawToolCall)) {
    throw new ModelResponseError("Tool call entry must be an object.");
  }

  const id = rawToolCall["id"];
  const rawFunction = rawToolCall["function"];

  if (typeof id !== "string") {
    throw new ModelResponseError("Tool call id must be a string.");
  }

  if (!isRecord(rawFunction)) {
    throw new ModelResponseError("Tool call function must be an object.");
  }

  const name = rawFunction["name"];
  const argumentsJson = rawFunction["arguments"];

  if (typeof name !== "string" || typeof argumentsJson !== "string") {
    throw new ModelResponseError("Tool call function must include string name and arguments.");
  }

  return {
    id,
    name,
    argumentsJson
  };
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "");
}

/**
 * 返回 openai 默认超时时间。
 */
export function getDefaultOpenAITimeoutMs(): number {
  return DEFAULT_OPENAI_TIMEOUT_MS;
}
