// test/unit_test.go
package test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentfactory "github.com/sukasukasuka123/Seele"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

// ── 测试基础设施 ────────────────────────────────────────────────

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

func newTestFactory(t *testing.T, mock *mockLLMServer) *agentfactory.Factory {
	t.Helper()
	hub := hubbase.New(&stubHubHandler{})
	f, err := agentfactory.NewFactory(agentfactory.LLMConfig{
		BaseURL: mock.baseURL(),
		APIKey:  "test-key",
		Model:   "test-model",
		Timeout: 5,
	}, hub, 5*time.Second)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	return f
}

// ── 微服务自动启动 ──────────────────────────────────────────────

// startTool 用 go run 启动一个 example_tool 子进程，返回 cleanup 函数。
// toolPkg 是完整 import 路径，如 "github.com/sukasukasuka123/Seele/example_tools/echo"。
func startTool(t *testing.T, toolPkg string) func() {
	t.Helper()
	cmd := exec.Command("go", "run", toolPkg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tool %s: %v", toolPkg, err)
	}
	t.Logf("started %s (pid=%d)", toolPkg, cmd.Process.Pid)
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

// waitPort 轮询 TCP 端口直到可达或超时。
func waitPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("port %s not reachable after %s", addr, timeout)
}

// ── Unit Tests ──────────────────────────────────────────────────

func TestNewFactory_Validation(t *testing.T) {
	hub := hubbase.New(&stubHubHandler{})
	cases := []struct {
		name    string
		cfg     agentfactory.LLMConfig
		hub     *hubbase.BaseHub
		wantErr bool
	}{
		{"hub nil", agentfactory.LLMConfig{BaseURL: "http://x", Model: "m"}, nil, true},
		{"missing BaseURL", agentfactory.LLMConfig{Model: "m"}, hub, true},
		{"missing Model", agentfactory.LLMConfig{BaseURL: "http://x"}, hub, true},
		{"valid", agentfactory.LLMConfig{BaseURL: "http://x", Model: "m"}, hub, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentfactory.NewFactory(tc.cfg, tc.hub, 5*time.Second)
			if (err != nil) != tc.wantErr {
				t.Errorf("wantErr=%v, got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestFactory_RetireRestore(t *testing.T) {
	if err := registry.Init("../config_example/registry.yaml"); err != nil {
		t.Skipf("registry.yaml not found: %v", err)
	}
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	all := f.Skills()
	if len(all) == 0 {
		t.Skip("registry has no tools")
	}
	target := all[0].Name

	f.Retire(target)
	for _, s := range f.Skills() {
		if s.Name == target {
			t.Errorf("skill %q should be retired", target)
		}
	}
	f.Restore(target)
	found := false
	for _, s := range f.Skills() {
		if s.Name == target {
			found = true
		}
	}
	if !found {
		t.Errorf("skill %q should be restored", target)
	}
}

func TestFactory_New_SystemPrompt(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)

	a := f.New("你是助手")
	hist := a.History()
	if len(hist) != 1 || hist[0].Role != "system" || hist[0].Content != "你是助手" {
		t.Errorf("unexpected history: %+v", hist)
	}
	b := f.New("")
	if len(b.History()) != 0 {
		t.Errorf("expected empty history, got %+v", b.History())
	}
}

func TestAgent_ClearHistory(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{Role: "assistant", Content: "ok"}
	})
	defer mock.close()
	f := newTestFactory(t, mock)
	ctx := context.Background()

	t.Run("keeps system", func(t *testing.T) {
		a := f.New("system prompt")
		_, _ = a.Chat(ctx, "消息1")
		_, _ = a.Chat(ctx, "消息2")
		a.ClearHistory()
		hist := a.History()
		if len(hist) != 1 || hist[0].Role != "system" {
			t.Errorf("expected only system msg, got %+v", hist)
		}
	})

	t.Run("no system", func(t *testing.T) {
		a := f.New("")
		_, _ = a.Chat(ctx, "消息1")
		a.ClearHistory()
		if len(a.History()) != 0 {
			t.Errorf("expected empty history, got %+v", a.History())
		}
	})
}

func TestAgent_Chat_PlainReply(t *testing.T) {
	mock := newMockLLM(func(msgs []agentfactory.Message) agentfactory.Message {
		last := msgs[len(msgs)-1]
		return agentfactory.Message{Role: "assistant", Content: "你说了：" + last.Content}
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
	if len(a.History()) != 3 {
		t.Errorf("expected 3 messages, got %d", len(a.History()))
	}
}

func TestAgent_Chat_MultiTurn(t *testing.T) {
	turn := 0
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		turn++
		return agentfactory.Message{Role: "assistant", Content: fmt.Sprintf("第%d轮回复", turn)}
	})
	defer mock.close()
	f := newTestFactory(t, mock)
	a := f.New("")

	for i := 1; i <= 3; i++ {
		reply, err := a.Chat(context.Background(), fmt.Sprintf("消息%d", i))
		if err != nil {
			t.Fatalf("turn %d Chat: %v", i, err)
		}
		if want := fmt.Sprintf("第%d轮回复", i); reply != want {
			t.Errorf("turn %d: want %q, got %q", i, want, reply)
		}
	}
	if len(a.History()) != 6 {
		t.Errorf("expected 6 messages, got %d", len(a.History()))
	}
}

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
						Function: agentfactory.ToolCallFunction{
							Name:      "echo",
							Arguments: `{"message":"hi"}`,
						},
					},
				},
			}
		}
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
	_, _ = a.Chat(context.Background(), "帮我 echo hi")
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}
}

func TestAgent_Chat_MaxLoops(t *testing.T) {
	mock := newMockLLM(func(_ []agentfactory.Message) agentfactory.Message {
		return agentfactory.Message{
			Role: "assistant",
			ToolCalls: []agentfactory.ToolCall{
				{
					ID:   "call_loop",
					Type: "function",
					Function: agentfactory.ToolCallFunction{
						Name:      "echo",
						Arguments: `{"message":"loop"}`,
					},
				},
			},
		}
	})
	defer mock.close()

	f := newTestFactory(t, mock)
	a := f.New("")
	a.SetMaxLoops(2)

	_, err := a.Chat(context.Background(), "进入循环")
	// 超出 maxLoops 时 agent.go 返回 error
	if err == nil {
		t.Error("expected error when maxLoops exceeded")
	}
}

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
		time.Sleep(1 * time.Microsecond)
		if ids[id] {
			t.Fatalf("duplicate session ID: %s", id)
		}
		ids[id] = true
	}
}

func TestLoadConfig(t *testing.T) {
	cfg, err := agentfactory.LoadConfig("config.yaml")
	if err != nil {
		t.Skipf("config.yaml not found: %v", err)
	}
	if cfg.BaseURL == "" {
		t.Error("BaseURL should not be empty")
	}
	if cfg.Model == "" {
		t.Error("Model should not be empty")
	}
}

// ── Integration Test（自动启动微服务）──────────────────────────
// TEST_INTEGRATION=1 go test -v -run TestDemo -timeout 120s

func TestDemo(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("跳过集成测试（设置 TEST_INTEGRATION=1 启用）")
	}

	const base = "github.com/sukasukasuka123/Seele/"
	tools := []struct {
		pkg  string
		port string
	}{
		{base + "example_tools/example", "localhost:50101"},
		{base + "example_tools/suka_secret", "localhost:50100"},
		{base + "example_tools/ping", "localhost:50102"},
		{base + "example_tools/fetch", "localhost:50103"},
		{base + "example_tools/cmd", "localhost:50104"},
		{base + "example_tools/registry_changer", "localhost:50105"},
		{base + "example_tools/tool_coder", "localhost:50106"},
	}
	for _, svc := range tools {
		stop := startTool(t, svc.pkg)
		defer stop()
	}
	for _, svc := range tools {
		waitPort(t, svc.port, 15*time.Second)
	}

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
	f, err := agentfactory.NewFactory(llmCfg, hub, 5*time.Second)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Logf("已加载 %d 个 skill", len(f.Skills()))

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
		label := fmt.Sprintf("%s/%s", tc.label, tc.input[:min(10, len(tc.input))])
		t.Run(label, func(t *testing.T) {
			reply, err := tc.agent.Chat(ctx, tc.input)
			if err != nil {
				t.Errorf("Chat error: %v", err)
				return
			}
			if reply == "" {
				t.Error("got empty reply")
			}
			t.Logf("reply: %s", reply)
		})
	}
}

// TestDemo_Interactive 交互式 REPL（手动测试）
// TEST_INTERACTIVE=1 go test -v -run TestDemo_Interactive -timeout 600s
func TestDemo_Interactive(t *testing.T) {
	if os.Getenv("TEST_INTERACTIVE") == "" {
		t.Skip("跳过交互测试（设置 TEST_INTERACTIVE=1 启用）")
	}

	const base = "github.com/sukasukasuka123/Seele/"
	for _, svc := range []struct{ pkg, port string }{
		{base + "example_tools/example", "localhost:50101"},
		{base + "example_tools/suka_secret", "localhost:50100"},
		{base + "example_tools/ping", "localhost:50102"},
		{base + "example_tools/fetch", "localhost:50103"},
		{base + "example_tools/cmd", "localhost:50104"},
	} {
		stop := startTool(t, svc.pkg)
		defer stop()
		waitPort(t, svc.port, 10*time.Second)
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
	f, err := agentfactory.NewFactory(llmCfg, hub, 5*time.Second)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	a := f.New("你是 suka-eva，一个通过微服务架构动态扩展 skill 的 AI 助手。")
	ctx := context.Background()
	fmt.Printf("=== suka-eva 交互测试 ===\n已加载 %d 个 skill\n命令：quit | reset | skills\n", len(f.Skills()))

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
			a.ClearHistory()
			fmt.Println("[已重置]")
		case "skills":
			for _, s := range f.Skills() {
				fmt.Printf("  %-20s %s\n", s.Name, s.Method)
			}
		default:
			reply, err := a.Chat(ctx, input)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}
			fmt.Printf("eva: %s\n\n", reply)
		}
	}
}

// ── Hub 路由（集成测试专用，新版 Method 路由）──────────────────

type demoRouter struct{}

func (r *demoRouter) ServiceName() string { return "test-hub" }

func (r *demoRouter) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	t, ok := registry.SelectToolByMethod(req.Method)
	if !ok {
		return nil, fmt.Errorf("no tool for method=%q", req.Method)
	}
	return []hubbase.DispatchTarget{{Addr: t.Addr, Request: req, Stream: true}}, nil
}

func (r *demoRouter) OnResults(results []hubbase.DispatchResult) {
	for _, res := range results {
		if !res.AllOK() {
			fmt.Printf("[Hub] dispatch error: %v\n", res.Err)
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
