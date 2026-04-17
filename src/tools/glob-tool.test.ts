/**
 * Glob 工具测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createGlobTool } from "./glob-tool.ts";

describe("Glob tool", () => {
  it("finds matching files under the workspace", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-glob-"));

    try {
      await mkdir(join(workspaceRoot, "src"), { recursive: true });
      await Bun.write(join(workspaceRoot, "src", "app.ts"), "export const app = true;\n");
      await Bun.write(join(workspaceRoot, "src", "theme.ts"), "export const theme = true;\n");
      await Bun.write(join(workspaceRoot, "README.md"), "# banka\n");

      const tool = createGlobTool();
      const result = await tool.execute({ pattern: "src/**/*.ts" }, { workspaceRoot });

      expect(result.isError).toBe(false);
      expect(result.content).toContain("src/app.ts");
      expect(result.content).toContain("src/theme.ts");
      expect(result.content).not.toContain("README.md");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("scopes matches to the provided path", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-glob-scope-"));

    try {
      await mkdir(join(workspaceRoot, "src"), { recursive: true });
      await mkdir(join(workspaceRoot, "test"), { recursive: true });
      await Bun.write(join(workspaceRoot, "src", "main.ts"), "console.log('src');\n");
      await Bun.write(join(workspaceRoot, "test", "main.ts"), "console.log('test');\n");

      const tool = createGlobTool();
      const result = await tool.execute(
        { pattern: "**/*.ts", path: "src" },
        { workspaceRoot }
      );

      expect(result.content).toContain("src/main.ts");
      expect(result.content).not.toContain("test/main.ts");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });

  it("rejects an empty pattern", async () => {
    const tool = createGlobTool();
    let error: unknown;

    try {
      await tool.execute({ pattern: "" }, { workspaceRoot: "/tmp" });
    } catch (caughtError) {
      error = caughtError;
    }

    expect(error).toBeInstanceOf(Error);
    expect(String(error)).toContain("Glob tool requires a non-empty 'pattern' string.");
  });
});
