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
	"github.com/sukasukasuka123/microHub/pb_api"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

// Factory 持有 LLM 客户端和 Hub 连接，负责：
//   - 从 registry 实时读取 skill 列表并转换为 OpenAI function schema
//   - 创建 Agent 实例
//   - 通过 Hub 分发 tool_call 请求
//
// Skill 的唯一数据源是 registry.yaml，热更新即时生效。
// 运行时临时屏蔽某个 skill 用 Retire / Restore；永久下线请直接修改 registry.yaml。
//
// Factory 并发安全（读写锁保护 retired 集合）。
type Factory struct {
	llm     *llmClient
	hub     *hubbase.BaseHub
	mu      sync.RWMutex
	retired map[string]struct{} // 运行时屏蔽集合，不持久化
}

// NewFactory 创建 Factory。
// 调用前必须已调用 registry.Init 完成 registry.yaml 加载。
func NewFactory(llmCfg LLMConfig, hub *hubbase.BaseHub) (*Factory, error) {
	if hub == nil {
		return nil, fmt.Errorf("agentFactory: hub must not be nil")
	}
	if llmCfg.BaseURL == "" || llmCfg.Model == "" {
		return nil, fmt.Errorf("agentFactory: LLMConfig requires BaseURL and Model")
	}
	return &Factory{
		llm:     newLLMClient(llmCfg),
		hub:     hub,
		retired: make(map[string]struct{}),
	}, nil
}

// ── Agent 工厂方法 ────────────────────────────────────────────────

// New 创建一个新 Agent。
// systemPrompt 为空时不注入 system 消息。
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

// ── Skill 管理 ────────────────────────────────────────────────────

// Retire 运行时屏蔽某个 skill，下一轮 LLM 调用起不再提供该工具。
// 不修改 registry.yaml，重启后自动恢复。
// 永久下线请直接删除 registry.yaml 中的对应条目。
func (f *Factory) Retire(name string) {
	f.mu.Lock()
	f.retired[name] = struct{}{}
	f.mu.Unlock()
	log.Printf("[Factory] retired skill=%q (runtime only, restarts will restore)", name)
}

// Restore 恢复被 Retire 临时屏蔽的 skill。
func (f *Factory) Restore(name string) {
	f.mu.Lock()
	delete(f.retired, name)
	f.mu.Unlock()
	log.Printf("[Factory] restored skill=%q", name)
}

// Skills 返回当前对 LLM 可见的 skill 摘要列表（已排除 retired 项）。
func (f *Factory) Skills() []SkillInfo {
	retired := f.retiredSnapshot()
	all := registry.GetAllTools()
	result := make([]SkillInfo, 0, len(all))
	for _, t := range all {
		if _, blocked := retired[t.Name]; blocked {
			continue
		}
		result = append(result, SkillInfo{
			Name:        t.Name,
			Method:      t.Method,
			Description: t.Method,
			Addr:        t.Addr,
		})
	}
	return result
}

// ── 内部方法（仅供 Agent 调用）────────────────────────────────────

// tools 构建当前对 LLM 可见的工具列表，每次调用都实时读取 registry。
func (f *Factory) tools() []Tool {
	retired := f.retiredSnapshot()
	all := registry.GetAllTools()
	result := make([]Tool, 0, len(all))
	for _, t := range all {
		if _, blocked := retired[t.Name]; blocked {
			continue
		}
		// Method 优先使用 registry 的 Method 字段，回退到 method
		desc := t.Method
		if desc == "" {
			desc = t.Method
		}
		result = append(result, Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        t.Name,
				Description: desc,
				Parameters:  buildParameters(t.InputSchema),
			},
		})
	}
	return result
}

// dispatch 通过 Hub 调用指定 skill。
//
// microHub API 关键说明：
//   - 路由使用 req.Method（registry.yaml 的 method 字段），大小写敏感
//   - 必须用 pb_api.Request().Method(...).Params(...).Build() 构造请求
//   - Execute 返回 <-chan *pb.ToolResponse（流式 channel）
//   - 帧状态：status="partial" 为中间帧，status="ok"/"error" 为终帧
//   - ToolResponse.ToolName 标识响应来源（旧版字段名 ServiceName 已废弃）
func (f *Factory) dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	// 1. 按 name 查 registry，取出路由用的 Method 字段
	t, ok := registry.SelectToolByName(name)
	if !ok {
		return "", fmt.Errorf("dispatch: skill %q not in registry", name)
	}

	// 2. 运行时屏蔽检查
	f.mu.RLock()
	_, blocked := f.retired[name]
	f.mu.RUnlock()
	if blocked {
		return "", fmt.Errorf("dispatch: skill %q is retired", name)
	}

	// 3. 校验参数 JSON 合法性
	if !json.Valid([]byte(argsJSON)) {
		return "", fmt.Errorf("dispatch: skill %q got invalid JSON args: %.200s", name, argsJSON)
	}

	// 4. 构造请求（禁止直接构造 pb.ToolRequest{}）
	req, err := pb_api.Request().
		Method(t.Method).
		Params([]byte(argsJSON)).
		Build()
	if err != nil {
		return "", fmt.Errorf("dispatch: skill %q build request: %w", name, err)
	}

	// 5. Hub Dispatch — 阻塞直到所有帧到齐或 ctx 超时
	start := time.Now()
	results := f.hub.Dispatch(ctx, req)
	log.Printf("[Factory] dispatch skill=%s method=%s latency=%dms",
		name, t.Method, time.Since(start).Milliseconds())

	if len(results) == 0 {
		return "", fmt.Errorf("dispatch: skill %q: no response (is the tool process running?)", name)
	}

	// 6. 聚合响应帧
	//    partial + ok 帧收集 Result；error 帧收集错误信息
	var parts, errs []string
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, r.Err.Error())
			continue
		}
		for _, resp := range r.Responses {
			switch resp.Status {
			case "error":
				for _, e := range resp.Errors {
					errs = append(errs, fmt.Sprintf("[%s] %s: %s",
						resp.ToolName, e.Code, e.Message))
				}
			case "ok", "partial":
				if raw := string(resp.Result); raw != "" && raw != "{}" {
					parts = append(parts, raw)
				}
			}
		}
	}

	if len(errs) > 0 && len(parts) == 0 {
		return "", fmt.Errorf("dispatch: skill %q failed: %s", name, strings.Join(errs, "; "))
	}
	return strings.Join(parts, "\n"), nil
}

// retiredSnapshot 返回 retired 集合的快照（在锁外使用）。
func (f *Factory) retiredSnapshot() map[string]struct{} {
	f.mu.RLock()
	defer f.mu.RUnlock()
	snap := make(map[string]struct{}, len(f.retired))
	for k := range f.retired {
		snap[k] = struct{}{}
	}
	return snap
}

// ── Schema 转换（microHub → OpenAI JSON Schema）────────────────────

// buildParameters 将 registry input_schema（microHub 格式）转为
// OpenAI function calling 标准的 parameters JSON Schema 对象。
//
// microHub schema 与标准 JSON Schema 的差异：
//   - data  → properties
//   - min   → minimum
//   - max   → maximum
func buildParameters(inputSchema string) map[string]interface{} {
	fallback := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
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

// schemaNodeToOpenAI 递归把 microHub SchemaNode 转为标准 OpenAI JSON Schema 子对象。
func schemaNodeToOpenAI(node *jsonSchema.SchemaNode) map[string]interface{} {
	if node == nil {
		return map[string]interface{}{"type": "string"}
	}
	m := map[string]interface{}{"type": string(node.Type)}
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
	if node.Type == jsonSchema.TypeArray && node.Items != nil {
		m["items"] = schemaNodeToOpenAI(node.Items)
	}
	return m
}
