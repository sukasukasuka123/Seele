package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sukasukasuka123/Seele/sdk/api"
	"github.com/sukasukasuka123/Seele/sdk/cli"
)

const (
	registryPath  = "config/registry.yaml"
	llmConfigPath = "config/config.yaml"
	hubAddr       = ":50051"

	systemPrompt = "你是 Seele，一个通过微服务架构动态扩展 skill 的 AI 助手。，但是同时，你要珍惜用户的token，重试次数不要太多次了" +
		"你的每个 skill 都运行在独立的 gRPC 微服务进程里，通过 microHub 调度执行。" +
		"请回答用户的问题，需要时主动调用合适的 skill。"
)

func main() {
	// 1. 初始化引擎（registry + Hub + Factory 一步到位）
	engine, err := api.New(api.Options{
		RegistryPath:    registryPath,
		LLMConfigPath:   llmConfigPath,
		HubAddr:         hubAddr,
		ToolCallTimeOut: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("启动失败: %v\n提示：确认 registry.yaml 和 config.yaml 路径正确，且 api_key 已填写", err)
	}
	defer engine.Shutdown()

	// 2. 监听退出信号
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 3. 启动 REPL（阻塞直到 exit 或 Ctrl-C）
	cli.RunREPL(ctx, cli.REPLOptions{
		Engine:       engine,
		SystemPrompt: systemPrompt,
		Prompt:       "seele> ",
		Stream:       true,
	})
}
