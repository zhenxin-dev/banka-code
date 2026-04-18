/**
 * Anthropic 模型客户端测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { AnthropicModelClient } from "./anthropic-model-client.ts";

describe("AnthropicModelClient", () => {
  it("parses text and tool_use blocks into assistant message", async () => {
    const originalFetch = globalThis.fetch;
    const fetchStub = Object.assign(
      async (_input: string | URL | Request, _init?: RequestInit): Promise<Response> => {
        return new Response(
          JSON.stringify({
            content: [
              {
                type: "text",
                text: "先看一下文件。"
              },
              {
                type: "tool_use",
                id: "toolu_1",
                name: "Read",
                input: {
                  path: "README.md"
                }
              }
            ]
          }),
          {
            status: 200,
            headers: {
              "Content-Type": "application/json"
            }
          }
        );
      },
      {
        preconnect: originalFetch.preconnect.bind(originalFetch)
      }
    );

    globalThis.fetch = fetchStub;

    try {
      const client = new AnthropicModelClient({
        apiKey: "test-key",
        baseUrl: "https://example.com/anthropic/v1",
        model: "MiniMax-M2.7"
      });

      const result = await client.createAssistantMessage({
        systemPrompt: "You are banka.",
        messages: [
          {
            role: "user",
            content: "看下 readme"
          }
        ],
        tools: []
      });

      expect(result.content).toBe("先看一下文件。");
      expect(result.toolCalls).toEqual([
        {
          id: "toolu_1",
            name: "Read",
          argumentsJson: JSON.stringify({ path: "README.md" })
        }
      ]);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
