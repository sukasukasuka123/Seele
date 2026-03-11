package Seele

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadConfig 从 YAML 文件加载 LLMConfig，并用环境变量覆盖关键字段。
// seele_api.go 的 Engine.New() 调用此函数。
//
// 支持的环境变量：
//
//	SEELE_LLM_BASE_URL  → cfg.LLM.BaseURL
//	SEELE_LLM_API_KEY   → cfg.LLM.APIKey
//	SEELE_LLM_MODEL     → cfg.LLM.Model
func LoadConfig(path string) (LLMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LLMConfig{}, fmt.Errorf("LoadConfig: read %q: %w", path, err)
	}

	var app AppConfig
	if err := yaml.Unmarshal(data, &app); err != nil {
		return LLMConfig{}, fmt.Errorf("LoadConfig: parse %q: %w", path, err)
	}

	applyLLMEnv(&app.LLM)

	if app.LLM.BaseURL == "" {
		return LLMConfig{}, fmt.Errorf("LoadConfig: llm.base_url is required (or set SEELE_LLM_BASE_URL)")
	}
	if app.LLM.Model == "" {
		return LLMConfig{}, fmt.Errorf("LoadConfig: llm.model is required (or set SEELE_LLM_MODEL)")
	}

	return app.LLM, nil
}

// LoadAppConfig 加载完整的 AppConfig（含 Hub、Registry 配置）。
// 供 cmd/main.go 等需要读取全部配置的入口使用。
//
// 支持的环境变量（在 YAML 值基础上覆盖）：
//
//	SEELE_LLM_BASE_URL   → cfg.LLM.BaseURL
//	SEELE_LLM_API_KEY    → cfg.LLM.APIKey
//	SEELE_LLM_MODEL      → cfg.LLM.Model
//	SEELE_HUB_ADDR       → cfg.Hub.Addr
//	SEELE_REGISTRY_PATH  → cfg.Registry.Path
func LoadAppConfig(path string) (AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppConfig{}, fmt.Errorf("LoadAppConfig: read %q: %w", path, err)
	}

	var app AppConfig
	if err := yaml.Unmarshal(data, &app); err != nil {
		return AppConfig{}, fmt.Errorf("LoadAppConfig: parse %q: %w", path, err)
	}

	applyLLMEnv(&app.LLM)

	if v := os.Getenv("SEELE_HUB_ADDR"); v != "" {
		app.Hub.Addr = v
	}
	if v := os.Getenv("SEELE_REGISTRY_PATH"); v != "" {
		app.Registry.Path = v
	}

	// 默认值
	if app.Hub.Addr == "" {
		app.Hub.Addr = ":50051"
	}
	if app.Hub.StartupDelayMs <= 0 {
		app.Hub.StartupDelayMs = 100
	}
	if app.Registry.Path == "" {
		app.Registry.Path = "./config/registry.yaml"
	}

	return app, nil
}

func applyLLMEnv(cfg *LLMConfig) {
	if v := os.Getenv("SEELE_LLM_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("SEELE_LLM_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("SEELE_LLM_MODEL"); v != "" {
		cfg.Model = v
	}
}
