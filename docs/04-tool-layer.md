# 第 4 章 工具层

### 设计思路：工具是 Agent 与现实世界的桥梁

大模型只能生成文本。它不能查数据库、不能读文件、不能调用 API。工具层的存在就是让 Agent 拥有"手和脚"——通过统一接口与外部世界交互。

设计工具层时的核心权衡：

| 方案 | 优点 | 缺点 |
|------|------|------|
| 内嵌在 Agent 中 | 简单 | 不可复用、不可测试 |
| 独立 Tool 接口 | 可复用、可测试 | 需要 Registry 管理 |
| MCP 协议 | 跨语言通用 | 复杂度高 |

我们选择方案 2（独立 Tool 接口）作为核心，方案 3（MCP）作为扩展。这是**渐进信任原则**的体现——先保证核心工具可靠，再逐步接入外部能力。

工具层是 Agent 与外部世界交互的桥梁。没有工具，Agent 只是一个"会说话的聊天机器人"。

---

## 4.1 工具层的设计目标

工具层需要解决三个核心问题：

| 问题 | 方案 |
|------|------|
| **统一抽象** | 无论内部工具还是外部 API，都通过同一个 `Tool` 接口暴露 |
| **可发现性** | Agent 需要知道有哪些工具可用、每个工具的入参是什么 |
| **安全执行** | 参数校验、权限检查、错误处理 |

### 4.1.1 什么是工具？

在 Harness 体系中，**工具** 是 Agent 可以调用的外部能力单元。每个工具：
- 有唯一的名称（用于模型引用）
- 有明确的入参定义（JSON Schema 格式）
- 接受上下文和参数，返回结构化结果

### 4.1.2工具与 API 的关系

```
Agent ←→ Tool Interface ←→ 具体实现（计算器/文件API/HTTP客户端/MCP）
```

工具接口是适配器模式的核心——它将千差万别的外部系统统一为相同的调用方式。

---

## 4.2 Tool 接口设计

```go
type Tool interface {
	Name() string                                           // 工具名称（模型通过名称调用）
	Description() string                                    // 工具描述（模型理解工具用途）
	Definition() model.ToolDefinition                       // 模型可识别的定义（JSON Schema）
	Validate(args map[string]any) error                     // 参数校验
	Execute(ctx context.Context, args map[string]any) (any, error) // 执行
}
```

### 方法职责分析

**Name()**
- 必须是唯一的（Registry 以此索引）
- 推荐使用小写字母和下划线，如 `query_order`、`track_logistics`

**Description()**
- 模型通过描述理解工具的用途
- 描述应简洁但清晰："查询订单信息，包括订单状态、商品、金额等"

**Definition()**
- 返回 OpenAI Tool Definition 格式
- 包含参数 Schema，模型据此生成参数

**Validate()**
- 校验参数是否存在、类型是否正确
- 在校验通过后才执行，避免无效调用

**Execute()**
- 实际执行工具逻辑
- 返回 `any` 类型，因为不同工具的返回千差万别

---

## 4.3 BaseTool 基类

为了避免每个工具都重复实现 `Name()`、`Description()`、`Definition()` 等通用方法，我们提供一个 `BaseTool` 结构体。

```go
type BaseTool struct {
	name        string
	description string
	parameters  []model.ToolParameter
}

func NewBaseTool(name, description string, params []model.ToolParameter) BaseTool {
	return BaseTool{
		name:        name,
		description: description,
		parameters:  params,
	}
}

func (b BaseTool) Name() string        { return b.name }
func (b BaseTool) Description() string { return b.description }

// Definition 将内部参数格式转换为模型的 ToolDefinition 格式
func (b BaseTool) Definition() model.ToolDefinition {
	props := make(map[string]model.ToolParameter)
	required := make([]string, 0)
	for _, p := range b.parameters {
		props[p.Name] = p
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return model.ToolDefinition{
		Name:        b.name,
		Description: b.description,
		Parameters: model.ToolParameters{
			Type:       "object",
			Properties: props,
			Required:   required,
		},
	}
}

// Validate 检查必需参数是否存在
func (b BaseTool) Validate(args map[string]any) error {
	for _, p := range b.parameters {
		if p.Required {
			if _, ok := args[p.Name]; !ok {
				return &ValidationError{
					Field:   p.Name,
					Message: "required field missing",
				}
			}
		}
	}
	return nil
}
```

**设计要点**：

`Definition()` 输出的是 OpenAI 兼容的 JSON Schema 格式。这是关键设计决策——我们直接输出模型 API 所需的格式，避免中间转换。

```go
// Definition 的输出示例：
{
  "name": "calculator",
  "description": "执行算术计算",
  "parameters": {
    "type": "object",
    "properties": {
      "expression": {
        "type": "string",
        "description": "算术表达式"
      }
    },
    "required": ["expression"]
  }
}
```

### 工具注册与执行流水线

```mermaid
flowchart LR
    subgraph REG["注册阶段（启动时）"]
        R1["NewCalculator()"] --> REGISTRY["ToolRegistry"]
        R2["NewReadFile()"] --> REGISTRY
        R3["MCP Server"] --> REGISTRY
    end
    
    subgraph EXEC["执行阶段（运行时）"]
        E1["Agent.Run()"] --> E2["Model.Invoke()"]
        E2 --> E3{"ToolCall?"}
        E3 -- 是 --> E4["Registry.Get(name)"]
        E4 --> E5["tool.Validate(args)"]
        E5 --> E6["tool.Execute(ctx, args)"]
        E6 --> E7["结果写入 Memory"]
        E7 --> E2
        E3 -- 否 --> E8["返回 RunOutput"]
    end
    
    REGISTRY -.->|"提供工具列表"| E2
```

**设计要点**：
- 注册阶段是一次性的，执行阶段是循环的
- Registry 解耦了工具的创建和使用
- Validate 和 Execute 分离，确保不执行非法参数

---

## 4.4 ToolParameter 定义

```go
type ToolParameter struct {
	Name        string   `json:"-"`           // 参数名（不参与序列化，由 Properties map 的 key 承载）
	Type        string   `json:"type"`        // 参数类型：string/number/boolean
	Description string   `json:"description"` // 参数描述
	Required    bool     `json:"-"`           // 是否必需
	Enum        []string `json:"enum,omitempty"` // 枚举值
}
```

---

## 4.5 Registry：工具注册中心

Registry 是工具层的核心组件，管理所有可用的工具。

```go
type Registry struct {
	mu    sync.RWMutex    // 并发安全
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}
```

### 为什么使用 `sync.RWMutex`？

Registry 是**读多写少**的典型场景：
- Agent 每次循环都调用 `List()` 获取工具列表（读）
- 工具注册仅在启动时或插件加载时调用（写）

`RWMutex` 允许多个读操作同时进行，只在写操作时互斥，性能优于 `sync.Mutex`。

### 为什么 Register 返回 error 而非 panic？

工具名冲突通常是配置错误。在 Harness 初始化时注册工具，即使某个工具重复注册，也不应该让整个系统崩溃。返回 error 让上层处理。

---

## 4.6 实现内置工具

### 4.6.1 计算器工具

计算器是 Agent 最常用的工具之一。实现一个支持 +、-、\*、/、括号的表达式计算器：

```go
type CalculatorTool struct {
	BaseTool
}

func NewCalculator() *CalculatorTool {
	return &CalculatorTool{
		BaseTool: NewBaseTool(
			"calculator",
			"Perform arithmetic calculations. Supports +, -, *, / and parentheses.",
			[]model.ToolParameter{
				{
					Name:        "expression",
					Type:        "string",
					Description: "Arithmetic expression to evaluate (e.g. 25 * 4 + 15)",
					Required:    true,
				},
			},
		),
	}
}

func (c *CalculatorTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	expr, _ := args["expression"].(string)
	if expr == "" {
		return nil, fmt.Errorf("expression is required")
	}

	result, err := evaluate(expr)
	if err != nil {
		return nil, fmt.Errorf("calculation error: %w", err)
	}

	return map[string]any{
		"expression": expr,
		"result":     result,
	}, nil
}
```

**为什么不用第三方表达式库？**
- 减少依赖
- 递归下降解析器是实现简单表达式求值的最佳选择
- 完整的错误处理（除零、语法错误）

`evaluate()` 实现了一个递归下降解析器，支持完整的四则运算和括号：

```go
type parser struct {
	input string
	pos   int
}

func evaluate(expr string) (float64, error) {
	p := &parser{input: expr}
	return p.parseExpr()
}

// parseExpr 解析加减法表达式
func (p *parser) parseExpr() (float64, error) {
	result, err := p.parseTerm()
	// 循环处理 + 和 -
}

// parseTerm 解析乘除法表达式
func (p *parser) parseTerm() (float64, error) {
	result, err := p.parseFactor()
	// 循环处理 * 和 /
}

// parseFactor 处理括号和数字
func (p *parser) parseFactor() (float64, error) {
	// '(' expr ')'
	// 或 number
}
```

### 4.6.2 文件操作工具

文件操作是 Agent 访问本地文件系统的桥梁。必须考虑安全限制：

```go
type ReadFileTool struct {
	BaseTool
	allowedDir string  // 安全限制：只允许读此目录下的文件
}

func NewReadFile(allowedDir string) *ReadFileTool {
	return &ReadFileTool{
		BaseTool: NewBaseTool(...),
		allowedDir: allowedDir,
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	path, _ := args["path"].(string)

	// 安全校验：防止路径遍历攻击
	fullPath, err := t.safePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return string(data), nil
}
```

**路径遍历防护**：

```go
func (t *ReadFileTool) safePath(path string) (string, error) {
	fullPath := filepath.Join(t.allowedDir, path)
	absPath, _ := filepath.Abs(fullPath)
	absAllowed, _ := filepath.Abs(t.allowedDir)

	rel, err := filepath.Rel(absAllowed, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path traversal detected: %q is outside allowed directory", path)
	}
	return absPath, nil
}
```

**为什么需要 `safePath`？**

如果没有此防护，Agent 可能通过 `../../etc/passwd` 读取系统敏感文件。`safePath` 通过计算相对路径并检查是否以 `..` 开头来防止此攻击。

### 4.6.3 其他常用内置工具

除计算器与文件读写外，Harness 还提供以下内置工具（通过 `allowed_tools` 按需启用）：

| 工具名 | 作用 | 关键参数 | 权限 |
|--------|------|----------|------|
| `get_current_time` | 获取当前时间（可选 IANA 时区） | `timezone` | 无 |
| `list_dir` | 列出目录条目（同文件沙箱） | `path` | `read_file` |
| `http_get` | HTTP GET，默认 10s 超时，正文上限 64KiB | `url`, `timeout_seconds` | `network_access` |
| `json_parse` | 将 JSON 字符串解析为结构化值 | `text` | 无 |

`list_dir` 与 `read_file` / `write_file` 共用路径穿越防护。`http_get` 在非 2xx 时仍返回 `status` + 截断后的 `body`，由 Agent 自行判断是否重试。

### 工具类图

```mermaid
classDiagram
    class Tool {
        <<interface>>
        +Name() string
        +Description() string
        +Definition() ToolDefinition
        +Validate(args) error
        +Execute(ctx, args) (any, error)
    }
    
    class BaseTool {
        +name: string
        +description: string
        +parameters: []ToolParameter
        +Name() string
        +Description() string
        +Definition() ToolDefinition
        +Validate(args) error
    }
    
    class CalculatorTool {
        +Execute(ctx, args) (any, error)
    }
    
    class ReadFileTool {
        -allowedDir: string
        +Execute(ctx, args) (any, error)
        -safePath(path) (string, error)
    }
    
    class WriteFileTool {
        -allowedDir: string
        +Execute(ctx, args) (any, error)
        -safePath(path) (string, error)
    }
    
    class Registry {
        -tools: map~string,Tool~
        +Register(t Tool) error
        +Get(name) (Tool, bool)
        +List() []Tool
    }
    
    Tool <|.. BaseTool : implements
    BaseTool <|-- CalculatorTool
    BaseTool <|-- ReadFileTool
    BaseTool <|-- WriteFileTool
    Registry o-- Tool : manages
```

**继承关系**：CalculatorTool、ReadFileTool 继承 BaseTool 的公共方法（Name、Description、Definition、Validate），只需实现 Execute。

---

## 4.7 工具在 Agent 中的调用流程

```
Agent.Run()
  │
  ├─ buildToolDefinitions()
  │     ├─ Registry.List()
  │     └─ 每个 Tool.Definition() → []ModelToolDefinition
  │
  ├─ Model.Invoke(req)  ← 工具定义作为参数传入
  │
  ├─ 模型返回 ToolCall
  │     └─ executeToolCall()
  │           ├─ Registry.Get(name)
  │           ├─ json.Unmarshal(arguments)
  │           ├─ tool.Validate(args)
  │           └─ tool.Execute(ctx, args) → result
  │
  └─ 工具结果 → RoleTool 消息 → 记忆 → 下一轮循环
```

---

## 4.8 工具的错误处理

工具执行过程中可能出现多种错误：

| 错误类型 | 处理方式 |
|---------|---------|
| 参数缺失 | 在 Validate 阶段返回，不执行 |
| 参数类型错误 | 同上 |
| 外部系统不可用 | 执行时返回错误，Agent 决定是否重试 |
| 结果过大 | 可能需要截断（避免超过 Token 限制） |
| 权限不足 | 在安全层拦截，不执行 |

---

## 4.9 本章小结

- 设计并实现了 `Tool` 接口，统一了所有外部能力的调用方式
- 实现了 `BaseTool` 基类，减少重复代码
- 实现了线程安全的 `Registry` 工具注册中心
- 实现了计算器、文件操作、时间、列目录、HTTP GET、JSON 解析等内置工具
- 建立了路径遍历防护等安全机制

---

## 练习

1. 为 `http_get` 增加域名 allowlist，并把响应截断上限做成可配置
2. 为 Registry 添加 `ListByPermission(perm string) []Tool` 方法，方便权限控制
3. 实现工具结果截断机制：当工具返回超过 2000 字符时，自动截断并添加 `[truncated]` 标记
