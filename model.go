package Seele

// ─────────────────────────────────────────────
// LLM 对话消息
// ─────────────────────────────────────────────

// Message 是 LLM 对话历史中的一条记录。
// Role: "system" | "user" | "assistant" | "tool"
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role="tool" 时使用
	Name       string     `json:"name,omitempty"`         // role="tool" 时填工具名
}

// ToolCall 是 LLM assistant 消息中发起的工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // 固定 "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 包含工具名称及 LLM 生成的参数 JSON 字符串。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串
}

// ─────────────────────────────────────────────
// Function calling schema
// ─────────────────────────────────────────────

// Tool 对应 OpenAI function calling 协议的 tool 描述项。
type Tool struct {
	Type     string       `json:"type"` // 固定 "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction 描述一个可调用工具。
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ─────────────────────────────────────────────
// Skill 摘要（Factory / Engine 对外暴露）
// ─────────────────────────────────────────────

// SkillInfo 是单个 skill 的对外摘要，由 Factory.Skills() 返回。
type SkillInfo struct {
	Name        string // registry.yaml 的 name 字段
	Description string // registry.yaml 的 description 字段
	Method      string // microHub 路由用的 method 字段
	Addr        string // gRPC 监听地址
}

// ─────────────────────────────────────────────
// 配置结构体（与 config.yaml 对应）
// ─────────────────────────────────────────────

// AppConfig 是整个 Seele 应用的配置，对应 config.yaml 顶层结构。
// 通过 LoadConfig 加载；部分字段可由环境变量覆盖（见 config.go）。
// AppConfig 顶层对应 config.yaml 的根结构。
type AppConfig struct {
	LLM      LLMConfig      `yaml:"agent"` // yaml 顶层 key 是 agent
	Hub      HubConfig      `yaml:"hub"`
	Registry RegistryConfig `yaml:"registry"`
}

// LLMConfig 对应 config.yaml 的 agent 块。
type LLMConfig struct {
	BaseURL     string  `yaml:"ai_url"`     // agent.ai_url
	APIKey      string  `yaml:"ai_api_key"` // agent.ai_api_key
	Model       string  `yaml:"ai_name"`    // agent.ai_name
	MaxTokens   int     `yaml:"max_tokens"`
	Timeout     int     `yaml:"timeout"`
	Temperature float64 `yaml:"temperature"`
}

// HubConfig 是 microHub 的连接配置。
type HubConfig struct {
	Addr           string `yaml:"addr"`             // Hub gRPC 监听地址，默认 ":50051"
	StartupDelayMs int    `yaml:"startup_delay_ms"` // 启动后等待毫秒数，默认 100
}

// RegistryConfig 是 registry.yaml 的加载配置。
type RegistryConfig struct {
	Path string `yaml:"path"` // registry.yaml 路径
}
