/**
 * 安全路径测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { join } from "node:path";
import { PathSecurityError } from "../errors/banka-error.ts";
import { resolveSafePath } from "./safe-path.ts";

describe("resolveSafePath", () => {
  it("resolves a child path inside workspace", () => {
    const workspaceRoot = "/tmp/banka";
    const resolvedPath = resolveSafePath(workspaceRoot, "src/index.ts");

    expect(resolvedPath).toBe(join(workspaceRoot, "src/index.ts"));
  });

  it("rejects a path that escapes the workspace", () => {
    const workspaceRoot = "/tmp/banka";

    expect(() => resolveSafePath(workspaceRoot, "../secret.txt")).toThrow(PathSecurityError);
  });
});
