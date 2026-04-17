/**
 * 工具系统核心类型定义。
 *
 * @author 真心
 */

/**
 * JSON Schema 属性定义。
 */
export interface JsonSchemaProperty {
  readonly type: "string" | "number" | "boolean";
  readonly description: string;
}

/**
 * 工具输入的对象 Schema。
 */
export interface JsonSchemaObject {
  readonly type: "object";
  readonly properties: Readonly<Record<string, JsonSchemaProperty>>;
  readonly required: readonly string[];
  readonly additionalProperties: boolean;
}

/**
 * 工具原始参数对象。
 */
export interface ToolArguments {
  readonly [key: string]: unknown;
}

/**
 * 工具执行上下文。
 */
export interface ToolExecutionContext {
  readonly workspaceRoot: string;
}

/**
 * 工具执行结果。
 */
export interface ToolResult {
  readonly content: string;
  readonly isError: boolean;
}

/**
 * 单个工具定义。
 */
export interface ToolDefinition {
  readonly name: string;
  readonly description: string;
  readonly inputSchema: JsonSchemaObject;
  execute(arguments_: ToolArguments, context: ToolExecutionContext): Promise<ToolResult>;
}
