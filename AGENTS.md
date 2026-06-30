# AGENTS.md

本仓库是 Go-Harness-Tutorial 教学项目，完全基于《智能体 Harness 工程指南》的设计原则实现。

## 项目结构

- `docs/` — 教程文档（13 章），每章含理论 + 手写代码实现 + 练习
- `src/` — 完整 Go 参考实现，Go 1.26 模块
- `src/internal/` — 各子系统实现包
- `src/cmd/jd-cs-service/` — JD 智能客服示例入口

## 关键包依赖关系

```
pkg/types       ← 基础类型（零外部依赖）
  ↑
internal/model    internal/memory    internal/tool
  ↑                    ↑                ↑
  └────────┬───────────┴─────┬──────────┘
           │                 │
   internal/engine           │
           ↑                 │
   internal/orchestrator     │
           ↑                 │
   internal/mcpclient        │
           ↑                 │
   internal/security         │
           ↑                 │
   internal/harness ─────────┘
           ↑
   internal/examples/jd_cs
           ↑
   cmd/jd-cs-service
```

## 编码约定

- 使用 slog 标准日志库
- 所有子系统通过接口解耦（`Tool`、`Model`、`Memory`、`Node`）
- Config 结构体 + New() 构造函数模式
- 错误使用 `*types.HarnessError` 带可识别错误码
- 并发安全：读取用 `RLock`，写入用 `Lock`
- 禁止循环依赖：上层可依赖下层，下层不可依赖上层
