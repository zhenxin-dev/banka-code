/**
 * Spinner 组件 — 对标 cc-haha 的 Spinner.tsx。
 *
 * Braille 动画帧 + 可选动词文本。
 *
 * @author 真心
 */

import type { JSX } from "@opentui/solid";
import { createSignal, onCleanup } from "solid-js";
import { getTheme } from "./theme.ts";

const SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"] as const;
const FRAME_INTERVAL = 120;

export interface SpinnerProps {
  /** 可选动词文本，如 "thinking" */
  readonly verb?: string;
}

/**
 * 渲染 braille 动画 Spinner。
 *
 * @param props - Spinner 配置
 * @returns Spinner 组件
 */
export function Spinner(props: SpinnerProps): JSX.Element {
  const t = getTheme();
  const [frame, setFrame] = createSignal(0);
  const interval = setInterval(() => {
    setFrame((f) => (f + 1) % SPINNER_FRAMES.length);
  }, FRAME_INTERVAL);

  onCleanup(() => clearInterval(interval));

  return (
    <box flexDirection="row">
      <text fg={t.text}>{SPINNER_FRAMES[frame()]}</text>
      {props.verb !== undefined && props.verb !== "" && (
        <text fg={t.inactive}> {props.verb}</text>
      )}
    </box>
  );
}
