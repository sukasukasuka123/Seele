package Seele

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// Agent 是一个独立的对话实体，持有私有的对话历史。
// 通过 [Factory.New] 创建；所有 Agent 共用 Factory 中注册的工具链。
//
// Agent 非并发安全——同一个 Agent 实例请勿跨 goroutine 并发调用 Chat。
// 需要并发时，为每个 goroutine 创建独立的 Agent 实例即可。
type Agent struct {
	factory   *Factory
	sessionID string
	history   []Message
	maxLoops  int
}

// Chat 发送一条用户消息，返回 Agent 的最终文本回复。
//
// 内部会自动处理多轮 tool_call → tool_result 循环，直到 LLM 给出纯文本回复
// 或达到最大循环次数（默认 8）。
func (a *Agent) Chat(ctx context.Context, userInput string) (string, error) {
	a.history = append(a.history, Message{Role: "user", Content: userInput})

	tools := a.factory.tools() // 每轮拉取最新列表，支持热注册/下线

	for loop := 0; loop < a.maxLoops; loop++ {
		msg, err := a.factory.llm.chat(a.history, tools)
		if err != nil {
			return "", fmt.Errorf("llm: %w", err)
		}

		a.history = append(a.history, msg)

		// 无工具调用 → 直接返回
		if len(msg.ToolCalls) == 0 {
			a.history = truncate(a.history)
			return msg.Content, nil
		}

		log.Printf("[Agent %s] loop %d: %d tool call(s)", a.sessionID, loop+1, len(msg.ToolCalls))

		// 并行执行所有 tool_call（按 OpenAI 规范，同一批次可并行）
		results := make([]Message, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			result, dispatchErr := a.factory.dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			toolMsg := Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			}
			if dispatchErr != nil {
				toolMsg.Content = fmt.Sprintf(`{"error":%q}`, dispatchErr.Error())
				log.Printf("[Agent %s] skill %s error: %v", a.sessionID, tc.Function.Name, dispatchErr)
			} else {
				toolMsg.Content = result
				log.Printf("[Agent %s] skill %s ok", a.sessionID, tc.Function.Name)
			}
			results[i] = toolMsg
		}
		a.history = append(a.history, results...)

		// 刷新工具列表（可能在执行期间有新 skill 注册）
		tools = a.factory.tools()
	}

	log.Printf("[Agent %s] max loops (%d) reached", a.sessionID, a.maxLoops)
	return "抱歉，处理过程过于复杂，已达到最大尝试次数。请简化问题或分步询问。", nil
}

// SetSystem 设置或替换 system prompt。
// 如果历史中已有 system 消息，直接替换；否则插到最前面。
func (a *Agent) SetSystem(prompt string) {
	msg := Message{Role: "system", Content: prompt}
	if len(a.history) > 0 && a.history[0].Role == "system" {
		a.history[0] = msg
	} else {
		a.history = append([]Message{msg}, a.history...)
	}
}

// Reset 清空对话历史。keepSystem=true 时保留 system prompt。
func (a *Agent) Reset(keepSystem bool) {
	if keepSystem && len(a.history) > 0 && a.history[0].Role == "system" {
		a.history = []Message{a.history[0]}
	} else {
		a.history = nil
	}
}

// History 返回当前对话历史的只读副本。
func (a *Agent) History() []Message {
	cp := make([]Message, len(a.history))
	copy(cp, a.history)
	return cp
}

// SessionID 返回本 Agent 的唯一会话 ID。
func (a *Agent) SessionID() string { return a.sessionID }

// SetMaxLoops 设置 tool_call 循环上限（默认 8）。
func (a *Agent) SetMaxLoops(n int) { a.maxLoops = n }

// ── 内部工具 ────────────────────────────────────────────────────

// truncate 保留 system prompt + 最近 20 条消息，防止 context 爆炸。
// TODO: 接入 tiktoken 做精确 token 计数。
func truncate(msgs []Message) []Message {
	const keep = 20
	if len(msgs) <= keep {
		return msgs
	}
	var sys []Message
	if msgs[0].Role == "system" {
		sys = msgs[:1]
		msgs = msgs[1:]
	}
	if len(msgs) > keep {
		msgs = msgs[len(msgs)-keep:]
	}
	return append(sys, msgs...)
}

// Summary 返回对话摘要字符串（调试 / 日志用）。
func (a *Agent) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Agent{session=%s messages=%d}", a.sessionID, len(a.history))
	return sb.String()
}
