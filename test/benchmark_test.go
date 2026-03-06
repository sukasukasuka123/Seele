// test/benchmark_test.go

package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	agentfactory "github.com/sukasukasuka123/Seele"

	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
)

// ── Benchmark 公共基础设施 ──────────────────────────────────────

// newBenchFactory 创建用于 benchmark 的 Factory（使用 mockLLM，不消耗真实 API）
func newBenchFactory(b *testing.B, respond func([]agentfactory.Message) agentfactory.Message) *agentfactory.Factory {
	b.Helper()
	mock := newMockLLM(respond)
	b.Cleanup(mock.close)

	hub := hubbase.New(&stubHubHandler{})
	f, err := agentfactory.NewFactory(agentfactory.LLMConfig{
		BaseURL: mock.baseURL(),
		APIKey:  "bench-key",
		Model:   "bench-model",
		Timeout: 5 * time.Second,
	}, hub)
	if err != nil {
		b.Fatalf("NewFactory: %v", err)
	}
	return f
}

// plainResponder 返回一个永远回复纯文本的 LLM mock（不触发 tool_call）
func plainResponder(content string) func([]agentfactory.Message) agentfactory.Message {
	return func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: content}
	}
}

// ── Factory benchmarks ──────────────────────────────────────────

// BenchmarkFactory_New 衡量创建 Agent 的开销
func BenchmarkFactory_New(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.New("system prompt")
	}
}

// BenchmarkFactory_New_Parallel 衡量并发创建 Agent 时 Factory 的竞争情况
func BenchmarkFactory_New_Parallel(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = f.New("prompt")
		}
	})
}

// BenchmarkFactory_Skills 衡量列举 skill 的开销（从 registry 读取 + retired 过滤）
// skill 列表来自 registry，数量取决于 registry.yaml；若无 registry 则为空列表，
// 仍可测试锁争用和 slice 分配开销。
func BenchmarkFactory_Skills(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Skills()
	}
}

// BenchmarkFactory_RetireRestore 衡量 Retire/Restore 的锁开销
// skill 名称不必在 registry 中真实存在，Retire/Restore 只操作 retired map。
func BenchmarkFactory_RetireRestore(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("skill_%d", i%16) // 16 个 slot 循环，模拟真实场景
		f.Retire(name)
		f.Restore(name)
	}
}

// BenchmarkFactory_RetireRestore_Parallel 衡量并发 Retire/Restore 的 mutex 性能
func BenchmarkFactory_RetireRestore_Parallel(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			name := fmt.Sprintf("skill_%d", i%16)
			f.Retire(name)
			f.Restore(name)
			i++
		}
	})
}

// ── Agent Chat benchmarks ───────────────────────────────────────

// BenchmarkAgent_Chat_SingleTurn 衡量单轮对话的端到端延迟（本地 mock，无网络）
func BenchmarkAgent_Chat_SingleTurn(b *testing.B) {
	f := newBenchFactory(b, plainResponder("这是回复"))
	a := f.New("你是助手")
	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		reply, err := a.Chat(ctx, "你好")
		if err != nil || reply == "" {
			b.Fatalf("Chat failed: err=%v reply=%q", err, reply)
		}
		a.Reset(true)
	}
}

// BenchmarkAgent_Chat_Parallel 衡量多个独立 Agent 并发 Chat 的吞吐量
func BenchmarkAgent_Chat_Parallel(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	ctx := context.Background()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		a := f.New("system")
		for pb.Next() {
			reply, err := a.Chat(ctx, "ping")
			if err != nil || reply == "" {
				b.Errorf("Chat failed: %v", err)
			}
			a.Reset(true)
		}
	})
}

// BenchmarkAgent_Chat_LongHistory 衡量携带较长历史时单次 Chat 的开销
func BenchmarkAgent_Chat_LongHistory(b *testing.B) {
	f := newBenchFactory(b, plainResponder("回复"))
	ctx := context.Background()

	// 预热：构造 historyDepth 轮历史后固定住
	const historyDepth = 18 // 不超过 truncate keep=20，避免截断干扰
	base := f.New("system")
	for i := 0; i < historyDepth; i++ {
		_, _ = base.Chat(ctx, fmt.Sprintf("预热消息%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = base.Chat(ctx, "bench消息")
		// 截断到固定深度，保持 bench 条件稳定
		base.Reset(true)
		for j := 0; j < historyDepth; j++ {
			_, _ = base.Chat(ctx, fmt.Sprintf("恢复消息%d", j))
		}
	}
}
