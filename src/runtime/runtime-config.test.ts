/**
 * 运行时配置加载测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { loadRuntimeConfig } from "./runtime-config.ts";

describe("loadRuntimeConfig", () => {
  it("loads ollama config without api key", () => {
    const originalProvider = Bun.env.BANKA_PROVIDER;
    const originalApiKey = Bun.env.BANKA_API_KEY;
    const originalBaseUrl = Bun.env.BANKA_BASE_URL;
    const originalModel = Bun.env.BANKA_MODEL;

    try {
      Bun.env.BANKA_PROVIDER = "ollama";
      delete Bun.env.BANKA_API_KEY;
      Bun.env.BANKA_BASE_URL = "127.0.0.1:11434";
      Bun.env.BANKA_MODEL = "llama3.2";

      const config = loadRuntimeConfig("/workspace");

      expect(config.provider).toBe("ollama");
      expect(config.baseUrl).toBe("127.0.0.1:11434");
      expect(config.model).toBe("llama3.2");
      expect(config.apiKey).toBeUndefined();
    } finally {
      restoreEnv("BANKA_PROVIDER", originalProvider);
      restoreEnv("BANKA_API_KEY", originalApiKey);
      restoreEnv("BANKA_BASE_URL", originalBaseUrl);
      restoreEnv("BANKA_MODEL", originalModel);
    }
  });
});

function restoreEnv(key: string, value: string | undefined): void {
  if (value === undefined) {
    delete Bun.env[key];
    return;
  }

  Bun.env[key] = value;
}
