/**
 * Mock 模型客户端。
 *
 * @author 真心
 */

import type { AssistantMessage, ConversationMessage } from "../messages/message.ts";
import type { ModelClient, ModelCompletionRequest } from "./model-client.ts";

/**
 * 无需外部 API 即可运行的占位模型。
 */
export class MockModelClient implements ModelClient {
  /**
   * 生成一条 mock 助手消息。
   */
  public async createAssistantMessage(
    request: ModelCompletionRequest
  ): Promise<AssistantMessage> {
    const lastMessage = request.messages.at(-1);
    const content = createMockContent(lastMessage);

    return {
      role: "assistant",
      content,
      toolCalls: []
    };
  }
}

function createMockContent(lastMessage: ConversationMessage | undefined): string {
  if (lastMessage === undefined) {
    return "banka mock client is ready.";
  }

  if (lastMessage.role === "tool") {
    return `Mock provider received tool result from ${lastMessage.toolName}:\n${lastMessage.content}`;
  }

  return [
    "Mock provider is active.",
    "Set BANKA_API_KEY, BANKA_BASE_URL and BANKA_MODEL to enable real tool-calling completions.",
    `Prompt: ${lastMessage.content}`
  ].join("\n");
}
