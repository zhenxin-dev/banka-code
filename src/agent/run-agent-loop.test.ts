/**
 * Agent 主循环测试。
 *
 * @author 真心
 */

import { describe, expect, it, mock, beforeEach } from "bun:test";
import type { ConversationMessage } from "../messages/message.ts";
import type { ToolArguments, ToolDefinition, ToolExecutionContext, ToolResult } from "../tools/tool.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";

const fakeGenerateText = mock<(options: Record<string, unknown>) => Promise<unknown>>();

mock.module("ai", () => ({
  generateText: fakeGenerateText
}));

const { runAgentLoop } = await import("./run-agent-loop.ts");

function makeResult(text: string, toolCalls: Array<{ toolCallId: string; toolName: string; input: Record<string, string> }>): unknown {
  return {
    text,
    toolCalls,
    steps: [],
    usage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
    totalUsage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
    finishReason: toolCalls.length > 0 ? "tool-calls" : "stop"
  };
}

describe("runAgentLoop", () => {
  beforeEach(() => {
    fakeGenerateText.mockClear();
  });

  it("executes tool calls until the assistant returns final text", async () => {
    const capturedRequests: Array<{ messages: unknown[] }> = [];

    fakeGenerateText
      .mockImplementationOnce(async (options) => {
        capturedRequests.push({ messages: options.messages as unknown[] });
        return makeResult("", [{ toolCallId: "call_1", toolName: "echo_tool", input: { text: "hello" } }]);
      })
      .mockImplementationOnce(async (options) => {
        capturedRequests.push({ messages: options.messages as unknown[] });
        return makeResult("done", []);
      });

    const registry = new ToolRegistry([createEchoTool()]);
    const result = await runAgentLoop({
      systemPrompt: "test-system-prompt",
      initialUserPrompt: "Say hello",
      languageModel: {} as never,
      toolRegistry: registry,
      toolContext: { workspaceRoot: "/tmp/banka" },
      maxIterations: 4
    });

    expect(result.finalText).toBe("done");
    expect(capturedRequests).toHaveLength(2);

    const toolMessage = (capturedRequests[1]?.messages as Array<{ role: string; content: unknown }>).find((m) => m.role === "tool");

    expect(toolMessage).toEqual({
      role: "tool",
      content: [
        {
          type: "tool-result",
          toolCallId: "call_1",
          toolName: "echo_tool",
          output: { type: "text", value: "echo: hello" }
        }
      ]
    });
  });

  it("appends previous messages before the new user prompt", async () => {
    const capturedRequests: Array<{ messages: unknown[] }> = [];

    fakeGenerateText.mockImplementationOnce(async (options) => {
      capturedRequests.push({ messages: options.messages as unknown[] });
      return makeResult("continued", []);
    });

    const registry = new ToolRegistry([createEchoTool()]);
    await runAgentLoop({
      systemPrompt: "test-system-prompt",
      initialUserPrompt: "next turn",
      previousMessages: [
        { role: "user", content: "first turn" },
        { role: "assistant", content: "first reply", toolCalls: [] }
      ],
      languageModel: {} as never,
      toolRegistry: registry,
      toolContext: { workspaceRoot: "/tmp/banka" },
      maxIterations: 2
    });

    expect(capturedRequests[0]?.messages).toEqual([
      { role: "user", content: "first turn" },
      { role: "assistant", content: [{ type: "text", text: "first reply" }] },
      { role: "user", content: "next turn" }
    ]);
  });

  it("emits tool call notifications during execution", async () => {
    fakeGenerateText
      .mockImplementationOnce(async () => makeResult("", [{ toolCallId: "call_2", toolName: "echo_tool", input: { text: "world" } }]))
      .mockImplementationOnce(async () => makeResult("done", []));

    const observedCalls: string[] = [];
    const registry = new ToolRegistry([createEchoTool()]);
    await runAgentLoop({
      systemPrompt: "test-system-prompt",
      initialUserPrompt: "Say world",
      languageModel: {} as never,
      toolRegistry: registry,
      toolContext: { workspaceRoot: "/tmp/banka" },
      maxIterations: 4,
      onToolCall(toolCall) {
        observedCalls.push(`${toolCall.name}:${toolCall.id}`);
      }
    });

    expect(observedCalls).toEqual(["echo_tool:call_2"]);
  });
});

function createEchoTool(): ToolDefinition {
  return {
    name: "echo_tool",
    description: "Echo a string back to the caller.",
    inputSchema: {
      type: "object",
      properties: {
        text: {
          type: "string",
          description: "Text to echo."
        }
      },
      required: ["text"],
      additionalProperties: false
    },
    async execute(arguments_: ToolArguments, _context: ToolExecutionContext): Promise<ToolResult> {
      const text = arguments_["text"];

      if (typeof text !== "string") {
        throw new Error("echo_tool requires a text string.");
      }

      return {
        content: `echo: ${text}`,
        isError: false
      };
    }
  };
}
