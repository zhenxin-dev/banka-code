/**
 * TUI 消息格式化测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { isExitCommand, titledRule, toDisplayLines } from "./message-format.ts";

describe("tui message format", () => {
  it("recognizes exit commands", () => {
    expect(isExitCommand("/exit")).toBe(true);
    expect(isExitCommand(" /quit ")).toBe(true);
    expect(isExitCommand("hello")).toBe(false);
  });

  it("splits content into display lines", () => {
    expect(toDisplayLines("a\nb")).toEqual(["a", "b"]);
    expect(toDisplayLines("a\r\nb")).toEqual(["a", "b"]);
  });

  it("creates a titled divider rule", () => {
    const rule = titledRule(20, "chat");

    expect(rule).toContain(" chat ");
    expect(rule.length).toBe(20);
  });
});
