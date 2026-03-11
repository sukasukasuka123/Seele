// sdk/api/seele_api.go
//
// Seele API SDK
//
// 提供对 agentfactory.Factory + microHub 生命周期的高层封装，
// 屏蔽 Hub 初始化、registry 加载、路由配置等样板代码，
// 让调用方只需关心：配置路径、system prompt、对话逻辑。
//
// 快速上手：
//
//	engine, err := api.New(api.Options{
//	    RegistryPath: "config/registry.yaml",
//	    LLMConfigPath: "config/config.yaml",
//	    HubAddr: ":50051",
//	})
//	defer engine.Shutdown()
//
//	agent := engine.NewAgent("你是助手")
//	reply, _ := agent.Chat(ctx, "你好")

package api

import (
	"context"
	"fmt"
	"log"
	"time"

	agentfactory "github.com/sukasukasuka123/Seele"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

// ── Options ──────────────────────────────────────────────────────────────────

// Options 是 Engine 的初始化配置。
// 所有字段均有合理默认值，最少只需提供 RegistryPath 和 LLMConfigPath。
type Options struct {
	// RegistryPath 是 registry.yaml 的路径（必填）。
	// 包含 skill 列表、hub 地址、连接池参数。
	RegistryPath string

	// LLMConfigPath 是 config.yaml 的路径（必填）。
	// 包含 LLM url / model / api_key。
	LLMConfigPath string

	// HubAddr 是本地 Hub gRPC 监听地址，默认 ":50051"。
	HubAddr string

	// HubStartupDelay 是等待 Hub 启动的时间，默认 100ms。
	HubStartupDelay time.Duration

	// Logger 用于输出内部日志，nil 时使用标准 log 包。
	Logger Logger
}

func (o *Options) withDefaults() {
	if o.HubAddr == "" {
		o.HubAddr = ":50051"
	}
	if o.HubStartupDelay == 0 {
		o.HubStartupDelay = 100 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = &stdLogger{}
	}
}

// ── Logger 接口 ───────────────────────────────────────────────────────────────

// Logger 是 SDK 内部日志接口，方便调用方替换为 zap/logrus 等。
type Logger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type stdLogger struct{}

func (l *stdLogger) Infof(format string, args ...interface{}) {
	log.Printf("[seele/api] "+format, args...)
}
func (l *stdLogger) Errorf(format string, args ...interface{}) {
	log.Printf("[seele/api] ERROR "+format, args...)
}

// ── Engine ────────────────────────────────────────────────────────────────────

// Engine 是 Seele API SDK 的核心对象，管理 Hub + Factory 的完整生命周期。
// 通过 New 创建，通过 Shutdown 关闭。Engine 并发安全。
type Engine struct {
	factory  *agentfactory.Factory
	hub      *hubbase.BaseHub
	opts     Options
	shutdown chan struct{}
}

// New 初始化 Engine：加载 registry、启动 Hub、创建 Factory。
//
// 典型用法：
//
//	engine, err := api.New(api.Options{
//	    RegistryPath:  "config/registry.yaml",
//	    LLMConfigPath: "config/config.yaml",
//	})
func New(opts Options) (*Engine, error) {
	opts.withDefaults()

	// 1. 加载 registry（skill 列表 + hub + pool 全在 yaml 里）
	if err := registry.Init(opts.RegistryPath); err != nil {
		return nil, fmt.Errorf("seele/api: registry init %q: %w", opts.RegistryPath, err)
	}

	// 2. 启动 Hub
	hub := hubbase.New(&registryRouter{})
	eng := &Engine{
		hub:      hub,
		opts:     opts,
		shutdown: make(chan struct{}),
	}
	go func() {
		if err := hub.ServeAsync(opts.HubAddr, 0); err != nil {
			opts.Logger.Errorf("hub exited: %v", err)
		}
	}()
	time.Sleep(opts.HubStartupDelay)
	opts.Logger.Infof("hub listening on %s", opts.HubAddr)

	// 3. 加载 LLM 配置，创建 Factory
	llmCfg, err := agentfactory.LoadConfig(opts.LLMConfigPath)
	if err != nil {
		return nil, fmt.Errorf("seele/api: load llm config %q: %w", opts.LLMConfigPath, err)
	}
	factory, err := agentfactory.NewFactory(llmCfg, hub)
	if err != nil {
		return nil, fmt.Errorf("seele/api: new factory: %w", err)
	}
	eng.factory = factory

	opts.Logger.Infof("engine ready, %d skill(s) loaded", len(factory.Skills()))
	return eng, nil
}

// Shutdown 关闭 Engine，释放资源。
// 目前 Hub 不提供优雅关闭接口，此方法预留用于未来扩展。
func (e *Engine) Shutdown() {
	select {
	case <-e.shutdown:
	default:
		close(e.shutdown)
		e.opts.Logger.Infof("engine shutdown")
	}
}

// ── Agent 管理 ────────────────────────────────────────────────────────────────

// NewAgent 创建一个新的对话 Agent。
// systemPrompt 为空时不注入 system 消息。
func (e *Engine) NewAgent(systemPrompt string) *agentfactory.Agent {
	return e.factory.New(systemPrompt)
}

// Skills 返回当前对 LLM 可见的 skill 摘要列表。
func (e *Engine) Skills() []agentfactory.SkillInfo {
	return e.factory.Skills()
}

// Retire 临时屏蔽某个 skill（重启后自动恢复）。
func (e *Engine) Retire(name string) {
	e.factory.Retire(name)
}

// Restore 恢复被 Retire 屏蔽的 skill。
func (e *Engine) Restore(name string) {
	e.factory.Restore(name)
}

// Factory 暴露底层 Factory，供需要精细控制的场景使用。
func (e *Engine) Factory() *agentfactory.Factory {
	return e.factory
}

// ── 便捷方法 ──────────────────────────────────────────────────────────────────

// QuickChat 创建临时 Agent，发送一条消息后返回回复，不保留历史。
// 适合一次性调用场景。
func (e *Engine) QuickChat(ctx context.Context, systemPrompt, userInput string) (string, error) {
	a := e.NewAgent(systemPrompt)
	return a.Chat(ctx, userInput)
}

// ── AgentPool ─────────────────────────────────────────────────────────────────

// AgentPool 管理一组具名 Agent，支持按名称切换。
// 适合多 Agent 协作或 REPL 多会话场景。
type AgentPool struct {
	engine  *Engine
	agents  []*namedAgent
	current int
}

type namedAgent struct {
	label string
	agent *agentfactory.Agent
}

// NewAgentPool 创建空的 AgentPool。
func (e *Engine) NewAgentPool() *AgentPool {
	return &AgentPool{engine: e}
}

// Add 向 Pool 中添加一个 Agent，返回其索引（从 0 开始）。
func (p *AgentPool) Add(label, systemPrompt string) int {
	p.agents = append(p.agents, &namedAgent{
		label: label,
		agent: p.engine.NewAgent(systemPrompt),
	})
	return len(p.agents) - 1
}

// Switch 切换当前活跃 Agent（索引从 0 开始）。
func (p *AgentPool) Switch(idx int) error {
	if idx < 0 || idx >= len(p.agents) {
		return fmt.Errorf("index %d out of range [0, %d)", idx, len(p.agents))
	}
	p.current = idx
	return nil
}

// Current 返回当前活跃的 Agent。
func (p *AgentPool) Current() *agentfactory.Agent {
	if len(p.agents) == 0 {
		return nil
	}
	return p.agents[p.current].agent
}

// CurrentLabel 返回当前活跃 Agent 的标签。
func (p *AgentPool) CurrentLabel() string {
	if len(p.agents) == 0 {
		return ""
	}
	return p.agents[p.current].label
}

// CurrentIndex 返回当前活跃 Agent 的索引（从 0 开始）。
func (p *AgentPool) CurrentIndex() int { return p.current }

// Len 返回 Pool 中 Agent 数量。
func (p *AgentPool) Len() int { return len(p.agents) }

// All 返回所有 Agent 的摘要（索引、标签、SessionID）。
func (p *AgentPool) All() []AgentSummary {
	result := make([]AgentSummary, len(p.agents))
	for i, na := range p.agents {
		result[i] = AgentSummary{
			Index:     i,
			Label:     na.label,
			SessionID: na.agent.SessionID(),
			MsgCount:  len(na.agent.History()),
			IsCurrent: i == p.current,
		}
	}
	return result
}

// AgentSummary 是 AgentPool.All() 返回的单条摘要。
type AgentSummary struct {
	Index     int
	Label     string
	SessionID string
	MsgCount  int
	IsCurrent bool
}

// Chat 向当前活跃 Agent 发送消息。
func (p *AgentPool) Chat(ctx context.Context, input string) (string, error) {
	a := p.Current()
	if a == nil {
		return "", fmt.Errorf("agentpool is empty, call Add first")
	}
	return a.Chat(ctx, input)
}

// ── Hub 路由（SDK 内部使用）───────────────────────────────────────────────────

// registryRouter 是 SDK 内置的默认路由器，完全依赖 registry 做地址查找。
// 与 cmd/main.go 中的 evaRouter 逻辑一致，但作为 SDK 内部实现对外隐藏。
type registryRouter struct{}

func (r *registryRouter) ServiceName() string { return "seele-sdk-hub" }

func (r *registryRouter) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	t, ok := registry.SelectToolByMethod(req.Method)
	if !ok {
		return nil, fmt.Errorf("no tool registered for method=%q", req.Method)
	}
	return []hubbase.DispatchTarget{
		{Addr: t.Addr, Request: req, Stream: true},
	}, nil
}

func (r *registryRouter) OnResults(results []hubbase.DispatchResult) {
	for _, res := range results {
		if !res.AllOK() {
			log.Printf("[seele/api hub] dispatch error addr=%s: %v", res.Target.Addr, res.Err)
		}
	}
}

func (r *registryRouter) Addrs() []string {
	tools := registry.GetAllTools()
	addrs := make([]string, len(tools))
	for i, t := range tools {
		addrs[i] = t.Addr
	}
	return addrs
}
