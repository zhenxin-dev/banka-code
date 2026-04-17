/**
 * Agent 主循环测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import type { AssistantMessage } from "../messages/message.ts";
import type { ModelClient, ModelCompletionRequest } from "../models/model-client.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";
import type { ToolArguments, ToolDefinition, ToolExecutionContext, ToolResult } from "../tools/tool.ts";
import { runAgentLoop } from "./run-agent-loop.ts";

describe("runAgentLoop", () => {
  it("executes tool calls until the assistant returns final text", async () => {
    const scriptedClient = new ScriptedModelClient([
      {
        role: "assistant",
        content: "",
        toolCalls: [
          {
            id: "call_1",
            name: "echo_tool",
            argumentsJson: JSON.stringify({ text: "hello" })
          }
        ]
      },
      {
        role: "assistant",
        content: "done",
        toolCalls: []
      }
    ]);

    const registry = new ToolRegistry([createEchoTool()]);
    const result = await runAgentLoop({
      systemPrompt: "test-system-prompt",
      initialUserPrompt: "Say hello",
      modelClient: scriptedClient,
      toolRegistry: registry,
      toolContext: {
        workspaceRoot: "/tmp/banka"
      },
      maxIterations: 4
    });

    expect(result.finalText).toBe("done");
    expect(scriptedClient.requests).toHaveLength(2);

    const secondRequest = scriptedClient.requests[1];
    const toolMessage = secondRequest?.messages.find((message) => message.role === "tool");

    expect(toolMessage).toEqual({
      role: "tool",
      toolCallId: "call_1",
      toolName: "echo_tool",
      content: "echo: hello",
      isError: false
    });
  });

  it("appends previous messages before the new user prompt", async () => {
    const scriptedClient = new ScriptedModelClient([
      {
        role: "assistant",
        content: "continued",
        toolCalls: []
      }
    ]);

    const registry = new ToolRegistry([createEchoTool()]);
    await runAgentLoop({
      systemPrompt: "test-system-prompt",
      initialUserPrompt: "next turn",
      previousMessages: [
        {
          role: "user",
          content: "first turn"
        },
        {
          role: "assistant",
          content: "first reply",
          toolCalls: []
        }
      ],
      modelClient: scriptedClient,
      toolRegistry: registry,
      toolContext: {
        workspaceRoot: "/tmp/banka"
      },
      maxIterations: 2
    });

    expect(scriptedClient.requests[0]?.messages).toEqual([
      {
        role: "user",
        content: "first turn"
      },
      {
        role: "assistant",
        content: "first reply",
        toolCalls: []
      },
      {
        role: "user",
        content: "next turn"
      }
    ]);
  });

  it("emits tool call notifications during execution", async () => {
    const scriptedClient = new ScriptedModelClient([
      {
        role: "assistant",
        content: "",
        toolCalls: [
          {
            id: "call_2",
            name: "echo_tool",
            argumentsJson: JSON.stringify({ text: "world" })
          }
        ]
      },
      {
        role: "assistant",
        content: "done",
        toolCalls: []
      }
    ]);

    const observedCalls: string[] = [];
    const registry = new ToolRegistry([createEchoTool()]);
    await runAgentLoop({
      systemPrompt: "test-system-prompt",
      initialUserPrompt: "Say world",
      modelClient: scriptedClient,
      toolRegistry: registry,
      toolContext: {
        workspaceRoot: "/tmp/banka"
      },
      maxIterations: 4,
      onToolCall(toolCall) {
        observedCalls.push(`${toolCall.name}:${toolCall.id}`);
      }
    });

    expect(observedCalls).toEqual(["echo_tool:call_2"]);
  });
});

class ScriptedModelClient implements ModelClient {
  readonly #responses: AssistantMessage[];
  readonly requests: ModelCompletionRequest[] = [];
  #cursor = 0;

  public constructor(responses: readonly AssistantMessage[]) {
    this.#responses = [...responses];
  }

  public async createAssistantMessage(
    request: ModelCompletionRequest
  ): Promise<AssistantMessage> {
    this.requests.push({
      systemPrompt: request.systemPrompt,
      messages: [...request.messages],
      tools: [...request.tools]
    });

    const nextResponse = this.#responses[this.#cursor];

    if (nextResponse === undefined) {
      throw new Error("No scripted assistant response available.");
    }

    this.#cursor += 1;
    return nextResponse;
  }
}

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
