/**
 * TUI 输出格式化测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import {
  formatAssistantReply,
  formatBanner,
  formatCompletion,
  formatError,
  formatToolCall
} from "./output.ts";

describe("tui output", () => {
  it("formats banner with provider and model", () => {
    const banner = formatBanner("mock", "banka-mock");

    expect(banner).toContain("Banka Code");
    expect(banner).toContain("Provider: mock | Model: banka-mock");
  });

  it("formats assistant reply", () => {
    expect(formatAssistantReply("hello")).toContain("hello");
  });

  it("formats tool call and error output", () => {
    expect(formatToolCall("Read")).toContain("Read");
    expect(formatError("boom")).toContain("boom");
    expect(formatCompletion(3)).toContain("3 iterations");
  });
});
