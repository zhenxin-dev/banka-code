/**
 * bubblewrap (bwrap) 沙箱集成。
 *
 * 当系统安装了 bwrap 时，Bash 工具的子进程会在隔离沙箱中执行；
 * 未安装时自动回退到直接执行（当前行为）。
 *
 * @author 真心
 */

import { existsSync } from "node:fs";

/**
 * 子进程启动选项。
 */
interface SpawnOptions {
  readonly stdout: "pipe";
  readonly stderr: "pipe";
  readonly stdin: "ignore";
}

/** bwrap 可用性缓存：undefined = 未检测，true/false = 已检测 */
let bwrapAvailable: boolean | undefined;

/** 需要传递到沙箱内的最小环境变量名列表 */
const MINIMAL_ENV_KEYS = ["HOME", "PATH", "TMPDIR", "LANG", "TERM"] as const;

/** 沙箱内以只读方式挂载的系统目录 */
const READONLY_BIND_DIRS = ["/usr", "/bin", "/lib", "/lib64", "/etc"] as const;

/**
 * 检测 bwrap 是否可用。结果会被缓存，整个进程生命周期只检测一次。
 */
export async function isBubblewrapAvailable(): Promise<boolean> {
  if (bwrapAvailable !== undefined) {
    return bwrapAvailable;
  }

  try {
    const proc = Bun.spawn({
      cmd: ["bwrap", "--version"],
      stdin: "ignore",
      stdout: "pipe",
      stderr: "pipe"
    });
    const exitCode = await proc.exited;
    bwrapAvailable = exitCode === 0;
    return bwrapAvailable;
  } catch (_error: unknown) {
    bwrapAvailable = false;
    return false;
  }
}

/**
 * 直接启动子进程（无沙箱），即原始的 Bun.spawn 行为。
 */
export function spawnDirect(
  command: string,
  workspaceRoot: string,
  options: SpawnOptions
): Bun.Subprocess<"ignore", "pipe", "pipe"> {
  return Bun.spawn({
    cmd: ["zsh", "-lc", command],
    cwd: workspaceRoot,
    stdin: options.stdin,
    stdout: options.stdout,
    stderr: options.stderr
  });
}

/**
 * 在 bwrap 沙箱中启动子进程。
 *
 * 沙箱策略：
 * - `--unshare-all` 隔离所有命名空间
 * - 系统目录（/usr, /bin, /lib, /lib64, /etc）只读挂载
 * - 工作区读写挂载
 * - /tmp 使用 tmpfs（沙箱内独立）
 * - /proc 和 /dev 按需挂载以支持常见命令
 * - 仅传递最小环境变量 + BANKA_* 变量
 */
export function spawnSandboxed(
  command: string,
  workspaceRoot: string,
  options: SpawnOptions
): Bun.Subprocess<"ignore", "pipe", "pipe"> {
  const args = buildBwrapArgs(command, workspaceRoot);
  return Bun.spawn({
    cmd: args,
    cwd: workspaceRoot,
    stdin: options.stdin,
    stdout: options.stdout,
    stderr: options.stderr
  });
}

/**
 * 构建 bwrap 命令参数列表。
 */
function buildBwrapArgs(command: string, workspaceRoot: string): string[] {
  const args: string[] = [
    "bwrap",
    "--unshare-all",
    "--new-session",
    "--die-with-parent",
    "--clearenv",
    "--proc",
    "/proc",
    "--dev",
    "/dev"
  ];

  for (const dir of READONLY_BIND_DIRS) {
    if (existsSync(dir)) {
      args.push("--ro-bind", dir, dir);
    }
  }

  args.push("--tmpfs", "/tmp");
  args.push("--bind", workspaceRoot, workspaceRoot);

  const envVars = collectMinimalEnv();
  for (const [key, value] of Object.entries(envVars)) {
    args.push("--setenv", key, value);
  }

  args.push("zsh", "-lc", command);
  return args;
}

/**
 * 收集需要传入沙箱的最小环境变量集合。
 */
function collectMinimalEnv(): Record<string, string> {
  const env: Record<string, string> = {};

  for (const key of MINIMAL_ENV_KEYS) {
    const value = process.env[key];
    if (value !== undefined && value !== "") {
      env[key] = value;
    }
  }

  for (const [key, value] of Object.entries(process.env)) {
    if (key.startsWith("BANKA_") && value !== undefined && value !== "") {
      env[key] = value;
    }
  }

  return env;
}

export type { SpawnOptions };
