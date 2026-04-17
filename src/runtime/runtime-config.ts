/**
 * 运行时配置加载器。
 *
 * @author 真心
 */

/**
 * 可用的 provider 类型。
 */
export type ProviderKind = "mock" | "openai-compatible" | "anthropic-compatible" | "ollama";

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

  if (isIncompleteApiConfig(provider, apiKey, baseUrl)) {
    return {
      workspaceRoot,
      provider: "mock",
      model: "bankacode-mock"
    };
  }

  if (model === undefined || model === "") {
    throw new Error("缺少 BANKA_MODEL 配置。已配置 API 访问参数时，必须显式指定模型。");
  }

  return {
    workspaceRoot,
    provider,
    model,
    ...(apiKey === undefined || apiKey === "" ? {} : { apiKey }),
    ...(baseUrl === undefined || baseUrl === "" ? {} : { baseUrl })
  };
}

function parseProviderKind(value: string | undefined): ProviderKind {
  if (value === undefined || value === "") {
    return "openai-compatible";
  }

  switch (value) {
    case "mock":
    case "openai-compatible":
    case "anthropic-compatible":
    case "ollama":
      return value;
    default:
      return "openai-compatible";
  }
}

function isIncompleteApiConfig(
  provider: ProviderKind,
  apiKey: string | undefined,
  baseUrl: string | undefined
): boolean {
  if (baseUrl === undefined || baseUrl === "") {
    return true;
  }

  if (provider === "ollama") {
    return false;
  }

  return apiKey === undefined || apiKey === "";
}
