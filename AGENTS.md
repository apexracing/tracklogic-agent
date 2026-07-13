# AGENTS.md

本仓库是 Go-Harness-Tutorial 教学项目，完全基于《智能体 Harness 工程指南》的设计原则实现。

## 项目结构

- docs/ — 教程文档（13 章），每章含理论 + 手写代码实现 + 练习
- src/ — 完整 Go 参考实现（Go 1.26 模块 go-harness-tutorial）
- src/internal/ — 各子系统实现包
- src/examples/jd_cs/ — JD 智能客服业务示例（Agent、工作流、工具）
- src/examples/cmd/jd-cs-service/ — Demo 入口；在 src/ 目录下 go run ./examples/cmd/jd-cs-service
- src/examples/config.example.json — Demo 硬编码加载的配置示例（vendor + api_format）

## 模型配置要点

- ModelConfig 使用 vendor（供应商标识/日志）与 api_format（选择 HTTP 协议实现），二者独立；BuildModel() 仅按 api_format 分支。
- 支持的 api_format：openai_response、openai_chat_completions、anthropic_message、mock。
- Demo 不支持 -config 或 MODEL_* 环境变量；配置路径写死在入口常量 examples/config.example.json。

## 关键包依赖关系

```
pkg/types  ← 基础类型（零外部依赖）
  ↑
internal/model    internal/memory    internal/tool
  ↑                    ↑                ↑
  └──────────┬─────────┴────────┬───────┘
             │                 │
     internal/engine           │
             ↑                 │
     internal/orchestrator     │
             ↑                 │
     internal/mcpclient        │
             ↑                 │
     internal/security          │
             ↑                 │
     internal/harness ─────────┘
             ↑
     examples/jd_cs
             ↑
     examples/cmd/jd-cs-service
```

（路径均相对于 src/ 模块根。）

## 编码约定

- 使用 slog 标准日志库
- 所有子系统通过接口解耦（Tool、Model、Memory、Node）
- Config 结构体 + New() 构造函数模式
- 错误使用 *types.HarnessError 带可识别错误码
- 并发安全：读取用 RLock，写入用 Lock
- 禁止循环依赖；上层可依赖下层，下层不可依赖上层
