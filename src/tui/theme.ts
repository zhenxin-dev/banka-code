/**
 * banka TUI 主题色彩系统。
 *
 * 对标 cc-haha 的 theme.ts，适配 banka 品牌色（丛雨/和风）。
 * 所有颜色使用 hex 格式，与 OpenTUI 的 fg/bg prop 直接兼容。
 *
 * @author 真心
 */

/** 主题色值表 */
export interface ThemeColors {
  /** 主文本 — 白 */
  readonly text: string;
  /** 反色文本 — 黑 */
  readonly inverseText: string;
  /** 品牌色 — 丛雨金（对标 cc-haha 的 claude orange） */
  readonly brand: string;
  /** 品牌色亮 — 用于 shimmer / 高亮 */
  readonly brandShimmer: string;
  /** 不活跃文本 — 浅灰 */
  readonly inactive: string;
  /** 微弱文本 — 深灰 */
  readonly subtle: string;
  /** 成功 */
  readonly success: string;
  /** 错误 */
  readonly error: string;
  /** 警告 */
  readonly warning: string;
  /** 建议色 — 蓝紫 */
  readonly suggestion: string;
  /** 用户消息背景 */
  readonly userMessageBackground: string;
  /** 用户消息 hover 背景 */
  readonly userMessageBackgroundHover: string;
  /** 消息操作背景（选中态） */
  readonly messageActionsBackground: string;
  /** 输入框边框 */
  readonly promptBorder: string;
  /** bash/tool 边框 — 樱粉 */
  readonly bashBorder: string;
  /** bash 消息背景 */
  readonly bashMessageBackgroundColor: string;
  /** 文本选中背景 */
  readonly selectionBg: string;
  /** 状态栏文本 */
  readonly statusText: string;
  /** 提示文本 */
  readonly hintText: string;
  /** 分隔线 */
  readonly divider: string;
  /** 活跃指示 */
  readonly active: string;
  /** 用户消息标记色 */
  readonly user: string;
  /** 助手标签色 */
  readonly assistantLabel: string;
  /** 助手正文色 */
  readonly assistantBody: string;
  /** 工具/次要信息色 */
  readonly tool: string;
  /** diff 新增 */
  readonly diffAdded: string;
  /** diff 删除 */
  readonly diffRemoved: string;
}

/** 暗色主题（默认） */
export const DARK_THEME: ThemeColors = {
  text: "#fff6f0",
  inverseText: "#000000",
  brand: "#ff8fb4",
  brandShimmer: "#ffd2df",
  inactive: "#f4e6df",
  subtle: "#d8bbb0",
  success: "#7fb069",
  error: "#ff9aa6",
  warning: "#ffc27a",
  suggestion: "#f6b37d",
  userMessageBackground: "#2b1717",
  userMessageBackgroundHover: "#382020",
  messageActionsBackground: "#352626",
  promptBorder: "#b89a9644",
  bashBorder: "#ff8bb8",
  bashMessageBackgroundColor: "#241313",
  selectionBg: "#6a353e",
  statusText: "#ffe5cb",
  hintText: "#d4bdb6",
  divider: "#c9a09c",
  active: "#ff8fb4",
  user: "#ffd4a0",
  assistantLabel: "#ffd8e6",
  assistantBody: "#fff7f2",
  tool: "#e8c4a4",
  diffAdded: "#356b3d",
  diffRemoved: "#8f3d4d",
};

/** 当前激活主题（默认暗色） */
let currentTheme: ThemeColors = DARK_THEME;

/**
 * 获取当前主题色值。
 *
 * @returns 当前主题色值表
 */
export function getTheme(): ThemeColors {
  return currentTheme;
}

/**
 * 设置当前主题。
 *
 * @param theme - 目标主题
 */
export function setTheme(theme: ThemeColors): void {
  currentTheme = theme;
}
