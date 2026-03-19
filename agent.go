package Seele

import (
	"context"
	"fmt"
	"log"
	"sync"
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

		a.history = append(a.history, Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		type dispatchResult struct {
			tc      ToolCall
			content string
		}
		results := make([]dispatchResult, len(msg.ToolCalls))

		var wg sync.WaitGroup
		for i, tc := range msg.ToolCalls {
			wg.Add(1)
			go func(i int, tc ToolCall) {
				defer wg.Done()
				start := time.Now()
				result, dispErr := a.factory.dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
				elapsed := time.Since(start).Milliseconds()

				if dispErr != nil {
					log.Printf("[Agent %s] tool_call %s FAILED (%dms): %v",
						a.sessionID, tc.Function.Name, elapsed, dispErr)
					results[i] = dispatchResult{tc, fmt.Sprintf(`{"error":%q}`, dispErr.Error())}
				} else {
					log.Printf("[Agent %s] tool_call %s OK (%dms)",
						a.sessionID, tc.Function.Name, elapsed)
					results[i] = dispatchResult{tc, result}
				}
			}(i, tc)
		}
		wg.Wait()

		for _, r := range results {
			a.history = append(a.history, Message{
				Role:       "tool",
				ToolCallID: r.tc.ID,
				Name:       r.tc.Function.Name,
				Content:    r.content,
			})
		}

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
		fullContent, toolCalls, err := a.factory.llm.completeStream(
			ctx, a.history, tools,
			func(delta string) {
				onChunk(delta)
			},
		)
		if err != nil {
			return "", fmt.Errorf("agent[%s] stream loop %d: %w", a.sessionID, loop, err)
		}

		// ── 无 tool_calls → 最终文本回复，流已经推完了 ───────────────
		if len(toolCalls) == 0 {
			a.history = append(a.history, Message{
				Role:    "assistant",
				Content: fullContent,
			})
			return fullContent, nil
		}

		// ── 有 tool_calls → 并发 dispatch，等全部完成再追加 history ───
		a.history = append(a.history, Message{
			Role:      "assistant",
			Content:   "",
			ToolCalls: toolCalls,
		})

		type dispatchResult struct {
			tc      ToolCall
			content string
		}
		results := make([]dispatchResult, len(toolCalls))

		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(i int, tc ToolCall) {
				defer wg.Done()
				start := time.Now()
				result, dispErr := a.factory.dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
				elapsed := time.Since(start).Milliseconds()

				if dispErr != nil {
					log.Printf("[Agent %s] tool_call %s FAILED (%dms): %v",
						a.sessionID, tc.Function.Name, elapsed, dispErr)
					results[i] = dispatchResult{tc, fmt.Sprintf(`{"error":%q}`, dispErr.Error())}
				} else {
					log.Printf("[Agent %s] tool_call %s OK (%dms)",
						a.sessionID, tc.Function.Name, elapsed)
					results[i] = dispatchResult{tc, result}
				}
			}(i, tc)
		}
		wg.Wait()

		for _, r := range results {
			a.history = append(a.history, Message{
				Role:       "tool",
				ToolCallID: r.tc.ID,
				Name:       r.tc.Function.Name,
				Content:    r.content,
			})
		}

		tools = a.factory.tools()
	}

	return "", fmt.Errorf("agent[%s]: reached maxLoops (%d) without a final text reply",
		a.sessionID, a.maxLoops)
}
