/**
 * 文件工具测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createEditFileTool } from "./file-tools.ts";

describe("Edit tool", () => {
  it("writes replacement text literally even when it contains dollar patterns", async () => {
    const workspaceRoot = await mkdtemp(join(tmpdir(), "banka-edit-file-"));
    const filePath = join(workspaceRoot, "sample.txt");

    try {
      await Bun.write(filePath, "hello world\n");

      const tool = createEditFileTool();
      const result = await tool.execute(
        {
          path: "sample.txt",
          oldText: "hello",
          newText: "$&-literal"
        },
        { workspaceRoot }
      );

      expect(result.isError).toBe(false);
      expect(await Bun.file(filePath).text()).toBe("$&-literal world\n");
    } finally {
      await rm(workspaceRoot, { recursive: true, force: true });
    }
  });
});
