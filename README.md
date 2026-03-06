基于对项目代码的深入分析，我发现实际功能比原 README 描述的更丰富。以下是更新后的完整 README：

---

# Seele

名字取自新世纪福音战士（EVANGELION）中的组织 **SEELE**，象征幕后操控的"人类补完委员会"。本项目是对 [microHub](https://github.com/sukasukasuka123/microHub) 的封装，构建了一个**生产就绪**的 Go 语言 Agent 框架。

**核心理念**：每个工具都是独立的 gRPC 微服务，运行在隔离进程中——可独立部署、热替换、崩溃不影响主进程。

```
用户输入
    │
    ▼
 Agent (Seele)          ← 管理对话历史、LLM 循环、工具调度
    │
    ├─ LLM（兼容 OpenAI 接口）
    │
    └─ microHub          ← gRPC 路由 + 连接池
         ├─ ping         :50102  (网络诊断)
         ├─ fetch        :50103  (网页/API 抓取)
         ├─ codegen      :50104  (Skill 代码生成)
         ├─ registry     :50105  (动态注册表管理)
         └─ ...          (任意扩展)
```

---

## 🚀 核心特性

### 1. 微服务架构
- 每个 skill 是独立 gRPC 服务器，进程隔离
- 支持任意语言编写 skill（Go/Python/Node.js...）
- skill 崩溃不影响 agent 主进程
- 热替换：修改 `registry.yaml` 几秒内生效

### 2. 动态 Skill 管理
- **registry skill**：运行时增删改 skill 条目，支持等待端口上线
- **codegen skill**：根据描述自动生成 Go skill 样板代码
- **retire/restore**：临时屏蔽/恢复 skill（重启后自动恢复）

### 3. 多 Agent 并发
- 单个 Factory 可创建多个独立 Agent
- 每个 Agent 拥有私有对话历史
- 共享同一套 skill 池
- Agent 非并发安全（每 goroutine 一个实例）

### 4. 智能工具调用
- 自动并行执行同一批次的多个 tool_call
- 对话历史自动截断（保留 system + 最近 20 条）
- 最大循环次数可配置（默认 8 轮）
- Schema 自动转换（microHub 格式 ↔ OpenAI 格式）

### 5. 企业级配置
- `registry.yaml`：skill 列表 + hub 地址 + 连接池参数
- `config.yaml`：LLM 连接配置
- 支持环境变量覆盖
- 连接池可配置 min_size/max_size

---

## 📦 内置 Skill

| Skill | 端口 | 功能 |
|-------|------|------|
| `ping` | 50102 | 测试网络连通性（跨平台） |
| `fetch` | 50103 | 抓取网页/API/文件内容 |
| `codegen` | 50104 | 生成新 skill 的 Go 样板代码 |
| `registry` | 50105 | 动态管理 registry.yaml |
| `suka_secret` | 50100 | 占位 skill（无功能） |
| `echo` | 50101 | 原样返回输入（调试用） |

---

## 🛠️ 快速开始

### 1. 安装依赖

```bash
go get github.com/sukasukasuka123/Seele
go get github.com/sukasukasuka123/microHub
```

需要 Go 1.21+。

### 2. 配置文件

**config/registry.yaml** - Skill 注册表：

```yaml
services:
  tools:
    - name: "ping"
      addr: "localhost:50102"
      method: "测试目标地址的网络连通性"
      input_schema: |
        {
          "type": "object",
          "data": {
            "host": { "type": "string" },
            "count": { "type": "integer", "default": 4, "min": 1, "max": 10 },
            "timeout": { "type": "integer", "default": 3000 }
          },
          "required": ["host"]
        }
      output_schema: |
        {
          "type": "object",
          "data": {
            "host": { "type": "string" },
            "reachable": { "type": "boolean" },
            "latency_ms": { "type": "number" },
            "packet_loss": { "type": "number" },
            "error": { "type": "string" }
          }
        }

    - name: "fetch"
      addr: "localhost:50103"
      method: "抓取任意 URL 的内容（网页/API/文件）"
      input_schema: |
        {
          "type": "object",
          "data": {
            "url": { "type": "string" },
            "method": { "type": "string", "default": "GET", "enum": ["GET", "POST", "HEAD"] },
            "headers": { "type": "object" },
            "body": { "type": "string", "default": "" },
            "limit": { "type": "integer", "default": 8000, "min": 100, "max": 50000 }
          },
          "required": ["url"]
        }

  hubs:
    - name: "suka-eva-hub"
      addr: "localhost:50051"

pool:
  grpc_conn:
    min_size: 1
    max_size: 5
```

**config/config.yaml** - LLM 配置：

```yaml
agent:
  ai_url: "https://api.openai.com/v1"
  ai_name: "gpt-4o"
  ai_api_key: "sk-..."
```

### 3. 启动 Skill 进程

```bash
# 终端 1
go run ./example_tools/ping

# 终端 2
go run ./example_tools/fetch

# 终端 3
go run ./example_tools/codegen

# 终端 4
go run ./example_tools/registry_changer
```

### 4. 运行 Agent

```bash
go run ./cmd
```

```
╔══════════════════════════════════════╗
║         suka-eva  agentfactory       ║
╚══════════════════════════════════════╝
已加载 5 个 skill（来源：registry.yaml）
命令: skills | new | list | switch <n> | reset | retire <name> | restore <name> | quit

👤 [1/1 …23957500] ping 一下 baidu.com
🤖 eva: baidu.com 可达，延迟约 35ms。
   (128ms | 4 msgs)
```

---

## 💻 编程接口

### 创建 Agent

```go
package main

import (
    "context"
    agentfactory "github.com/sukasukasuka123/Seele"
    hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
    registry "github.com/sukasukasuka123/microHub/service_registry"
)

func main() {
    // 1. 初始化注册表
    registry.Init("config/registry.yaml")

    // 2. 启动 Hub
    hub := hubbase.New(&MyHubHandler{})
    go hub.ServeAsync(":50051", 0)

    // 3. 创建 Factory
    llmCfg, _ := agentfactory.LoadConfig("config/config.yaml")
    factory, _ := agentfactory.NewFactory(llmCfg, hub)

    // 4. 创建多个独立 Agent
    eva := factory.New("你是 eva，一个通用助手。")
    debugger := factory.New("你是网络调试助手，优先使用 ping 和 fetch。")

    // 5. 对话
    ctx := context.Background()
    reply, _ := eva.Chat(ctx, "介绍一下自己")
    diagnosis, _ := debugger.Chat(ctx, "诊断 github.com 的网络状况")
}
```

### 运行时管理 Skill

```go
// 临时屏蔽某个 skill（重启后自动恢复）
factory.Retire("fetch")

// 恢复被屏蔽的 skill
factory.Restore("fetch")

// 查看当前可见的 skill 列表
for _, s := range factory.Skills() {
    fmt.Printf("%s @ %s - %s\n", s.Name, s.Addr, s.Description)
}
```

### 动态注册新 Skill

```go
// 通过 registry skill 动态添加
// 假设已启动 registry skill (:50105)
ctx := context.Background()
args := `{
    "action": "add",
    "tool": {
        "name": "weather",
        "addr": "localhost:50106",
        "method": "查询天气信息",
        "input_schema": "{...}",
        "output_schema": "{...}"
    },
    "wait_online": true,
    "wait_timeout": 30
}`
result, _ := factory.dispatch(ctx, "registry", args)
```

### 自动生成 Skill 代码

```go
// 通过 codegen skill 生成新 skill 样板
args := `{
    "name": "weather",
    "port": 50106,
    "description": "查询天气信息",
    "input_fields": [
        {"name": "city", "type": "string", "required": true, "comment": "城市名称"}
    ],
    "output_fields": [
        {"name": "temperature", "type": "float64", "comment": "温度"},
        {"name": "condition", "type": "string", "comment": "天气状况"}
    ],
    "logic_hints": ["调用天气 API", "解析响应", "格式化输出"]
}`
result, _ := factory.dispatch(ctx, "codegen", args)
// 生成 TOOLS_DIR/weather/main.go
```

---

## 🔧 编写自定义 Skill

### 方式一：手动编写

```go
package main

import (
    "encoding/json"
    pb   "github.com/sukasukasuka123/microHub/proto/gen/proto"
    tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

type WeatherRequest struct {
    City string `json:"city"`
}

type WeatherResponse struct {
    Temperature float64 `json:"temperature"`
    Condition   string  `json:"condition"`
    Error       string  `json:"error,omitempty"`
}

type WeatherHandler struct{}

func (h *WeatherHandler) ServiceName() string { return "weather" }

func (h *WeatherHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
    var p WeatherRequest
    json.Unmarshal(req.Params, &p)

    // TODO: 实现天气查询逻辑
    result := WeatherResponse{Temperature: 25.5, Condition: "晴"}

    resp, _ := tool.NewOKResp(h.ServiceName(), result)
    return []*pb.ToolResponse{resp}, nil
}

func main() {
    tool.New(&WeatherHandler{}).Serve(":50106")
}
```

然后在 `registry.yaml` 中注册：

```yaml
- name: "weather"
  addr: "localhost:50106"
  method: "查询天气信息"
  input_schema: |
    {
      "type": "object",
      "data": {
        "city": { "type": "string" }
      },
      "required": ["city"]
    }
```

### 方式二：使用 codegen skill 自动生成

```bash
# 启动 codegen skill
go run ./example_tools/codegen

# 在 agent 中调用
👤 帮我生成一个叫 weather 的 skill，端口 50106，输入是 city 字符串，输出是 temperature 和 condition
🤖 已生成 TOOLS_DIR/weather/main.go，请执行 go build 并启动进程
```

---

## 📊 Schema 格式

Seele 使用 microHub 自定义的 JSON Schema 格式，自动转换为 OpenAI function calling 参数：

```json
{
  "type": "object",
  "data": {
    "host":    { "type": "string" },
    "count":   { "type": "integer", "default": 4, "min": 1, "max": 10 },
    "verbose": { "type": "boolean" },
    "tags":    { "type": "array", "items": { "type": "string" } },
    "metadata": {
      "type": "object",
      "data": {
        "source": { "type": "string" },
        "priority": { "type": "integer" }
      }
    }
  },
  "required": ["host"]
}
```

**转换规则**：
- `data` → OpenAI 的 `properties`
- `min/max` → `minimum/maximum`
- `enum` 直接保留
- `default` 直接保留
- 嵌套 object/array 递归转换

---

## 🧪 测试

```bash
# 单元测试
cd test && go test -v -timeout 30s

# 基准测试
cd test && go test -benchmem -bench .

# 集成测试（需要运行中的 skill 进程）
cd test && TEST_INTEGRATION=1 go test -v -run TestDemo -timeout 120s

# 交互式 REPL 测试
cd test && TEST_INTERACTIVE=1 go test -v -run TestDemo_Interactive -timeout 600s
```

**基准测试参考值**（i5-1155G7）：

```
BenchmarkAgent_Chat_SingleTurn-8    9810    108820 ns/op    13100 B/op    153 allocs/op
```

单次 Chat 循环耗时 **108µs**（mock LLM，本地 httptest）。实际瓶颈永远是 LLM 网络延迟（秒级）或工具进程延迟（毫秒级）。     

---

## 🏗️ 项目结构

```
Seele/
├── agentFactory.go      # Factory、skill 路由、schema 转换、dispatch
├── agent.go             # Agent、Chat 循环、历史管理、截断
├── llm.go               # LLM 客户端（兼容 OpenAI 接口）
├── model.go             # Message、Tool、ToolCall 类型定义
├── config.go            # LoadConfig 加载 YAML 配置
├── util/                # 工具函数（RunCmd 等）
│
├── cmd/                 # 交互式 REPL demo
│   └── main.go          # 多 Agent 管理、REPL 命令系统
│
├── example_tools/       # 内置 skill 实现
│   ├── ping/            # 网络连通性测试
│   ├── fetch/           # 网页/API 抓取
│   ├── codegen/         # Skill 代码生成器
│   ├── registry_changer/# 动态注册表管理
│   ├── suka_secret/     # 占位 skill
│   └── example/         # echo skill（调试用）
│
├── config_example/
│   ├── registry.yaml    # Skill + hub + 连接池配置模板
│   └── config.yaml      # LLM 配置模板
│
└── test/
    ├── integration_test.go
    └── benchmark_test.go
```

---

## 🎯 REPL 命令

| 命令 | 功能 |
|------|------|
| `skills` | 查看当前可用 skill 列表 |
| `new` | 创建新 Agent |
| `list` | 列出所有 Agent |
| `switch <n>` | 切换到第 n 个 Agent |
| `reset` | 清空当前 Agent 历史（保留 system） |
| `retire <name>` | 临时屏蔽 skill |
| `restore <name>` | 恢复被屏蔽的 skill |
| `quit` | 退出并显示会话摘要 |

---

## 📝 设计说明

### 为什么用 gRPC 进程而不是进程内函数？

| 优势 | 说明 |
|------|------|
| **隔离性** | skill 崩溃不影响 agent 主进程 |
| **语言无关** | skill 可以用任意语言编写 |
| **独立部署** | skill 可部署在不同机器 |
| **热替换** | 修改 registry.yaml 地址即可切换实现 |
| **扩缩容** | 可针对高负载 skill 独立扩容 |

### 为什么 registry.yaml 是唯一数据源？

- 一个地方增删改 skill，避免配置漂移
- LLM 看到的 schema 和工具实际接受的参数来自同一份定义
- 热更新意味着运行中的 agent 几秒内反映变更
- 支持 `registry` skill 动态修改（无需手动编辑文件）

### Seele 目前不做的事（有意留白）

- Memory / 跨重启的会话持久化 → 可通过 `memory` skill 扩展
- RAG / 向量检索 → 可通过 `rag` skill 扩展
- 链式工具调用（A 调用 B）→ 可在 skill 内部实现
- 流式响应 → 当前为同步阻塞模式

**架构支持无限扩展**——任何功能都可以是一个 gRPC 服务。

---

## 🔗 依赖

- [microHub](https://github.com/sukasukasuka123/microHub) — gRPC hub、连接池、注册表、schema
- [spf13/viper](https://github.com/spf13/viper) — YAML 配置加载
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) — YAML 解析（registry_changer 使用）
- 任意兼容 OpenAI 接口的 LLM（OpenAI、Qwen、DeepSeek、本地 Ollama 等）

---

## 📄 License

MIT

---

## 🌟 示例场景

### 场景 1：网络诊断助手

```
👤 帮我诊断一下 github.com 的网络状况
🤖 正在执行 ping 测试...
   github.com 可达，延迟 142ms，无丢包。
   需要我进一步测试其他地址吗？
```

### 场景 2：网页内容分析

```
👤 抓取 https://example.com 的内容并总结
🤖 已获取页面内容（8000 字符）。
   这是一个示例域名，用于文档和测试。
   页面包含标题、简介和链接列表...
```

### 场景 3：动态扩展能力

```
👤 我需要一个查询天气的 skill
🤖 正在生成 weather skill 代码...
   已创建 TOOLS_DIR/weather/main.go
   请执行：go build && ./weather
   完成后我可以帮你注册到系统
```

---