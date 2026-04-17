/**
 * 模型客户端工厂测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import type { RuntimeConfig } from "../runtime/runtime-config.ts";
import { createModelClient } from "./create-model-client.ts";

describe("createModelClient", () => {
  it("normalizes ollama base url and uses placeholder api key", async () => {
    const originalFetch = globalThis.fetch;
    let calledUrl = "";
    let authorizationHeader = "";

    const fetchStub = Object.assign(
      async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
        calledUrl = String(input);
        authorizationHeader = new Headers(init?.headers).get("Authorization") ?? "";

        return new Response(
          JSON.stringify({
            choices: [
              {
                message: {
                  role: "assistant",
                  content: "先连通了。"
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
      const client = createModelClient({
        workspaceRoot: "/workspace",
        provider: "ollama",
        model: "llama3.2",
        baseUrl: "127.0.0.1:11434"
      } satisfies RuntimeConfig);

      const result = await client.createAssistantMessage({
        systemPrompt: "You are Banka Code.",
        messages: [
          {
            role: "user",
            content: "你好"
          }
        ],
        tools: []
      });

      expect(calledUrl).toBe("http://127.0.0.1:11434/v1/chat/completions");
      expect(authorizationHeader).toBe("Bearer ollama");
      expect(result.content).toBe("先连通了。");
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
