package Seele

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Config 对应 config.yaml，只包含 LLM 连接参数。
// Skill 列表统一在 registry.yaml（microHub）中管理，不在这里定义。
type Config struct {
	Agent struct {
		AIURL    string `mapstructure:"ai_url"`
		AIName   string `mapstructure:"ai_name"`
		AIAPIKey string `mapstructure:"ai_api_key"`
	} `mapstructure:"agent"`
}

// LoadConfig 从 YAML 文件加载 LLM 配置，返回 LLMConfig。
//
// 典型用法（registry.Init 在此之前调用）：
//
//	registry.Init("config/registry.yaml")
//	llmCfg, _ := Seele.LoadConfig("config.yaml")
//	f, _ := Seele.NewFactory(llmCfg, hub)
func LoadConfig(path string) (LLMConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return LLMConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return LLMConfig{}, fmt.Errorf("unmarshal config: %w", err)
	}
	if cfg.Agent.AIURL == "" {
		return LLMConfig{}, fmt.Errorf("config %s: agent.ai_url is required", path)
	}
	if cfg.Agent.AIName == "" {
		return LLMConfig{}, fmt.Errorf("config %s: agent.ai_name is required", path)
	}
	return LLMConfig{
		BaseURL: cfg.Agent.AIURL,
		Model:   cfg.Agent.AIName,
		APIKey:  cfg.Agent.AIAPIKey,
		Timeout: 60 * time.Second,
	}, nil
}
