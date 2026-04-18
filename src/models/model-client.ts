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
 * 流式输出回调集合。
 */
export interface StreamCallbacks {
  /** 收到文本增量时调用。 */
  readonly onTextDelta?: (text: string) => void;
}

/**
 * 模型客户端统一接口。
 */
export interface ModelClient {
  createAssistantMessage(request: ModelCompletionRequest): Promise<AssistantMessage>;

  /**
   * 流式调用模型，通过回调实时推送文本增量。
   * 返回值与 createAssistantMessage 一致，但过程中会调用 onTextDelta。
   */
  streamAssistantMessage?(request: ModelCompletionRequest, callbacks: StreamCallbacks): Promise<AssistantMessage>;
}
