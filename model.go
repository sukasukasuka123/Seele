package Seele

// ── OpenAI 格式数据链（全库唯一一套）────────────────────────────
// 所有模块统一使用这里的类型，禁止在子包中重新定义等价结构体。

// Message 是对话历史中的一条消息，直接对应 OpenAI messages 格式。
type Message struct {
	Role       string     `json:"role"`                   // system / user / assistant / tool
	Content    string     `json:"content"`                // 消息正文
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool 时关联的调用 ID
	Name       string     `json:"name,omitempty"`         // role=tool 时填工具名
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // role=assistant 发起调用时携带
}

// ToolCall 对应 OpenAI tool_calls 数组中的单个调用。
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // 固定 "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字符串
	} `json:"function"`
}

// Tool 是一个可供 LLM 调用的工具定义（OpenAI function calling 格式）。
type Tool struct {
	Type     string `json:"type"` // 固定 "function"
	Function struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"` // JSON Schema
	} `json:"function"`
}

// ── LLM 请求 / 响应（仅库内 llm 包使用，对外不暴露）──────────────

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// ── Skill 元信息（factory 内部维护）────────────────────────────

// skillMeta 描述一个已注册并可用的微服务工具。
type skillMeta struct {
	Name        string
	Description string
	Addr        string
	Schema      map[string]interface{} // JSON Schema，直接填入 Tool.Function.Parameters
}

// toTool 将 skillMeta 转换为 OpenAI Tool 定义。
func (s *skillMeta) toTool() Tool {
	var t Tool
	t.Type = "function"
	t.Function.Name = s.Name
	t.Function.Description = s.Description
	t.Function.Parameters = s.Schema
	return t
}
