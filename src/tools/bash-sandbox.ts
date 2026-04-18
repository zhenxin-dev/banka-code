/**
 * Bash 命令安全校验模块。
 *
 * 在应用层拦截可能越出工作区的 shell 命令，包括绝对路径访问、
 * 提权操作、危险环境变量篡改、重定向到工作区外等模式。
 *
 * @author 真心
 */

import { isAbsolute, relative, resolve } from "node:path";

/** 不允许通过 export 或内联赋值修改的危险环境变量 */
const DANGEROUS_ENV_VARS: ReadonlySet<string> = new Set([
  "PATH",
  "HOME",
  "LD_PRELOAD",
  "LD_LIBRARY_PATH",
  "DYLD_INSERT_LIBRARIES",
  "SHELL",
  "USER",
  "LOGNAME"
]);

/** 危险环境变量的正则拼接，用于构建匹配模式 */
const DANGEROUS_ENV_PATTERN = [...DANGEROUS_ENV_VARS].join("|");

/**
 * 校验 shell 命令是否在工作区安全范围内。
 *
 * @param command 待校验的 shell 命令
 * @param workspaceRoot 工作区根目录的绝对路径
 * @returns 若命令安全则返回 null，否则返回错误消息
 */
export function validateCommand(command: string, workspaceRoot: string): string | null {
  const trimmed = command.trim();

  if (trimmed === "") {
    return null;
  }

  // 0. 拒绝 Windows 绝对路径（C:\...），需在 tokenize 之前检测因为 \ 是分隔符
  const windowsPathMatch = trimmed.match(/[A-Za-z]:[\\/]/);
  if (windowsPathMatch !== null) {
    return `Windows absolute paths are not allowed: ${windowsPathMatch[0]}...`;
  }

  // 1. 拒绝提权命令（sudo、su）
  if (/\b(sudo|su)\b/.test(trimmed)) {
    return "Privilege escalation commands (sudo, su) are not allowed.";
  }

  // 2. 拒绝危险环境变量操作：export VAR=... 或内联 VAR=...
  const envMatch = trimmed.match(
    new RegExp(`\\b(?:export\\s+)?(${DANGEROUS_ENV_PATTERN})=`, "g")
  );
  if (envMatch !== null) {
    const varName = envMatch[0].match(new RegExp(`\\b(${DANGEROUS_ENV_PATTERN})=`))?.[1];
    if (varName !== undefined) {
      return `Manipulating environment variable '${varName}' is not allowed.`;
    }
  }

  // 3. 拒绝重定向到工作区外的路径（> /path、>> /path）
  const redirectMatches = trimmed.matchAll(/>>?\s*([^\s;|&>]+)/g);
  for (const match of redirectMatches) {
    const target = match[1];
    if (target !== undefined && isPathEscape(target, workspaceRoot)) {
      return `Redirect target escapes workspace: ${target}`;
    }
  }

  // 4. 拒绝命令 token 中的越界路径
  const tokens = tokenizeCommand(trimmed);
  for (const token of tokens) {
    if (isPathEscape(token, workspaceRoot)) {
      return `Path argument escapes workspace: ${token}`;
    }
  }

  return null;
}

/**
 * 将命令拆分为 token（简化版，不处理引号嵌套和子命令）。
 *
 * 按 shell 操作符和空白字符拆分，足以捕获常见的路径参数。
 */
function tokenizeCommand(command: string): string[] {
  return command
    .split(/[\s;|&<>()$`'"\\]+/)
    .filter((token) => token.length > 0);
}

/**
 * 判断路径标记是否越出工作区。
 *
 * 检测三类路径：
 * - Windows 绝对路径（C:\...）：直接视为越界
 * - Unix 绝对路径（/...）：判断是否在工作区内
 * - 含 .. 的相对路径：解析后判断是否越界
 */
function isPathEscape(pathStr: string, workspaceRoot: string): boolean {
  // Unix 绝对路径
  if (pathStr.startsWith("/")) {
    return !isWithinWorkspace(pathStr, workspaceRoot);
  }

  // 含 .. 的相对路径（仅匹配目录遍历模式，排除 foo..bar 这类无意义匹配）
  if (/(?:^|\/)\.\.(?:\/|$)/.test(pathStr)) {
    const resolved = resolve(workspaceRoot, pathStr);
    return !isWithinWorkspace(resolved, workspaceRoot);
  }

  return false;
}

/**
 * 判断路径是否在工作区内。
 *
 * 与 safe-path.ts 中的逻辑一致：通过 relative 计算相对路径，
 * 若结果以 .. 开头或为绝对路径，则说明越界。
 */
function isWithinWorkspace(pathStr: string, workspaceRoot: string): boolean {
  const normalizedRoot = resolve(workspaceRoot);
  const resolved = resolve(pathStr);
  const rel = relative(normalizedRoot, resolved);
  return rel === "" || (!rel.startsWith("..") && !isAbsolute(rel));
}
