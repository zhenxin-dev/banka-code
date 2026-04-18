/**
 * TUI 输出格式化工具。
 *
 * @author 真心
 */

import type { ProviderKind } from "../runtime/runtime-config.ts";

const ANSI_RESET = "\u001B[0m";
const ANSI_CYAN = "\u001B[36m";
const ANSI_GREEN = "\u001B[32m";
const ANSI_RED = "\u001B[31m";
const ANSI_GRAY = "\u001B[90m";

/**
 * 返回 TUI 欢迎横幅。
 */
export function formatBanner(provider: ProviderKind, model: string): string {
  return [
    `${ANSI_CYAN}Banka Code — 神刀觉醒，代码为刃。${ANSI_RESET}`,
    `${ANSI_GRAY}Provider: ${provider} | Model: ${model}${ANSI_RESET}`,
    `${ANSI_GRAY}输入 /help 查看命令，/exit 或 /quit 退出 Banka Code。${ANSI_RESET}`
  ].join("\n");
}

/**
 * 格式化助手回复。
 */
export function formatAssistantReply(content: string): string {
  return `${ANSI_CYAN}[Banka Code]${ANSI_RESET} ${content}`;
}

/**
 * 格式化工具调用提示。
 */
export function formatToolCall(name: string): string {
  return `${ANSI_GRAY}→ ${name}${ANSI_RESET}`;
}

/**
 * 格式化错误输出。
 */
export function formatError(content: string): string {
  return `${ANSI_RED}[error]${ANSI_RESET} ${content}`;
}

/**
 * 格式化完成提示。
 */
export function formatCompletion(iterations: number): string {
  return `${ANSI_GREEN}✓ done${ANSI_RESET} (${iterations} iterations)`;
}
