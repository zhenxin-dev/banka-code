/**
 * Agent 主循环实现。
 *
 * @author 真心
 */

import { streamText, type ToolSet, type LanguageModel } from "ai";
import { ModelResponseError } from "../errors/banka-error.ts";
import type { ConversationMessage, ToolCall } from "../messages/message.ts";
import { executeToolCall } from "../tools/execute-tool-call.ts";
import type { ToolDefinition, ToolExecutionContext } from "../tools/tool.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";
import type { ModelMessage, AssistantContent } from "@ai-sdk/provider-utils";
import { jsonSchema } from "@ai-sdk/provider-utils";

/**
 * 工具调用观察器。
 */
export interface ToolCallObserver {
  (toolCall: ToolCall): void;
}

export interface TextDeltaObserver {
  (delta: string): void;
}

/**
 * Agent 执行参数。
 */
export interface AgentRunOptions {
  readonly systemPrompt: string;
  readonly initialUserPrompt: string;
  readonly previousMessages?: readonly ConversationMessage[];
  readonly languageModel: LanguageModel;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
  readonly maxIterations: number;
  readonly onToolCall?: ToolCallObserver;
  readonly onTextDelta?: TextDeltaObserver;
}

/**
 * Agent 执行结果。
 */
export interface AgentRunResult {
  readonly finalText: string;
  readonly transcript: readonly ConversationMessage[];
  readonly iterations: number;
}

/**
 * 运行 banka 的主循环，直到模型停止请求工具。
 */
export async function runAgentLoop(options: AgentRunOptions): Promise<AgentRunResult> {
  const messages: ConversationMessage[] = [
    ...(options.previousMessages ?? []),
    {
      role: "user",
      content: options.initialUserPrompt
    }
  ];

  const sdkTools = toSdkTools(options.toolRegistry.list());

  for (let iteration = 0; iteration < options.maxIterations; iteration += 1) {
    const coreMessages = toModelMessages(messages);

    const stream = streamText({
      model: options.languageModel,
      system: options.systemPrompt,
      messages: coreMessages,
      tools: sdkTools
    });

    let accumulatedText = "";
    const collectedToolCalls: Array<{ toolCallId: string; toolName: string; input: unknown }> = [];

    for await (const part of stream.fullStream) {
      switch (part.type) {
        case "text-delta":
          accumulatedText += part.text;
          options.onTextDelta?.(part.text);
          break;
        case "error":
          throw new ModelResponseError(String(part.error));
        case "tool-call":
          collectedToolCalls.push({
            toolCallId: part.toolCallId,
            toolName: part.toolName,
            input: part.input
          });
          break;
      }
    }

    const toolCallsFromSdk = collectedToolCalls.map(
      (tc): ToolCall => ({
        id: tc.toolCallId,
        name: tc.toolName,
        argumentsJson: JSON.stringify(tc.input)
      })
    );

    const assistantMessage: ConversationMessage = {
      role: "assistant",
      content: accumulatedText,
      toolCalls: toolCallsFromSdk
    };

    messages.push(assistantMessage);

    if (toolCallsFromSdk.length === 0) {
      return {
        finalText: accumulatedText,
        transcript: [...messages],
        iterations: iteration + 1
      };
    }

    for (const toolCall of toolCallsFromSdk) {
      options.onToolCall?.(toolCall);
      const toolResult = await executeToolCall(toolCall, options.toolRegistry, options.toolContext);
      messages.push(toolResult);
    }
  }

  throw new ModelResponseError(
    `Agent exceeded the maximum iteration limit (${options.maxIterations}).`
  );
}

function toModelMessages(messages: readonly ConversationMessage[]): ModelMessage[] {
  return messages.map((msg): ModelMessage => {
    switch (msg.role) {
      case "user":
        return { role: "user", content: msg.content };
      case "assistant": {
        const content: AssistantContent = [
          ...(msg.content !== "" ? [{ type: "text" as const, text: msg.content }] : []),
          ...msg.toolCalls.map(tc => ({
            type: "tool-call" as const,
            toolCallId: tc.id,
            toolName: tc.name,
            input: JSON.parse(tc.argumentsJson)
          }))
        ];
        return { role: "assistant", content };
      }
      case "tool":
        return {
          role: "tool",
          content: [
            {
              type: "tool-result",
              toolCallId: msg.toolCallId,
              toolName: msg.toolName,
              output: msg.isError
                ? { type: "error-text" as const, value: msg.content }
                : { type: "text" as const, value: msg.content }
            }
          ]
        };
    }
  });
}

function toSdkTools(tools: readonly ToolDefinition[]): ToolSet {
  const result: ToolSet = {};

  for (const t of tools) {
    result[t.name] = {
      description: t.description,
      inputSchema: jsonSchema(t.inputSchema)
    };
  }

  return result;
}
