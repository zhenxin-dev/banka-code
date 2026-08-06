package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRuntimeConfig(t *testing.T) {
	withCleanBankaEnv(t)
	t.Setenv("BANKA_PROVIDER", "anthropic")
	t.Setenv("BANKA_API_KEY", "sk-test")
	t.Setenv("BANKA_BASE_URL", "https://example.com/v1")
	t.Setenv("BANKA_MODEL", "claude-test")

	config, err := Load("/workspace")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if config.WorkspaceRoot != "/workspace" || config.Provider != ProviderAnthropic || config.APIKey != "sk-test" || config.BaseURL != "https://example.com/v1" || config.Model != "claude-test" {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestLoadDefaultsUnknownProviderToOpenAI(t *testing.T) {
	withCleanBankaEnv(t)
	t.Setenv("BANKA_PROVIDER", "unknown")
	t.Setenv("BANKA_API_KEY", "key")
	t.Setenv("BANKA_MODEL", "model")

	config, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if config.Provider != ProviderOpenAI {
		t.Fatalf("got provider %q, want %q", config.Provider, ProviderOpenAI)
	}
}

func TestLoadRequiresModelAndAPIKey(t *testing.T) {
	withCleanBankaEnv(t)
	t.Setenv("BANKA_API_KEY", "key")
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatalf("Load accepted missing model")
	}

	t.Setenv("BANKA_MODEL", "model")
	t.Setenv("BANKA_API_KEY", "")
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatalf("Load accepted missing API key")
	}
}

func TestLoadReadsDotEnvWithoutOverridingProcessEnvironment(t *testing.T) {
	withCleanBankaEnv(t)
	root := t.TempDir()
	content := "BANKA_PROVIDER=openai-chat\nBANKA_API_KEY=dotenv-key\nBANKA_BASE_URL='https://dotenv.example/v1'\nBANKA_MODEL=dotenv-model\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BANKA_MODEL", "process-model")

	config, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if config.Provider != ProviderOpenAIChat || config.APIKey != "dotenv-key" || config.BaseURL != "https://dotenv.example/v1" || config.Model != "process-model" {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func withCleanBankaEnv(t *testing.T) {
	t.Helper()
	keys := []string{"BANKA_PROVIDER", "BANKA_API_KEY", "BANKA_BASE_URL", "BANKA_MODEL"}
	for _, key := range keys {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		key, value, exists := key, value, exists
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}
