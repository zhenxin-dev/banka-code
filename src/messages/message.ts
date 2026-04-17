/**
 * 会话消息模型定义。
 *
 * @author 真心
 */

/**
 * 模型触发的工具调用。
 */
export interface ToolCall {
  readonly id: string;
  readonly name: string;
  readonly argumentsJson: string;
}

/**
 * 用户消息。
 */
export interface UserMessage {
  readonly role: "user";
  readonly content: string;
}

/**
 * 助手消息。
 */
export interface AssistantMessage {
  readonly role: "assistant";
  readonly content: string;
  readonly toolCalls: readonly ToolCall[];
}

/**
 * 工具结果消息。
 */
export interface ToolResultMessage {
  readonly role: "tool";
  readonly toolCallId: string;
  readonly toolName: string;
  readonly content: string;
  readonly isError: boolean;
}

/**
 * banka 内部会话消息联合类型。
 */
export type ConversationMessage = UserMessage | AssistantMessage | ToolResultMessage;
