// sdk/cli/repl.go
//
// Seele CLI SDK
//
// 提供可复用的 REPL（交互式命令行）引擎。
// 调用方通过 repl.New(engine, opts) 拿到一个 REPL 实例，
// 调用 Run() 即可进入交互循环。
//
// 支持自定义：
//   - Banner 文案
//   - 提示符格式
//   - 额外命令钩子（CommandHook）
//   - 输入/输出 io.Reader / io.Writer（方便测试）
//
// 内置命令：
//
//	skills               查看当前可用 skill 列表
//	new [label]          创建新 Agent（可选自定义标签）
//	list                 列出所有 Agent
//	switch <n>           切换到第 n 个 Agent（从 1 开始）
//	reset                清空当前 Agent 历史（保留 system prompt）
//	retire  <skill>      临时屏蔽 skill
//	restore <skill>      恢复被屏蔽的 skill
//	help                 显示命令列表
//	quit / exit          退出

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sukasukasuka123/Seele/sdk/api"
)

// ── Options ───────────────────────────────────────────────────────────────────

// Options 控制 REPL 的外观与行为。
type Options struct {
	// SystemPrompt 是新建 Agent 时默认注入的 system 提示词。
	SystemPrompt string

	// BotName 是机器人显示名称，用于输出前缀，默认 "eva"。
	BotName string

	// Banner 是启动时打印的横幅文字。nil 时使用内置默认横幅。
	// 传入空 slice 可禁用横幅。
	Banner []string

	// Prompt 自定义输入提示符函数，nil 时使用默认格式。
	// 参数：当前 Agent 索引(1-based)、总数、SessionID 后缀。
	Prompt func(current, total int, sessionSuffix string) string

	// CommandHook 在所有内置命令之前被调用。
	// 返回 true 表示已处理，REPL 不再继续内置命令匹配。
	CommandHook func(input string, pool *api.AgentPool) (handled bool)

	// In / Out 是 REPL 的输入输出流，nil 时使用 os.Stdin / os.Stdout。
	In  io.Reader
	Out io.Writer

	// ExitSummary 控制退出时是否打印会话摘要，默认 true。
	ExitSummary *bool
}

func (o *Options) withDefaults() {
	if o.BotName == "" {
		o.BotName = "eva"
	}
	if o.Banner == nil {
		o.Banner = defaultBanner()
	}
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.ExitSummary == nil {
		t := true
		o.ExitSummary = &t
	}
}

func defaultBanner() []string {
	return []string{
		"╔══════════════════════════════════════╗",
		"║         suka-eva  agentfactory       ║",
		"╚══════════════════════════════════════╝",
	}
}

// ── REPL ──────────────────────────────────────────────────────────────────────

// REPL 是交互式命令行引擎。
// 通过 New 创建，通过 Run 启动阻塞式交互循环。
type REPL struct {
	engine *api.Engine
	pool   *api.AgentPool
	opts   Options
	out    *bufio.Writer
}

// New 创建 REPL 实例。
// engine 必须已初始化（api.New 返回），opts 可使用零值（全部取默认）。
func New(engine *api.Engine, opts Options) *REPL {
	opts.withDefaults()
	pool := engine.NewAgentPool()
	pool.Add("agent-1", opts.SystemPrompt)

	return &REPL{
		engine: engine,
		pool:   pool,
		opts:   opts,
		out:    bufio.NewWriter(opts.Out),
	}
}

// Run 启动 REPL，阻塞直到用户输入 quit/exit 或输入流关闭。
// ctx 可用于外部取消（例如信号处理）。
func (r *REPL) Run(ctx context.Context) {
	r.printBanner()
	r.printf("已加载 %d 个 skill（来源：registry.yaml）\n", len(r.engine.Skills()))
	r.printf("命令: skills | new [label] | list | switch <n> | reset | retire <name> | restore <name> | help | quit\n\n")
	r.flush()

	scanner := bufio.NewScanner(r.opts.In)
	for {
		// 检查 ctx 取消
		select {
		case <-ctx.Done():
			r.printf("\n👋 收到取消信号，退出\n")
			r.flush()
			return
		default:
		}

		r.printPrompt()

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// 外部命令钩子优先
		if r.opts.CommandHook != nil {
			if r.opts.CommandHook(input, r.pool) {
				continue
			}
		}

		if r.handleBuiltin(ctx, input) {
			// quit/exit 返回 false 以外的信号通过 done channel 处理
		}
	}
}

// ── 内置命令处理 ──────────────────────────────────────────────────────────────

// handleBuiltin 处理内置命令，返回 false 表示应退出循环。
func (r *REPL) handleBuiltin(ctx context.Context, input string) bool {
	lower := strings.ToLower(input)

	switch {
	// ── 退出 ──
	case lower == "quit" || lower == "exit":
		r.printf("\n👋 再见\n")
		if *r.opts.ExitSummary {
			r.printExitSummary()
		}
		r.flush()
		os.Exit(0)
		return false

	// ── skill 列表 ──
	case lower == "skills":
		r.printSkills()

	// ── 帮助 ──
	case lower == "help":
		r.printHelp()

	// ── 新建 Agent ──
	case lower == "new" || strings.HasPrefix(lower, "new "):
		label := strings.TrimSpace(strings.TrimPrefix(input, "new"))
		if label == "" {
			label = fmt.Sprintf("agent-%d", r.pool.Len()+1)
		}
		idx := r.pool.Add(label, r.opts.SystemPrompt)
		_ = r.pool.Switch(idx)
		r.printf("✅ 新建 Agent [%d] %q  %s\n\n", idx+1, label, shortID(r.pool.Current().SessionID()))

	// ── 列出所有 Agent ──
	case lower == "list":
		r.printAgentList()

	// ── 切换 Agent ──
	case strings.HasPrefix(lower, "switch "):
		r.handleSwitch(strings.TrimPrefix(input, "switch "))

	// ── 重置历史 ──
	case lower == "reset":
		r.pool.Current().Reset(true)
		r.printf("✅ 历史已清空（system prompt 保留）\n\n")

	// ── 屏蔽 skill ──
	case strings.HasPrefix(lower, "retire "):
		name := strings.TrimSpace(strings.TrimPrefix(input, "retire "))
		r.engine.Retire(name)
		r.printf("✅ skill %q 已临时屏蔽（永久下线请修改 registry.yaml）\n\n", name)

	// ── 恢复 skill ──
	case strings.HasPrefix(lower, "restore "):
		name := strings.TrimSpace(strings.TrimPrefix(input, "restore "))
		r.engine.Restore(name)
		r.printf("✅ skill %q 已恢复\n\n", name)

	// ── 默认：发送给 LLM ──
	default:
		r.handleChat(ctx, input)
	}

	r.flush()
	return true
}

func (r *REPL) handleSwitch(arg string) {
	idx, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || idx < 1 {
		r.printf("❌ 无效序号，用法：switch <n>（n 从 1 开始）\n\n")
		return
	}
	if err := r.pool.Switch(idx - 1); err != nil {
		r.printf("❌ %v（当前共 %d 个 Agent）\n\n", err, r.pool.Len())
		return
	}
	r.printf("✅ 切换到 [%d] %q  %s\n\n", idx, r.pool.CurrentLabel(), shortID(r.pool.Current().SessionID()))
}

func (r *REPL) handleChat(ctx context.Context, input string) {
	start := time.Now()
	reply, err := r.pool.Chat(ctx, input)
	elapsed := time.Since(start).Round(time.Millisecond)

	if err != nil {
		r.printf("❌ %v\n\n", err)
		return
	}
	r.printf("🤖 %s: %s\n", r.opts.BotName, reply)
	r.printf("   (%s | %d msgs)\n\n", elapsed, len(r.pool.Current().History()))
}

// ── 打印方法 ──────────────────────────────────────────────────────────────────

func (r *REPL) printBanner() {
	for _, line := range r.opts.Banner {
		r.printf("%s\n", line)
	}
}

func (r *REPL) printPrompt() {
	current := r.pool.CurrentIndex() + 1
	total := r.pool.Len()
	sessionID := ""
	if a := r.pool.Current(); a != nil {
		sessionID = shortID(a.SessionID())
	}

	var prompt string
	if r.opts.Prompt != nil {
		prompt = r.opts.Prompt(current, total, sessionID)
	} else {
		prompt = fmt.Sprintf("👤 [%d/%d %s] ", current, total, sessionID)
	}
	r.printf("%s", prompt)
	r.flush()
}

func (r *REPL) printSkills() {
	ss := r.engine.Skills()
	if len(ss) == 0 {
		r.printf("  （无可用 skill，检查 registry.yaml）\n\n")
		return
	}
	r.printf("  %-20s %-40s %s\n", "NAME", "DESCRIPTION", "ADDR")
	r.printf("  %s\n", strings.Repeat("─", 72))
	for _, s := range ss {
		r.printf("  %-20s %-40s %s\n", s.Name, s.Description, s.Addr)
	}
	r.printf("\n")
}

func (r *REPL) printAgentList() {
	for _, s := range r.pool.All() {
		marker := "  "
		if s.IsCurrent {
			marker = "▶ "
		}
		r.printf("%s[%d] %q  session=%s  msgs=%d\n",
			marker, s.Index+1, s.Label, shortID(s.SessionID), s.MsgCount)
	}
	r.printf("\n")
}

func (r *REPL) printExitSummary() {
	r.printf("\n── 会话摘要 ──────────────────────\n")
	for _, s := range r.pool.All() {
		marker := " "
		if s.IsCurrent {
			marker = "▶"
		}
		r.printf("  %s [%d] %q  %d 条消息\n", marker, s.Index+1, s.Label, s.MsgCount)
	}
}

func (r *REPL) printHelp() {
	cmds := [][2]string{
		{"skills", "查看当前可用 skill 列表"},
		{"new [label]", "创建新 Agent（可选标签名）"},
		{"list", "列出所有 Agent"},
		{"switch <n>", "切换到第 n 个 Agent（从 1 开始）"},
		{"reset", "清空当前 Agent 历史（保留 system prompt）"},
		{"retire <name>", "临时屏蔽 skill（重启后自动恢复）"},
		{"restore <name>", "恢复被屏蔽的 skill"},
		{"help", "显示本帮助"},
		{"quit / exit", "退出"},
	}
	r.printf("\n内置命令：\n")
	for _, c := range cmds {
		r.printf("  %-20s %s\n", c[0], c[1])
	}
	r.printf("\n")
}

// ── 工具方法 ──────────────────────────────────────────────────────────────────

func (r *REPL) printf(format string, args ...interface{}) {
	fmt.Fprintf(r.out, format, args...)
}

func (r *REPL) flush() {
	_ = r.out.Flush()
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return "…" + id[len(id)-8:]
}

// ── AgentPool 暴露给外部（用于 CommandHook 中操作 Pool）─────────────────────

// Pool 返回当前 REPL 管理的 AgentPool，供外部 CommandHook 使用。
func (r *REPL) Pool() *api.AgentPool { return r.pool }

// Engine 返回底层 Engine，供外部访问。
func (r *REPL) Engine() *api.Engine { return r.engine }
