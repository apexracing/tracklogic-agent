# tracklogic-agent

`tracklogic-agent` 是一个用于构建模型驱动 Agent、工具调用、记忆、Team 和 Workflow 的纯 Go 公共库。根包 `agent` 提供 Harness 与配置入口；`engine`、`model`、`memory`、`tool`、`security`、`types`、`workflow` 和 `mcp` 是职责独立的公共扩展包。

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

    assistant := harness.NewAgent("assistant", "你是一个简洁、可靠的助手。")
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

## 公共扩展包

- `engine`：Agent、执行循环、流式输出和运行选项。
- `model`：Model 接口及 OpenAI、Chat Completions、Anthropic、Mock 实现。
- `memory`：Memory 接口和并发安全的 BufferMemory。
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

教程入口为 [docs/01-introduction.md](docs/01-introduction.md)。

```bash
go test ./...
go test -race ./...
go vet ./...
```

## 许可

项目使用 MIT License，详见 [LICENSE](LICENSE)。
