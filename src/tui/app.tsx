/**
 * banka TUI 主界面 — 1:1 复刻 cc-haha FullscreenLayout。
 *
 * 布局结构：
 * ┌──────────────────────────────────────────────┐
 * │ Header: MurasameLogo + provider/model info   │
 * ├──────────────────────────────────────────────┤
 * │ ScrollBox: Message list (scrollable)         │
 * │   ├─ UserTextMessage                        │
 * │   ├─ AssistantTextMessage                    │
 * │   ├─ ToolCallMessage                        │
 * │   ├─ ErrorMessage                            │
 * │   └─ StatusMessage                           │
 * ├──────────────────────────────────────────────┤
 * │ Footer: Spinner + PromptInput                │
 * │   ├─ StatusLine                              │
 * │   └─ Input (▸ prompt)                        │
 * └──────────────────────────────────────────────┘
 *
 * @author 真心
 */

import { TextAttributes, type InputRenderable } from "@opentui/core";
import { useRenderer, useTerminalDimensions } from "@opentui/solid";
import { For, Show, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import type { AgentRunResult } from "../agent/run-agent-loop.ts";
import { runAgentLoop } from "../agent/run-agent-loop.ts";
import type { ConversationMessage, ToolCall } from "../messages/message.ts";
import type { ModelClient } from "../models/model-client.ts";
import type { RuntimeConfig } from "../runtime/runtime-config.ts";
import type { ToolExecutionContext } from "../tools/tool.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";
import { isExitCommand, titledRule, toDisplayLines } from "./message-format.ts";
import { Logo } from "./logo.tsx";
import { Spinner } from "./spinner.tsx";
import { getTheme } from "./theme.ts";

interface UiEntry {
  readonly id: string;
  readonly kind: "user" | "assistant" | "tool" | "error" | "status";
  readonly title: string;
  readonly body: string;
}

export interface TuiAppProps {
  readonly systemPrompt: string;
  readonly runtimeConfig: RuntimeConfig;
  readonly modelClient: ModelClient;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
  readonly maxIterations: number;
}

export function TuiApp(props: TuiAppProps) {
  const t = getTheme();
  const renderer = useRenderer();
  const terminal = useTerminalDimensions();

  const [entries, setEntries] = createSignal<readonly UiEntry[]>([]);
  const [busy, setBusy] = createSignal(false);
  const [draft, setDraft] = createSignal("");
  const [previousMessages, setPreviousMessages] = createSignal<readonly ConversationMessage[]>([]);
  const [statusTick, setStatusTick] = createSignal(0);
  let inputRef: InputRenderable | undefined;

  const dividerWidth = createMemo(() => Math.max(24, terminal().width - 4));

  const statusMarquee = createMemo(() => buildStatusMarquee(statusTick()));

  const statusInterval = setInterval(() => {
    setStatusTick((value) => value + 1);
  }, 120);
  onCleanup(() => clearInterval(statusInterval));

  onMount(() => {
    inputRef?.focus();
    applyInputCursorStyle(inputRef, t.brandShimmer);
  });

  const submitPrompt = async (value: string) => {
    const prompt = value.trim();

    if (prompt === "" || busy()) {
      return;
    }

    if (isExitCommand(prompt)) {
      renderer.destroy();
      return;
    }

    if (inputRef !== undefined) {
      inputRef.value = "";
    }
    setDraft("");
    setBusy(true);
    appendEntry(setEntries, createEntry("user", "你", prompt));

    try {
      const result = await runSingleTurn({
        systemPrompt: props.systemPrompt,
        prompt,
        previousMessages: previousMessages(),
        modelClient: props.modelClient,
        toolRegistry: props.toolRegistry,
        toolContext: props.toolContext,
        maxIterations: props.maxIterations,
        onToolCall(toolCall) {
          appendEntry(setEntries, createEntry("tool", "tool", formatToolCallEntry(toolCall)));
        }
      });

      setPreviousMessages(result.transcript);
      appendEntry(
        setEntries,
        createEntry("assistant", "Banka Code", result.finalText || "（空回复）")
      );
      appendEntry(
        setEntries,
        createEntry("status", "status", `✓ ${result.iterations} 轮`)
      );
    } catch (error) {
        appendEntry(setEntries, createEntry("error", "error", toErrorMessage(error)));
    } finally {
      setBusy(false);
      inputRef?.focus();
    }
  };

  return (
    <box width="100%" height="100%" flexDirection="column" paddingX={1} paddingTop={1} paddingBottom={0}>
      {/* ── Header: Logo + Info ── */}
      <box flexDirection="column" alignItems="center">
        <Logo />
      </box>

      <box>
        <text fg={t.text}> </text>
      </box>

      <box>
        <text fg={t.divider}> {titledRule(dividerWidth(), "")}</text>
      </box>

      {/* ── Message List (Scrollable) ── */}
      <scrollbox flexGrow={1} scrollY stickyScroll>
        <For each={entries()}>
          {(entry) => renderEntry(entry, t)}
        </For>

      </scrollbox>

      {/* ── Footer: Input Area ── */}
      <box flexDirection="column" paddingX={2} flexShrink={0}>
        <box
          flexDirection="row"
          alignItems="center"
          height={3}
          border
          borderColor={t.promptBorder}
          paddingLeft={1}
          paddingRight={1}
          flexShrink={0}
        >
          <Show
            when={busy()}
            fallback={<text fg={t.brandShimmer} attributes={TextAttributes.BOLD}>◈ </text>}
          >
            <text fg={t.statusText}>◇ </text>
          </Show>
          <input
            ref={(value) => {
              inputRef = value;
              applyInputCursorStyle(value, t.brandShimmer);
            }}
            focused={!busy()}
            value={draft()}
            flexGrow={1}
            placeholder={busy() ? "等待中…" : "给 Banka Code 发消息…"}
            onInput={setDraft}
            onSubmit={() => {
              void submitPrompt(inputRef?.plainText ?? draft());
            }}
          />
        </box>
        <box flexDirection="row" justifyContent="space-between" paddingLeft={2} paddingRight={1}>
          <Show when={busy()} fallback={<text fg={t.hintText}> </text>}>
            <box flexDirection="row" gap={1}>
              <box flexDirection="row">
                <For each={statusMarquee()}>
                  {(segment) => <text fg={segment.color}>{segment.text}</text>}
                </For>
              </box>
              <text fg={t.hintText}>esc 中断</text>
            </box>
          </Show>
          <text fg={t.hintText} attributes={TextAttributes.DIM}>{formatRuntimeBadge(props.runtimeConfig)}</text>
        </box>
      </box>
    </box>
  );
}

function applyInputCursorStyle(input: InputRenderable | undefined, color: string): void {
  if (input === undefined) {
    return;
  }

  input.cursorStyle = {
    style: "line",
    blinking: false
  };
  input.cursorColor = color;
}

function formatRuntimeBadge(runtimeConfig: RuntimeConfig): string {
  return `${toProviderLabel(runtimeConfig.provider)} · ${runtimeConfig.model}`;
}

function toProviderLabel(provider: RuntimeConfig["provider"]): string {
  switch (provider) {
    case "openai":
      return "OpenAI";
    case "anthropic":
      return "Anthropic";
    case "ollama":
      return "Ollama";
  }
}

function formatToolCallEntry(toolCall: ToolCall): string {
  const displayName = toToolDisplayName(toolCall.name);
  const summary = summarizeToolArguments(toolCall.name, toolCall.argumentsJson);

  if (summary === "") {
    return displayName;
  }

  return `${displayName} · ${summary}`;
}

function toToolDisplayName(toolName: string): string {
  switch (toolName) {
    case "Bash":
      return "Bash";
    case "Read":
      return "Read";
    case "Write":
      return "Write";
    case "Edit":
      return "Edit";
    case "Glob":
      return "Glob";
    case "Grep":
      return "Grep";
    // TODO: 待实现工具 — 命名统一 PascalCase
    // case "Todo": return "Todo";
    // case "Task": return "Task";
    // case "Fetch": return "Fetch";
    // case "Search": return "Search";
    default:
      return toTitleCase(toolName.replaceAll("_", " "));
  }
}

function summarizeToolArguments(toolName: string, argumentsJson: string): string {
  const args = parseToolArguments(argumentsJson);

  switch (toolName) {
    case "Bash":
      return readTrimmedString(args, "command", 72);
    case "Read":
    case "Write":
    case "Edit":
      return readTrimmedString(args, "path", 72);
    case "Glob":
      return readTrimmedString(args, "pattern", 72);
    case "Grep": {
      const pattern = readTrimmedString(args, "pattern", 44);
      const include = readTrimmedString(args, "include", 24);
      if (pattern !== "" && include !== "") {
        return `${pattern} in ${include}`;
      }
      return pattern || include;
    }
    // TODO: 待实现工具 — 命名统一 PascalCase
    // case "Todo": return readTrimmedString(args, "todos", 72);
    // case "Task": return readTrimmedString(args, "description", 72) || readTrimmedString(args, "prompt", 72);
    // case "Fetch": return readTrimmedString(args, "url", 72);
    // case "Search": return readTrimmedString(args, "query", 72);
    default:
      return "";
  }
}

function parseToolArguments(argumentsJson: string): Record<string, unknown> | undefined {
  try {
    const parsedValue: unknown = JSON.parse(argumentsJson);

    if (parsedValue !== null && typeof parsedValue === "object" && !Array.isArray(parsedValue)) {
      return parsedValue as Record<string, unknown>;
    }
  } catch {
    return undefined;
  }

  return undefined;
}

function readTrimmedString(
  record: Record<string, unknown> | undefined,
  key: string,
  maxLength: number
): string {
  const value = record?.[key];

  if (typeof value !== "string") {
    return "";
  }

  const normalized = value.trim().replace(/\s+/g, " ");

  if (normalized.length <= maxLength) {
    return normalized;
  }

  return `${normalized.slice(0, maxLength - 1)}…`;
}

function toTitleCase(value: string): string {
  return value
    .split(" ")
    .filter((part) => part !== "")
    .map((part) => `${part[0]?.toUpperCase() ?? ""}${part.slice(1)}`)
    .join(" ");
}

function renderEntry(entry: UiEntry, t: ReturnType<typeof getTheme>) {
  switch (entry.kind) {
    case "user":
      return (
        <box flexDirection="row" marginTop={1} marginBottom={1} paddingLeft={2}>
          <text fg={t.user}>◈ </text>
          <box flexDirection="column">
            <For each={toDisplayLines(entry.body)}>
              {(line) => <text fg={t.text}>{line}</text>}
            </For>
          </box>
        </box>
      );

    case "assistant":
      return (
        <box flexDirection="row" marginTop={1} marginBottom={0} paddingLeft={2}>
          <text fg={t.brandShimmer}>❀ </text>
          <box flexDirection="column">
            <For each={toDisplayLines(entry.body)}>
              {(line) => <text fg={t.assistantBody}>{line}</text>}
            </For>
          </box>
        </box>
      );

    case "tool":
      return (
        <box flexDirection="row" paddingLeft={4} marginTop={0} marginBottom={0}>
          <text fg={t.tool} attributes={TextAttributes.DIM}>⊰ </text>
          <text fg={t.tool} attributes={TextAttributes.DIM}>{entry.body}</text>
        </box>
      );

    case "error":
      return (
        <box flexDirection="column" marginTop={1} paddingLeft={3}>
          <text fg={t.error}>⚠ {entry.body}</text>
        </box>
      );

    case "status":
      return (
        <box flexDirection="row" marginTop={0} marginBottom={0} paddingLeft={3}>
          <text fg={t.statusText} attributes={TextAttributes.DIM}>◌ {entry.body}</text>
        </box>
      );
  }
}

interface SingleTurnOptions {
  readonly systemPrompt: string;
  readonly prompt: string;
  readonly previousMessages: readonly ConversationMessage[];
  readonly modelClient: ModelClient;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
  readonly maxIterations: number;
  readonly onToolCall: (toolCall: ToolCall) => void;
}

async function runSingleTurn(options: SingleTurnOptions): Promise<AgentRunResult> {
  return await runAgentLoop({
    systemPrompt: options.systemPrompt,
    initialUserPrompt: options.prompt,
    previousMessages: options.previousMessages,
    modelClient: options.modelClient,
    toolRegistry: options.toolRegistry,
    toolContext: options.toolContext,
    maxIterations: options.maxIterations,
    onToolCall(toolCall) {
      options.onToolCall(toolCall);
    }
  });
}

function createEntry(kind: UiEntry["kind"], title: string, body: string): UiEntry {
  return {
    id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
    kind,
    title,
    body
  };
}

function appendEntry(
  setEntries: (setter: (previous: readonly UiEntry[]) => readonly UiEntry[]) => void,
  entry: UiEntry
): void {
  setEntries((previous) => [...previous, entry]);
}

function toErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }

  return "未知错误";
}

interface MarqueeSegment {
  readonly text: string;
  readonly color: string;
}

function buildStatusMarquee(tick: number): readonly MarqueeSegment[] {
  const barWidth = 7;
  const period = barWidth * 2 - 2;
  const raw = tick % period;
  // 三角波：0→6→0，模拟灯笼来回轻晃
  const head = raw <= barWidth - 1 ? raw : period - raw;

  // 暖色灯笼尾焰：金→橙→樱→暗红→深褐，逐级衰减
  const trail = [
    { distance: 0, color: "#ffd27a", glyph: "●" },
    { distance: 1, color: "#ffb347", glyph: "◉" },
    { distance: 2, color: "#f2966b", glyph: "◎" },
    { distance: 3, color: "#e07060", glyph: "○" },
    { distance: 4, color: "#a04840", glyph: "◌" },
    { distance: 5, color: "#602a24", glyph: "·" },
  ] as const;

  const bars = Array.from({ length: barWidth }, (_, index) => {
    const distance = Math.abs(head - index);
    const segment = trail.find((item) => item.distance === distance);
    if (segment !== undefined) {
      return { text: segment.glyph, color: segment.color };
    }

    return { text: " ", color: "#40201a" };
  });

  return bars;
}
