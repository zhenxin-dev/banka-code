/**
 * 运行时配置加载器。
 *
 * @author 真心
 */

/**
 * 可用的 provider 类型。
 *
 * openai: OpenAI Responses API（默认）。
 * openai-chat: OpenAI Chat Completions API（兼容不支持 Responses API 的第三方服务）。
 * anthropic: Anthropic 原生 API。
 */
export type ProviderKind = "openai" | "openai-chat" | "anthropic";

/**
 * Banka Code 运行时配置。
 */
export interface RuntimeConfig {
  readonly workspaceRoot: string;
  readonly provider: ProviderKind;
  readonly model: string;
  readonly apiKey?: string;
  readonly baseUrl?: string;
}

/**
 * 从环境变量加载运行时配置。
 */
export function loadRuntimeConfig(workspaceRoot: string): RuntimeConfig {
  const provider = parseProviderKind(Bun.env.BANKA_PROVIDER?.trim());
  const apiKey = Bun.env.BANKA_API_KEY?.trim();
  const baseUrl = Bun.env.BANKA_BASE_URL?.trim();
  const model = Bun.env.BANKA_MODEL?.trim();

  if (model === undefined || model === "") {
    throw new Error("缺少 BANKA_MODEL 配置。必须显式指定模型。");
  }

  if (apiKey === undefined || apiKey === "") {
    throw new Error("缺少 BANKA_API_KEY 配置。请设置 BANKA_API_KEY 环境变量。");
  }

  return {
    workspaceRoot,
    provider,
    model,
    apiKey,
    ...(baseUrl === undefined || baseUrl === "" ? {} : { baseUrl })
  };
}

function parseProviderKind(value: string | undefined): ProviderKind {
  if (value === undefined || value === "") {
    return "openai";
  }

  switch (value) {
    case "openai":
    case "openai-chat":
    case "anthropic":
      return value;
    default:
      return "openai";
  }
}
