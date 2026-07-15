package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/apexracing/tracklogic-agent/engine"
	"github.com/apexracing/tracklogic-agent/model"
)

type TeamMode string

const (
	ModeSequential     TeamMode = "sequential"
	ModeParallel       TeamMode = "parallel"
	ModeLeaderFollower TeamMode = "leader_follower"
)

type Team struct {
	ID          string
	Name        string
	Mode        TeamMode
	Agents      []*engine.Agent
	Leader      *engine.Agent
	SharedModel model.Model
	logger      *slog.Logger
}

type TeamConfig struct {
	ID          string
	Name        string
	Mode        TeamMode
	Agents      []*engine.Agent
	Leader      *engine.Agent
	SharedModel model.Model
}

func NewTeam(cfg TeamConfig) *Team {
	return &Team{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Mode:        cfg.Mode,
		Agents:      cfg.Agents,
		Leader:      cfg.Leader,
		SharedModel: cfg.SharedModel,
		logger:      slog.With("component", "team", "name", cfg.Name),
	}
}

type TeamOutput struct {
	AgentOutputs map[string]*engine.RunOutput
	FinalOutput  string
	Success      bool
	Error        string
}

func (t *Team) Run(ctx context.Context, input string) *TeamOutput {
	t.logger.Info("team run started", "mode", t.Mode)

	switch t.Mode {
	case ModeSequential:
		return t.runSequential(ctx, input)
	case ModeParallel:
		return t.runParallel(ctx, input)
	case ModeLeaderFollower:
		return t.runLeaderFollower(ctx, input)
	default:
		return &TeamOutput{Success: false, Error: fmt.Sprintf("unknown mode: %s", t.Mode)}
	}
}

func (t *Team) applySharedModel(agent *engine.Agent) *engine.Agent {
	if t.SharedModel == nil || agent == nil {
		return agent
	}
	agent.Model = t.SharedModel
	return agent
}

func (t *Team) runSequential(ctx context.Context, input string) *TeamOutput {
	agents := t.applyAllAgents()
	if len(agents) == 0 {
		return &TeamOutput{Success: false, Error: "no agents configured"}
	}

	outputs := make(map[string]*engine.RunOutput)
	currentInput := input

	for _, agent := range agents {
		t.logger.Info("sequential: running agent", "agent", agent.Name)
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
		currentInput = output.Content
	}

	finalOutput := outputs[agents[len(agents)-1].Name].Content
	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  finalOutput,
		Success:      true,
	}
}

func (t *Team) runParallel(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}

	for _, agent := range t.applyAllAgents() {
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

	var parts []string
	allSuccess := true
	for _, agent := range t.Agents {
		output := outputs[agent.Name]
		if output == nil || !output.Success {
			allSuccess = false
			continue
		}
		parts = append(parts, fmt.Sprintf("**%s**: %s", agent.Name, output.Content))
	}

	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  strings.Join(parts, "\n\n"),
		Success:      allSuccess,
	}
}

func (t *Team) runLeaderFollower(ctx context.Context, input string) *TeamOutput {
	outputs := make(map[string]*engine.RunOutput)

	if t.Leader == nil {
		return &TeamOutput{Success: false, Error: "leader is required for leader_follower mode"}
	}

	leader := t.applySharedModel(t.Leader)
	planOutput := leader.Run(ctx, fmt.Sprintf("Plan the approach for: %s\n\nProvide a step-by-step plan.", input))
	outputs[t.Leader.Name] = planOutput

	if !planOutput.Success {
		return &TeamOutput{
			AgentOutputs: outputs,
			Success:      false,
			Error:        fmt.Sprintf("leader failed: %s", planOutput.Error),
		}
	}

	for _, agent := range t.applyAllAgents() {
		if agent.Name == t.Leader.Name {
			continue
		}
		t.logger.Info("follower executing", "agent", agent.Name)
		followerInput := fmt.Sprintf("Plan: %s\n\nTask: %s\n\nYour role: %s", planOutput.Content, input, agent.SystemPrompt)
		output := agent.Run(ctx, followerInput)
		outputs[agent.Name] = output
	}

	synthOutput := leader.Run(ctx, fmt.Sprintf(
		"Synthesize the following results into a final answer.\n\nOriginal request: %s\n\nResults:\n%s",
		input, t.formatOutputs(outputs),
	))

	return &TeamOutput{
		AgentOutputs: outputs,
		FinalOutput:  synthOutput.Content,
		Success:      synthOutput.Success,
	}
}

func (t *Team) applyAllAgents() []*engine.Agent {
	result := make([]*engine.Agent, len(t.Agents))
	copy(result, t.Agents)
	if t.SharedModel == nil {
		return result
	}
	for i := range result {
		result[i] = t.applySharedModel(result[i])
	}
	return result
}

func (t *Team) formatOutputs(outputs map[string]*engine.RunOutput) string {
	var sb strings.Builder
	for name, out := range outputs {
		sb.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", name, out.Content))
	}
	return sb.String()
}
