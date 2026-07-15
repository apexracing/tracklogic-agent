package agent

import (
	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/orchestrator"
	"github.com/apexracing/tracklogic-agent/security"
)

// Agent is the runtime agent implementation exposed by the root package.
type Agent = engine.Agent

// AgentConfig configures a standalone Agent.
type AgentConfig = engine.AgentConfig

// RunOutput is the result of an Agent run.
type RunOutput = engine.RunOutput

// RunOption customizes an individual Agent run.
type RunOption = engine.RunOption

// NewAgent creates a standalone Agent. Most applications can instead use
// Harness.NewAgent, which shares the Harness model and tool registry.
func NewAgent(cfg AgentConfig) *Agent { return engine.NewAgent(cfg) }

func WithMaxLoops(n int) RunOption            { return engine.WithMaxLoops(n) }
func WithStream(fn func(string)) RunOption    { return engine.WithStream(fn) }
func WithTemperature(value float64) RunOption { return engine.WithTemperature(value) }
func WithMaxTokens(n int) RunOption           { return engine.WithMaxTokens(n) }

type Team = orchestrator.Team
type TeamConfig = orchestrator.TeamConfig
type TeamMode = orchestrator.TeamMode
type TeamOutput = orchestrator.TeamOutput

const (
	ModeSequential     = orchestrator.ModeSequential
	ModeParallel       = orchestrator.ModeParallel
	ModeLeaderFollower = orchestrator.ModeLeaderFollower
)

func NewTeam(cfg TeamConfig) *Team { return orchestrator.NewTeam(cfg) }

type Permission = security.Permission

const (
	PermReadFile  = security.PermReadFile
	PermWriteFile = security.PermWriteFile
	PermExec      = security.PermExec
	PermNetAccess = security.PermNetAccess
	PermReadDB    = security.PermReadDB
	PermWriteDB   = security.PermWriteDB
	PermSendEmail = security.PermSendEmail
)

type Workflow = orchestrator.Workflow
type WorkflowConfig = orchestrator.WorkflowConfig
type WorkflowResult = orchestrator.WorkflowResult
type Node = orchestrator.Node
type NodeType = orchestrator.NodeType
type StepNode = orchestrator.StepNode
type ConditionNode = orchestrator.ConditionNode
type LoopNode = orchestrator.LoopNode
type ParallelNode = orchestrator.ParallelNode
type StepLog = orchestrator.StepLog

const (
	NodeTypeStep      = orchestrator.NodeTypeStep
	NodeTypeCondition = orchestrator.NodeTypeCondition
	NodeTypeLoop      = orchestrator.NodeTypeLoop
	NodeTypeParallel  = orchestrator.NodeTypeParallel
)

func NewWorkflow(cfg WorkflowConfig) *Workflow { return orchestrator.NewWorkflow(cfg) }

func NewStepNode(id string, runtimeAgent *Agent, after ...func(input, output string, state map[string]any)) *StepNode {
	return orchestrator.NewStepNode(id, runtimeAgent, after...)
}

func NewConditionNode(id string, condition func(input string, state map[string]any) (bool, error), trueNode, falseNode Node) *ConditionNode {
	return orchestrator.NewConditionNode(id, condition, trueNode, falseNode)
}

func NewLoopNode(id string, body Node, condition func(iteration int, input string, state map[string]any) (bool, error), maxIterations int) *LoopNode {
	return orchestrator.NewLoopNode(id, body, condition, maxIterations)
}

func NewParallelNode(id string, nodes ...Node) *ParallelNode {
	return orchestrator.NewParallelNode(id, nodes...)
}
