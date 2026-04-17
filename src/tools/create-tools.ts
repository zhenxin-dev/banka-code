/**
 * MVP 工具集合创建器。
 *
 * @author 真心
 */

import { createBashTool } from "./bash-tool.ts";
import { createEditFileTool, createReadFileTool, createWriteFileTool } from "./file-tools.ts";
import { createGlobTool } from "./glob-tool.ts";
import { createGrepTool } from "./grep-tool.ts";
import type { ToolDefinition } from "./tool.ts";

/**
 * 创建 banka 首版可用的基础工具集。
 */
export function createTools(): readonly ToolDefinition[] {
  return [
    createBashTool(),
    createReadFileTool(),
    createWriteFileTool(),
    createEditFileTool(),
    createGlobTool(),
    createGrepTool()
  ];
}
