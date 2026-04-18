/**
 * 模型工厂 — 基于 Vercel AI SDK 创建 LanguageModel。
 *
 * @author 真心
 */

import { createAnthropic } from "@ai-sdk/anthropic";
import { createOpenAI } from "@ai-sdk/openai";
import type { LanguageModel } from "ai";
import type { RuntimeConfig } from "../runtime/runtime-config.ts";

/**
 * 根据运行时配置创建 Vercel AI SDK LanguageModel 实例。
 *
 * - anthropic: 通过 @ai-sdk/anthropic 接入。
 * - openai: 通过 @ai-sdk/openai 接入（兼容所有 OpenAI 兼容 API）。
 */
export function createLanguageModel(config: RuntimeConfig): LanguageModel {
  const baseUrl = config.baseUrl;
  const apiKey = config.apiKey ?? "";

  if (config.provider === "anthropic") {
    const provider = createAnthropic({
      apiKey,
      ...(baseUrl === undefined ? {} : { baseURL: baseUrl })
    });

    return provider.languageModel(config.model);
  }

  const provider = createOpenAI({
    name: config.provider,
    apiKey,
    ...(baseUrl === undefined ? {} : { baseURL: baseUrl })
  });

  if (config.provider === "openai-chat") {
    return provider.chat(config.model);
  }

  return provider.languageModel(config.model);
}
