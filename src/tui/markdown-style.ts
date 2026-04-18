/**
 * Markdown 语法样式 — 基于千恋万花主题色。
 *
 * 为 OpenTUI MarkdownRenderable 提供 SyntaxStyle，
 * 覆盖内联 Markdown 元素（粗体、斜体、代码、链接等）的渲染样式。
 *
 * @author 真心
 */

import { RGBA, SyntaxStyle } from "@opentui/core";
import type { ThemeColors } from "./theme.ts";

/**
 * 基于当前主题创建 Markdown 语法样式。
 *
 * @param theme - 当前主题色值表
 * @returns 配置好的 SyntaxStyle 实例
 */
export function createMarkdownSyntaxStyle(theme: ThemeColors): SyntaxStyle {
  return SyntaxStyle.fromStyles({
    "default": {
      fg: RGBA.fromHex(theme.assistantBody),
    },

    // ── 内联样式 ──

    /** 粗体 — 品牌亮色 + bold */
    "markup.strong": {
      fg: RGBA.fromHex(theme.brandShimmer),
      bold: true,
    },

    /** 斜体 — 微弱色 + italic */
    "markup.italic": {
      fg: RGBA.fromHex(theme.subtle),
      italic: true,
    },

    /** 删除线 — 暗淡 */
    "markup.strikethrough": {
      fg: RGBA.fromHex(theme.inactive),
      dim: true,
    },

    /** 行内代码 — 工具色 */
    "markup.raw": {
      fg: RGBA.fromHex(theme.tool),
    },

    // ── 链接 ──

    /** 链接括号与装饰符 */
    "markup.link": {
      fg: RGBA.fromHex(theme.suggestion),
    },

    /** 链接 URL — 下划线 */
    "markup.link.url": {
      fg: RGBA.fromHex(theme.suggestion),
      underline: true,
    },

    /** 链接文本 */
    "markup.link.label": {
      fg: RGBA.fromHex(theme.suggestion),
    },

    // ── 块级 ──

    /** 标题 — 品牌色 + bold（用于表格头部等） */
    "markup.heading": {
      fg: RGBA.fromHex(theme.brand),
      bold: true,
    },

    /** 隐藏标记色 — 分隔线色（用于表格边框等） */
    "conceal": {
      fg: RGBA.fromHex(theme.divider),
    },
  });
}
