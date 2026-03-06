// cmd/demo/main.go
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	agentfactory "github.com/sukasukasuka123/Seele"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

const (
	// registryPath：唯一的服务配置源，管理 skill 列表 + hub 地址 + 连接池参数
	registryPath = "config_example/registry.yaml"
	// llmConfigPath：只管 LLM 连接参数（url / model / api_key）
	llmConfigPath = "config_example/config.yaml"
	hubAddr       = ":50051"
	systemPrompt  = "你是 suka-eva，一个通过微服务架构动态扩展 skill 的 AI 助手。" +
		"你的每个 skill 都运行在独立的 gRPC 微服务进程里，通过 microHub 调度执行。" +
		"请回答用户的问题，需要时主动调用合适的 skill。"
)

func main() {
	// ── 1. 初始化 registry（skill 列表 + hub + pool 全在 registry.yaml）──
	if err := registry.Init(registryPath); err != nil {
		log.Fatalf("registry init: %v", err)
	}

	// ── 2. 启动 Hub ────────────────────────────────────────────────────────
	hub := hubbase.New(&evaRouter{})
	go func() {
		if err := hub.ServeAsync(hubAddr, 0); err != nil {
			log.Fatalf("hub: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)

	// ── 3. 创建 Factory（skill 来自 registry，无需手动注册）───────────────
	llmCfg, err := agentfactory.LoadConfig(llmConfigPath)
	if err != nil {
		log.Fatalf("LoadConfig: %v\n提示：复制 config_example/config.yaml 到 config.yaml 并填写 api_key", err)
	}
	f, err := agentfactory.NewFactory(llmCfg, hub)
	if err != nil {
		log.Fatalf("NewFactory: %v", err)
	}

	// ── 4. REPL ────────────────────────────────────────────────────────────
	agents := []*agentfactory.Agent{f.New(systemPrompt)}
	current := 0

	printBanner(f)
	ctx := context.Background()
	scanner := bufio.NewScanner(os.Stdin)

	for {
		a := agents[current]
		fmt.Printf("👤 [%d/%d %s] ", current+1, len(agents), shortID(a.SessionID()))
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		switch {
		case input == "quit" || input == "exit":
			fmt.Println("\n👋 再见")
			for i, ag := range agents {
				fmt.Printf("  [%d] %s\n", i+1, ag.Summary())
			}
			return

		case input == "skills":
			printSkills(f)

		case input == "new":
			agents = append(agents, f.New(systemPrompt))
			current = len(agents) - 1
			fmt.Printf("✅ 新建 Agent [%d]  %s\n", current+1, agents[current].SessionID())

		case input == "list":
			for i, ag := range agents {
				marker := " "
				if i == current {
					marker = "▶"
				}
				fmt.Printf(" %s [%d] %s\n", marker, i+1, ag.Summary())
			}

		case strings.HasPrefix(input, "switch "):
			idx, err := strconv.Atoi(strings.TrimPrefix(input, "switch "))
			if err != nil || idx < 1 || idx > len(agents) {
				fmt.Printf("❌ 无效序号，当前共 %d 个 Agent\n", len(agents))
			} else {
				current = idx - 1
				fmt.Printf("✅ 切换到 [%d] %s\n", current+1, shortID(agents[current].SessionID()))
			}

		case input == "reset":
			a.Reset(true)
			fmt.Println("✅ 历史已清空（system prompt 保留）")

		case strings.HasPrefix(input, "retire "):
			name := strings.TrimPrefix(input, "retire ")
			f.Retire(name)
			fmt.Printf("✅ skill %q 已临时屏蔽（永久下线请修改 registry.yaml）\n", name)

		case strings.HasPrefix(input, "restore "):
			name := strings.TrimPrefix(input, "restore ")
			f.Restore(name)
			fmt.Printf("✅ skill %q 已恢复\n", name)

		default:
			start := time.Now()
			reply, err := a.Chat(ctx, input)
			elapsed := time.Since(start).Round(time.Millisecond)
			if err != nil {
				fmt.Printf("❌ %v\n\n", err)
				continue
			}
			fmt.Printf("🤖 eva: %s\n", reply)
			fmt.Printf("   (%s | %d msgs)\n\n", elapsed, len(a.History()))
		}
	}
}

// ── 辅助 ──────────────────────────────────────────────────────────

func printBanner(f *agentfactory.Factory) {
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║         suka-eva  agentfactory       ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Printf("已加载 %d 个 skill（来源：registry.yaml）\n", len(f.Skills()))
	fmt.Println("命令: skills | new | list | switch <n> | reset | retire <name> | restore <name> | quit")
	fmt.Println()
}

func printSkills(f *agentfactory.Factory) {
	ss := f.Skills()
	if len(ss) == 0 {
		fmt.Println("  （无可用 skill，检查 registry.yaml）")
		return
	}
	fmt.Printf("  %-20s %-8s %-40s %s\n", "NAME", "SCHEMA", "DESCRIPTION", "ADDR")
	fmt.Println("  " + strings.Repeat("─", 80))
	for _, s := range ss {
		// 从 registry 读 schema 有无，给用户直观提示
		in, out, _ := registry.GetToolSchema(s.Name)
		schemaTag := "  ✗  "
		if in != "" && out != "" {
			schemaTag = " in+out"
		} else if in != "" {
			schemaTag = "  in  "
		} else if out != "" {
			schemaTag = "  out "
		}
		fmt.Printf("  %-20s %-8s %-40s %s\n", s.Name, schemaTag, s.Description, s.Addr)
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return "…" + id[len(id)-8:]
}

// ── Hub 路由 ──────────────────────────────────────────────────────

type evaRouter struct{}

func (r *evaRouter) ServiceName() string { return "suka-eva-hub" }

func (r *evaRouter) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	t, ok := registry.SelectToolByName(req.ServiceName)
	if !ok {
		return nil, fmt.Errorf("skill %q not in registry", req.ServiceName)
	}
	req.From = r.ServiceName()
	return []hubbase.DispatchTarget{
		{Addr: t.Addr, Request: req, Stream: true},
	}, nil
}

func (r *evaRouter) OnResults(results []hubbase.DispatchResult) {
	for _, res := range results {
		if !res.AllOK() {
			log.Printf("[Hub] error addr=%s: %v", res.Target.Addr, res.Err)
		}
	}
}

func (r *evaRouter) Addrs() []string {
	tools := registry.GetAllTools()
	addrs := make([]string, len(tools))
	for i, t := range tools {
		addrs[i] = t.Addr
	}
	return addrs
}
