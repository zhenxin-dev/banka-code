/**
 * banka 错误类型定义。
 *
 * @author 真心
 */

/**
 * banka 的基础错误类型。
 */
export class BankaError extends Error {
  public constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = new.target.name;
  }
}

/**
 * 运行时配置错误。
 */
export class ConfigurationError extends BankaError {}

/**
 * 模型响应格式错误。
 */
export class ModelResponseError extends BankaError {}

/**
 * 工具执行错误。
 */
export class ToolExecutionError extends BankaError {}

/**
 * 工作区路径越界错误。
 */
export class PathSecurityError extends BankaError {}
