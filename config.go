package Seele

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadConfig 从 YAML 文件加载 LLMConfig。
//
// config.yaml 期望格式：
//
//	agent:
//	  ai_url:     "https://..."
//	  ai_name:    "qwen-plus"
//	  ai_api_key: "sk-xxx"
//	  timeout:    60
//	  temperature: 1.0
func LoadConfig(path string) (LLMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LLMConfig{}, fmt.Errorf("LoadConfig: read %q: %w", path, err)
	}

	var app AppConfig
	if err := yaml.Unmarshal(data, &app); err != nil {
		return LLMConfig{}, fmt.Errorf("LoadConfig: parse %q: %w", path, err)
	}

	if app.LLM.BaseURL == "" {
		return LLMConfig{}, fmt.Errorf("LoadConfig: agent.ai_url is required")
	}
	if app.LLM.Model == "" {
		return LLMConfig{}, fmt.Errorf("LoadConfig: agent.ai_name is required")
	}

	return app.LLM, nil
}

// LoadAppConfig 加载完整的 AppConfig（含 Hub、Registry 配置）。
// 供 cmd/main.go 等需要读取全部配置的入口使用。
//
// config.yaml 期望格式：
//
//	agent:
//	  ai_url:     "https://..."
//	  ai_name:    "qwen-plus"
//	  ai_api_key: "sk-xxx"
//
//	hub:
//	  addr: ":50051"
//	  startup_delay_ms: 100
//
//	registry:
//	  path: "./config/registry.yaml"
func LoadAppConfig(path string) (AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppConfig{}, fmt.Errorf("LoadAppConfig: read %q: %w", path, err)
	}

	var app AppConfig
	if err := yaml.Unmarshal(data, &app); err != nil {
		return AppConfig{}, fmt.Errorf("LoadAppConfig: parse %q: %w", path, err)
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
