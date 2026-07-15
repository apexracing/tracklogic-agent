# tracklogic-agent

`tracklogic-agent` 是一个用于构建模型驱动 Agent、工具调用、记忆、Team 和 Workflow 的纯 Go Harness 库。根包 `agent` 提供常用入口；`model`、`memory`、`tool`、`engine`、`orchestrator`、`security` 和 `types` 包提供可替换的扩展接口。

项目同时保留《智能体 Harness 工程指南》的 13 章配套教程和一个可运行的 JD 智能客服示例。

## 环境

- Go 1.26 或更高版本
- 使用真实模型时需要相应供应商的 API 地址、模型 ID 和 API Key
- 无 API Key 时可以使用内置 Mock 模型完成本地测试

## 安装

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
        agent.WithMaxLoops(5),
        agent.WithTemperature(0.2),
    )
    if !result.Success {
        log.Fatal(result.Error)
    }
    fmt.Println(result.Content)
}
```

## 扩展接口

应用可以实现并注入自己的 `model.Model`、`memory.Memory` 和 `tool.Tool`。例如注册自定义工具：

```go
registry := tool.NewRegistry()
err := registry.Register(myTool)

runtimeAgent := agent.NewAgent(agent.AgentConfig{
    Name:         "custom-agent",
    Model:        myModel,
    Memory:       myMemory,
    ToolRegistry: registry,
})
```

根包同时导出 Team、Workflow、Node 和常用构造函数；复杂编排也可以直接使用 `orchestrator` 包。

## 模型配置

`vendor` 只用于供应商标识和日志；`api_format` 独立决定 HTTP 协议实现。支持：

- `openai_response`
- `openai_chat_completions`
- `anthropic_message`
- `mock`

`base_url`、`api_key` 和 `model_id` 均由调用方显式配置，不会根据 `vendor` 推断。

## JD 智能客服示例

示例固定读取仓库根目录下的 `examples/config.example.json`。默认示例不保存 API Key；Key 为空时自动回退到 Mock 模型。

真实模型测试时通过进程环境传入 Key，避免修改公开示例：

```powershell
$env:TRACKLOGIC_AGENT_API_KEY="<your-api-key>"
go run ./examples/cmd/jd-cs-service
```

```bash
go run ./examples/cmd/jd-cs-service
```

## 目录结构

```text
agent.go, harness.go, config.go   package agent 根公共 API
engine/                           Agent 运行循环与流式处理
model/                            模型接口及协议实现
memory/                           记忆接口与 BufferMemory
tool/, tool/builtin/              工具接口、注册表与内置工具
orchestrator/                     Team、Workflow 与 Node
security/, types/                 安全、权限和共享基础类型
internal/mcpclient/               内部 MCP 客户端实现
examples/                         JD 智能客服示例
docs/                             13 章 Harness 教程
```

## 教程

教程从架构、运行时、工具、记忆和模型集成开始，继续覆盖输出治理、编排、MCP、生产可靠性、安全以及完整业务示例。阅读入口为 [docs/01-introduction.md](docs/01-introduction.md)。

## 测试

```bash
go test ./...
go vet ./...
```

## 许可

项目使用 MIT License，详见 [LICENSE](LICENSE)。
