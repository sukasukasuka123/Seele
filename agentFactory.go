package Seele

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	jsonSchema "github.com/sukasukasuka123/microHub/jsonSchema"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

// Factory 持有 Hub 连接和 LLM 客户端。
// Skill 列表以 microHub registry（registry.yaml）为唯一数据源。
//
// 永久增删 skill：修改 registry.yaml，热更新自动生效。
// 运行时临时屏蔽：调用 Retire / Restore，重启后恢复。
//
// Factory 并发安全。
type Factory struct {
	llm     *llmClient
	hub     *hubbase.BaseHub
	mu      sync.RWMutex
	retired map[string]struct{} // 运行时屏蔽集合，不持久化
}

// NewFactory 创建 Factory。
// registry.Init 必须在此之前调用。
func NewFactory(llmCfg LLMConfig, hub *hubbase.BaseHub) (*Factory, error) {
	if hub == nil {
		return nil, fmt.Errorf("agentfactory: hub must not be nil")
	}
	if llmCfg.BaseURL == "" || llmCfg.Model == "" {
		return nil, fmt.Errorf("agentfactory: LLMConfig requires BaseURL and Model")
	}
	return &Factory{
		llm:     newLLMClient(llmCfg),
		hub:     hub,
		retired: make(map[string]struct{}),
	}, nil
}

// New 创建一个新 Agent。systemPrompt 为空时不注入 system 消息。
func (f *Factory) New(systemPrompt string) *Agent {
	a := &Agent{
		factory:   f,
		sessionID: fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		maxLoops:  8,
	}
	if systemPrompt != "" {
		a.history = []Message{{Role: "system", Content: systemPrompt}}
	}
	return a
}

// Retire 运行时屏蔽某个 skill，下一轮 LLM 调用起从工具列表消失。
// 不修改 registry.yaml，重启后自动恢复。
// 永久下线请直接删除 registry.yaml 中对应条目。
func (f *Factory) Retire(name string) {
	f.mu.Lock()
	f.retired[name] = struct{}{}
	f.mu.Unlock()
	log.Printf("[Factory] retired skill: %s (runtime only, restarts will restore)", name)
}

// Restore 恢复被 Retire 临时屏蔽的 skill。
func (f *Factory) Restore(name string) {
	f.mu.Lock()
	delete(f.retired, name)
	f.mu.Unlock()
	log.Printf("[Factory] restored skill: %s", name)
}

// Skills 返回当前对 LLM 可见的 skill 列表。
func (f *Factory) Skills() []SkillInfo {
	f.mu.RLock()
	retired := make(map[string]struct{}, len(f.retired))
	for k := range f.retired {
		retired[k] = struct{}{}
	}
	f.mu.RUnlock()

	all := registry.GetAllTools()
	result := make([]SkillInfo, 0, len(all))
	for _, t := range all {
		if _, blocked := retired[t.Name]; !blocked {
			result = append(result, SkillInfo{
				Name:        t.Name,
				Description: t.Method,
				Addr:        t.Addr,
			})
		}
	}
	return result
}

// SkillInfo 是对外暴露的 skill 摘要。
type SkillInfo struct {
	Name        string
	Description string
	Addr        string
}

// ── 内部方法（Agent 专用）────────────────────────────────────────

// tools 构建当前对 LLM 可见的 []Tool 列表，每次调用都从 registry 实时读取。
//
// 若 registry 条目定义了 input_schema，将其转为标准 OpenAI function schema；
// 否则回退到开放 schema。
func (f *Factory) tools() []Tool {
	f.mu.RLock()
	retired := make(map[string]struct{}, len(f.retired))
	for k := range f.retired {
		retired[k] = struct{}{}
	}
	f.mu.RUnlock()

	all := registry.GetAllTools()
	result := make([]Tool, 0, len(all))
	for _, t := range all {
		if _, blocked := retired[t.Name]; blocked {
			continue
		}
		var tool Tool
		tool.Type = "function"
		tool.Function.Name = t.Name
		tool.Function.Description = t.Method
		tool.Function.Parameters = buildParameters(t.InputSchema)
		result = append(result, tool)
	}
	return result
}

// buildParameters 将 registry input_schema（microHub 格式）转为
// OpenAI function calling 标准的 parameters JSON Schema 对象。
//
// microHub schema 格式（data 替代 properties）：
//
//	{ "type": "object", "data": { "field": { "type": "...", "default": ... } }, "required": [...] }
//
// 无 input_schema 或解析失败时回退到开放 schema（additionalProperties）。
func buildParameters(inputSchema string) map[string]interface{} {
	fallback := map[string]interface{}{
		"type":                 "object",
		"properties":           map[string]interface{}{},
		"additionalProperties": map[string]interface{}{"type": "string"},
	}

	if inputSchema == "" {
		return fallback
	}

	var node jsonSchema.SchemaNode
	if err := json.Unmarshal([]byte(inputSchema), &node); err != nil {
		log.Printf("[Factory] buildParameters: parse input_schema failed: %v", err)
		return fallback
	}
	if node.Type != jsonSchema.TypeObject {
		return fallback
	}

	properties := make(map[string]interface{}, len(node.Data))
	for fieldName, fieldNode := range node.Data {
		properties[fieldName] = schemaNodeToOpenAI(fieldNode)
	}

	params := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(node.Required) > 0 {
		params["required"] = node.Required
	}
	return params
}

// schemaNodeToOpenAI 递归把 SchemaNode 转为 OpenAI JSON Schema 子对象。
func schemaNodeToOpenAI(node *jsonSchema.SchemaNode) map[string]interface{} {
	if node == nil {
		return map[string]interface{}{"type": "string"}
	}

	m := map[string]interface{}{
		"type": string(node.Type),
	}
	if len(node.Enum) > 0 {
		m["enum"] = node.Enum
	}
	if node.Min != nil {
		m["minimum"] = *node.Min
	}
	if node.Max != nil {
		m["maximum"] = *node.Max
	}
	if node.Default != nil {
		m["default"] = node.Default
	}

	// 递归 object：data → properties
	if node.Type == jsonSchema.TypeObject && len(node.Data) > 0 {
		props := make(map[string]interface{}, len(node.Data))
		for k, v := range node.Data {
			props[k] = schemaNodeToOpenAI(v)
		}
		m["properties"] = props
		if len(node.Required) > 0 {
			m["required"] = node.Required
		}
	}

	// 递归 array
	if node.Type == jsonSchema.TypeArray && node.Items != nil {
		m["items"] = schemaNodeToOpenAI(node.Items)
	}

	return m
}

// dispatch 通过 Hub 调用指定 skill，argsJSON 是 LLM 生成的 JSON 参数字符串。
func (f *Factory) dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	// 1. registry 中必须存在
	t, ok := registry.SelectToolByName(name)
	if !ok {
		return "", fmt.Errorf("skill %q not in registry", name)
	}

	// 2. 未被运行时屏蔽
	f.mu.RLock()
	_, blocked := f.retired[name]
	f.mu.RUnlock()
	if blocked {
		return "", fmt.Errorf("skill %q is retired", name)
	}

	// 3. 校验 LLM 返回的 argsJSON 是合法 JSON
	if !json.Valid([]byte(argsJSON)) {
		return "", fmt.Errorf("skill %q: LLM returned invalid JSON args: %s", name, argsJSON)
	}

	// 4. Hub Dispatch —— Params 直接传 []byte，microHub proto 定义是 bytes
	start := time.Now()
	results := f.hub.Dispatch(ctx, &pb.ToolRequest{
		From:        "agentfactory",
		ServiceName: t.Name,
		Params:      []byte(argsJSON),
	})
	log.Printf("[Factory] dispatch skill=%s latency=%dms", name, time.Since(start).Milliseconds())

	if len(results) == 0 {
		return "", fmt.Errorf("skill %q: no response from hub (is the tool process running?)", name)
	}

	// 5. 聚合响应，结构化错误一并收集
	var parts, errs []string
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, r.Err.Error())
			continue
		}
		for _, resp := range r.Responses {
			if resp.Status != "ok" {
				for _, e := range resp.Errors {
					errs = append(errs, fmt.Sprintf("[%s] %s: %s", resp.ServiceName, e.Code, e.Message))
				}
			} else {
				parts = append(parts, string(resp.Result))
			}
		}
	}
	if len(errs) > 0 && len(parts) == 0 {
		return "", fmt.Errorf("skill %q failed: %s", name, strings.Join(errs, "; "))
	}
	return strings.Join(parts, "\n"), nil
}
