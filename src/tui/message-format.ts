/**
 * TUI 消息格式化工具。
 *
 * @author 真心
 */

/**
 * 判断一条输入是否为退出命令。
 */
export function isExitCommand(value: string): boolean {
  const prompt = value.trim();
  return prompt === "/exit" || prompt === "/quit";
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
