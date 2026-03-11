package Seele

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Agent 是绑定到单个会话的智能体实例。
//
// 每个 Agent 拥有：
//   - 独立的对话历史（history）
//   - 唯一的会话 ID（sessionID）
//   - tool_call 循环上限（maxLoops，默认 8）
//
// 并发安全性：Agent 本身不加锁，同一个 Agent 不应跨 goroutine 并发调用。
// 如需并发，请通过 Factory.New() 各自创建独立 Agent。
type Agent struct {
	factory   *Factory
	sessionID string
	history   []Message
	maxLoops  int
}

// SessionID 返回本 Agent 的唯一会话标识符。
func (a *Agent) SessionID() string {
	return a.sessionID
}

// History 返回当前对话历史的只读副本。
func (a *Agent) History() []Message {
	cp := make([]Message, len(a.history))
	copy(cp, a.history)
	return cp
}

// ClearHistory 清空对话历史，但保留 system 消息（如有）。
func (a *Agent) ClearHistory() {
	var sys []Message
	for _, m := range a.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	a.history = sys
}

// SetMaxLoops 设置单次 Chat 调用中最多允许的 tool_call 循环次数。
// 默认值为 8；设置过大可能导致长时间阻塞。
func (a *Agent) SetMaxLoops(n int) {
	if n > 0 {
		a.maxLoops = n
	}
}

// Chat 追加 userInput 消息，驱动 LLM 推理并自动执行 tool_calls，
// 直至 LLM 返回纯文本回复或达到 maxLoops 上限。
//
// 循环流程：
//  1. 调用 LLM（携带完整历史 + 当前可用工具列表）
//  2. 若回复含 tool_calls → 依次 dispatch → 结果追加为 tool 消息
//  3. 重新调用 LLM（携带工具结果）
//  4. 重复直到没有 tool_calls 或达到 maxLoops
//
// 每轮开始前都会实时读取 registry 刷新工具列表，支持热更新。
func (a *Agent) Chat(ctx context.Context, userInput string) (string, error) {
	if userInput != "" {
		a.history = append(a.history, Message{Role: "user", Content: userInput})
	}

	tools := a.factory.tools()

	for loop := 0; loop < a.maxLoops; loop++ {
		msg, err := a.factory.llm.complete(ctx, a.history, tools)
		if err != nil {
			return "", fmt.Errorf("agent[%s] chat loop %d: %w", a.sessionID, loop, err)
		}

		// 将 assistant 回复追加到历史
		a.history = append(a.history, Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})

		// 没有 tool_calls → 直接返回文本回复
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		// 依次执行每个 tool_call
		for _, tc := range msg.ToolCalls {
			start := time.Now()
			result, dispErr := a.factory.dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			elapsed := time.Since(start).Milliseconds()

			var toolContent string
			if dispErr != nil {
				log.Printf("[Agent %s] tool_call %s FAILED (%dms): %v",
					a.sessionID, tc.Function.Name, elapsed, dispErr)
				toolContent = fmt.Sprintf(`{"error":%q}`, dispErr.Error())
			} else {
				log.Printf("[Agent %s] tool_call %s OK (%dms)",
					a.sessionID, tc.Function.Name, elapsed)
				toolContent = result
			}

			a.history = append(a.history, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    toolContent,
			})
		}

		// 刷新工具列表（registry 支持热更新）
		tools = a.factory.tools()
	}

	return "", fmt.Errorf("agent[%s]: reached maxLoops (%d) without a final text reply",
		a.sessionID, a.maxLoops)
}
