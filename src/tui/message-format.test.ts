/**
 * TUI 消息格式化测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { findBuiltinCommands, getBuiltinCommands, isExitCommand, parseBuiltinCommand, titledRule, toDisplayLines } from "./message-format.ts";

describe("tui message format", () => {
  it("recognizes exit commands", () => {
    expect(isExitCommand("/exit")).toBe(true);
    expect(isExitCommand(" /quit ")).toBe(true);
    expect(isExitCommand("hello")).toBe(false);
  });

  it("parses builtin commands", () => {
    expect(parseBuiltinCommand(" /help ")).toEqual({ name: "help", raw: "/help" });
    expect(parseBuiltinCommand("/status")).toEqual({ name: "status", raw: "/status" });
    expect(parseBuiltinCommand("hello")).toBeUndefined();
  });

  it("lists builtin commands", () => {
    expect(getBuiltinCommands()).toEqual([
      { name: "help", command: "/help", description: "查看内置命令帮助" },
      { name: "clear", command: "/clear", description: "清空当前会话内容" },
      { name: "status", command: "/status", description: "查看当前会话状态" },
      { name: "exit", command: "/exit", description: "退出 Banka Code" },
      { name: "quit", command: "/quit", description: "退出 Banka Code" }
    ]);
  });

  it("finds builtin command suggestions by prefix", () => {
    expect(findBuiltinCommands("/").map((command) => command.command)).toEqual([
      "/help",
      "/clear",
      "/status",
      "/exit",
      "/quit"
    ]);
    expect(findBuiltinCommands("/st")).toEqual([
      { name: "status", command: "/status", description: "查看当前会话状态" }
    ]);
    expect(findBuiltinCommands("hello")).toEqual([]);
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
