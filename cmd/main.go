// cmd/main.go
//
// suka-eva 交互式入口
//
// 启动流程：
//  1. api.New() — 加载 registry、启动 Hub、创建 Factory（一行完成）
//  2. cli.New() — 创建 REPL，注入 system prompt 和可选钩子
//  3. repl.Run() — 进入交互循环
//
// 所有样板代码（Hub 路由、registry 初始化、Factory 构建、命令解析）
// 均已下沉到 sdk/api 和 sdk/cli，main 只负责配置拼接。

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/sukasukasuka123/Seele/sdk/api"
	"github.com/sukasukasuka123/Seele/sdk/cli"
)

const (
	registryPath  = "config_example/registry.yaml"
	llmConfigPath = "config_example/config.yaml"
	hubAddr       = ":50051"

	systemPrompt = "你是 suka-eva，一个通过微服务架构动态扩展 skill 的 AI 助手。" +
		"你的每个 skill 都运行在独立的 gRPC 微服务进程里，通过 microHub 调度执行。" +
		"请回答用户的问题，需要时主动调用合适的 skill。"
)

func main() {
	// ── 1. 初始化引擎（registry + Hub + Factory 一步到位）────────────────
	engine, err := api.New(api.Options{
		RegistryPath:  registryPath,
		LLMConfigPath: llmConfigPath,
		HubAddr:       hubAddr,
	})
	if err != nil {
		log.Fatalf("启动失败: %v\n提示：确认 registry.yaml 和 config.yaml 路径正确，且 api_key 已填写", err)
	}
	defer engine.Shutdown()

	// ── 2. 创建 REPL ─────────────────────────────────────────────────────
	repl := cli.New(engine, cli.Options{
		SystemPrompt: systemPrompt,
		BotName:      "eva",
	})

	// ── 3. 监听退出信号，优雅关闭 ────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── 4. 启动交互循环（阻塞直到 quit 或 Ctrl-C）────────────────────────
	repl.Run(ctx)
}
