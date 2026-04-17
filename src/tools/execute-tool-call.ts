/**
 * 工具调用执行器。
 *
 * @author 真心
 */

import { ToolExecutionError } from "../errors/banka-error.ts";
import type { ToolCall, ToolResultMessage } from "../messages/message.ts";
import { isRecord } from "../shared/is-record.ts";
import type { ToolArguments, ToolExecutionContext } from "./tool.ts";
import { ToolRegistry } from "./tool-registry.ts";

/**
 * 执行一次工具调用并返回工具结果消息。
 */
export async function executeToolCall(
  toolCall: ToolCall,
  registry: ToolRegistry,
  context: ToolExecutionContext
): Promise<ToolResultMessage> {
  const tool = registry.get(toolCall.name);

  if (tool === undefined) {
    return createErrorResult(toolCall, `Unknown tool: ${toolCall.name}`);
  }

  try {
    const parsedArguments = parseToolArguments(toolCall.argumentsJson);
    const result = await tool.execute(parsedArguments, context);

    return {
      role: "tool",
      toolCallId: toolCall.id,
      toolName: toolCall.name,
      content: result.content,
      isError: result.isError
    };
  } catch (error) {
    return createErrorResult(toolCall, errorToMessage(error));
  }
}

function parseToolArguments(argumentsJson: string): ToolArguments {
  let parsedValue: unknown;

  try {
    parsedValue = JSON.parse(argumentsJson);
  } catch (error) {
    throw new ToolExecutionError("Tool arguments must be valid JSON.", { cause: error });
  }

  if (!isRecord(parsedValue)) {
    throw new ToolExecutionError("Tool arguments must decode to a JSON object.");
  }

  return parsedValue;
}

function createErrorResult(toolCall: ToolCall, message: string): ToolResultMessage {
  return {
    role: "tool",
    toolCallId: toolCall.id,
    toolName: toolCall.name,
    content: `Tool execution failed: ${message}`,
    isError: true
  };
}

function errorToMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }

  return "Unknown error";
}
