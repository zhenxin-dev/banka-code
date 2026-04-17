/**
 * 工作区安全路径解析。
 *
 * @author 真心
 */

import { isAbsolute, relative, resolve } from "node:path";
import { PathSecurityError } from "../errors/banka-error.ts";

/**
 * 在工作区内解析并校验目标路径。
 */
export function resolveSafePath(workspaceRoot: string, targetPath: string): string {
  const normalizedWorkspaceRoot = resolve(workspaceRoot);
  const candidatePath = resolve(normalizedWorkspaceRoot, targetPath);
  const relativePath = relative(normalizedWorkspaceRoot, candidatePath);

  if (
    relativePath === "" ||
    (!relativePath.startsWith("..") && !isAbsolute(relativePath))
  ) {
    return candidatePath;
  }

  throw new PathSecurityError(`Path escapes workspace root: ${targetPath}`);
}
