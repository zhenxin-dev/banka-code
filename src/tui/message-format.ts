/**
 * TUI 消息格式化工具。
 *
 * @author 真心
 */

/**
 * TUI 内置命令名。
 */
export type BuiltinCommandName = "help" | "clear" | "status" | "exit" | "quit";

/**
 * TUI 内置命令描述。
 */
export interface BuiltinCommandDefinition {
  readonly name: BuiltinCommandName;
  readonly command: `/${BuiltinCommandName}`;
  readonly description: string;
}

/**
 * 解析后的内置命令。
 */
export interface ParsedBuiltinCommand {
  readonly name: BuiltinCommandName;
  readonly raw: string;
}

const BUILTIN_COMMANDS: readonly BuiltinCommandDefinition[] = [
  { name: "help", command: "/help", description: "查看内置命令帮助" },
  { name: "clear", command: "/clear", description: "清空当前会话内容" },
  { name: "status", command: "/status", description: "查看当前会话状态" },
  { name: "exit", command: "/exit", description: "退出 Banka Code" },
  { name: "quit", command: "/quit", description: "退出 Banka Code" }
] as const;

const BUILTIN_COMMAND_ALIASES: Readonly<Record<string, BuiltinCommandName>> = {
  "/help": "help",
  "/clear": "clear",
  "/status": "status",
  "/exit": "exit",
  "/quit": "quit"
};

/**
 * 返回全部内置命令定义。
 */
export function getBuiltinCommands(): readonly BuiltinCommandDefinition[] {
  return BUILTIN_COMMANDS;
}

/**
 * 解析一条输入中的内置命令。
 */
export function parseBuiltinCommand(value: string): ParsedBuiltinCommand | undefined {
  const prompt = value.trim().toLowerCase();
  const name = BUILTIN_COMMAND_ALIASES[prompt];

  if (name === undefined) {
    return undefined;
  }

  return {
    name,
    raw: prompt
  };
}

/**
 * 根据输入前缀返回匹配的内置命令。
 */
export function findBuiltinCommands(value: string): readonly BuiltinCommandDefinition[] {
  const prompt = value.trim().toLowerCase();

  if (!prompt.startsWith("/")) {
    return [];
  }

  if (prompt === "/") {
    return BUILTIN_COMMANDS;
  }

  return BUILTIN_COMMANDS.filter((command) => command.command.startsWith(prompt));
}

/**
 * 判断一条输入是否为退出命令。
 */
export function isExitCommand(value: string): boolean {
  const command = parseBuiltinCommand(value);
  return command?.name === "exit" || command?.name === "quit";
}

/**
 * 将消息文本拆成适合逐行渲染的内容。
 */
export function toDisplayLines(content: string): readonly string[] {
  return content.replace(/\r\n/g, "\n").split("\n");
}

/**
 * 为多行消息体添加缩进前缀（首行除外）。
 */
export function indentBody(lines: readonly string[], prefix: string): readonly string[] {
  if (lines.length === 0) {
    return lines;
  }

  const first = lines[0] ?? "";
  return [first, ...lines.slice(1).map((line) => `${prefix}${line}`)];
}

/**
 * 水平分隔线。
 */
export function horizontalRule(width: number, char: string = "─"): string {
  return char.repeat(Math.max(1, width));
}

/**
 * 生成带标题的水平分隔线。
 */
export function titledRule(
  width: number,
  title: string,
  char: string = "─"
): string {
  const safeWidth = Math.max(1, width);
  const normalizedTitle = title.trim();

  if (normalizedTitle === "" || safeWidth <= normalizedTitle.length + 2) {
    return horizontalRule(safeWidth, char);
  }

  const decoratedTitle = ` ${normalizedTitle} `;
  const sideWidth = Math.max(0, safeWidth - decoratedTitle.length);
  const leftWidth = Math.floor(sideWidth / 2);
  const rightWidth = sideWidth - leftWidth;

  return `${char.repeat(leftWidth)}${decoratedTitle}${char.repeat(rightWidth)}`;
}
