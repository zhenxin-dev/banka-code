/**
 * Grep 工具测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createGrepTool } from "./grep-tool.ts";

describe("Grep tool", () => {
  it("returns matching content lines with line numbers", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-grep-"));

    try {
      await mkdir(join(workspaceRoot, "src"), { recursive: true });
      await Bun.write(
        join(workspaceRoot, "src", "app.ts"),
        "const title = 'Banka';\nconst theme = 'Senren';\n"
      );

      const tool = createGrepTool();
      const result = await tool.execute(
        { pattern: "Banka", include: "src/**/*.ts" },
        { workspaceRoot }
      );

      expect(result.isError).toBe(false);
      expect(result.content).toContain("src/app.ts:1: const title = 'Banka';");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("supports files_with_matches output mode", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-grep-files-"));

    try {
      await mkdir(join(workspaceRoot, "src"), { recursive: true });
      await Bun.write(join(workspaceRoot, "src", "one.ts"), "match me\n");
      await Bun.write(join(workspaceRoot, "src", "two.ts"), "nope\n");

      const tool = createGrepTool();
      const result = await tool.execute(
        { pattern: "match", include: "src/**/*.ts", outputMode: "files_with_matches" },
        { workspaceRoot }
      );

      expect(result.isError).toBe(false);
      expect(result.content).toContain("src/one.ts");
      expect(result.content).not.toContain(":1:");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("rejects an invalid regular expression", async () => {
    const tool = createGrepTool();
    let error: unknown;

    try {
      await tool.execute({ pattern: "[" }, { workspaceRoot: "/tmp" });
    } catch (caughtError) {
      error = caughtError;
    }

    expect(error).toBeInstanceOf(Error);
    expect(String(error)).toContain("Invalid grep pattern");
  });
});
