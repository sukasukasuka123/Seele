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

// ChatStream 与 Chat 行为完全一致，但最终的文本回复改为流式推送。
//
// 流程：
//   - tool_call 轮次：走非流式 complete（LLM 必须返回完整 JSON 才能 dispatch）
//   - 最终文本轮次：走流式 completeStream，每个 delta 同步调用 onChunk
//
// onChunk 在 LLM 推送每个文本 token 时被同步调用；
// 所有 chunk 拼接即完整回复，也作为返回值返回（同时追加进 history）。
func (a *Agent) ChatStream(ctx context.Context, userInput string, onChunk func(delta string)) (string, error) {
	if userInput != "" {
		a.history = append(a.history, Message{Role: "user", Content: userInput})
	}

	tools := a.factory.tools()

	for loop := 0; loop < a.maxLoops; loop++ {
		// ── 先用非流式判断是否有 tool_calls ──────────────────────────
		// tool_calls 的 JSON 必须完整才能 dispatch，流式逐帧拼接虽可行但
		// 复杂度高；此处采用"tool_call 轮非流式，最终文本轮流式"策略，
		// 保持代码清晰，代价是 tool_call 轮不产生流式输出（本就无需展示）。
		msg, err := a.factory.llm.complete(ctx, a.history, tools)
		if err != nil {
			return "", fmt.Errorf("agent[%s] stream loop %d: %w", a.sessionID, loop, err)
		}

		// ── 无 tool_calls → 这是最终回复轮，改用流式重新请求 ──────────
		if len(msg.ToolCalls) == 0 {
			// 丢弃上面的非流式结果，用流式重发相同的 history 获取 token 流。
			// history 此时尚未追加 assistant 消息，因此重发内容与上次完全一致。
			fullContent, _, streamErr := a.factory.llm.completeStream(ctx, a.history, tools, onChunk)
			if streamErr != nil {
				return "", fmt.Errorf("agent[%s] final stream loop %d: %w", a.sessionID, loop, streamErr)
			}
			a.history = append(a.history, Message{
				Role:    "assistant",
				Content: fullContent,
			})
			return fullContent, nil
		}

		// ── 有 tool_calls → 追加历史，依次 dispatch ───────────────────
		a.history = append(a.history, Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})

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

		tools = a.factory.tools()
	}

	return "", fmt.Errorf("agent[%s]: reached maxLoops (%d) without a final text reply",
		a.sessionID, a.maxLoops)
}
