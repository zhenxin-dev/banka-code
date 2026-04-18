/**
 * 运行时配置加载测试。
 *
 * @author 真心
 */

import { describe, expect, it } from "bun:test";
import { loadRuntimeConfig } from "./runtime-config.ts";

describe("loadRuntimeConfig", () => {
  it("loads openai config", () => {
    withEnv(
      { BANKA_PROVIDER: "openai", BANKA_API_KEY: "sk-test", BANKA_BASE_URL: "https://api.openai.com/v1", BANKA_MODEL: "gpt-4" },
      () => {
        const config = loadRuntimeConfig("/workspace");

        expect(config.provider).toBe("openai");
        expect(config.apiKey).toBe("sk-test");
        expect(config.baseUrl).toBe("https://api.openai.com/v1");
        expect(config.model).toBe("gpt-4");
      }
    );
  });

  it("loads anthropic config", () => {
    withEnv(
      { BANKA_PROVIDER: "anthropic", BANKA_API_KEY: "sk-ant-test", BANKA_BASE_URL: "https://api.anthropic.com", BANKA_MODEL: "claude-sonnet-4-20250514" },
      () => {
        const config = loadRuntimeConfig("/workspace");

        expect(config.provider).toBe("anthropic");
        expect(config.apiKey).toBe("sk-ant-test");
        expect(config.model).toBe("claude-sonnet-4-20250514");
      }
    );
  });

  it("defaults to openai when BANKA_PROVIDER is empty", () => {
    withEnv(
      { BANKA_API_KEY: "key", BANKA_BASE_URL: "https://api.openai.com/v1", BANKA_MODEL: "gpt-4" },
      () => {
        const config = loadRuntimeConfig("/workspace");
        expect(config.provider).toBe("openai");
      }
    );
  });

  it("defaults to openai for unknown provider", () => {
    withEnv(
      { BANKA_PROVIDER: "unknown", BANKA_API_KEY: "key", BANKA_BASE_URL: "https://example.com/v1", BANKA_MODEL: "model" },
      () => {
        const config = loadRuntimeConfig("/workspace");
        expect(config.provider).toBe("openai");
      }
    );
  });

  it("throws when BANKA_MODEL is missing", () => {
    withEnv(
      { BANKA_PROVIDER: "openai", BANKA_API_KEY: "key", BANKA_BASE_URL: "https://example.com/v1" },
      () => {
        expect(() => loadRuntimeConfig("/workspace")).toThrow("缺少 BANKA_MODEL 配置");
      }
    );
  });

  it("throws when BANKA_API_KEY is missing", () => {
    withEnv(
      { BANKA_PROVIDER: "openai", BANKA_BASE_URL: "https://example.com/v1", BANKA_MODEL: "gpt-4" },
      () => {
        expect(() => loadRuntimeConfig("/workspace")).toThrow("缺少 BANKA_API_KEY 配置");
      }
    );
  });
});

interface EnvOverrides {
  readonly BANKA_PROVIDER?: string;
  readonly BANKA_API_KEY?: string;
  readonly BANKA_BASE_URL?: string;
  readonly BANKA_MODEL?: string;
}

function withEnv(overrides: EnvOverrides, fn: () => void): void {
  const keys = ["BANKA_PROVIDER", "BANKA_API_KEY", "BANKA_BASE_URL", "BANKA_MODEL"] as const;
  const originals = new Map<string, string | undefined>();

  for (const key of keys) {
    originals.set(key, Bun.env[key]);
  }

  try {
    for (const key of keys) {
      delete Bun.env[key];
    }

    for (const [key, value] of Object.entries(overrides)) {
      Bun.env[key] = value;
    }

    fn();
  } finally {
    for (const key of keys) {
      const original = originals.get(key);
      if (original === undefined) {
        delete Bun.env[key];
      } else {
        Bun.env[key] = original;
      }
    }
  }
}
