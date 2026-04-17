/**
 * 工具注册表。
 *
 * @author 真心
 */

import { ToolExecutionError } from "../errors/banka-error.ts";
import type { ToolDefinition } from "./tool.ts";

/**
 * 负责按名称索引工具定义。
 */
export class ToolRegistry {
  readonly #tools: ReadonlyMap<string, ToolDefinition>;
  readonly #toolList: readonly ToolDefinition[];

  public constructor(tools: readonly ToolDefinition[]) {
    const toolEntries = new Map<string, ToolDefinition>();

    for (const tool of tools) {
      if (toolEntries.has(tool.name)) {
        throw new ToolExecutionError(`Duplicate tool name: ${tool.name}`);
      }

      toolEntries.set(tool.name, tool);
    }

    this.#tools = toolEntries;
    this.#toolList = [...tools];
  }

  /**
   * 返回全部工具定义。
   */
  public list(): readonly ToolDefinition[] {
    return this.#toolList;
  }

  /**
   * 根据名称读取工具定义。
   */
  public get(name: string): ToolDefinition | undefined {
    return this.#tools.get(name);
  }
}
