# tracklogic-agent

`tracklogic-agent` 是一个用于构建模型驱动 Agent、工具调用、记忆、Team 和 Workflow 的纯 Go 公共库。根包 `agent` 提供 Harness 与配置入口；`engine`、`model`、`memory`、`tool`、`security`、`types`、`workflow` 和 `mcp` 是职责独立的公共扩展包。

本库只负责通用 Harness。业务数据接入、业务逻辑、展示与持久化属于依赖本库的上层应用，通过自定义 Tool、MCP、Model 或 Workflow 组合实现，不在核心包中定义领域类型。

项目同时包含《智能体 Harness 工程指南》的 13 章教程和一个可运行的 JD 智能客服示例。

## 环境与安装

- Go 1.26 或更高版本
- 使用真实模型时需要供应商 API 地址、模型 ID 和 API Key
- 无 API Key 时可以使用内置 Mock 模型

```bash
go get github.com/apexracing/tracklogic-agent
```

## 快速开始

```go
package main

import (
    "context"
    "fmt"
    "log"

    agent "github.com/apexracing/tracklogic-agent"
    "github.com/apexracing/tracklogic-agent/engine"
)

func main() {
    cfg := agent.DefaultConfig()
    cfg.DefaultModel.APIFormat = "mock"
    cfg.DefaultModel.ModelID = "local-demo"

    harness, err := agent.New(cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer harness.Close()

    assistant, err := harness.CreateAgent("assistant", "你是一个简洁、可靠的助手。")
    if err != nil {
        log.Fatal(err)
    }
    result := assistant.Run(context.Background(), "你好",
        engine.WithMaxLoops(5),
        engine.WithTemperature(0.2),
    )
    if !result.Success {
        log.Fatal(result.Error)
    }
    fmt.Println(result.Content)
}
```

需要后台 Turn、实时事件和安全恢复时，使用 Task API；TaskID 和事件处理由调用方提供：

```go
runtimeTask, err := harness.NewTask(task.Options{
    TaskID:    taskID,
    EventSink: sink,
})
if err != nil { return err }
defer runtimeTask.Close()

turn, err := runtimeTask.StartAgent(ctx, "assistant", input)
if err != nil { return err }
result, err := runtimeTask.WaitTurn(ctx, turn.ID)
```

Task 模式默认提供模型 5 次尝试、按 Model 实例断路器、Summary Memory、结构化询问和检查点事件。库不提供数据库、JSONL、HTTP/SSE/WebSocket 或默认存储目录；这些属于上层应用。原有 `RunAgent`、`RunTeam`、`RunWorkflow` 和 `Agent.Run` 同步 API 行为保持不变。

## 公共扩展包

- `engine`：Agent、执行循环、流式输出和运行选项。
- `model`：Model 接口及 OpenAI、Chat Completions、Anthropic、Mock 实现。
- `memory`：Memory 接口、BufferMemory 和 Task 使用的 SummaryMemory。
- `task`、`interaction`：Task/Turn/Item 事件协议、检查点和结构化用户询问。
- `tool`、`tool/builtin`：Tool、Registry 和内置工具。
- `security`：可替换的权限、输入输出校验和脱敏接口。
- `types`：Message、ToolCall、Usage、RunContext 和结构化错误等跨层协议。
- `workflow`：Team、Workflow 和各种 Node。
- `mcp`：可直接使用的公共 MCP 客户端。

创建完全自定义的 Agent：

```go
registry := tool.NewRegistry()
if err := registry.Register(myTool); err != nil {
    return err
}

runtimeAgent := engine.NewAgent(engine.AgentConfig{
    Name:         "custom-agent",
    Model:        myModel,
    Memory:       myMemory,
    ToolRegistry: registry,
})
```

根包不重复导出这些类型；例如 Team 和 Workflow 使用 `workflow.NewTeam`、`workflow.NewWorkflow` 创建，再通过 Harness 注册和运行。

## 模型配置

`vendor` 只用于供应商标识和日志；`api_format` 独立决定 HTTP 协议实现。支持：

- `openai_response`
- `openai_chat_completions`
- `anthropic_message`
- `mock`

`base_url`、`api_key` 和 `model_id` 均由调用方显式配置，不会根据 `vendor` 推断。

`agent.New` 会自动归一化并校验配置。需要统一应用日志时，使用 `agent.WithLogger` 注入运行时依赖；库不会替换进程的 `slog.Default`。

生产代码建议使用返回 error 的 `CreateAgent`、`CreateTeam` 和 `CreateWorkflow`，避免非法名称或同名注册被忽略。多个 Agent 共用 Registry 时，使用 `CreateAgentWithTools` 声明每个 Agent 的最小工具集合；白名单同时限制模型可见定义和实际执行。同一个有状态 Agent 的 Run 会串行执行，等待期间可被 Context 取消；不同会话应使用不同 Agent/Memory，才能并行且隔离上下文。

Model Provider 对普通响应设置 16 MiB 上限，对错误响应设置 8 KiB 上限；429、超时和取消会保留可识别错误码。`RunOutput`、`TeamOutput` 和 `WorkflowResult` 的 `Err` 可用于 `errors.As`，字符串 `Error` 用于兼容序列化。需要企业代理、mTLS、自定义 Header 或应用层重试时，可预构造 Model 并通过 `agent.WithModel` 注入。

MCP Client 支持 Streamable HTTP、协议协商、Session、JSON/SSE 响应和完整 JSON Schema 保留。含认证 Header、mTLS 或代理的 Client 应通过 `mcp.NewClient(...)` 预构造，再用 `agent.WithMCPClient` 注入；远端工具仍需显式调用 `InitMCPClients` 才会注册。

内置文件工具使用 Go 的受限目录根 API，拒绝 `..` 和越界符号链接，并限制单文件为 1 MiB；`http_get` 默认拒绝私网、回环和链路本地地址，限制重定向、超时和正文大小。需要访问内网服务时应显式构造带策略的 Tool，而不是放宽整个 Harness。

## JD 智能客服示例

示例固定读取仓库根目录下的 `examples/config.example.json`。配置不保存 API Key；Key 为空时自动回退到 Mock 模型。

```powershell
$env:TRACKLOGIC_AGENT_API_KEY="<your-api-key>"
go run ./cmd/jd-cs-service
```

无真实 Key 时：

```bash
go run ./cmd/jd-cs-service
```

## 目录结构

```text
agent.go, harness.go, config.go   package agent：Harness 与配置 façade
engine/                           Agent 运行循环与流式处理
model/                            模型接口及协议实现
memory/                           记忆接口与 BufferMemory
tool/, tool/builtin/              工具接口、注册表与内置工具
security/                         权限、校验与脱敏扩展
types/                            跨层稳定协议类型
workflow/                         Team、Workflow 与 Node
mcp/                              公共 MCP 客户端
examples/jdcs/                    JD 智能客服业务示例
cmd/jd-cs-service/                Demo 入口
docs/                             13 章 Harness 教程
```

## 教程与测试

建议先阅读 [学习路线与设计决策总览](docs/00-learning-guide.md)，建立“概率模型 + 确定性 Harness”的整体心智模型，再从 [第 1 章](docs/01-introduction.md) 开始按章学习。

```bash
go test ./...
go test -race ./...
go vet ./...
```

## 许可

项目使用 MIT License，详见 [LICENSE](LICENSE)。
