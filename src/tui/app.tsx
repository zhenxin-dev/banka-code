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

import { TextAttributes, type InputRenderable, type SyntaxStyle } from "@opentui/core";
import { useKeyboard, useRenderer, useSelectionHandler, useTerminalDimensions } from "@opentui/solid";
import { For, Show, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import type { AgentRunResult, TextDeltaObserver } from "../agent/run-agent-loop.ts";
import { runAgentLoop } from "../agent/run-agent-loop.ts";
import type { LanguageModel } from "ai";
import { OperationAbortedError } from "../errors/banka-error.ts";
import type { ConversationMessage, ToolCall } from "../messages/message.ts";
import type { RuntimeConfig } from "../runtime/runtime-config.ts";
import type { ToolExecutionContext } from "../tools/tool.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";
import { findBuiltinCommands, getBuiltinCommands, parseBuiltinCommand, titledRule, toDisplayLines } from "./message-format.ts";
import { Logo } from "./logo.tsx";
import { createMarkdownSyntaxStyle } from "./markdown-style.ts";
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
  readonly languageModel: LanguageModel;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
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
  const [streamingEntryId, setStreamingEntryId] = createSignal<string | undefined>(undefined);
  let inputRef: InputRenderable | undefined;
  let activeAbortController: AbortController | undefined;
  let pendingStreamText = "";
  let pendingStreamEntryId: string | undefined;
  let streamFlushTimer: ReturnType<typeof setTimeout> | undefined;

  const markdownStyle = createMarkdownSyntaxStyle(t);

  const dividerWidth = createMemo(() => Math.max(24, terminal().width - 4));

  const statusMarquee = createMemo(() => buildStatusMarquee(statusTick()));
  const commandSuggestions = createMemo(() => findBuiltinCommands(draft()));
  const commandPanel = createMemo(() => buildCommandPanelState(draft(), commandSuggestions(), busy()));
  let lastCopiedSelectionText = "";

  const statusInterval = setInterval(() => {
    setStatusTick((value) => value + 1);
  }, 120);
  onCleanup(() => {
    clearInterval(statusInterval);
    if (streamFlushTimer !== undefined) {
      clearTimeout(streamFlushTimer);
    }
  });

  const flushPendingStreamText = () => {
    if (pendingStreamEntryId === undefined || pendingStreamText === "") {
      return;
    }

    const entryId = pendingStreamEntryId;
    const bufferedText = pendingStreamText;
    pendingStreamText = "";

    updateEntryBody(setEntries, entryId, (body) => body + bufferedText);
  };

  const clearStreamFlushTimer = () => {
    if (streamFlushTimer === undefined) {
      return;
    }

    clearTimeout(streamFlushTimer);
    streamFlushTimer = undefined;
  };

  const flushAndResetPendingStreamText = () => {
    clearStreamFlushTimer();
    flushPendingStreamText();
    pendingStreamEntryId = undefined;
  };

  const schedulePendingStreamTextFlush = (entryId: string, delta: string) => {
    if (pendingStreamEntryId !== undefined && pendingStreamEntryId !== entryId) {
      flushAndResetPendingStreamText();
    }

    pendingStreamEntryId = entryId;
    pendingStreamText += delta;

    if (streamFlushTimer !== undefined) {
      return;
    }

    streamFlushTimer = setTimeout(() => {
      streamFlushTimer = undefined;
      flushPendingStreamText();
    }, 33);
  };

  onMount(() => {
    inputRef?.focus();
    applyInputCursorStyle(inputRef, t.brandShimmer);
  });

  useKeyboard((key) => {
    if (key.name !== "escape" || !busy()) {
      return;
    }

    key.preventDefault();
    flushAndResetPendingStreamText();
    activeAbortController?.abort();
  });

  useSelectionHandler((selection) => {
    if (selection.isDragging) {
      return;
    }

    const selectedText = selection.getSelectedText();

    if (selectedText === "") {
      lastCopiedSelectionText = "";
      return;
    }

    if (selectedText === lastCopiedSelectionText) {
      return;
    }

    writeClipboardText(selectedText);
    lastCopiedSelectionText = selectedText;
  });

  const submitPrompt = async (value: string) => {
    const prompt = value.trim();
    const builtinCommand = parseBuiltinCommand(prompt);

    if (prompt === "") {
      return;
    }

    if (builtinCommand?.name === "exit" || builtinCommand?.name === "quit") {
      clearDraft(inputRef, setDraft);
      renderer.destroy();
      return;
    }

    if (busy()) {
      return;
    }

    if (builtinCommand !== undefined) {
      clearDraft(inputRef, setDraft);
      handleBuiltinCommand({
        commandName: builtinCommand.name,
        runtimeConfig: props.runtimeConfig,
        entriesCount: entries().length,
        transcriptCount: previousMessages().length,
        appendEntry(entry) {
          appendEntry(setEntries, entry);
        },
        clearSession() {
          setEntries([]);
          setPreviousMessages([]);
          setStreamingEntryId(undefined);
        }
      });
      return;
    }

    clearDraft(inputRef, setDraft);
    setBusy(true);
    appendEntry(setEntries, createEntry("user", "你", prompt));

    const abortController = new AbortController();
    activeAbortController = abortController;

    const streamingEntry = createEntry("assistant", "Banka Code", "");
    let currentStreamingId = streamingEntry.id;
    setStreamingEntryId(currentStreamingId);
    appendEntry(setEntries, streamingEntry);

    try {
      const result = await runSingleTurn({
        systemPrompt: props.systemPrompt,
        prompt,
        previousMessages: previousMessages(),
        languageModel: props.languageModel,
        toolRegistry: props.toolRegistry,
        toolContext: props.toolContext,
        abortSignal: abortController.signal,
        onToolCall(toolCall) {
          flushAndResetPendingStreamText();
          const currentId = currentStreamingId;
          setEntries((prev) => {
            const current = prev.find((e) => e.id === currentId);
            // 无文本时替换为 tool entry，有文本时保留并追加
            if (current !== undefined && current.body === "") {
              return [...prev.filter((e) => e.id !== currentId), createEntry("tool", "tool", formatToolCallEntry(toolCall))];
            }
            return [...prev, createEntry("tool", "tool", formatToolCallEntry(toolCall))];
          });

          const nextEntry = createEntry("assistant", "Banka Code", "");
          currentStreamingId = nextEntry.id;
          setStreamingEntryId(currentStreamingId);
          appendEntry(setEntries, nextEntry);
        },
        onTextDelta(delta) {
          schedulePendingStreamTextFlush(currentStreamingId, delta);
        }
      });

      // 最终更新 last streaming entry：写入 finalText 或移除空白
      flushAndResetPendingStreamText();
      const lastId = currentStreamingId;
      setEntries((prev) => {
        const last = prev.find((e) => e.id === lastId);
        if (last === undefined) {
          return prev;
        }
        if (result.finalText !== "") {
          return prev.map((e) => e.id === lastId ? { ...e, body: result.finalText } : e);
        }
        if (last.body === "") {
          return prev.filter((e) => e.id !== lastId);
        }
        return prev;
      });

      setPreviousMessages(result.transcript);
      appendEntry(
        setEntries,
        createEntry("status", "status", `✓ ${result.iterations} 轮`)
      );
    } catch (error) {
      flushAndResetPendingStreamText();
      if (!(error instanceof OperationAbortedError)) {
        appendEntry(setEntries, createEntry("error", "error", toErrorMessage(error)));
      }
    } finally {
      flushAndResetPendingStreamText();
      if (activeAbortController === abortController) {
        activeAbortController = undefined;
      }
      setStreamingEntryId(undefined);
      setBusy(false);
      inputRef?.focus();
    }
  };

  return (
    <box width="100%" height="100%" flexDirection="column" paddingX={1} paddingTop={1} paddingBottom={0}>
      {/* ── Header: Logo + Info ── */}
      <box flexDirection="column" alignItems="center" flexShrink={0}>
        <Logo />
      </box>

      <box flexShrink={0}>
        <text fg={t.divider}> {titledRule(dividerWidth(), "")}</text>
      </box>

      {/* ── Message List (Scrollable) ── */}
      <scrollbox flexGrow={1} scrollY stickyScroll stickyStart="bottom" paddingRight={1}>
        <For each={entries()}>
          {(entry) => renderEntry(entry, t, markdownStyle, streamingEntryId())}
        </For>

      </scrollbox>

      {/* ── Footer: Input Area ── */}
      <box flexDirection="column" paddingX={2} flexShrink={0}>
        <Show when={commandPanel() !== undefined}>
          <>
            <box
              flexDirection="column"
              border
              borderColor={t.promptBorder}
              marginBottom={1}
              paddingLeft={1}
              paddingRight={1}
              flexShrink={0}
            >
              <Show
                when={commandPanel()!.commands.length > 0}
                fallback={<text fg={t.hintText}>未找到命令，输入 /help 查看可用命令</text>}
              >
                <For each={commandPanel()!.commands}>
                  {(command, index) => (
                    <box flexDirection="row" gap={1}>
                      <box width={12} flexShrink={0}>
                        <text
                          fg={index() === 0 ? t.brandShimmer : t.text}
                          attributes={index() === 0 ? TextAttributes.BOLD : TextAttributes.NONE}
                        >
                          {`${index() === 0 ? "›" : " "} ${command.command}`}
                        </text>
                      </box>
                      <box flexGrow={1} flexShrink={1} minWidth={0}>
                        <text fg={index() === 0 ? t.brandShimmer : t.hintText}>{command.description}</text>
                      </box>
                    </box>
                  )}
                </For>
                <Show when={commandPanel()!.hasMore}>
                  <text fg={t.hintText} attributes={TextAttributes.DIM}>… 还有更多命令</text>
                </Show>
              </Show>
            </box>
          </>
        </Show>
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
              value.focus();
            }}
            focused={true}
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
          <Show
            when={busy()}
            fallback={<text fg={t.hintText}> </text>}
          >
            <box flexDirection="row" gap={1}>
              <box flexDirection="row">
                <For each={statusMarquee()}>
                  {(segment) => <text fg={segment.color}>{segment.text}</text>}
                </For>
              </box>
              <text fg={t.hintText}>按 ESC 中断</text>
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

interface BuiltinCommandHandlerOptions {
  readonly commandName: "help" | "clear" | "status";
  readonly runtimeConfig: RuntimeConfig;
  readonly entriesCount: number;
  readonly transcriptCount: number;
  readonly appendEntry: (entry: UiEntry) => void;
  readonly clearSession: () => void;
}

function clearDraft(
  input: InputRenderable | undefined,
  setDraft: (value: string) => void
): void {
  if (input !== undefined) {
    input.value = "";
  }

  setDraft("");
}

function writeClipboardText(text: string): void {
  const encoded = Buffer.from(text, "utf8").toString("base64");
  process.stdout.write(`\u001B]52;c;${encoded}\u0007`);
}

function handleBuiltinCommand(options: BuiltinCommandHandlerOptions): void {
  switch (options.commandName) {
    case "help":
      options.appendEntry(createEntry("assistant", "Banka Code", buildBuiltinHelpBody()));
      return;
    case "clear":
      options.clearSession();
      return;
    case "status":
      options.appendEntry(createEntry(
        "assistant",
        "Banka Code",
        buildBuiltinStatusBody(options.runtimeConfig, options.entriesCount, options.transcriptCount)
      ));
      return;
  }
}

interface CommandPanelState {
  readonly commands: readonly { readonly command: string; readonly description: string }[];
  readonly hasMore: boolean;
}

function buildCommandPanelState(
  draft: string,
  commands: readonly { readonly command: string; readonly description: string }[],
  busy: boolean
): CommandPanelState | undefined {
  const prompt = draft.trim();

  if (busy || !prompt.startsWith("/")) {
    return undefined;
  }

  const visibleCommands = commands.slice(0, 6);

  return {
    commands: visibleCommands,
    hasMore: commands.length > visibleCommands.length
  };
}

function buildBuiltinHelpBody(): string {
  const lines = ["## 可用命令", ""];

  for (const command of getBuiltinCommands()) {
    lines.push(`- \`${command.command}\`：${command.description}`);
  }

  return lines.join("\n");
}

function buildBuiltinStatusBody(
  runtimeConfig: RuntimeConfig,
  entriesCount: number,
  transcriptCount: number
): string {
  return [
    "## 当前状态",
    "",
    `- Provider：${toProviderLabel(runtimeConfig.provider)}`,
    `- Model：${runtimeConfig.model}`,
    `- 当前屏幕消息数：${entriesCount}`,
    `- 当前会话消息数：${transcriptCount}`
  ].join("\n");
}

function formatRuntimeBadge(runtimeConfig: RuntimeConfig): string {
  return `${toProviderLabel(runtimeConfig.provider)} · ${runtimeConfig.model}`;
}

function toProviderLabel(provider: RuntimeConfig["provider"]): string {
  switch (provider) {
    case "openai":
      return "OpenAI";
    case "openai-chat":
      return "OpenAI";
    case "anthropic":
      return "Anthropic";
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

function renderEntry(
  entry: UiEntry,
  t: ReturnType<typeof getTheme>,
  mdStyle: SyntaxStyle,
  currentStreamingId: string | undefined
) {
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
      if (entry.body === "") {
        return null;
      }
      return (
        <box width="100%" flexDirection="row" marginTop={1} marginBottom={0} paddingLeft={2}>
          <box width={2} flexShrink={0}>
            <text fg={t.brandShimmer}>❀ </text>
          </box>
          <box flexDirection="column" flexGrow={1} flexShrink={1} minWidth={0} paddingRight={1}>
            <markdown
              content={entry.body}
              syntaxStyle={mdStyle}
              fg={t.assistantBody}
              conceal={true}
              streaming={entry.id === currentStreamingId}
              width="100%"
              flexShrink={1}
              tableOptions={{
                widthMode: "content",
                columnFitter: "balanced",
                wrapMode: "word",
                cellPadding: 1,
                borders: true,
                outerBorder: true,
                borderStyle: "single",
                borderColor: t.divider,
                selectable: true
              }}
            />
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
  readonly languageModel: LanguageModel;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
  readonly abortSignal?: AbortSignal;
  readonly onToolCall: (toolCall: ToolCall) => void;
  readonly onTextDelta: TextDeltaObserver;
}

async function runSingleTurn(options: SingleTurnOptions): Promise<AgentRunResult> {
  return await runAgentLoop({
    systemPrompt: options.systemPrompt,
    initialUserPrompt: options.prompt,
    previousMessages: options.previousMessages,
    languageModel: options.languageModel,
    toolRegistry: options.toolRegistry,
    toolContext: options.toolContext,
    ...(options.abortSignal === undefined ? {} : { abortSignal: options.abortSignal }),
    onToolCall(toolCall) {
      options.onToolCall(toolCall);
    },
    onTextDelta(delta) {
      options.onTextDelta(delta);
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

function updateEntryBody(
  setEntries: (setter: (previous: readonly UiEntry[]) => readonly UiEntry[]) => void,
  entryId: string,
  updateBody: (body: string) => string
): void {
  setEntries((previous) => previous.map((entry) =>
    entry.id === entryId
      ? { ...entry, body: updateBody(entry.body) }
      : entry
  ));
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
