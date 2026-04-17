/**
 * Agent 主循环实现。
 *
 * @author 真心
 */

import { ModelResponseError } from "../errors/banka-error.ts";
import type { ConversationMessage, ToolCall } from "../messages/message.ts";
import type { ModelClient } from "../models/model-client.ts";
import { executeToolCall } from "../tools/execute-tool-call.ts";
import type { ToolExecutionContext } from "../tools/tool.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";

/**
 * 工具调用观察器。
 */
export interface ToolCallObserver {
  (toolCall: ToolCall): void;
}

/**
 * Agent 执行参数。
 */
export interface AgentRunOptions {
  readonly systemPrompt: string;
  readonly initialUserPrompt: string;
  readonly previousMessages?: readonly ConversationMessage[];
  readonly modelClient: ModelClient;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
  readonly maxIterations: number;
  readonly onToolCall?: ToolCallObserver;
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

  for (let iteration = 0; iteration < options.maxIterations; iteration += 1) {
    const assistantMessage = await options.modelClient.createAssistantMessage({
      systemPrompt: options.systemPrompt,
      messages,
      tools: options.toolRegistry.list()
    });

    messages.push(assistantMessage);

    if (assistantMessage.toolCalls.length === 0) {
      return {
        finalText: assistantMessage.content,
        transcript: [...messages],
        iterations: iteration + 1
      };
    }

    for (const toolCall of assistantMessage.toolCalls) {
      options.onToolCall?.(toolCall);
      const toolResult = await executeToolCall(toolCall, options.toolRegistry, options.toolContext);
      messages.push(toolResult);
    }
  }

  throw new ModelResponseError(
    `Agent exceeded the maximum iteration limit (${options.maxIterations}).`
  );
}
