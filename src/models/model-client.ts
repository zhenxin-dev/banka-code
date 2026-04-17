/**
 * 模型客户端抽象定义。
 *
 * @author 真心
 */

import type { AssistantMessage, ConversationMessage } from "../messages/message.ts";
import type { ToolDefinition } from "../tools/tool.ts";

/**
 * 单次模型补全请求。
 */
export interface ModelCompletionRequest {
  readonly systemPrompt: string;
  readonly messages: readonly ConversationMessage[];
  readonly tools: readonly ToolDefinition[];
}

/**
 * 模型客户端统一接口。
 */
export interface ModelClient {
  createAssistantMessage(request: ModelCompletionRequest): Promise<AssistantMessage>;
}
