/**
 * 模型客户端工厂。
 *
 * @author 真心
 */

import { ConfigurationError } from "../errors/banka-error.ts";
import type { RuntimeConfig } from "../runtime/runtime-config.ts";
import type { ModelClient } from "./model-client.ts";
import {
  AnthropicCompatibleModelClient,
  getDefaultAnthropicTimeoutMs
} from "./anthropic-compatible-model-client.ts";
import { MockModelClient } from "./mock-model-client.ts";
import {
  getDefaultOpenAITimeoutMs,
  OpenAICompatibleModelClient
} from "./openai-compatible-model-client.ts";

/**
 * 根据运行时配置创建模型客户端。
 */
export function createModelClient(config: RuntimeConfig): ModelClient {
  if (config.provider === "mock") {
    return new MockModelClient();
  }

  const baseUrl = config.baseUrl;

  if (baseUrl === undefined) {
    throw new ConfigurationError(`${config.provider} provider requires baseUrl.`);
  }

  if (config.provider !== "ollama" && config.apiKey === undefined) {
    throw new ConfigurationError(`${config.provider} provider requires apiKey and baseUrl.`);
  }

  const apiKey = config.apiKey ?? "ollama";

  if (config.provider === "anthropic-compatible") {
    return new AnthropicCompatibleModelClient({
      apiKey,
      baseUrl,
      model: config.model,
      timeoutMs: getDefaultAnthropicTimeoutMs()
    });
  }

  if (config.provider === "ollama") {
    return new OpenAICompatibleModelClient({
      apiKey: normalizeOllamaApiKey(config.apiKey),
      baseUrl: normalizeOllamaBaseUrl(baseUrl),
      model: config.model,
      timeoutMs: getDefaultOpenAITimeoutMs()
    });
  }

  return new OpenAICompatibleModelClient({
    apiKey,
    baseUrl,
    model: config.model,
    timeoutMs: getDefaultOpenAITimeoutMs()
  });
}

function normalizeOllamaApiKey(apiKey: string | undefined): string {
  if (apiKey === undefined || apiKey === "") {
    return "ollama";
  }

  return apiKey;
}

function normalizeOllamaBaseUrl(baseUrl: string): string {
  const withScheme = hasUrlScheme(baseUrl) ? baseUrl : `http://${baseUrl}`;
  const normalizedBaseUrl = trimTrailingSlash(withScheme);

  if (normalizedBaseUrl.endsWith("/v1")) {
    return normalizedBaseUrl;
  }

  return `${normalizedBaseUrl}/v1`;
}

function hasUrlScheme(value: string): boolean {
  return /^[a-zA-Z][a-zA-Z\d+.-]*:\/\//.test(value);
}

function trimTrailingSlash(value: string): string {
  if (value.endsWith("/")) {
    return value.slice(0, -1);
  }

  return value;
}
