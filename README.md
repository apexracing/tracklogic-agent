# Go-Harness-Tutorial

从零构建一个 Go 语言 Agent Harness 框架的完整教程。

## 概述

**智能体 = 大模型 + Harness**

大模型提供推理与「思考」，Harness 提供执行、编排与安全边界的「骨架」。本教程手把手实现后者——一个可运行的、纯 Go 语言的 Agent Harness 系统。

本教程完全基于《智能体 Harness 工程指南》（yeasy.gitbook.io/harness_engineering_guide）中的工程原则，不依赖任何第三方 Agent 框架，从零开始逐层构建。

## 前置知识

- Go 语言基础：接口、结构体、goroutine、context 包
- 了解大模型 API 基本概念（Chat Completion、Tool Call、Streaming）
- Go 1.26 环境

## 快速运行 Demo

在仓库中进入 Go 模块根目录并启动 JD 智能客服示例（无 -config 参数）：

```bash
cd src
go run ./examples/cmd/jd-cs-service
```

入口程序硬编码读取 **examples/config.example.json**（相对 src/）。若 api_key 为空且 api_format 不是 mock，会自动降级为 mock 以便本地无密钥演示。

## default_model 配置字段

| JSON 字段 | 说明 |
|-----------|------|
| vendor | 供应商标识（如 deepseek、openai、anthropic、custom），用于日志；**不**决定 HTTP 协议 |
| api_format | 协议实现：openai_response / openai_chat_completions / anthropic_message / mock |
| base_url | API 基址（如 DeepSeek https://api.deepseek.com/v1）；需显式配置，不会从 vendor 推断 |
| api_key | 密钥；Demo 中为空则回退 mock |
| model_id | 模型 ID |
| timeout_seconds | HTTP 超时（秒） |

示例（与仓库 src/examples/config.example.json 一致）：

```json
"default_model": {
  "vendor": "deepseek",
  "api_format": "openai_chat_completions",
  "base_url": "https://api.deepseek.com/v1",
  "api_key": "",
  "model_id": "deepseek-v4-flash",
  "timeout_seconds": 60
}
```

## Harness 工程化原则

贯穿教程的五条核心原则：

| 原则 | 核心思想 |
|------|---------|
| **约束优先** | 先设定 Agent 能做什么、不能做什么，再给它自由 |
| **可验证性** | 每个输出与工具响应应可验证、可审计 |
| **最小权限** | 从最小权限开始，逐步开放能力 |
| **故障隔离** | 默认一切都会失败，设计容错与降级 |
| **整体工程学** | 像工厂学一样系统化构建，而非堆砌「提示词技巧」 |

## 学习路径

| 部分 | 章节 | 主题 |
|------|------|------|
| Part 1: 基础篇 | 第 1-2 章 | 介绍 + 架构 |
| Part 2: 核心子系统 | 第 3-7 章 | 引擎 → 工具 → 记忆 → 模型 → 输出治理 |
| Part 3: 系统能力 | 第 8-11 章 | 编排 → MCP → 生产化 → 容错 |
| Part 4: 安全与实战 | 第 12-13 章 | 安全 → JD 智能客服 |

## 目录

| 章 | 标题 | 核心产出 |
|----|------|---------|
| 01 | [Harness 介绍 + 项目脚手架](docs/01-introduction.md) | 项目结构、依赖与约定 |
| 02 | [参考架构全景](docs/02-architecture.md) | 分层架构设计、Harness 结构体 |
| 03 | [运行时引擎](docs/03-runtime-engine.md) | Agent 结构体、Run 循环、ToolCall 执行 |
| 04 | [工具层](docs/04-tool-layer.md) | Tool 接口、Registry、Calculator/File 实现 |
| 05 | [记忆子系统](docs/05-memory.md) | BufferMemory、消息窗口管理 |
| 06 | [模型集成](docs/06-model-integration.md) | Model 接口、api_format 与三种 HTTP 协议 |
| 07 | [输出治理](docs/07-output-governance.md) | 格式校验、幻觉检测 |
| 08 | [编排能力](docs/08-orchestration.md) | Team（3 模式）+ Workflow（4 节点） |
| 09 | [MCP 协议](docs/09-mcp-integration.md) | JSON-RPC 客户端、工具发现 |
| 10 | [生产化](docs/10-production.md) | 配置管理、结构化日志、插件注册 |
| 11 | [容错与可靠性](docs/11-reliability.md) | 重试、断路器、可观测性 |
| 12 | [安全体系](docs/12-security.md) | 权限、注入检测、PII 脱敏 |
| 13 | [JD 智能客服](docs/13-jd-customer-service.md) | 端到端示例 |

## 代码与文档

src/ 目录包含完整 Go 参考实现；示例应用在 src/examples/jd_cs，入口在 src/examples/cmd/jd-cs-service。阅读教程时可对照 src/internal/ 下对应包。

## 参考资源

- [《智能体 Harness 工程指南》](https://yeasy.gitbook.io/harness_engineering_guide)
- [OpenAI API 文档](https://platform.openai.com/docs/api-reference)
- [MCP 协议规范](https://modelcontextprotocol.io)
