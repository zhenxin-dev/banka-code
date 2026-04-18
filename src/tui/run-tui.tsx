/**
 * OpenTUI 运行入口。
 *
 * @author 真心
 */

import { render } from "@opentui/solid";
import type { LanguageModel } from "ai";
import type { RuntimeConfig } from "../runtime/runtime-config.ts";
import type { ToolExecutionContext } from "../tools/tool.ts";
import { ToolRegistry } from "../tools/tool-registry.ts";
import { TuiApp } from "./app.tsx";

/**
 * TUI 运行参数。
 */
export interface TuiRunOptions {
  readonly systemPrompt: string;
  readonly runtimeConfig: RuntimeConfig;
  readonly languageModel: LanguageModel;
  readonly toolRegistry: ToolRegistry;
  readonly toolContext: ToolExecutionContext;
  readonly maxIterations: number;
}

/**
 * 启动 banka 的 OpenTUI 终端界面。
 */
export async function runTui(options: TuiRunOptions): Promise<void> {
  await render(
    () => (
      <TuiApp
        systemPrompt={options.systemPrompt}
        runtimeConfig={options.runtimeConfig}
        languageModel={options.languageModel}
        toolRegistry={options.toolRegistry}
        toolContext={options.toolContext}
        maxIterations={options.maxIterations}
      />
    ),
    {
      targetFps: 30,
      screenMode: "alternate-screen",
      useMouse: true,
      exitOnCtrlC: true
    }
  );
}
