# 第 8 章 编排引擎

### 设计思路：为什么需要编排？

单 Agent 的局限在于：
1. **上下文窗口限制**：复杂流程需要多轮对话，单 Agent 难以管理
2. **专业分工**：一个 Agent 不可能在所有领域都专业
3. **并行处理**：单 Agent 串行执行，无法利用并发优势
4. **容错**：单点故障导致整体失败

编排引擎的核心价值是**将复杂任务分解为可管理的子任务**，每个子任务由专门的 Agent 负责。

```mermaid
graph TD
    COMPLEX["复杂任务<br/>查订单+物流+推荐"] --> DECOMPOSE["分解为子任务"]
    DECOMPOSE --> T1["子任务 1: 意图识别"]
    DECOMPOSE --> T2["子任务 2: 订单查询"]
    DECOMPOSE --> T3["子任务 3: 物流跟踪"]
    DECOMPOSE --> T4["子任务 4: 综合回答"]
    
    T1 -->|"意图: 订单"| T2
    T2 -->|"快递单号"| T3
    T3 -->|"物流信息"| T4
```

单 Agent 的能力是有限的。编排引擎让多个 Agent 协同工作，完成单 Agent 无法胜任的复杂任务。

---

## 8.1 为什么需要编排？

### 8.1.1 单 Agent 的局限

| 场景 | 单 Agent 的问题 |
|------|----------------|
| 复杂多步骤流程 | 上下文窗口限制，单 Agent 难以管理长流程 |
| 专业分工 | 一个 Agent 不可能在所有领域都专业 |
| 并行处理 | 单 Agent 串行执行，无法并行 |
| 容错 | 单 Agent 故障导致整体失败 |

### 8.1.2 编排的两个层次

Harness 提供两种编排能力：**Team（团队协作）** 和 **Workflow（工作流 DAG）**。

```
Team: 固定模式的 Agent 协作
  Sequential: A → B → C（串行管道）
  Parallel: A || B || C（并发执行）
  LeaderFollower: 领导者规划 + 执行者执行 + 领导者综合

Workflow: 灵活的 DAG
  Step: 执行一个 Agent
  Condition: 条件分支
  Loop: 循环执行
  Parallel: 并行分支
```

---

## 8.2 Team 设计

### 8.2.1 Team 结构体

```go
type TeamMode string

const (
	ModeSequential     TeamMode = "sequential"       // 串行传递
	ModeParallel       TeamMode = "parallel"         // 并发执行
	ModeLeaderFollower TeamMode = "leader_follower"  // 领导-追随者
)

type Team struct {
	ID          string         // 团队标识
	Name        string         // 名称
	Mode        TeamMode       // 协作模式
	Agents      []*engine.Agent // 团队成员
	Leader      *engine.Agent  // 领导者（仅 leader_follower 模式）
	SharedModel model.Model    // 共享模型（可选）
	logger      *slog.Logger
}
```

### 8.2.2 三种协作模式

**Sequential 模式**：

```
输入 → Agent A → Agent B → Agent C → 输出
```

每个 Agent 的输出成为下一个 Agent 的输入。这种模式适合流水线式处理：

```go
func (t *Team) runSequential(ctx context.Context, input string) *TeamOutput {
	currentInput := input
	outputs := make(map[string]*engine.RunOutput)

	for _, agent := range t.Agents {
		output := agent.Run(ctx, currentInput)
		outputs[agent.Name] = output

		if !output.Success {
			return &TeamOutput{
				AgentOutputs: outputs,
				FinalOutput:  output.Content,
				Success:      false,
				Error:        fmt.Sprintf("agent %s failed: %s", agent.Name, output.Error),
			}
		}

		currentInput = output.Content // 传递输出
	}

	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  currentInput,
		Success:      true,
	}
}
```

**Parallel 模式**：

```
输入 → Agent A ──┐
输入 → Agent B ──┤→ 合并 → 输出
输入 → Agent C ──┘
```

所有 Agent 通过 goroutine 并发执行，结果合并：

```go
func (t *Team) runParallel(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}

	for _, agent := range t.Agents {
		wg.Add(1)
		a := agent
		go func() {
			defer wg.Done()
			output := a.Run(ctx, input)
			mu.Lock()
			outputs[a.Name] = output
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 合并所有 Agent 的输出
	var parts []string
	for _, agent := range t.Agents {
		if out, ok := outputs[agent.Name]; ok && out.Success {
			parts = append(parts, fmt.Sprintf("**%s**: %s", agent.Name, out.Content))
		}
	}

	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  strings.Join(parts, "\n\n"),
		Success:      true,
	}
}
```

**LeaderFollower 模式**：

```
         ┌─ Agent B（执行）─┐
Leader A ── Agent C（执行）──┤→ Leader A → 输出
         └─ Agent D（执行）─┘
```

领导者规划 → 追随者执行 → 领导者综合：

```go
func (t *Team) runLeaderFollower(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)

	// 阶段 1：领导者制定计划
	planOutput := t.Leader.Run(ctx,
		fmt.Sprintf("Plan the approach for: %s\n\nProvide a step-by-step plan.", input))
	outputs[t.Leader.Name] = planOutput

	// 阶段 2：追随者执行
	for _, agent := range t.Agents {
		if agent.Name == t.Leader.Name { continue }
		followerInput := fmt.Sprintf(
			"Plan: %s\n\nTask: %s\n\nYour role: %s",
			planOutput.Content, input, agent.SystemPrompt)
		output := agent.Run(ctx, followerInput)
		outputs[agent.Name] = output
	}

	// 阶段 3：领导者综合结果
	synthOutput := t.Leader.Run(ctx,
		fmt.Sprintf("Synthesize results for: %s\n\nResults:\n%s",
			input, formatOutputs(outputs)))

	return &TeamOutput{FinalOutput: synthOutput.Content, Success: true}
}
```

### 三种模式的可视化对比

#### Sequential 模式（串行传递）

```mermaid
sequenceDiagram
    participant I as 输入
    participant A as Agent A
    participant B as Agent B
    participant C as Agent C
    participant O as 输出

    I->>A: "搜索Go教程"
    A->>B: "Go教程推荐结果"
    B->>C: "整理后的教程列表"
    C->>O: "最终回答"
    
    Note over A,C: 输出逐步精炼
```

#### Parallel 模式（并发执行）

```mermaid
sequenceDiagram
    participant I as 输入
    participant A as Agent A
    participant B as Agent B
    participant C as Agent C
    participant M as 合并
    participant O as 输出

    par 并发执行
        I->>A: "分析代码质量"
        I->>B: "检查安全漏洞"
        I->>C: "性能评估"
    end
    
    A->>M: 代码质量报告
    B->>M: 安全漏洞报告
    C->>M: 性能评估报告
    M->>O: 综合报告
```

#### LeaderFollower 模式（领导-追随者）

```mermaid
sequenceDiagram
    participant L as 领导者
    participant F1 as 执行者 A
    participant F2 as 执行者 B
    participant O as 输出

    L->>L: 制定计划
    L->>F1: 分配任务 A
    L->>F2: 分配任务 B
    
    F1->>F1: 执行任务 A
    F2->>F2: 执行任务 B
    
    F1->>L: 任务 A 结果
    F2->>L: 任务 B 结果
    
    L->>L: 综合所有结果
    L->>O: 最终回答
```

**如何选择模式？**

| 场景 | 推荐模式 | 理由 |
|------|---------|------|
| 流水线处理 | Sequential | 前一步的输出是后一步的输入 |
| 多角度分析 | Parallel | 同时从不同角度分析，结果合并 |
| 复杂决策 | LeaderFollower | 需要规划者统筹全局 |

---

## 8.3 Workflow 设计

与 Team 的固定模式不同，Workflow 提供了更灵活的 DAG（有向无环图）编排能力。

### 8.3.1 Node 接口

```go
type NodeType string

const (
	NodeTypeStep      NodeType = "step"
	NodeTypeCondition NodeType = "condition"
	NodeTypeLoop      NodeType = "loop"
	NodeTypeParallel  NodeType = "parallel"
)

type Node interface {
	ID() string                                    // 节点唯一标识
	Type() NodeType                                // 节点类型
	Execute(ctx context.Context, input string, state *State) (string, error)
}
```

**state 参数**：Workflow 通过并发安全的 `State` 在节点间共享数据。使用 `Get`、`Set` 和 `Snapshot` 访问，避免 ParallelNode 读写裸 map 时产生竞态。

### 8.3.2 四种节点实现

**StepNode**：执行一个 Agent

```go
type StepNode struct {
	id    string
	Agent *engine.Agent
}

func (n *StepNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	output := n.Agent.Run(ctx, input)
	if !output.Success {
		return "", fmt.Errorf("step %s failed: %s", n.id, output.Error)
	}
	return output.Content, nil
}
```

**ConditionNode**：条件分支

```go
type ConditionNode struct {
	id         string
	Condition  func(input string, state *State) (bool, error)
	TrueNode   Node
	FalseNode  Node
}

func (n *ConditionNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	result, err := n.Condition(input, state)
	if err != nil { return "", err }
	if result { return n.TrueNode.Execute(ctx, input, state) }
	// FalseNode 为 nil 时直接透传输入
	if n.FalseNode != nil { return n.FalseNode.Execute(ctx, input, state) }
	return input, nil
}
```

**LoopNode**：条件循环

```go
type LoopNode struct {
	id         string
	BodyNode   Node
	Condition  func(iteration int, input string, state *State) (bool, error)
	MaxIter    int
}

func (n *LoopNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	current := input
	for i := 0; i < n.MaxIter; i++ {
		shouldContinue, err := n.Condition(i, current, state)
		if err != nil { return current, err }
		if !shouldContinue { break }

		current, err = n.BodyNode.Execute(ctx, current, state)
		if err != nil { return current, err }
	}
	return current, nil
}
```

**ParallelNode**：并行执行

```go
type ParallelNode struct {
	id    string
	Nodes []Node
}

func (n *ParallelNode) Execute(ctx context.Context, input string, state *State) (string, error) {
	type nodeResult struct { output string; err error }
	results := make([]nodeResult, len(n.Nodes))
	var wg sync.WaitGroup

	for i, node := range n.Nodes {
		wg.Add(1)
		go func(idx int, nd Node) {
			defer wg.Done()
			out, err := nd.Execute(ctx, input, state)
			results[idx] = nodeResult{out, err}
		}(i, node)
	}
	wg.Wait()

	var parts []string
	for i, r := range results {
		if r.err != nil {
			parts = append(parts, fmt.Sprintf("[%s] error: %v", n.Nodes[i].ID(), r.err))
		} else {
			parts = append(parts, fmt.Sprintf("[%s] %s", n.Nodes[i].ID(), r.output))
		}
	}
	return strings.Join(parts, "\n"), nil
}
```

### 8.3.3 Workflow 引擎

```go
type Workflow struct {
	ID           string
	Name         string
	Nodes        []Node
	initialState *State // 每次运行从初始状态复制
	logger       *slog.Logger
}

func (w *Workflow) Run(ctx context.Context, input string) *WorkflowResult {
	start := time.Now()
	current := input
	state := NewState(w.initialState.Snapshot())
	state.Set("user_input", input)

	for _, node := range w.Nodes {
		select {
		case <-ctx.Done():
			return &WorkflowResult{
				Output: current, Success: false,
				Error: "workflow cancelled",
			}
		default:
		}

		output, err := node.Execute(ctx, current, state)
		if err != nil {
			return &WorkflowResult{
				Output: current, Success: false,
				Error: fmt.Sprintf("node %s failed: %s", node.ID(), err),
			}
		}
		current = output
	}

	return &WorkflowResult{
		Output:   current,
		Success:  true,
		State:    state.Snapshot(),
		Duration: time.Since(start),
	}
}
```

### 8.3.4 构建 Workflow 示例

```go
wf := workflow.NewWorkflow(workflow.WorkflowConfig{
	ID:   "customer-service",
	Name: "智能客服工作流",
})

// 构建节点
classify := workflow.NewStepNode("classify", triageAgent)
handleQuery := workflow.NewStepNode("handle", orderAgent)
transfer := workflow.NewStepNode("transfer", orderAgent)

// 条件判断：是否转人工
route := workflow.NewConditionNode("route",
	func(input string, state *workflow.State) (bool, error) {
		value, _ := state.Get("intent")
		intent, _ := value.(string)
		return intent == "refund", nil
	},
	handleQuery,  // 需要退款 → 订单处理
	transfer,     // 否则 → 转人工
)

// 组装工作流
wf.AddNode(classify)  // 1. 意图分类
wf.AddNode(route)     // 2. 路由选择

result := wf.Run(ctx, "我想退货")
```

### Workflow 可视化

```mermaid
flowchart TD
    A["classify<br/>意图分类"] --> B{"route<br/>条件判断"}
    B -->|"refund"| C["refund_agent<br/>退款处理"]
    B -->|"其他"| D["transfer<br/>转人工"]
    C --> E["compensation<br/>补偿判断"]
    E -->|"需要补偿"| F["send_coupon<br/>发优惠券"]
    E -->|"不需要"| G["done<br/>完成"]
    
    style A fill:#cce5ff
    style B fill:#fff3cd
    style C fill:#f8d7da
    style D fill:#f8d7da
    style E fill:#fff3cd
    style F fill:#d4edda
    style G fill:#d4edda
```

**节点颜色含义**：
- 蓝色：Step 节点（执行 Agent）
- 黄色：Condition 节点（条件分支）
- 红色：需要关注的节点（退款/转人工）
- 绿色：正常结束节点

---

## 8.4 Team 与 Workflow 的选择

| 场景 | 推荐方式 | 理由 |
|------|---------|------|
| 固定流程的流水线 | Team Sequential | 简单、直接 |
| 需要并行处理 | Team Parallel | 天然并发 |
| 领导-下属模式 | Team LeaderFollower | 明确分工 |
| 需要条件分支 | Workflow Condition | 灵活 |
| 需要循环 | Workflow Loop | 内置循环 |
| 复杂 DAG | Workflow | 最灵活 |

---

## 8.5 本章小结

- 实现了 Team 三种协作模式：Sequential、Parallel、LeaderFollower
- 实现了 Workflow 四种节点：Step、Condition、Loop、Parallel
- 理解了 Team 与 Workflow 的适用场景区别
- 通过 state 机制实现了工作流节点间的数据共享

---

## 练习

1. 为 Team 添加 Consensus（共识）模式：所有 Agent 多轮讨论，每轮并行执行，直到达成共识
2. 为 Workflow 添加 Resume 功能：每步执行后保存快照，失败后可从断点恢复
3. 实现 Workflow 的 `RouterNode`：根据 state 中的值路由到不同的子节点路径
