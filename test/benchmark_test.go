// test/benchmark_test.go
package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	runtime "github.com/sukasukasuka123/Seele"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
)

// newBenchFactory 创建用于 benchmark 的 Factory（mockLLM，不消耗真实 API）
func newBenchFactory(b *testing.B, respond func([]runtime.Message) runtime.Message) *runtime.Runtime {
	b.Helper()
	mock := newMockLLM(respond)
	b.Cleanup(mock.close)

	hub := hubbase.New(&stubHubHandler{})
	f, err := runtime.NewRuntime(runtime.LLMConfig{
		BaseURL: mock.baseURL(),
		APIKey:  "bench-key",
		Model:   "bench-model",
		Timeout: 5,
	}, hub, 5*time.Second)
	if err != nil {
		b.Fatalf("NewFactory: %v", err)
	}
	return f
}

func plainResponder(content string) func([]runtime.Message) runtime.Message {
	return func(_ []runtime.Message) runtime.Message {
		return runtime.Message{Role: "assistant", Content: content}
	}
}

// ── Factory benchmarks ──────────────────────────────────────────

func BenchmarkFactory_New(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.New("system prompt")
	}
}

func BenchmarkFactory_New_Parallel(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = f.New("prompt")
		}
	})
}

func BenchmarkFactory_Skills(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Skills()
	}
}

func BenchmarkFactory_RetireRestore(b *testing.B) {
	f := newBenchFactory(b, plainResponder("ok"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("skill_%d", i%16)
		f.Retire(name)
		f.Restore(name)
	}
}

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

// BenchmarkAgent_Chat_SingleTurn 单轮对话端到端延迟（本地 mock，无网络）
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
		a.ClearHistory() // 对应旧版 Reset(true)，保留 system 消息
	}
}

// BenchmarkAgent_Chat_Parallel 多个独立 Agent 并发 Chat 吞吐量
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
			a.ClearHistory()
		}
	})
}

// BenchmarkAgent_Chat_LongHistory 携带较长历史时单次 Chat 的开销
func BenchmarkAgent_Chat_LongHistory(b *testing.B) {
	f := newBenchFactory(b, plainResponder("回复"))
	ctx := context.Background()

	const historyDepth = 18
	base := f.New("system")
	for i := 0; i < historyDepth; i++ {
		_, _ = base.Chat(ctx, fmt.Sprintf("预热消息%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = base.Chat(ctx, "bench消息")
		base.ClearHistory()
		for j := 0; j < historyDepth; j++ {
			_, _ = base.Chat(ctx, fmt.Sprintf("恢复消息%d", j))
		}
	}
}
