// Package config loads Banka runtime configuration.
package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ProviderKind is the configured model provider.
type ProviderKind string

const (
	// ProviderOpenAI uses OpenAI Chat Completions compatible APIs.
	ProviderOpenAI ProviderKind = "openai"
	// ProviderOpenAIChat uses OpenAI Chat Completions compatible APIs.
	ProviderOpenAIChat ProviderKind = "openai-chat"
	// ProviderAnthropic uses Anthropic native APIs.
	ProviderAnthropic ProviderKind = "anthropic"
)

// RuntimeConfig is the environment-derived configuration.
type RuntimeConfig struct {
	WorkspaceRoot string
	Provider      ProviderKind
	Model         string
	APIKey        string
	BaseURL       string
}

// Load reads .env and process environment, then validates required settings.
func Load(workspaceRoot string) (RuntimeConfig, error) {
	if err := loadDotEnv(filepath.Join(workspaceRoot, ".env")); err != nil {
		return RuntimeConfig{}, err
	}

	model := strings.TrimSpace(os.Getenv("BANKA_MODEL"))
	if model == "" {
		return RuntimeConfig{}, errors.New("缺少 BANKA_MODEL 配置。必须显式指定模型。")
	}

	apiKey := strings.TrimSpace(os.Getenv("BANKA_API_KEY"))
	if apiKey == "" {
		return RuntimeConfig{}, errors.New("缺少 BANKA_API_KEY 配置。请设置 BANKA_API_KEY 环境变量。")
	}

	return RuntimeConfig{
		WorkspaceRoot: workspaceRoot,
		Provider:      parseProviderKind(os.Getenv("BANKA_PROVIDER")),
		Model:         model,
		APIKey:        apiKey,
		BaseURL:       strings.TrimSpace(os.Getenv("BANKA_BASE_URL")),
	}, nil
}

func parseProviderKind(value string) ProviderKind {
	switch strings.TrimSpace(value) {
	case string(ProviderOpenAIChat):
		return ProviderOpenAIChat
	case string(ProviderAnthropic):
		return ProviderAnthropic
	default:
		return ProviderOpenAI
	}
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, unquoteEnvValue(strings.TrimSpace(value)))
	}

	return scanner.Err()
}

func unquoteEnvValue(value string) string {
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}
