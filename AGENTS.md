# AGENTS.md

本仓库是 `tracklogic-agent` 公共 Go Agent/Harness 库，同时包含《智能体 Harness 工程指南》的配套教程。

## 项目结构

- 根目录 — Go 1.26 模块 `github.com/apexracing/tracklogic-agent`，根包名 `agent`
- engine/ — Agent 运行循环与流式处理
- model/ — Model 接口及 OpenAI Responses、Chat Completions、Anthropic Messages、Mock 实现
- memory/ — Memory 接口与 BufferMemory
- tool/、tool/builtin/ — Tool 接口、Registry 与内置工具
- orchestrator/ — Team、Workflow 与 Node
- security/、types/ — 权限、安全校验和共享基础类型
- internal/mcpclient/ — 非公开 MCP 客户端实现
- examples/jd_cs/ — JD 智能客服业务示例
- examples/cmd/jd-cs-service/ — Demo 入口；在仓库根目录运行 `go run ./examples/cmd/jd-cs-service`
- examples/config.example.json — Demo 固定加载的配置示例
- docs/ — 13 章教程文档

## 模型配置要点

- ModelConfig 使用 `vendor`（供应商标识/日志）与 `api_format`（选择 HTTP 协议实现），二者独立；BuildModel() 仅按 `api_format` 分支。
- 支持的 `api_format`：`openai_response`、`openai_chat_completions`、`anthropic_message`、`mock`。
- Demo 不支持 `-config` 或通用 `MODEL_*` 环境变量；配置路径固定为 `examples/config.example.json`，仅允许用 `TRACKLOGIC_AGENT_API_KEY` 安全覆盖 Key。
- 示例配置禁止保存真实 API Key；无 Key 时 Demo 自动回退至 Mock 模型。

## 关键包依赖关系

```text
types ← model / memory
model ← tool
model + memory + tool ← engine
engine + model ← orchestrator
model ← internal/mcpclient
以上公开包 + internal/mcpclient ← 根包 agent
根包 agent + 公开扩展包 ← examples
```

## 编码约定

- 使用 slog 标准日志库。
- 所有子系统通过接口解耦（Tool、Model、Memory、Node）。
- 使用 Config 结构体 + New() 构造函数模式。
- 错误使用 `*types.HarnessError` 和可识别错误码。
- 并发安全：读取用 RLock，写入用 Lock。
- 禁止循环依赖；上层可依赖下层，下层不可依赖上层。
- 根包保持易用 façade；高级扩展放在公开子包，实现细节才放入 `internal/`。
