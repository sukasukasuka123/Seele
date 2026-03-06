// test/integration_test.go
//
// 集成测试 demo，演示 agentfactory 库的完整使用。
// 运行前需要：
//   1. 启动三个 example_tools（echo / ping / suka_secret）
//   2. 准备好 test/config.yaml（参考 config_example/config.yaml）
//
// 运行：
//   cd test && go test -v -run TestDemo -timeout 120s
//   或跑全部：
//   cd test && go test -v -timeout 120s

package test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	agentfactory "github.com/sukasukasuka123/Seele"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

// ── 测试基础设施 ────────────────────────────────────────────────

// mockLLMServer 启动一个本地 HTTP 服务伪装成 OpenAI 端点。
// 通过 respond 函数控制每次返回的内容，方便单元级集成测试不依赖真实 API。
type mockLLMServer struct {
	srv     *httptest.Server
	respond func(msgs []agentfactory.Message) agentfactory.Message
}

func newMockLLM(respond func(msgs []agentfactory.Message) agentfactory.Message) *mockLLMServer {
	m := &mockLLMServer{respond: respond}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []agentfactory.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		reply := m.respond(req.Messages)

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": reply, "finish_reason": "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return m
}

func (m *mockLLMServer) baseURL() string { return m.srv.URL }
func (m *mockLLMServer) close()          { m.srv.Close() }

// stubHubHandler 实现 hubbase.HubHandler 接口，不做任何路由（unit test 用）
type stubHubHandler struct{}

func (h *stubHubHandler) ServiceName() string                                       { return "stub-hub" }
func (h *stubHubHandler) Execute(*pb.ToolRequest) ([]hubbase.DispatchTarget, error) { return nil, nil }
func (h *stubHubHandler) OnResults([]hubbase.DispatchResult)                        {}
func (h *stubHubHandler) Addrs() []string                                           { return nil }

// newTestFactory 构建一个使用 mockLLM + stub Hub 的 Factory。
// skill 列表来自 registry，调用此函数前需已调用 registry.Init。
// 若无 registry（纯单元测试），Factory 的 tools() 返回空列表，不影响纯对话逻辑测试。
func newTestFactory(t *testing.T, mock *mockLLMServer) *agentfactory.Factory {
	t.Helper()
	hub := hubbase.New(&stubHubHandler{})
	f, err := agentfactory.NewFactory(agentfactory.LLMConfig{
		BaseURL: mock.baseURL(),
		APIKey:  "test-key",
		Model:   "test-model",
		Timeout: 5 * time.Second,
	}, hub)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	return f
}

// ── Unit Tests ──────────────────────────────────────────────────

// TestNewFactory_Validation 验证参数校验逻辑
func TestNewFactory_Validation(t *testing.T) {
	hub := hubbase.New(&stubHubHandler{})

	cases := []struct {
		name    string
		cfg     agentfactory.LLMConfig
		hub     *hubbase.BaseHub
		wantErr bool
	}{
		{
			name:    "hub nil",
			cfg:     agentfactory.LLMConfig{BaseURL: "http://x", Model: "m"},
			hub:     nil,
			wantErr: true,
		},
		{
			name:    "missing BaseURL",
			cfg:     agentfactory.LLMConfig{Model: "m"},
			hub:     hub,
			wantErr: true,
		},
		{
			name:    "missing Model",
			cfg:     agentfactory.LLMConfig{BaseURL: "http://x"},
			hub:     hub,
			wantErr: true,
		},
		{
			name:    "valid",
			cfg:     agentfactory.LLMConfig{BaseURL: "http://x", Model: "m"},
			hub:     hub,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentfactory.NewFactory(tc.cfg, tc.hub)
			if (err != nil) != tc.wantErr {
				t.Errorf("wantErr=%v, got err=%v", tc.wantErr, err)
			}
		})
	}
}

// TestFactory_RetireRestore 验证 skill 的运行时屏蔽与恢复
// skill 来源是 registry，因此这里用真实 registry 初始化
func TestFactory_RetireRestore(t *testing.T) {
	if err := registry.Init("../config_example/registry.yaml"); err != nil {
		t.Skipf("registry.yaml not found, skipping: %v", err)
	}

	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	all := f.Skills()
	if len(all) == 0 {
		t.Skip("registry has no tools, skipping")
	}

	target := all[0].Name

	// 屏蔽第一个 skill
	f.Retire(target)
	after := f.Skills()
	for _, s := range after {
		if s.Name == target {
			t.Errorf("skill %q should be retired but still visible", target)
		}
	}
	if len(after) != len(all)-1 {
		t.Errorf("expected %d skills after retire, got %d", len(all)-1, len(after))
	}

	// 恢复
	f.Restore(target)
	restored := f.Skills()
	found := false
	for _, s := range restored {
		if s.Name == target {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("skill %q should be restored but not visible", target)
	}
}

// TestFactory_New_SystemPrompt 验证 New 注入 system prompt
func TestFactory_New_SystemPrompt(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	// 有 system prompt
	a := f.New("你是助手")
	hist := a.History()
	if len(hist) != 1 || hist[0].Role != "system" || hist[0].Content != "你是助手" {
		t.Errorf("unexpected history: %+v", hist)
	}

	// 无 system prompt
	b := f.New("")
	if len(b.History()) != 0 {
		t.Errorf("expected empty history, got %+v", b.History())
	}
}

// TestAgent_SetSystem 验证 SetSystem 的替换与插入行为
func TestAgent_SetSystem(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	t.Run("replace existing system", func(t *testing.T) {
		a := f.New("旧 prompt")
		a.SetSystem("新 prompt")
		hist := a.History()
		if hist[0].Content != "新 prompt" {
			t.Errorf("expected '新 prompt', got %q", hist[0].Content)
		}
		if len(hist) != 1 {
			t.Errorf("expected 1 message, got %d", len(hist))
		}
	})

	t.Run("insert when no system", func(t *testing.T) {
		a := f.New("")
		a.SetSystem("新增 prompt")
		hist := a.History()
		if len(hist) != 1 || hist[0].Role != "system" {
			t.Errorf("unexpected history: %+v", hist)
		}
	})
}

// TestAgent_Reset 验证历史清空逻辑
func TestAgent_Reset(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	t.Run("reset keep system", func(t *testing.T) {
		a := f.New("system prompt")
		ctx := context.Background()
		_, _ = a.Chat(ctx, "消息1")
		_, _ = a.Chat(ctx, "消息2")

		a.Reset(true)
		hist := a.History()
		if len(hist) != 1 || hist[0].Role != "system" {
			t.Errorf("expected only system msg, got %+v", hist)
		}
	})

	t.Run("reset drop all", func(t *testing.T) {
		a := f.New("system prompt")
		ctx := context.Background()
		_, _ = a.Chat(ctx, "消息1")

		a.Reset(false)
		if len(a.History()) != 0 {
			t.Errorf("expected empty history, got %+v", a.History())
		}
	})
}

// TestAgent_Chat_PlainReply 验证无 tool_call 时的普通对话
func TestAgent_Chat_PlainReply(t *testing.T) {
	mock := newMockLLM(func(msgs []agentfactory.Message) agentfactory.Message {
		last := msgs[len(msgs)-1]
		return agentfactory.Message{
			Role:    "assistant",
			Content: "你说了：" + last.Content,
		}
	})
	defer mock.close()
	f := newTestFactory(t, mock)
	a := f.New("你是助手")

	reply, err := a.Chat(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply != "你说了：hello" {
		t.Errorf("unexpected reply: %q", reply)
	}

	// history: system + user + assistant
	hist := a.History()
	if len(hist) != 3 {
		t.Errorf("expected 3 messages, got %d", len(hist))
	}
	if hist[1].Role != "user" || hist[2].Role != "assistant" {
		t.Errorf("unexpected roles: %v %v", hist[1].Role, hist[2].Role)
	}
}

// TestAgent_Chat_MultiTurn 验证多轮对话历史累积
func TestAgent_Chat_MultiTurn(t *testing.T) {
	turn := 0
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		turn++
		return agentfactory.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("第%d轮回复", turn),
		}
	})
	defer mock.close()
	f := newTestFactory(t, mock)
	a := f.New("")

	for i := 1; i <= 3; i++ {
		reply, err := a.Chat(context.Background(), fmt.Sprintf("消息%d", i))
		if err != nil {
			t.Fatalf("turn %d Chat: %v", i, err)
		}
		want := fmt.Sprintf("第%d轮回复", i)
		if reply != want {
			t.Errorf("turn %d: want %q, got %q", i, want, reply)
		}
	}

	// 3轮：每轮 user+assistant，共 6 条
	if len(a.History()) != 6 {
		t.Errorf("expected 6 messages, got %d", len(a.History()))
	}
}

// TestAgent_Chat_ToolCall 验证 tool_call → tool_result → 最终回复 的完整循环
// dispatch 到 stub hub 会失败，但 LLM mock 仍能感知 tool message 并推进循环。
func TestAgent_Chat_ToolCall(t *testing.T) {
	callCount := 0
	mock := newMockLLM(func(msgs []agentfactory.Message) agentfactory.Message {
		callCount++
		if callCount == 1 {
			return agentfactory.Message{
				Role: "assistant",
				ToolCalls: []agentfactory.ToolCall{
					{
						ID:   "call_001",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{
							Name:      "echo",
							Arguments: `{"content":"hi"}`,
						},
					},
				},
			}
		}
		// 第二轮：有 tool message 则回复确认
		for _, m := range msgs {
			if m.Role == "tool" && m.Name == "echo" {
				return agentfactory.Message{Role: "assistant", Content: "echo 已执行：" + m.Content}
			}
		}
		return agentfactory.Message{Role: "assistant", Content: "完成"}
	})
	defer mock.close()

	f := newTestFactory(t, mock)
	a := f.New("")
	_, err := a.Chat(context.Background(), "帮我 echo hi")

	// 不关心 dispatch 是否成功，只验证 tool_call 循环正确推进了两轮
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls (tool_call loop), got %d", callCount)
	}
	_ = err
}

// TestAgent_Chat_MaxLoops 验证超出 maxLoops 时的降级返回
func TestAgent_Chat_MaxLoops(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{
			Role: "assistant",
			ToolCalls: []agentfactory.ToolCall{
				{
					ID:   "call_loop",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "echo", Arguments: `{"content":"loop"}`},
				},
			},
		}
	})
	defer mock.close()

	f := newTestFactory(t, mock)
	a := f.New("")
	a.SetMaxLoops(2)

	reply, err := a.Chat(context.Background(), "进入循环")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(reply, "最大尝试次数") {
		t.Errorf("expected fallback message, got: %q", reply)
	}
}

// TestAgent_SessionID 验证每个 Agent 有唯一 SessionID
func TestAgent_SessionID(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	ids := make(map[string]bool)
	for i := 0; i < 10; i++ {
		id := f.New("").SessionID()
		if id == "" {
			t.Fatal("empty session ID")
		}
		if ids[id] {
			t.Fatalf("duplicate session ID: %s", id)
		}
		ids[id] = true
	}
}

// TestTruncate 验证历史截断（发送超过 keep 数量的消息后历史不超限）
func TestTruncate(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	a := f.New("system")
	ctx := context.Background()

	// 发送 30 轮对话触发截断（keep=20）
	for i := 0; i < 30; i++ {
		_, err := a.Chat(ctx, fmt.Sprintf("消息%d", i))
		if err != nil {
			t.Fatalf("Chat %d: %v", i, err)
		}
	}

	hist := a.History()
	// 截断后：system(1) + 最近 20 条，最多 21 条
	if len(hist) > 21 {
		t.Errorf("expected at most 21 messages after truncation, got %d", len(hist))
	}
	if hist[0].Role != "system" {
		t.Error("system prompt should be preserved after truncation")
	}
}

// TestLoadConfig 验证配置文件加载（需要 test/config.yaml 存在）
func TestLoadConfig(t *testing.T) {
	cfg, err := agentfactory.LoadConfig("config.yaml")
	if err != nil {
		t.Skipf("config.yaml not found, skipping: %v", err)
	}
	if cfg.BaseURL == "" {
		t.Error("BaseURL should not be empty")
	}
	if cfg.Model == "" {
		t.Error("Model should not be empty")
	}
	if cfg.Timeout == 0 {
		t.Error("Timeout should not be zero")
	}
}

// ── Integration Test（需要真实环境）────────────────────────────
//
// 运行条件：
//   - TEST_INTEGRATION=1 环境变量已设置
//   - test/config.yaml 已配置真实 API Key
//   - example_tools 已启动（echo :50101 / ping :50102 / suka_secret :50100）

func TestDemo(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("跳过集成测试（设置 TEST_INTEGRATION=1 启用）")
	}

	// ── 初始化 registry（skill 列表从 yaml 来，无需手动 Register）──
	if err := registry.Init("../config_example/registry.yaml"); err != nil {
		t.Fatalf("registry init: %v", err)
	}

	hub := hubbase.New(&demoRouter{})
	go func() {
		if err := hub.ServeAsync(":50051", 0); err != nil {
			t.Logf("hub: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	llmCfg, err := agentfactory.LoadConfig("config.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	f, err := agentfactory.NewFactory(llmCfg, hub)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	t.Logf("已加载 %d 个 skill（来自 registry.yaml）", len(f.Skills()))

	// ── 创建两个独立 Agent，共享同一套 skill ──────────────────
	eva := f.New(
		"你是 suka-eva，一个通过微服务架构动态扩展 skill 的 AI 助手。" +
			"请回答用户的问题，需要时主动调用合适的 skill。",
	)
	debugger := f.New("你是调试助手，优先调用 echo 和 ping 进行网络诊断。")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cases := []struct {
		agent *agentfactory.Agent
		label string
		input string
	}{
		{eva, "eva", "你好，介绍一下你自己"},
		{eva, "eva", "帮我 echo 一下「测试消息」"},
		{eva, "eva", "ping 一下 127.0.0.1"},
		{debugger, "debugger", "诊断一下本机网络"},
		{eva, "eva", "叫一下 suka_secret"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%s", tc.label, tc.input[:min(10, len(tc.input))]), func(t *testing.T) {
			reply, err := tc.agent.Chat(ctx, tc.input)
			if err != nil {
				t.Errorf("Chat error: %v", err)
				return
			}
			if reply == "" {
				t.Error("got empty reply")
				return
			}
			t.Logf("input:  %s", tc.input)
			t.Logf("reply:  %s", reply)
		})
	}

	// 验证两个 Agent 的历史完全独立
	evaHist := eva.History()
	dbgHist := debugger.History()
	if len(evaHist) > 0 && len(dbgHist) > 0 &&
		evaHist[len(evaHist)-1].Content == dbgHist[len(dbgHist)-1].Content {
		t.Error("eva and debugger histories should be independent")
	}

	// 验证 Retire 对所有 Agent 立即生效（以第一个 skill 为测试目标）
	skills := f.Skills()
	if len(skills) > 0 {
		target := skills[0].Name
		f.Retire(target)
		for _, s := range f.Skills() {
			if s.Name == target {
				t.Errorf("skill %q should have been retired", target)
			}
		}
		f.Restore(target) // 恢复，避免影响后续用例
	}

	t.Logf("eva:      %s", eva.Summary())
	t.Logf("debugger: %s", debugger.Summary())
}

// TestDemo_Interactive 交互式 REPL（手动测试用，需要真实环境）
// 运行：go test -v -run TestDemo_Interactive -timeout 600s
func TestDemo_Interactive(t *testing.T) {
	if os.Getenv("TEST_INTERACTIVE") == "" {
		t.Skip("跳过交互测试（设置 TEST_INTERACTIVE=1 启用）")
	}

	if err := registry.Init("../config_example/registry.yaml"); err != nil {
		t.Fatalf("registry init: %v", err)
	}
	hub := hubbase.New(&demoRouter{})
	go func() { _ = hub.ServeAsync(":50051", 0) }()
	time.Sleep(200 * time.Millisecond)

	llmCfg, err := agentfactory.LoadConfig("config.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	f, err := agentfactory.NewFactory(llmCfg, hub)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	a := f.New("你是 suka-eva，一个通过微服务架构动态扩展 skill 的 AI 助手。")
	ctx := context.Background()

	fmt.Println("=== suka-eva 交互测试 ===")
	fmt.Printf("已加载 %d 个 skill\n", len(f.Skills()))
	fmt.Println("命令：quit=退出 | reset=清空 | skills=查看工具")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		switch input {
		case "quit", "exit":
			return
		case "reset":
			a.Reset(true)
			fmt.Println("[已重置]")
			continue
		case "skills":
			for _, s := range f.Skills() {
				fmt.Printf("  %-20s %s\n", s.Name, s.Description)
			}
			continue
		}
		reply, err := a.Chat(ctx, input)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		fmt.Printf("eva: %s\n\n", reply)
	}
}

// ── Hub 路由（集成测试专用）────────────────────────────────────

type demoRouter struct{}

func (r *demoRouter) ServiceName() string { return "test-hub" }
func (r *demoRouter) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	t, ok := registry.SelectToolByName(req.ServiceName)
	if !ok {
		return nil, fmt.Errorf("skill %q not in registry", req.ServiceName)
	}
	req.From = r.ServiceName()
	return []hubbase.DispatchTarget{{Addr: t.Addr, Request: req, Stream: true}}, nil
}
func (r *demoRouter) OnResults(results []hubbase.DispatchResult) {
	for _, res := range results {
		if !res.AllOK() {
			fmt.Printf("[Hub] error: %v\n", res.Err)
		}
	}
}
func (r *demoRouter) Addrs() []string {
	tools := registry.GetAllTools()
	addrs := make([]string, len(tools))
	for i, t := range tools {
		addrs[i] = t.Addr
	}
	return addrs
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
